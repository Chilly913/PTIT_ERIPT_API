package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/mail"
	"github.com/Chilly913/PTIT_ERIPT_API/migrations"
)

// Integration tests run against TEST_DATABASE_URL (migrated to the latest
// version first). TEST_REDIS_URL (e.g. redis://127.0.0.1:6380/5) enables the
// rate-limit test. Every user created here is deleted at the end.

// fakeRoles implements auth.RoleService in memory.
type fakeRoles struct {
	mu    sync.Mutex
	roles map[int64][]string
	perms map[int64][]string
}

func (f *fakeRoles) UserRolesPermissions(_ context.Context, id int64) ([]string, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.roles[id]), slices.Clone(f.perms[id]), nil
}

func (f *fakeRoles) AssignRole(_ context.Context, _ *gorm.DB, id int64, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.roles[id], role) {
		f.roles[id] = append(f.roles[id], role)
	}
	return nil
}

func (f *fakeRoles) set(id int64, roles ...string) {
	f.mu.Lock()
	f.roles[id] = roles
	f.mu.Unlock()
}

// fakeGoogle returns a fixed profile for code "good".
type fakeGoogle struct{ user GoogleUser }

func (g *fakeGoogle) AuthCodeURL(state string) string {
	return "https://accounts.google.com/o/oauth2/auth?state=" + url.QueryEscape(state)
}

func (g *fakeGoogle) User(_ context.Context, code string) (GoogleUser, error) {
	if code != "good" {
		return GoogleUser{}, fmt.Errorf("invalid_grant")
	}
	return g.user, nil
}

// syncBuffer collects the log mailer's output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type env struct {
	t      *testing.T
	db     *gorm.DB
	router *gin.Engine
	roles  *fakeRoles
	store  *fakeStore
	google *fakeGoogle
	mails  *syncBuffer
	run    string
	now    time.Time
	nowMu  sync.Mutex
	rdb    *redis.Client
}

func (e *env) clock() time.Time {
	e.nowMu.Lock()
	defer e.nowMu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.nowMu.Lock()
	e.now = e.now.Add(d)
	e.nowMu.Unlock()
}

func (e *env) email(name string) string { return fmt.Sprintf("%s.%s@phase1.test", name, e.run) }

func setup(t *testing.T, withRedis bool) *env {
	t.Helper()
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
	gdb, err := db.Open(ctx, config.DBConfig{URL: dsn, MaxOpenConns: 5, MaxIdleConns: 2, LogLevel: "silent"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO roles (name) VALUES ('student') ON CONFLICT (name) DO NOTHING`).Error; err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadFrom(map[string]string{
		"APP_ENV":      "testing",
		"APP_NAME":     "ERIPT",
		"APP_URL":      "https://eript-lms.io.vn",
		"FRONTEND_URL": "http://localhost:3000",
		"APP_KEY":      "base64:MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"REDIS_PREFIX": "phase1test:",
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{
		t: t, db: gdb, run: fmt.Sprint(time.Now().UnixNano()),
		roles:  &fakeRoles{roles: map[int64][]string{}, perms: map[int64][]string{}},
		store:  newFakeStore("http://localhost:9000/lms-videos"),
		google: &fakeGoogle{},
		mails:  &syncBuffer{},
		now:    time.Now().UTC().Truncate(time.Microsecond),
	}
	a := &app.App{Cfg: cfg, Log: quiet, DB: gdb}
	limits := Limits{} // disabled
	if withRedis {
		ru := os.Getenv("TEST_REDIS_URL")
		if ru == "" {
			t.Skip("TEST_REDIS_URL not set")
		}
		opts, err := redis.ParseURL(ru)
		if err != nil {
			t.Fatal(err)
		}
		e.rdb = redis.NewClient(opts)
		if err := e.rdb.Ping(ctx).Err(); err != nil {
			t.Fatal(err)
		}
		a.Redis = e.rdb
		a.Cfg.Redis.Prefix = "phase1test:" + e.run + ":"
		limits = DefaultLimits
	}
	logMailer := &mail.LogMailer{Log: slog.New(slog.NewTextHandler(e.mails, nil)), From: "noreply@lms.local"}
	mod := New(a, e.roles,
		WithMailer(logMailer), WithStore(e.store), WithGoogle(e.google),
		WithLimits(limits), WithClock(e.clock))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.ContextWithFallback = true
	mod.Register(r.Group("/api"))
	e.router = r

	t.Cleanup(func() {
		gdb.Exec(`DELETE FROM users WHERE email LIKE ?`, "%."+e.run+"@phase1.test")
		if e.rdb != nil {
			keys, _ := e.rdb.Keys(ctx, a.Cfg.Redis.Prefix+"*").Result()
			if len(keys) > 0 {
				e.rdb.Del(ctx, keys...)
			}
			_ = e.rdb.Close()
		}
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return e
}

type resp struct {
	Code   int
	Header http.Header
	Body   map[string]any
	Raw    string
}

func (e *env) do(method, path, token string, body any, hdr ...string) resp {
	e.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case *multipartBody:
		rd, ct = &b.buf, b.ct
	default:
		js, _ := json.Marshal(b)
		rd, ct = bytes.NewReader(js), "application/json"
	}
	req := httptest.NewRequest(method, path, rd)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Accept", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	out := resp{Code: w.Code, Header: w.Header(), Raw: w.Body.String()}
	_ = json.Unmarshal(w.Body.Bytes(), &out.Body)
	return out
}

func (e *env) expect(r resp, code int, msg string) {
	e.t.Helper()
	if r.Code != code {
		e.t.Fatalf("status %d, want %d: %s", r.Code, code, r.Raw)
	}
	if msg != "" && r.Body["message"] != msg {
		e.t.Fatalf("message %q, want %q", r.Body["message"], msg)
	}
}

func (e *env) userID(email string) int64 {
	var id int64
	if err := e.db.Raw(`SELECT id FROM users WHERE email = ?`, email).Scan(&id).Error; err != nil || id == 0 {
		e.t.Fatalf("user %s not found: %v", email, err)
	}
	return id
}

var (
	verifyLinkRe = regexp.MustCompile(`/verify-email\?expires=(\d+)&signature=([0-9a-f]+)&id=(\d+)&hash=([0-9a-f]+)`)
	resetLinkRe  = regexp.MustCompile(`/reset-password\?token=([A-Za-z0-9_-]+)&email=([^\s"\\]+)`)
)

func (e *env) lastMatch(re *regexp.Regexp) []string {
	e.t.Helper()
	all := re.FindAllStringSubmatch(e.mails.String(), -1)
	if len(all) == 0 {
		e.t.Fatalf("no mail matching %s in:\n%s", re, e.mails.String())
	}
	return all[len(all)-1]
}

func (e *env) registerVerified(name, password string) (string, int64) {
	e.t.Helper()
	email := e.email(name)
	e.expect(e.do("POST", "/api/auth/register", "", map[string]any{
		"name": "User " + name, "email": email, "password": password, "password_confirmation": password,
	}), 201, "")
	id := e.userID(email)
	e.db.Exec(`UPDATE users SET email_verified_at = now() WHERE id = ?`, id)
	return email, id
}

func (e *env) login(email, password string) string {
	e.t.Helper()
	r := e.do("POST", "/api/auth/login", "", map[string]any{"email": email, "password": password})
	e.expect(r, 200, "Login success")
	return r.Body["access_token"].(string)
}

func TestRegisterVerifyLoginMeLogout(t *testing.T) {
	e := setup(t, false)
	email := e.email("alice")

	// Validation: every failing field in one 422, Laravel messages and order.
	r := e.do("POST", "/api/auth/register", "", map[string]any{"email": "bad", "password": "123", "password_confirmation": "999"})
	e.expect(r, 422, "The name field is required. (and 3 more errors)")
	errs := r.Body["errors"].(map[string]any)
	if fmt.Sprint(errs["password"]) != "[The password field must be at least 6 characters. The password field confirmation does not match.]" ||
		fmt.Sprint(errs["email"]) != "[The email field must be a valid email address.]" {
		t.Fatalf("errors: %v", errs)
	}

	r = e.do("POST", "/api/auth/register", "", map[string]any{
		"name": "Alice", "email": email, "password": "secret1", "password_confirmation": "secret1",
	})
	e.expect(r, 201, "Đăng ký thành công. Vui lòng kiểm tra email để xác nhận tài khoản.")
	if r.Body["email"] != email || r.Body["requires_verification"] != true {
		t.Fatalf("register body: %v", r.Body)
	}
	id := e.userID(email)
	if roles, _, _ := e.roles.UserRolesPermissions(context.Background(), id); !slices.Equal(roles, []string{"student"}) {
		t.Fatalf("default role: %v", roles)
	}

	// Duplicate e-mail (case-insensitive) is reported together with other errors.
	r = e.do("POST", "/api/register", "", map[string]any{"email": strings.ToUpper(email), "password": "secret1", "password_confirmation": "secret1"})
	e.expect(r, 422, "The name field is required. (and 1 more error)")
	if fmt.Sprint(r.Body["errors"].(map[string]any)["email"]) != "[The email has already been taken.]" {
		t.Fatalf("unique: %v", r.Body)
	}

	// Unverified user: 403 and the verification mail is re-sent.
	before := strings.Count(e.mails.String(), "verify-email?")
	r = e.do("POST", "/api/auth/login", "", map[string]any{"email": email, "password": "whatever"})
	e.expect(r, 403, "Email của bạn chưa được xác nhận. Chúng tôi đã gửi lại email xác nhận.")
	if r.Body["requires_verification"] != true || r.Body["email"] != email {
		t.Fatalf("403 body: %v", r.Body)
	}
	if strings.Count(e.mails.String(), "verify-email?") != before+1 {
		t.Fatal("verification mail not re-sent")
	}

	// Verify e-mail with the link from the (log) mail.
	m := e.lastMatch(verifyLinkRe)
	exp, sig, idStr, hash := m[1], m[2], m[3], m[4]
	if idStr != fmt.Sprint(id) || hash != auth.EmailHash(email) {
		t.Fatalf("link params: %v", m)
	}
	bad := e.do("GET", fmt.Sprintf("/api/auth/verify-email/%s/%s?expires=%s&signature=%s", idStr, hash, exp, strings.Repeat("0", 64)), "", nil)
	e.expect(bad, 403, "Invalid signature.")
	r = e.do("GET", fmt.Sprintf("/api/auth/verify-email/%s/%s?expires=%s&signature=%s", idStr, hash, exp, sig), "", nil)
	e.expect(r, 200, "Xác nhận email thành công. Bạn có thể đăng nhập ngay bây giờ.")
	if r.Body["verified"] != true {
		t.Fatalf("verify body %v", r.Body)
	}
	e.expect(e.do("POST", "/api/auth/resend-verification-email", "", map[string]any{"email": email}), 200, "Email này đã được xác nhận.")
	e.expect(e.do("POST", "/api/auth/resend-verification-email", "", map[string]any{"email": e.email("nobody")}), 404, "Không tìm thấy tài khoản phù hợp.")

	// Wrong password / unknown user: 401 like Auth::attempt failure.
	e.expect(e.do("POST", "/api/auth/login", "", map[string]any{"email": email, "password": "nope!!"}), 401, "Invalid email or password")
	e.expect(e.do("POST", "/api/login", "", map[string]any{"email": e.email("ghost"), "password": "nope!!"}), 401, "Invalid email or password")
	e.expect(e.do("POST", "/api/auth/login", "", map[string]any{}), 422, "The email field is required. (and 1 more error)")

	r = e.do("POST", "/api/auth/login", "", map[string]any{"email": strings.ToUpper(email), "password": "secret1"})
	e.expect(r, 200, "Login success")
	token := r.Body["access_token"].(string)
	if r.Body["token_type"] != "Bearer" {
		t.Fatalf("token_type: %v", r.Body)
	}
	user := r.Body["user"].(map[string]any)
	wantKeys := []string{"id", "name", "email", "avatar", "role", "roles", "permissions", "email_verified", "email_verified_at",
		"user_type", "student_code", "staff_code", "phone", "gender", "date_of_birth", "hometown", "permanent_address",
		"nationality", "organization", "academic"}
	for _, k := range wantKeys {
		if _, ok := user[k]; !ok {
			t.Errorf("user.%s missing", k)
		}
	}
	if len(user) != len(wantKeys) {
		t.Errorf("user has %d keys, want %d: %v", len(user), len(wantKeys), user)
	}
	if user["role"] != "student" || user["email_verified"] != true || user["user_type"] != "student" || user["nationality"] != "Việt Nam" {
		t.Fatalf("user: %v", user)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6}Z$`).MatchString(user["email_verified_at"].(string)) {
		t.Fatalf("email_verified_at format: %v", user["email_verified_at"])
	}
	if fmt.Sprint(user["organization"]) != "map[institution_id:<nil> institution_name:<nil> unit_id:<nil> unit_name:<nil>]" {
		t.Fatalf("organization: %v", user["organization"])
	}
	if len(user["academic"].(map[string]any)) != 8 {
		t.Fatalf("academic: %v", user["academic"])
	}

	// me + aliases.
	r = e.do("GET", "/api/auth/me", token, nil)
	e.expect(r, 200, "")
	if r.Body["email"] != email || r.Body["id"] != float64(id) {
		t.Fatalf("me: %v", r.Body)
	}
	e.expect(e.do("GET", "/api/me", token, nil), 200, "")
	r = e.do("GET", "/api/user", token, nil)
	e.expect(r, 200, "")
	if r.Body["points_balance"] != float64(0) || r.Body["email"] != email {
		t.Fatalf("/api/user: %v", r.Body)
	}
	if _, leaked := r.Body["password_hash"]; leaked {
		t.Fatal("password hash leaked")
	}

	e.expect(e.do("GET", "/api/auth/me", "", nil), 401, "Unauthenticated.")
	e.expect(e.do("GET", "/api/auth/me", "garbage", nil), 401, "Unauthenticated.")
	e.expect(e.do("POST", "/api/auth/logout", token, nil), 200, "Logout success")
	e.expect(e.do("GET", "/api/auth/me", token, nil), 401, "Unauthenticated.")
	e.expect(e.do("POST", "/api/logout", token, nil), 401, "Unauthenticated.")
}

func TestVerifyEmailHashMismatchAndExpiry(t *testing.T) {
	e := setup(t, false)
	email := e.email("bob")
	e.expect(e.do("POST", "/api/auth/register", "", map[string]any{
		"name": "Bob", "email": email, "password": "secret1", "password_confirmation": "secret1",
	}), 201, "")
	m := e.lastMatch(verifyLinkRe)
	// E-mail changed after the link was sent: signature still valid, hash not.
	e.db.Exec(`UPDATE users SET email = ? WHERE email = ?`, e.email("bob2"), email)
	r := e.do("GET", fmt.Sprintf("/api/auth/verify-email/%s/%s?expires=%s&signature=%s", m[3], m[4], m[1], m[2]), "", nil)
	e.expect(r, 403, "Liên kết xác nhận đã hết hạn hoặc không hợp lệ.")
	// Expired.
	e.advance(61 * time.Minute)
	r = e.do("GET", fmt.Sprintf("/api/auth/verify-email/%s/%s?expires=%s&signature=%s", m[3], m[4], m[1], m[2]), "", nil)
	e.expect(r, 403, "Invalid signature.")
}

func TestAdminMayLoginUnverified(t *testing.T) {
	e := setup(t, false)
	email := e.email("admin")
	e.expect(e.do("POST", "/api/auth/register", "", map[string]any{
		"name": "Admin", "email": email, "password": "secret1", "password_confirmation": "secret1",
	}), 201, "")
	id := e.userID(email)
	e.roles.set(id, "student", "admin")
	r := e.do("POST", "/api/auth/login", "", map[string]any{"email": email, "password": "secret1"})
	e.expect(r, 200, "Login success")
	u := r.Body["user"].(map[string]any)
	if u["role"] != "admin" || u["email_verified"] != false || u["email_verified_at"] != nil {
		t.Fatalf("admin user: %v", u)
	}
}

func TestSoftDeletedUser(t *testing.T) {
	e := setup(t, false)
	email, id := e.registerVerified("carol", "secret1")
	token := e.login(email, "secret1")
	e.db.Exec(`UPDATE users SET deleted_at = now() WHERE id = ?`, id)

	e.expect(e.do("GET", "/api/auth/me", token, nil), 401, "Unauthenticated.")
	e.expect(e.do("POST", "/api/auth/login", "", map[string]any{"email": email, "password": "secret1"}), 401, "Invalid email or password")
	e.expect(e.do("POST", "/api/auth/resend-verification-email", "", map[string]any{"email": email}), 404, "")
	e.expect(e.do("POST", "/api/auth/forgot-password", "", map[string]any{"email": email}), 422, "We can't find a user with that email address.")
	// The address stays reserved.
	r := e.do("POST", "/api/auth/register", "", map[string]any{"name": "C", "email": email, "password": "secret1", "password_confirmation": "secret1"})
	e.expect(r, 422, "The email has already been taken.")
}

func TestTokenSlidingExpiry(t *testing.T) {
	e := setup(t, false)
	email, _ := e.registerVerified("dave", "secret1")
	token := e.login(email, "secret1")
	expires := func() (time.Time, *time.Time) {
		var row struct {
			ExpiresAt  time.Time
			LastUsedAt *time.Time
		}
		e.db.Raw(`SELECT expires_at, last_used_at FROM access_tokens WHERE token_hash = ?`, auth.HashToken(token)).Scan(&row)
		return row.ExpiresAt.UTC(), row.LastUsedAt
	}
	issued, last := expires()
	if last != nil || !issued.Equal(e.clock().Add(auth.DefaultTokenTTL)) {
		t.Fatalf("issued expires_at %v last_used %v", issued, last)
	}

	e.advance(time.Minute)
	e.expect(e.do("GET", "/api/auth/me", token, nil), 200, "") // first use: touched
	first, last := expires()
	if last == nil || !first.Equal(e.clock().Add(auth.DefaultTokenTTL)) {
		t.Fatalf("first use: %v %v", first, last)
	}
	e.advance(2 * time.Minute)
	e.expect(e.do("GET", "/api/auth/me", token, nil), 200, "") // throttled: no write
	if again, _ := expires(); !again.Equal(first) {
		t.Fatalf("write not throttled: %v vs %v", again, first)
	}
	e.advance(10 * time.Minute)
	e.expect(e.do("GET", "/api/auth/me", token, nil), 200, "")
	if slid, _ := expires(); !slid.Equal(e.clock().Add(auth.DefaultTokenTTL)) {
		t.Fatalf("not slid: %v", slid)
	}
	// Idle longer than the TTL: rejected.
	e.advance(auth.DefaultTokenTTL + time.Second)
	e.expect(e.do("GET", "/api/auth/me", token, nil), 401, "Unauthenticated.")
}

func TestChangePasswordAndProfile(t *testing.T) {
	e := setup(t, false)
	email, id := e.registerVerified("erin", "secret1")
	token := e.login(email, "secret1")

	r := e.do("PUT", "/api/auth/change-password", token, map[string]any{"current_password": "wrong", "password": "newpass1", "password_confirmation": "newpass1"})
	e.expect(r, 422, "Current password is incorrect")
	if _, has := r.Body["errors"]; has {
		t.Fatal("legacy body has no errors key")
	}
	r = e.do("PUT", "/api/auth/change-password", token, map[string]any{"current_password": "secret1", "password": "newpass1", "password_confirmation": "x"})
	e.expect(r, 422, "The password field confirmation does not match.")
	e.expect(e.do("PUT", "/api/auth/change-password", token, map[string]any{"current_password": "secret1", "password": "newpass1", "password_confirmation": "newpass1"}), 200, "Password changed successfully")
	e.expect(e.do("POST", "/api/auth/login", "", map[string]any{"email": email, "password": "secret1"}), 401, "")
	e.login(email, "newpass1")

	// Legacy $2y$ hash imported from Laravel still logs in.
	e.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, "$2y$12$Q6OBfh6GVdSDD4/fTOxCxONvR5i/QSmVsZle4GUnBqpmuCfgjpABe", id)
	e.login(email, "secret-password")

	r = e.do("PUT", "/api/auth/profile", token, map[string]any{"name": "Erin New", "avatar": "/minio/lms-videos/users/avatars/e.jpg"})
	e.expect(r, 200, "Profile updated")
	u := r.Body["user"].(map[string]any)
	if u["name"] != "Erin New" || u["avatar"] != "/minio/lms-videos/users/avatars/e.jpg" {
		t.Fatalf("profile: %v", u)
	}
	var stored string
	e.db.Raw(`SELECT avatar_path FROM users WHERE id = ?`, id).Scan(&stored)
	if stored != "users/avatars/e.jpg" {
		t.Fatalf("stored avatar %q", stored)
	}
	// Omitted avatar keeps it (mobile sends name/phone only); null clears it.
	r = e.do("PUT", "/api/auth/profile", token, map[string]any{"name": "Erin", "phone": "0900"})
	e.expect(r, 200, "")
	if r.Body["user"].(map[string]any)["avatar"] != "/minio/lms-videos/users/avatars/e.jpg" {
		t.Fatalf("avatar dropped: %v", r.Body)
	}
	r = e.do("PUT", "/api/auth/profile", token, map[string]any{"name": "Erin", "avatar": nil})
	if r.Body["user"].(map[string]any)["avatar"] != nil {
		t.Fatalf("avatar not cleared: %v", r.Body)
	}
	r = e.do("PUT", "/api/auth/profile", token, map[string]any{"name": "", "avatar": 5})
	e.expect(r, 422, "The name field is required. (and 1 more error)")
	if fmt.Sprint(r.Body["errors"].(map[string]any)["avatar"]) != "[The avatar field must be a string.]" {
		t.Fatalf("avatar type error: %v", r.Body)
	}
}

func TestForgotResetPassword(t *testing.T) {
	e := setup(t, false)
	email, _ := e.registerVerified("frank", "secret1")
	oldToken := e.login(email, "secret1")

	e.expect(e.do("POST", "/api/auth/forgot-password", "", map[string]any{"email": e.email("nobody")}), 422, "We can't find a user with that email address.")
	e.expect(e.do("POST", "/api/auth/forgot-password", "", map[string]any{"email": "bad"}), 422, "The email field must be a valid email address.")
	e.expect(e.do("POST", "/api/auth/forgot-password", "", map[string]any{"email": email}), 200, "We have emailed your password reset link.")
	m := e.lastMatch(resetLinkRe)
	tok, mailedEmail := m[1], m[2]
	if dec, _ := url.QueryUnescape(mailedEmail); dec != email {
		t.Fatalf("email param %q", mailedEmail)
	}
	e.expect(e.do("POST", "/api/auth/forgot-password", "", map[string]any{"email": email}), 422, "Please wait before retrying.")

	reset := func(token, pw string) resp {
		return e.do("POST", "/api/auth/reset-password", "", map[string]any{"token": token, "email": email, "password": pw, "password_confirmation": pw})
	}
	e.expect(reset("wrong-token", "brandnew1"), 422, "This password reset token is invalid.")
	e.expect(reset(tok, "brandnew1"), 200, "Your password has been reset.")
	e.expect(reset(tok, "another1"), 422, "This password reset token is invalid.") // single use
	e.expect(e.do("GET", "/api/auth/me", oldToken, nil), 401, "")                  // sessions revoked
	e.login(email, "brandnew1")

	// Expired token.
	e.advance(2 * time.Minute)
	e.expect(e.do("POST", "/api/auth/forgot-password", "", map[string]any{"email": email}), 200, "")
	tok2 := e.lastMatch(resetLinkRe)[1]
	if tok2 == tok {
		t.Fatal("token reused")
	}
	e.advance(61 * time.Minute)
	e.expect(reset(tok2, "expired1"), 422, "This password reset token is invalid.")
}

type multipartBody struct {
	buf bytes.Buffer
	ct  string
}

func newMultipart(t *testing.T, fields map[string]string, filename string, content []byte) *multipartBody {
	mb := &multipartBody{}
	w := multipart.NewWriter(&mb.buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if filename != "" {
		fw, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(content)
	}
	_ = w.Close()
	mb.ct = w.FormDataContentType()
	return mb
}

// 1x1 PNG.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

func TestUpload(t *testing.T) {
	e := setup(t, false)
	email, id := e.registerVerified("gina", "secret1")
	token := e.login(email, "secret1")

	e.store.objects["users/avatars/old.png"] = 1
	e.store.objects["admin/settings/logo.png"] = 1
	r := e.do("POST", "/api/auth/upload", token, newMultipart(t, map[string]string{"old_path": "/minio/lms-videos/users/avatars/old.png"}, "me.png", pngBytes))
	e.expect(r, 201, "Upload successful")
	path := r.Body["path"].(string)
	if !regexp.MustCompile(`^users/avatars/\d+_[A-Za-z0-9]{10}\.png$`).MatchString(path) || r.Body["url"] != "/minio/lms-videos/"+path {
		t.Fatalf("upload body: %v", r.Body)
	}
	meta := r.Body["meta"].(map[string]any)
	if meta["name"] != "me.png" || meta["mime"] != "image/png" || meta["path"] != path || meta["size"] != float64(0) {
		t.Fatalf("meta: %v", meta)
	}
	if _, ok := e.store.objects[path]; !ok || slices.Index(e.store.deleted, "users/avatars/old.png") < 0 {
		t.Fatalf("store: %v deleted %v", e.store.objects, e.store.deleted)
	}
	// old_path outside the target folder is not deleted.
	e.expect(e.do("POST", "/api/auth/upload", token, newMultipart(t, map[string]string{"old_path": "admin/settings/logo.png"}, "me.png", pngBytes)), 201, "")
	if _, ok := e.store.objects["admin/settings/logo.png"]; !ok {
		t.Fatal("foreign object deleted")
	}

	e.expect(e.do("POST", "/api/auth/upload", token, newMultipart(t, map[string]string{"folder": "settings"}, "x.png", pngBytes)), 403, "Forbidden")
	e.roles.mu.Lock()
	e.roles.perms[id] = []string{"manage_courses"}
	e.roles.mu.Unlock()
	e.expect(e.do("POST", "/api/auth/upload", token, newMultipart(t, map[string]string{"folder": "courses"}, "x.png", pngBytes)), 201, "")

	r = e.do("POST", "/api/auth/upload", token, newMultipart(t, map[string]string{"folder": "evil"}, "x.txt", []byte("hello")))
	e.expect(r, 422, "The file field must be an image. (and 1 more error)")
	if fmt.Sprint(r.Body["errors"].(map[string]any)["folder"]) != "[The selected folder is invalid.]" {
		t.Fatalf("errors: %v", r.Body)
	}
	e.expect(e.do("POST", "/api/auth/upload", token, newMultipart(t, nil, "", nil)), 422, "The file field is required.")
	big := append(append([]byte{}, pngBytes...), make([]byte, 5121*1024)...)
	e.expect(e.do("POST", "/api/auth/upload", token, newMultipart(t, nil, "big.png", big)), 422, "The file field must not be greater than 5120 kilobytes.")
}

func TestGoogleOAuth(t *testing.T) {
	e := setup(t, false)
	email := e.email("gwen")
	e.google.user = GoogleUser{ID: "sub-" + e.run, Email: email, Name: "Gwen G", Avatar: "https://lh3.googleusercontent.com/a/gwen"}

	r := e.do("GET", "/api/auth/google/url", "", nil)
	e.expect(r, 200, "")
	u, _ := url.Parse(r.Body["url"].(string))
	state := u.Query().Get("state")
	if state == "" {
		t.Fatal("no state")
	}

	r = e.do("GET", "/api/auth/google/callback?code=good&state=bogus&format=json", "", nil)
	e.expect(r, 422, "Google authentication failed: invalid state")
	r = e.do("GET", "/api/auth/google/callback?code=bad&state="+url.QueryEscape(state), "", nil)
	e.expect(r, 422, "Google authentication failed: invalid_grant")

	r = e.do("GET", "/api/auth/google/callback?code=good&format=json&state="+url.QueryEscape(state), "", nil)
	e.expect(r, 200, "Google login success")
	user := r.Body["user"].(map[string]any)
	if user["email"] != email || user["email_verified"] != true || user["avatar"] != "https://lh3.googleusercontent.com/a/gwen" || user["role"] != "student" {
		t.Fatalf("google user: %v", user)
	}
	e.expect(e.do("GET", "/api/auth/me", r.Body["access_token"].(string), nil), 200, "")
	var sub string
	e.db.Raw(`SELECT provider_subject FROM user_identities WHERE user_id = ?`, e.userID(email)).Scan(&sub)
	if sub != "sub-"+e.run {
		t.Fatalf("identity %q", sub)
	}
	var hash *string
	e.db.Raw(`SELECT password_hash FROM users WHERE email = ?`, email).Scan(&hash)
	if hash != nil {
		t.Fatal("OAuth-only account must not have a password")
	}
	e.expect(e.do("POST", "/api/auth/login", "", map[string]any{"email": email, "password": ""}), 422, "")

	// Browser navigation: redirect to the frontend with the token / error.
	r = e.do("GET", "/api/auth/google/callback?code=good&state="+url.QueryEscape(state), "", nil, "Accept", "text/html")
	if r.Code != http.StatusFound || !strings.HasPrefix(r.Header.Get("Location"), "http://localhost:3000/auth/google?token=") {
		t.Fatalf("redirect: %d %v", r.Code, r.Header)
	}
	tok, _ := url.Parse(r.Header.Get("Location"))
	e.expect(e.do("GET", "/api/auth/me", tok.Query().Get("token"), nil), 200, "")
	r = e.do("GET", "/api/auth/google/callback?code=bad&state="+url.QueryEscape(state), "", nil, "Accept", "text/html")
	if r.Code != http.StatusFound || r.Header.Get("Location") != "http://localhost:3000/auth/google?error=Google+authentication+failed%3A+invalid_grant" {
		t.Fatalf("error redirect: %v", r.Header.Get("Location"))
	}
	r = e.do("GET", "/api/auth/google/redirect", "", nil, "Accept", "text/html")
	if r.Code != http.StatusFound || !strings.HasPrefix(r.Header.Get("Location"), "https://accounts.google.com/") {
		t.Fatalf("redirect endpoint: %d", r.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := setup(t, true)
	email, _ := e.registerVerified("hank", "secret1")
	for i := 0; i < DefaultLimits.LoginFailures; i++ {
		e.expect(e.do("POST", "/api/auth/login", "", map[string]any{"email": email, "password": "wrong!"}), 401, "")
	}
	r := e.do("POST", "/api/auth/login", "", map[string]any{"email": email, "password": "secret1"})
	e.expect(r, 429, "Too Many Attempts.")
	if r.Header.Get("Retry-After") == "" || r.Header.Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("429 headers: %v", r.Header)
	}
	// Other accounts from the same IP are unaffected.
	other, _ := e.registerVerified("ivy", "secret1")
	e.login(other, "secret1")

	// Per-IP route throttle.
	var last resp
	for i := 0; i <= DefaultLimits.Sensitive; i++ {
		last = e.do("POST", "/api/auth/resend-verification-email", "", map[string]any{"email": other})
	}
	e.expect(last, 429, "Too Many Attempts.")
}
