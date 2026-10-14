package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
	"github.com/Chilly913/PTIT_ERIPT_API/migrations"
)

func TestTokenServiceDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := db.NewMigrator(dsn, migrations.FS, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	gdb, err := db.Open(ctx, config.DBConfig{URL: dsn, MaxOpenConns: 3, MaxIdleConns: 1, LogLevel: "silent"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	email := fmt.Sprintf("tok.%d@phase1.test", time.Now().UnixNano())
	u := User{Email: email, Name: "Tok", UserType: "student"}
	if err := gdb.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gdb.Exec(`DELETE FROM users WHERE id = ?`, u.ID) })
	if loaded, err := FindUserByEmail(ctx, gdb, email); err != nil || loaded.Nationality != "Việt Nam" || loaded.UserType != "student" {
		t.Fatalf("defaults: %+v %v", loaded, err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	s := NewTokenService(gdb, TokenOptions{TTL: time.Hour, TouchInterval: time.Minute, Now: func() time.Time { return now }})
	a, err := s.Issue(ctx, nil, u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Issue(ctx, nil, u.ID, "mobile")
	if p, err := s.Verify(ctx, a.PlainText); err != nil || p.UserID != u.ID || p.TokenID != a.ID {
		t.Fatalf("verify: %+v %v", p, err)
	}
	if _, err := s.Verify(ctx, a.PlainText+"x"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("tampered token accepted")
	}

	// Optional middleware: guest without token, principal with one.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/opt", s.Optional(), func(c *gin.Context) { c.String(200, fmt.Sprint(UserID(c))) })
	r.GET("/req", s.Required(), func(c *gin.Context) { c.String(200, fmt.Sprint(UserID(c))) })
	call := func(path, tok string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if c, body := call("/opt", ""); c != 200 || body != "0" {
		t.Fatalf("optional guest: %d %s", c, body)
	}
	if c, body := call("/opt", "bad"); c != 200 || body != "0" {
		t.Fatalf("optional bad token: %d %s", c, body)
	}
	if c, body := call("/opt", b.PlainText); c != 200 || body != fmt.Sprint(u.ID) {
		t.Fatalf("optional user: %d %s", c, body)
	}
	if c, body := call("/req", ""); c != 401 || body != `{"message":"Unauthenticated."}` {
		t.Fatalf("required: %d %s", c, body)
	}

	if err := s.Revoke(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, a.PlainText); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("revoked token accepted")
	}
	if _, err := s.Verify(ctx, b.PlainText); err != nil {
		t.Fatal("other token must survive logout")
	}
	if err := s.RevokeAllForUser(ctx, nil, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, b.PlainText); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("revoke all failed")
	}
	n, err := s.PurgeExpired(ctx, now.Add(time.Second))
	if err != nil || n < 2 {
		t.Fatalf("purge: %d %v", n, err)
	}
}
