package catalog

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/rbac"
)

type gormDB = gorm.DB

// listSections is GET /courses/{course}/sections: the full builder tree for
// admins, the owner and manage_courses holders; the public outline of
// published courses for everybody else (404 "Course not found" otherwise).
func (m *Module) listSections(c *gin.Context) {
	ctx := c.Request.Context()
	course := boundCourse(c)
	who, authed, err := m.caller(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	canManage := authed && (who.IsOwner(course) || who.Allows(rbac.PermManageCourses))
	if !canManage && course.Status != "published" {
		httpx.NotFound(c, "Course not found")
		return
	}
	lessons, err := m.lessonsOf(ctx, "l.course_id = ? AND l.section_id IS NOT NULL", course.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !canManage {
		out, err := m.sectionOutlines(ctx, course.ID, lessons)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": out, "message": "Sections retrieved successfully"})
		return
	}

	sections, err := m.sectionsOf(ctx, course.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ids := lessonIDs(lessons)
	atts, err := m.attachmentsOf(ctx, ids)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	vcs, err := m.virtualClassesOf(ctx, ids)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	scorms, err := m.scormPackagesOf(ctx, ids)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	assignments, err := m.assignmentsOf(ctx, ids)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	bySection := map[int64][]lessonBuilder{}
	for _, l := range lessons {
		a := atts[l.ID]
		if a == nil {
			a = []attachmentJSON{}
		}
		bySection[*l.SectionID] = append(bySection[*l.SectionID], lessonBuilder{
			lessonJSON: m.lessonJSON(l), Attachments: a,
			VirtualClass: vcs[l.ID], ScormPackage: scorms[l.ID], Assignment: assignments[l.ID],
		})
	}
	out := make([]sectionBuilder, len(sections))
	for i, s := range sections {
		ls := bySection[s.ID]
		if ls == nil {
			ls = []lessonBuilder{}
		}
		out[i] = sectionBuilder{sectionJSON: s.json(), Lessons: ls}
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "message": "Sections retrieved successfully"})
}

// storeSection is POST /courses/{course}/sections.
func (m *Module) storeSection(c *gin.Context) {
	ctx := c.Request.Context()
	course := boundCourse(c)
	if !m.ownerOnly(c, course, "Unauthorized") {
		return
	}
	in, err := readInput(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v := newValidator(in)
	v.field("title").required().string().max(255)
	v.field("description").nullableRule().string()
	v.field("position").nullableRule().integer().min(0)
	if err := v.err(); err != nil {
		httpx.Fail(c, err)
		return
	}
	var position int64
	if in.get("position") != nil {
		position = phpInt(in.get("position"))
	} else if err := m.db.WithContext(ctx).Raw(`SELECT coalesce(max(position), 0) + 1 FROM sections WHERE course_id = ?`, course.ID).Scan(&position).Error; err != nil {
		httpx.Fail(c, fmtErr("max section position", err))
		return
	}
	var id int64
	if err := m.db.WithContext(ctx).Raw(`INSERT INTO sections (course_id, title, description, position) VALUES (?, ?, ?, ?) RETURNING id`,
		course.ID, in.str("title"), nullableStr(in, "description"), position).Scan(&id).Error; err != nil {
		httpx.Fail(c, fmtErr("create section", err))
		return
	}
	row, err := m.findSection(ctx, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": row.json(), "message": "Section created successfully"})
}

// showSection is GET /sections/{section} (any manage_courses holder, like legacy).
func (m *Module) showSection(c *gin.Context) {
	ctx := c.Request.Context()
	row, err := m.findSection(ctx, boundSection(c).ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	lessons, err := m.lessonsOf(ctx, "l.section_id = ?", row.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := sectionWithLessons{sectionJSON: row.json(), Lessons: make([]lessonJSON, len(lessons))}
	for i, l := range lessons {
		out.Lessons[i] = m.lessonJSON(l)
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "message": "Section retrieved successfully"})
}

// sectionOwner checks ownership of the section's course (403 "Unauthorized").
func (m *Module) sectionOwner(c *gin.Context) (learning.SectionRef, bool) {
	sec := boundSection(c)
	course, err := m.findCourse(c.Request.Context(), sec.CourseID)
	if err != nil {
		httpx.Fail(c, err)
		return sec, false
	}
	return sec, m.ownerOnly(c, course.ref(), "Unauthorized")
}

// updateSection is PUT /sections/{section}.
func (m *Module) updateSection(c *gin.Context) {
	ctx := c.Request.Context()
	sec, ok := m.sectionOwner(c)
	if !ok {
		return
	}
	in, err := readInput(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v := newValidator(in)
	v.field("title").sometimes().string().max(255)
	v.field("description").nullableRule().string()
	v.field("position").sometimes().integer().min(0)
	if err := v.err(); err != nil {
		httpx.Fail(c, err)
		return
	}
	set := map[string]any{}
	if in.has("title") {
		set["title"] = in.str("title")
	}
	if in.has("description") {
		set["description"] = nullableStr(in, "description")
	}
	if in.has("position") {
		set["position"] = phpInt(in.get("position"))
	}
	if err := m.updateRow(ctx, "sections", sec.ID, set, nil); err != nil {
		httpx.Fail(c, err)
		return
	}
	row, err := m.findSection(ctx, sec.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": row.json(), "message": "Section updated successfully"})
}

// destroySection is DELETE /sections/{section} (refused while it has lessons).
func (m *Module) destroySection(c *gin.Context) {
	ctx := c.Request.Context()
	sec, ok := m.sectionOwner(c)
	if !ok {
		return
	}
	var n int64
	if err := m.db.WithContext(ctx).Raw(`SELECT count(*) FROM lessons WHERE section_id = ?`, sec.ID).Scan(&n).Error; err != nil {
		httpx.Fail(c, fmtErr("count section lessons", err))
		return
	}
	if n > 0 {
		httpx.Message(c, http.StatusUnprocessableEntity, "Cannot delete section with lessons. Please delete lessons first.")
		return
	}
	if err := m.db.WithContext(ctx).Exec(`DELETE FROM sections WHERE id = ?`, sec.ID).Error; err != nil {
		httpx.Fail(c, fmtErr("delete section", err))
		return
	}
	httpx.Message(c, http.StatusOK, "Section deleted successfully")
}

// reorderSections is POST /courses/{course}/sections/reorder
// ({"sections": [{"id", "position"}]}).
func (m *Module) reorderSections(c *gin.Context) {
	ctx := c.Request.Context()
	course := boundCourse(c)
	if !m.ownerOnly(c, course, "Unauthorized") {
		return
	}
	in, err := readInput(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v := newValidator(in)
	v.field("sections").required().array()
	keys, vals := arrayItems(in.get("sections"))
	type item struct{ id, position int64 }
	items := make([]item, 0, len(vals))
	for i, k := range keys {
		obj, _ := vals[i].(map[string]any)
		id, hasID := obj["id"]
		pos, hasPos := obj["position"]
		v.value("sections."+k+".id", id, hasID).required().exists(m.existsIn(ctx, "sections"))
		v.value("sections."+k+".position", pos, hasPos).required().integer().min(0)
		items = append(items, item{phpInt(id), phpInt(pos)})
	}
	if err := v.err(); err != nil {
		httpx.Fail(c, err)
		return
	}
	err = m.db.WithContext(ctx).Transaction(func(tx *gormDB) error {
		for _, it := range items {
			if err := tx.Exec(`UPDATE sections SET position = ? WHERE id = ? AND course_id = ?`, it.position, it.id, course.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, fmtErr("reorder sections", err))
		return
	}
	httpx.Message(c, http.StatusOK, "Sections reordered successfully")
}
