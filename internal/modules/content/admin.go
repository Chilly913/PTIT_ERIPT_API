package content

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/textutil"
)

// registerAdmin mounts the admin-only management routes under /api/admin.
func (m *Module) registerAdmin(api *gin.RouterGroup) {
	g := api.Group("/admin", m.tokens.Required(), auth.RequireRole(m.roles, auth.RoleAdmin))
	g.GET("/news", m.adminListNews)
	g.GET("/news/export", m.adminExportNews)
	g.POST("/news/import", m.adminImportNews)
	g.POST("/news", m.adminSaveNews)
	g.GET("/news/:id", m.adminShowNews)
	g.PUT("/news/:id", m.adminSaveNews)
	g.DELETE("/news/:id", m.adminDeleteNews)

	partnersResource.Mount(g, m.db)
}

func adminPage(c *gin.Context, data any, total int64, p httpx.PageParams) {
	last := int((total + int64(p.PerPage) - 1) / int64(p.PerPage))
	if last < 1 {
		last = 1
	}
	c.JSON(http.StatusOK, gin.H{"data": data, "meta": gin.H{"page": p.Page, "per_page": p.PerPage, "total": total, "last_page": last}})
}

func idParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		httpx.NotFound(c, "")
		return 0, false
	}
	return id, true
}

// ---- news ---------------------------------------------------------------

type adminNewsRow struct {
	ID          int64       `json:"id"`
	Title       string      `json:"title"`
	Slug        string      `json:"slug"`
	Excerpt     *string     `json:"excerpt"`
	Cover       *string     `json:"cover_image" gorm:"column:cover_image_path"`
	Status      string      `json:"status"`
	IsFeatured  bool        `json:"is_featured"`
	PublishedAt *httpx.Time `json:"published_at"`
	UpdatedAt   httpx.Time  `json:"updated_at"`
}

type adminNewsDetail struct {
	ID          int64       `json:"id"`
	Title       string      `json:"title"`
	Slug        string      `json:"slug"`
	Excerpt     *string     `json:"excerpt"`
	Content     *string     `json:"content"`
	Cover       *string     `json:"cover_image" gorm:"column:cover_image_path"`
	Status      string      `json:"status"`
	IsFeatured  bool        `json:"is_featured"`
	PublishedAt *httpx.Time `json:"published_at"`
	UpdatedAt   httpx.Time  `json:"updated_at"`
}

const adminNewsCols = "id, title, slug, excerpt, content, cover_image_path, status, is_featured, published_at, updated_at"

func (m *Module) adminListNews(c *gin.Context) {
	p := httpx.ParsePage(c, 15, 100)
	q := m.db.Table("news_posts")
	if s := strings.TrimSpace(c.Query("search")); s != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
		q = q.Where("(title ILIKE ? OR excerpt ILIKE ?)", like, like)
	}
	if st := c.Query("status"); st == "draft" || st == "published" {
		q = q.Where("status = ?", st)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows := []adminNewsRow{}
	err := q.Select("id, title, slug, excerpt, cover_image_path, status, is_featured, published_at, updated_at").
		Order(newsOrder(c)).Limit(p.PerPage).Offset((p.Page - 1) * p.PerPage).Scan(&rows).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	adminPage(c, rows, total, p)
}

func (m *Module) adminShowNews(c *gin.Context) {
	id, ok := idParam(c)
	if !ok {
		return
	}
	var d adminNewsDetail
	res := m.db.Table("news_posts").Where("id = ?", id).Select(adminNewsCols).Limit(1).Scan(&d)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	c.JSON(http.StatusOK, d)
}

type newsRequest struct {
	Title       string  `json:"title" binding:"required,max=255"`
	Slug        string  `json:"slug" binding:"omitempty,max=200"`
	Excerpt     *string `json:"excerpt" binding:"omitempty,max=500"`
	Content     *string `json:"content"`
	CoverImage  *string `json:"cover_image" binding:"omitempty,max=2048"`
	Status      string  `json:"status" binding:"required,oneof=draft published"`
	IsFeatured  bool    `json:"is_featured"`
	PublishedAt *string `json:"published_at"`
}

// slugify turns a Vietnamese title into an ASCII url slug.
func slugify(s string) string { return textutil.Slugify(s) }

// uniqueSlug appends -2, -3, ... until the slug is free (ignoring the row being edited).
func (m *Module) uniqueSlug(base string, selfID int64) (string, error) {
	if base == "" {
		base = "bai-viet"
	}
	slug := base
	for i := 2; ; i++ {
		var n int64
		if err := m.db.Table("news_posts").Where("slug = ? AND id <> ?", slug, selfID).Count(&n).Error; err != nil {
			return "", err
		}
		if n == 0 {
			return slug, nil
		}
		slug = base + "-" + strconv.Itoa(i)
	}
}

func nilIfBlank(s *string) any {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	v := strings.TrimSpace(*s)
	return v
}

func (m *Module) adminSaveNews(c *gin.Context) {
	var id int64
	editing := c.Request.Method == http.MethodPut
	if editing {
		var ok bool
		if id, ok = idParam(c); !ok {
			return
		}
	}
	var req newsRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	req.Title = strings.TrimSpace(req.Title)

	var publishedAt any
	switch {
	case req.PublishedAt != nil && strings.TrimSpace(*req.PublishedAt) != "":
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(*req.PublishedAt))
		if err != nil {
			httpx.Fail(c, httpx.NewValidationError().Add("published_at", "The published at field must be a valid date."))
			return
		}
		publishedAt = t
	case req.Status == "published":
		publishedAt = time.Now().UTC()
	}

	base := slugify(req.Slug)
	if base == "" {
		base = slugify(req.Title)
	}
	slug, err := m.uniqueSlug(base, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	var out adminNewsDetail
	if editing {
		res := m.db.Raw(`UPDATE news_posts SET title = ?, slug = ?, excerpt = ?, content = ?, cover_image_path = ?, status = ?,
				is_featured = ?, published_at = COALESCE(?::timestamptz, CASE WHEN ? = 'published' THEN published_at END)
			WHERE id = ? RETURNING `+adminNewsCols,
			req.Title, slug, nilIfBlank(req.Excerpt), req.Content, nilIfBlank(req.CoverImage), req.Status,
			req.IsFeatured, publishedAt, req.Status, id).Scan(&out)
		if res.Error != nil {
			httpx.Fail(c, res.Error)
			return
		}
		if res.RowsAffected == 0 {
			httpx.NotFound(c, "")
			return
		}
		c.JSON(http.StatusOK, out)
		return
	}
	res := m.db.Raw(`INSERT INTO news_posts (author_id, title, slug, excerpt, content, cover_image_path, status, is_featured, published_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING `+adminNewsCols,
		auth.UserID(c), req.Title, slug, nilIfBlank(req.Excerpt), req.Content, nilIfBlank(req.CoverImage), req.Status,
		req.IsFeatured, publishedAt).Scan(&out)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (m *Module) adminDeleteNews(c *gin.Context) {
	id, ok := idParam(c)
	if !ok {
		return
	}
	res := m.db.Exec("DELETE FROM news_posts WHERE id = ?", id)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}
