package catalog

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
)

// programByCategorySlug maps a root category to its training-programme code
// (CourseController::categories counts curriculum courses of that programme).
var programByCategorySlug = map[string]string{
	"cong-nghe-thong-tin": "CNTT",
	"quan-tri-kinh-doanh": "QTKD",
	"dien-tu-vien-thong":  "DTVT",
}

// categoryTree is the whole (small) category table with published course counts.
type categoryTree struct {
	rows     []categoryRow // ordered by sort_order, name, id
	children map[int64][]categoryRow
	counts   map[int64]int64 // direct published courses per category
}

func (m *Module) loadCategoryTree(ctx context.Context) (*categoryTree, error) {
	t := &categoryTree{children: map[int64][]categoryRow{}, counts: map[int64]int64{}}
	db := m.db.WithContext(ctx)
	if err := db.Raw(`SELECT id, parent_id, name, slug, icon, sort_order, created_at, updated_at
FROM categories ORDER BY sort_order, name, id`).Scan(&t.rows).Error; err != nil {
		return nil, fmtErr("load categories", err)
	}
	for _, r := range t.rows {
		if r.ParentID != nil {
			t.children[*r.ParentID] = append(t.children[*r.ParentID], r)
		}
	}
	var counts []struct {
		CategoryID int64 `gorm:"column:category_id"`
		N          int64 `gorm:"column:n"`
	}
	if err := db.Raw(`SELECT category_id, count(*) AS n FROM courses
WHERE status = 'published' AND category_id IS NOT NULL GROUP BY category_id`).Scan(&counts).Error; err != nil {
		return nil, fmtErr("count category courses", err)
	}
	for _, c := range counts {
		t.counts[c.CategoryID] = c.N
	}
	return t, nil
}

// node builds a category with `depth` levels of children loaded. courses_count
// is replaced by the subtree total of the loaded levels
// (CategoryController::applySubtreeCourseCounts).
func (t *categoryTree) node(r categoryRow, depth int) categoryNode {
	n := categoryNode{categoryAttrs: r.attrs(), CoursesCount: t.counts[r.ID]}
	if depth > 0 {
		kids := make([]categoryNode, 0, len(t.children[r.ID]))
		for _, ch := range t.children[r.ID] {
			k := t.node(ch, depth-1)
			n.CoursesCount += k.CoursesCount
			kids = append(kids, k)
		}
		n.Children = &kids
	}
	return n
}

// listCategories is GET /categories: root categories with two levels of children.
func (m *Module) listCategories(c *gin.Context) {
	t, err := m.loadCategoryTree(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := []categoryNode{}
	for _, r := range t.rows {
		if r.ParentID == nil {
			out = append(out, t.node(r, 2))
		}
	}
	httpx.OK(c, out)
}

// findCategory resolves {category} by id (implicit binding).
func (m *Module) findCategory(ctx context.Context, key string) (categoryRow, bool, error) {
	id, ok := learning.RouteID(key)
	if !ok {
		return categoryRow{}, false, nil
	}
	var rows []categoryRow
	if err := m.db.WithContext(ctx).Raw(`SELECT id, parent_id, name, slug, icon, sort_order, created_at, updated_at
FROM categories WHERE id = ?`, id).Scan(&rows).Error; err != nil {
		return categoryRow{}, false, fmtErr("find category", err)
	}
	if len(rows) == 0 {
		return categoryRow{}, false, nil
	}
	return rows[0], true, nil
}

// showCategory is GET /categories/{category}: parent, two levels of children
// and the subtree course count.
func (m *Module) showCategory(c *gin.Context) {
	v, _ := c.Get(ctxCategory)
	cat, _ := v.(categoryRow)
	t, err := m.loadCategoryTree(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	n := t.node(cat, 2)
	out := categoryDetail{categoryAttrs: n.categoryAttrs, CoursesCount: n.CoursesCount, Children: *n.Children}
	if cat.ParentID != nil {
		for _, r := range t.rows {
			if r.ID == *cat.ParentID {
				out.Parent = &categoryParent{ID: r.ID, Name: r.Name, Slug: r.Slug, ParentID: r.ParentID}
				break
			}
		}
	}
	httpx.OK(c, out)
}

// programCategories is GET /courses/categories: root categories with their
// direct children; courses_count = published curriculum courses of the mapped
// programme + published extension courses of the category and its children.
func (m *Module) programCategories(c *gin.Context) {
	ctx := c.Request.Context()
	t, err := m.loadCategoryTree(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	db := m.db.WithContext(ctx)
	out := []programCategory{}
	for _, r := range t.rows {
		if r.ParentID != nil {
			continue
		}
		var curriculum int64
		if code, ok := programByCategorySlug[r.Slug]; ok {
			if err := db.Raw(`SELECT count(DISTINCT cc.course_id)
FROM curriculum_courses cc
JOIN curricula cu ON cu.id = cc.curriculum_id
JOIN programs p ON p.id = cu.program_id
JOIN courses co ON co.id = cc.course_id
WHERE p.code = ? AND co.status = 'published'`, code).Scan(&curriculum).Error; err != nil {
				httpx.Fail(c, fmtErr("count curriculum courses", err))
				return
			}
		}
		ids := []int64{r.ID}
		kids := []categoryAttrs{}
		for _, ch := range t.children[r.ID] {
			ids = append(ids, ch.ID)
			kids = append(kids, ch.attrs())
		}
		var extension int64
		if err := db.Raw(`SELECT count(*) FROM courses
WHERE category_id IN ? AND status = 'published' AND course_mode = 'extension'`, ids).Scan(&extension).Error; err != nil {
			httpx.Fail(c, fmtErr("count extension courses", err))
			return
		}
		out = append(out, programCategory{
			categoryAttrs: r.attrs(), CoursesCount: curriculum + extension,
			CurriculumCoursesCount: curriculum, ExtensionCoursesCount: extension, Children: kids,
		})
	}
	httpx.OK(c, out)
}
