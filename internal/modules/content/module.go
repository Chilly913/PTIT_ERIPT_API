// Package content serves the public marketing content of the site: news posts,
// cooperation partners and the non-secret site settings (name, footer, contact).
//
//	GET /api/news            published posts, newest first (Laravel-style paginator)
//	GET /api/news/:slug      one published post with its HTML content
//	GET /api/partners        active partners ordered by sort_order
//	GET /api/site-settings   public site settings
package content

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Module implements app.Module.
type Module struct {
	db     *gorm.DB
	tokens *auth.TokenService
	roles  auth.RoleService
}

// New builds the module from the app container; tokens and roles guard the admin routes.
func New(a *app.App, tokens *auth.TokenService, roles auth.RoleService) *Module {
	return &Module{db: a.DB, tokens: tokens, roles: roles}
}

// Register mounts the public routes.
func (m *Module) Register(api *gin.RouterGroup) {
	api.GET("/news", m.listNews)
	api.GET("/news/:slug", m.showNews)
	api.GET("/partners", m.listPartners)
	api.GET("/site-settings", m.siteSettings)
	api.GET("/uploads/*path", m.serveUpload)
	api.POST("/uploads", m.tokens.Required(), m.adminUpload) // any signed-in user (admins, instructors) may upload images
	m.registerAdmin(api)
}

type newsRow struct {
	ID          int64      `json:"id"`
	Title       string     `json:"title"`
	Slug        string     `json:"slug"`
	Excerpt     *string    `json:"excerpt"`
	Cover       *string    `json:"cover_image" gorm:"column:cover_image_path"`
	IsFeatured  bool       `json:"is_featured"`
	PublishedAt httpx.Time `json:"published_at"`
}

type newsDetail struct {
	ID          int64      `json:"id"`
	Title       string     `json:"title"`
	Slug        string     `json:"slug"`
	Excerpt     *string    `json:"excerpt"`
	Content     *string    `json:"content"`
	Cover       *string    `json:"cover_image" gorm:"column:cover_image_path"`
	IsFeatured  bool       `json:"is_featured"`
	PublishedAt httpx.Time `json:"published_at"`
}

const publishedWhere = "status = 'published' AND published_at <= now()"

func (m *Module) listNews(c *gin.Context) {
	page := atoiDefault(c.Query("page"), 1)
	per := atoiDefault(c.Query("per_page"), 12)
	if page < 1 {
		page = 1
	}
	if per < 1 || per > 50 {
		per = 12
	}

	q := m.db.Table("news_posts").Where(publishedWhere)
	if s := strings.TrimSpace(c.Query("search")); s != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
		q = q.Where("(title ILIKE ? OR excerpt ILIKE ?)", like, like)
	}
	if c.Query("featured") == "1" {
		q = q.Where("is_featured")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows := []newsRow{}
	err := q.Select("id, title, slug, excerpt, cover_image_path, is_featured, published_at").
		Order("published_at DESC, id DESC").Limit(per).Offset((page - 1) * per).Scan(&rows).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	last := int((total + int64(per) - 1) / int64(per))
	if last < 1 {
		last = 1
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "current_page": page, "last_page": last, "per_page": per, "total": total})
}

func (m *Module) showNews(c *gin.Context) {
	var d newsDetail
	res := m.db.Table("news_posts").Where(publishedWhere).Where("slug = ?", c.Param("slug")).
		Select("id, title, slug, excerpt, content, cover_image_path, is_featured, published_at").Limit(1).Scan(&d)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.Fail(c, httpx.ErrNotFound)
		return
	}
	c.JSON(http.StatusOK, d)
}

type partnerRow struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Logo       string  `json:"logo" gorm:"column:logo_path"`
	WebsiteURL *string `json:"website_url"`
}

func (m *Module) listPartners(c *gin.Context) {
	rows := []partnerRow{}
	err := m.db.Table("partners").Where("is_active").Select("id, name, logo_path, website_url").
		Order("sort_order, id").Scan(&rows).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, rows)
}

// publicSettingKeys are the only site_settings keys exposed without authentication.
var publicSettingKeys = []string{"site_name", "site_logo", "footer_text", "contact_email", "contact_phone", "contact_address", "social_links"}

func (m *Module) siteSettings(c *gin.Context) {
	var kv []struct{ Key, Value string }
	err := m.db.Table("site_settings").Where("key IN ? AND value IS NOT NULL", publicSettingKeys).Select("key, value").Scan(&kv).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := map[string]any{
		"site_name": "", "site_logo": nil, "footer_text": "", "contact_email": "", "contact_phone": "", "contact_address": "",
		"social_links": map[string]string{},
	}
	for _, r := range kv {
		if r.Key == "social_links" {
			links := map[string]string{}
			if json.Unmarshal([]byte(r.Value), &links) == nil {
				out["social_links"] = links
			}
			continue
		}
		out[r.Key] = r.Value
	}
	c.JSON(http.StatusOK, out)
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
