package learning

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// The training plan of a class: which curriculum it follows, which courses (môn) sit in which curriculum term,
// and which real term (học kỳ) each curriculum term maps to, so courses open progressively over the school years.
// All edits go through the class, so only admins, academic managers and the class advisor can change them.

// listTerms: GET /academic/terms — real terms with their academic year, for the term pickers.
func (m *Module) listTerms(c *gin.Context) {
	rows, err := m.jsonRows(`SELECT jsonb_build_object('id', t.id, 'name', t.name, 'code', t.code, 'start_date', t.start_date, 'end_date', t.end_date,
		'is_current', t.is_current, 'year_id', ay.id, 'year', ay.name)
		FROM terms t JOIN academic_years ay ON ay.id = t.academic_year_id ORDER BY t.start_date DESC, t.id`)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"terms": rows})
}

// listCurricula: GET /curricula?program_id=
func (m *Module) listCurricula(c *gin.Context) {
	conds, args := "TRUE", []any{}
	if v := strings.TrimSpace(c.Query("program_id")); v != "" {
		conds, args = "cu.program_id::text = ?", []any{v}
	}
	rows, err := m.jsonRows(`SELECT jsonb_build_object('id', cu.id, 'code', cu.code, 'name', cu.name, 'program_id', cu.program_id,
		'courses', (SELECT count(*) FROM curriculum_courses cc WHERE cc.curriculum_id = cu.id),
		'classes', (SELECT count(*) FROM administrative_classes ac WHERE ac.curriculum_id = cu.id))
		FROM curricula cu WHERE `+conds+` ORDER BY cu.name`, args...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"curricula": rows})
}

// setClassCurriculum: PUT /classes/:id/curriculum {curriculum_id} or {name, code?} to create one for the class's program.
func (m *Module) setClassCurriculum(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok || !m.canManageClass(c, id) {
		return
	}
	var b struct {
		CurriculumID *int64 `json:"curriculum_id"`
		Name         string `json:"name"`
		Code         string `json:"code"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	var class struct {
		ProgramID int64
		Code      string
	}
	if err := m.db.Raw("SELECT program_id, code::text AS code FROM administrative_classes WHERE id = ?", id).Scan(&class).Error; err != nil || class.ProgramID == 0 {
		httpx.NotFound(c, "")
		return
	}
	cid := b.CurriculumID
	if cid == nil && strings.TrimSpace(b.Name) != "" {
		code := strings.TrimSpace(b.Code)
		if code == "" {
			code = "CTDT-" + class.Code
		}
		var dup int64
		_ = m.db.Raw("SELECT count(*) FROM curricula WHERE program_id = ? AND code = ?", class.ProgramID, code).Scan(&dup).Error
		if dup > 0 {
			httpx.Fail(c, httpx.NewValidationError().Add("code", "Mã chương trình đã tồn tại."))
			return
		}
		var nid int64
		if err := m.db.Raw("INSERT INTO curricula (program_id, name, code) VALUES (?, ?, ?) RETURNING id", class.ProgramID, strings.TrimSpace(b.Name), code).Scan(&nid).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
		cid = &nid
	}
	if cid != nil {
		var n int64
		_ = m.db.Raw("SELECT count(*) FROM curricula WHERE id = ? AND program_id = ?", *cid, class.ProgramID).Scan(&n).Error
		if n == 0 {
			httpx.Fail(c, httpx.NewValidationError().Add("curriculum_id", "Chương trình không thuộc ngành của lớp."))
			return
		}
	}
	if err := m.db.Exec("UPDATE administrative_classes SET curriculum_id = ? WHERE id = ?", cid, id).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"curriculum_id": cid})
}

// setClassTerms: PUT /classes/:id/terms {terms: [{term_number, term_id}]} replaces the term mapping (term_id null/0 removes one).
func (m *Module) setClassTerms(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok || !m.canManageClass(c, id) {
		return
	}
	var b struct {
		Terms []struct {
			TermNumber int    `json:"term_number"`
			TermID     *int64 `json:"term_id"`
		} `json:"terms"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	for _, t := range b.Terms {
		if t.TermNumber < 1 || t.TermNumber > 12 {
			httpx.Fail(c, httpx.NewValidationError().Add("terms", "Kỳ phải từ 1 đến 12."))
			return
		}
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		for _, t := range b.Terms {
			if t.TermID == nil || *t.TermID == 0 {
				if e := tx.Exec("DELETE FROM administrative_class_terms WHERE administrative_class_id = ? AND term_number = ?", id, t.TermNumber).Error; e != nil {
					return e
				}
				continue
			}
			if e := tx.Exec(`INSERT INTO administrative_class_terms (administrative_class_id, term_number, term_id) VALUES (?, ?, ?)
				ON CONFLICT (administrative_class_id, term_number) DO UPDATE SET term_id = EXCLUDED.term_id`, id, t.TermNumber, *t.TermID).Error; e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Saved."})
}

// addCurriculumCourse: POST /classes/:id/curriculum-courses {course_id, term_number, is_required, credits}
func (m *Module) addCurriculumCourse(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok || !m.canManageClass(c, id) {
		return
	}
	var b struct {
		CourseID   int64  `json:"course_id"`
		TermNumber int    `json:"term_number"`
		IsRequired *bool  `json:"is_required"`
		Credits    *int   `json:"credits"`
		Notes      string `json:"notes"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	v := httpx.NewValidationError()
	if b.TermNumber < 1 || b.TermNumber > 12 {
		v.Add("term_number", "Kỳ phải từ 1 đến 12.")
	}
	var cur *int64
	_ = m.db.Raw("SELECT curriculum_id FROM administrative_classes WHERE id = ?", id).Scan(&cur).Error
	if cur == nil {
		v.Add("curriculum", "Lớp chưa có chương trình đào tạo, hãy chọn hoặc tạo chương trình trước.")
	}
	var n int64
	_ = m.db.Raw("SELECT count(*) FROM courses WHERE id = ?", b.CourseID).Scan(&n).Error
	if n == 0 {
		v.Add("course_id", "Khóa học không tồn tại.")
	}
	if v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	req := true
	if b.IsRequired != nil {
		req = *b.IsRequired
	}
	res := m.db.Exec(`INSERT INTO curriculum_courses (curriculum_id, course_id, term_number, is_required, credits, position)
		VALUES (?, ?, ?, ?, ?, (SELECT coalesce(max(position) + 1, 0) FROM curriculum_courses WHERE curriculum_id = ? AND term_number = ?))
		ON CONFLICT (curriculum_id, course_id) DO UPDATE SET term_number = EXCLUDED.term_number, is_required = EXCLUDED.is_required, credits = EXCLUDED.credits`,
		*cur, b.CourseID, b.TermNumber, req, b.Credits, *cur, b.TermNumber)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Added to term %d.", b.TermNumber)})
}

// removeCurriculumCourse: DELETE /classes/:id/curriculum-courses/:course_id
func (m *Module) removeCurriculumCourse(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok || !m.canManageClass(c, id) {
		return
	}
	course, ok := idParam(c, "course_id")
	if !ok {
		return
	}
	res := m.db.Exec(`DELETE FROM curriculum_courses WHERE course_id = ? AND curriculum_id = (SELECT curriculum_id FROM administrative_classes WHERE id = ?)`, course, id)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Removed."})
}
