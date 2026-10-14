package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

const (
	msgGoogleNotConfigured = "Google OAuth chưa được cấu hình. Vui lòng thiết lập GOOGLE_CLIENT_ID và GOOGLE_CLIENT_SECRET trong file .env"
	msgGoogleFailed        = "Google authentication failed: "
	msgGoogleNoEmail       = "Google account does not provide an email"
	msgGoogleSuccess       = "Google login success"
	googleStateTTL         = 30 * time.Minute
)

// GoogleUser is the profile returned by Google's userinfo endpoint.
type GoogleUser struct {
	ID     string
	Email  string
	Name   string
	Avatar string
}

// GoogleProvider is the OAuth client (faked in tests).
type GoogleProvider interface {
	AuthCodeURL(state string) string
	User(ctx context.Context, code string) (GoogleUser, error)
}

// Endpoints used by Laravel Socialite's Google driver.
var googleEndpoint = oauth2.Endpoint{
	AuthURL:   "https://accounts.google.com/o/oauth2/auth",
	TokenURL:  "https://www.googleapis.com/oauth2/v4/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

const googleUserInfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"

type googleOAuth struct{ cfg *oauth2.Config }

func newGoogleOAuth(g config.GoogleConfig) *googleOAuth {
	return &googleOAuth{cfg: &oauth2.Config{
		ClientID:     g.ClientID,
		ClientSecret: g.ClientSecret,
		RedirectURL:  g.RedirectURL,
		Scopes:       []string{"openid", "profile", "email"},
		Endpoint:     googleEndpoint,
	}}
}

func (g *googleOAuth) AuthCodeURL(state string) string { return g.cfg.AuthCodeURL(state) }

func (g *googleOAuth) User(ctx context.Context, code string) (GoogleUser, error) {
	if code == "" {
		return GoogleUser{}, errors.New("missing authorization code")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tok, err := g.cfg.Exchange(ctx, code)
	if err != nil {
		return GoogleUser{}, err
	}
	resp, err := g.cfg.Client(ctx, tok).Get(googleUserInfoURL)
	if err != nil {
		return GoogleUser{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return GoogleUser{}, fmt.Errorf("userinfo: HTTP %d", resp.StatusCode)
	}
	var info struct {
		Sub     string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return GoogleUser{}, err
	}
	return GoogleUser{ID: info.Sub, Email: info.Email, Name: info.Name, Avatar: info.Picture}, nil
}

// state is "<expires>.<hmac>" (stateless, like Socialite::stateless(), but
// still CSRF-protected).
func (m *Module) googleState() string {
	exp := strconv.FormatInt(m.now().Add(googleStateTTL).Unix(), 10)
	return exp + "." + m.signer.Sign("google-oauth-state", exp)
}

func (m *Module) validGoogleState(s string) bool {
	exp, sig, ok := strings.Cut(s, ".")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || m.now().Unix() > n {
		return false
	}
	return m.signer.Valid(sig, "google-oauth-state", exp)
}

func (m *Module) googleLoginURL(c *gin.Context) {
	if m.google == nil {
		httpx.Abort(c, http.StatusUnprocessableEntity, msgGoogleNotConfigured)
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": m.google.AuthCodeURL(m.googleState())})
}

func (m *Module) redirectToGoogle(c *gin.Context) {
	if m.google == nil {
		httpx.Abort(c, http.StatusUnprocessableEntity, msgGoogleNotConfigured)
		return
	}
	c.Redirect(http.StatusFound, m.google.AuthCodeURL(m.googleState()))
}

// wantsGoogleBrowserRedirect mirrors the legacy helper: JSON unless the
// request is a browser navigation.
func wantsGoogleBrowserRedirect(c *gin.Context) bool {
	if c.Query("format") == "json" {
		return false
	}
	accept := c.GetHeader("Accept")
	if strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html") {
		return false
	}
	return true
}

func (m *Module) frontendURL() string {
	if m.cfg.App.FrontendURL != "" {
		return m.cfg.App.FrontendURL
	}
	if m.cfg.App.URL != "" {
		return m.cfg.App.URL
	}
	return "http://localhost:3000"
}

func (m *Module) googleFail(c *gin.Context, redirect bool, msg string) {
	if redirect {
		c.Redirect(http.StatusFound, m.frontendURL()+"/auth/google?error="+url.QueryEscape(msg))
		return
	}
	httpx.Abort(c, http.StatusUnprocessableEntity, msg)
}

func (m *Module) handleGoogleCallback(c *gin.Context) {
	ctx := c.Request.Context()
	redirect := wantsGoogleBrowserRedirect(c)
	if m.google == nil {
		m.googleFail(c, redirect, msgGoogleFailed+"Google OAuth is not configured")
		return
	}
	if e := c.Query("error"); e != "" {
		m.googleFail(c, redirect, msgGoogleFailed+e)
		return
	}
	if !m.validGoogleState(c.Query("state")) {
		m.googleFail(c, redirect, msgGoogleFailed+"invalid state")
		return
	}
	gu, err := m.google.User(ctx, c.Query("code"))
	if err != nil {
		m.googleFail(c, redirect, msgGoogleFailed+err.Error())
		return
	}
	if gu.Email == "" {
		m.googleFail(c, redirect, msgGoogleNoEmail)
		return
	}

	var user *auth.User
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		u, err := auth.FindUserByEmail(ctx, tx, gu.Email)
		if errors.Is(err, auth.ErrUserNotFound) {
			taken, err := auth.EmailExists(ctx, tx, gu.Email)
			if err != nil {
				return err
			}
			if taken { // soft-deleted account keeps its address
				return errAccountUnavailable
			}
			name := gu.Name
			if name == "" {
				name = "Google User"
			}
			now := m.now()
			u = &auth.User{Name: name, Email: gu.Email, EmailVerifiedAt: &now, UserType: "student"}
			if err := tx.Create(u).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		updates := map[string]any{"avatar_path": nilIfEmpty(gu.Avatar)}
		if gu.Name != "" {
			updates["name"] = gu.Name
		}
		if err := tx.Model(u).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM user_identities WHERE provider = 'google' AND (provider_subject = ? OR user_id = ?)`, gu.ID, u.ID).Error; err != nil {
			return err
		}
		if gu.ID != "" {
			if err := tx.Exec(`INSERT INTO user_identities (user_id, provider, provider_subject, email, last_login_at) VALUES (?, 'google', ?, ?, ?)`,
				u.ID, gu.ID, gu.Email, m.now()).Error; err != nil {
				return err
			}
		}
		user = u
		return nil
	})
	if errors.Is(err, errAccountUnavailable) {
		m.googleFail(c, redirect, msgGoogleFailed+err.Error())
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if user, err = auth.FindUserByID(ctx, m.db, user.ID); err != nil {
		httpx.Fail(c, err)
		return
	}

	if redirect {
		var tok auth.IssuedToken
		err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := m.assignDefaultRole(ctx, tx, user.ID, nil); err != nil {
				return err
			}
			var err error
			tok, err = m.tokens.Issue(ctx, tx, user.ID, auth.DefaultTokenName)
			return err
		})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Redirect(http.StatusFound, m.frontendURL()+"/auth/google?token="+url.QueryEscape(tok.PlainText))
		return
	}
	m.respondWithToken(c, user, nil, msgGoogleSuccess)
}

var errAccountUnavailable = errors.New("account is unavailable")

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
