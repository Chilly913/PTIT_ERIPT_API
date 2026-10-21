package content

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/crud"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

func siteURLCheck(v map[string]any) map[string]string {
	out := map[string]string{}
	if l, _ := v["logo"].(string); l != "" && !strings.HasPrefix(l, "/") && !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
		out["logo"] = "The logo must be an image URL or a site path starting with /."
	}
	if w, _ := v["website_url"].(string); w != "" && !strings.HasPrefix(w, "http://") && !strings.HasPrefix(w, "https://") {
		out["website_url"] = "The website url must start with http:// or https://."
	}
	return out
}

// partnersResource is the admin CRUD (list/sort/filter/export/import) of the partner logo wall.
var partnersResource = &crud.Resource{
	Path: "partners", Table: "partners", File: "doi-tac", UniqueKey: "name",
	Fields: []crud.Field{
		{Key: "name", Col: "name", Kind: "text", Required: true, Max: 150},
		{Key: "logo", Col: "logo_path", Kind: "text", Required: true, Max: 2048},
		{Key: "website_url", Col: "website_url", Kind: "text", Max: 2048},
		{Key: "sort_order", Col: "sort_order", Kind: "int", Min: 0, Max: 100000, Default: int64(0)},
		{Key: "is_active", Col: "is_active", Kind: "bool", DefBool: true},
	},
	Select:      "t.id, t.name, t.logo_path AS logo, t.website_url, t.sort_order, t.is_active, t.created_at",
	From:        "partners t",
	Search:      []string{"t.name", "t.website_url"},
	Sort:        map[string]string{"id": "t.id", "name": "t.name", "sort_order": "t.sort_order", "is_active": "t.is_active", "created_at": "t.created_at"},
	DefaultSort: "sort_order",
	Filters:     []crud.Filter{{Param: "is_active", Expr: "t.is_active = ?", Bool: true}},
	Check:       siteURLCheck,
	Export:      []crud.ExportCol{{Header: "name", Key: "name"}, {Header: "logo", Key: "logo"}, {Header: "website_url", Key: "website_url"}, {Header: "sort_order", Key: "sort_order"}, {Header: "is_active", Key: "is_active"}},
}

// newsSortKeys maps the `sort` query parameter to SQL.
var newsSortKeys = map[string]string{
	"title": "title", "status": "status", "is_featured": "is_featured", "updated_at": "updated_at",
	"published_at": "published_at", "created_at": "created_at",
}

func newsOrder(c *gin.Context) string {
	col, ok := newsSortKeys[c.Query("sort")]
	if !ok {
		return "COALESCE(published_at, created_at) DESC, id DESC"
	}
	dir := "ASC"
	if strings.EqualFold(c.Query("dir"), "desc") {
		dir = "DESC"
	}
	return col + " " + dir + " NULLS LAST, id DESC"
}

func (m *Module) adminExportNews(c *gin.Context) {
	q := m.db.Table("news_posts")
	if s := strings.TrimSpace(c.Query("search")); s != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
		q = q.Where("(title ILIKE ? OR excerpt ILIKE ?)", like, like)
	}
	if st := c.Query("status"); st == "draft" || st == "published" {
		q = q.Where("status = ?", st)
	}
	var rows []adminNewsDetail
	if err := q.Select(adminNewsCols).Order(newsOrder(c)).Scan(&rows).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF")
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"title", "slug", "excerpt", "content", "cover_image", "status", "is_featured", "published_at"})
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, r := range rows {
		pub := ""
		if r.PublishedAt != nil {
			pub = r.PublishedAt.Time.Format(time.RFC3339)
		}
		_ = w.Write([]string{r.Title, r.Slug, str(r.Excerpt), str(r.Content), str(r.Cover), r.Status, fmt.Sprint(r.IsFeatured), pub})
	}
	w.Flush()
	c.Header("Content-Disposition", `attachment; filename="tin-tuc.csv"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
}

type newsImportError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// adminImportNews creates posts from a CSV (title, slug, excerpt, content, cover_image, status, is_featured).
func (m *Module) adminImportNews(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "The file field is required."))
		return
	}
	if fh.Size > 10<<20 {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "The file must not be greater than 10240 kilobytes."))
		return
	}
	f, err := fh.Open()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	defer f.Close()
	raw, _ := io.ReadAll(f)
	cr := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, []byte("\xEF\xBB\xBF"))))
	cr.FieldsPerRecord = -1
	recs, err := cr.ReadAll()
	if err != nil || len(recs) < 2 || len(recs) > 501 {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "The file must be a CSV with a header row and between 1 and 500 data rows."))
		return
	}
	idx := map[string]int{}
	for i, h := range recs[0] {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(rec []string, key string) string {
		if i, ok := idx[key]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	created, errs := 0, []newsImportError{}
	for n, rec := range recs[1:] {
		row := n + 2
		title := get(rec, "title")
		status := get(rec, "status")
		if status == "" {
			status = "draft"
		}
		switch {
		case title == "":
			errs = append(errs, newsImportError{row, "The title field is required."})
			continue
		case len([]rune(title)) > 255:
			errs = append(errs, newsImportError{row, "The title must not be greater than 255 characters."})
			continue
		case status != "draft" && status != "published":
			errs = append(errs, newsImportError{row, "The selected status is invalid."})
			continue
		}
		excerpt, content, cover := get(rec, "excerpt"), get(rec, "content"), get(rec, "cover_image")
		if len([]rune(excerpt)) > 500 {
			errs = append(errs, newsImportError{row, "The excerpt must not be greater than 500 characters."})
			continue
		}
		base := slugify(get(rec, "slug"))
		if base == "" {
			base = slugify(title)
		}
		slug, err := m.uniqueSlug(base, 0)
		if err != nil {
			errs = append(errs, newsImportError{row, "could not be saved"})
			continue
		}
		var published any
		if status == "published" {
			published = time.Now().UTC()
		}
		featured := strings.EqualFold(get(rec, "is_featured"), "true") || get(rec, "is_featured") == "1"
		err = m.db.Exec(`INSERT INTO news_posts (author_id, title, slug, excerpt, content, cover_image_path, status, is_featured, published_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, auth.UserID(c), title, slug, nilIfBlank(&excerpt), nilIfBlank(&content), nilIfBlank(&cover), status, featured, published).Error
		if err != nil {
			errs = append(errs, newsImportError{row, "could not be saved"})
			continue
		}
		created++
	}
	c.JSON(http.StatusOK, gin.H{"created": created, "failed": len(errs), "errors": errs})
}
