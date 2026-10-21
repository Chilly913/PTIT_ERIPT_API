package learning

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Gradebook: the grade structure of a course (weighted components) and per-class-section score entry.
// Totals are on a 10-point scale; letter grades follow the usual Vietnamese credit-system table.

type gradeComponent struct {
	ID         int64   `json:"id,omitempty"`
	Name       string  `json:"name"`
	Weight     float64 `json:"weight"`
	MaxScore   float64 `json:"max_score"`
	IsRequired bool    `json:"is_required"`
	Note       *string `json:"note"`
}

var gradePresets = map[string][]gradeComponent{
	"standard": {{Name: "Chuyên cần", Weight: 10, MaxScore: 10, IsRequired: true}, {Name: "Giữa kỳ", Weight: 30, MaxScore: 10, IsRequired: true}, {Name: "Cuối kỳ", Weight: 60, MaxScore: 10, IsRequired: true}},
	"with_lab": {{Name: "Chuyên cần", Weight: 10, MaxScore: 10, IsRequired: true}, {Name: "Thực hành", Weight: 20, MaxScore: 10, IsRequired: true}, {Name: "Giữa kỳ", Weight: 20, MaxScore: 10, IsRequired: true}, {Name: "Cuối kỳ", Weight: 50, MaxScore: 10, IsRequired: true}},
	"project":  {{Name: "Chuyên cần", Weight: 10, MaxScore: 10, IsRequired: true}, {Name: "Bài tập lớn", Weight: 30, MaxScore: 10, IsRequired: true}, {Name: "Cuối kỳ", Weight: 60, MaxScore: 10, IsRequired: true}},
}

// letterOf maps a 10-point total to (letter, 4-point GPA, passed).
func letterOf(total float64) (string, float64, bool) {
	switch {
	case total >= 8.5:
		return "A", 4.0, true
	case total >= 8.0:
		return "B+", 3.5, true
	case total >= 7.0:
		return "B", 3.0, true
	case total >= 6.5:
		return "C+", 2.5, true
	case total >= 5.5:
		return "C", 2.0, true
	case total >= 5.0:
		return "D+", 1.5, true
	case total >= 4.0:
		return "D", 1.0, true
	}
	return "F", 0, false
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// courseForGrades resolves :id to a course the caller manages.
func (m *Module) courseForGrades(c *gin.Context) (int64, int64, bool) {
	id, ok := idParam(c, "id")
	if !ok {
		return 0, 0, false
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return 0, 0, false
	}
	var n int64
	if err := m.db.Table("courses").Where("id = ?", id).Count(&n).Error; err != nil {
		httpx.Fail(c, err)
		return 0, 0, false
	}
	if n == 0 {
		httpx.NotFound(c, "")
		return 0, 0, false
	}
	if !m.canUseCourse(uid, roles, id) && !hasRole(roles, auth.RoleAcademicManager) {
		httpx.Forbidden(c, "")
		return 0, 0, false
	}
	return id, uid, true
}

func (m *Module) loadComponents(courseID int64) ([]gradeComponent, float64, error) {
	var comps []gradeComponent
	err := m.db.Raw(`SELECT id, name, weight::float8 AS weight, max_score::float8 AS max_score, is_required, note
		FROM grade_components WHERE course_id = ? ORDER BY position, id`, courseID).Scan(&comps).Error
	total := 0.0
	for _, c := range comps {
		total += c.Weight
	}
	if comps == nil {
		comps = []gradeComponent{}
	}
	return comps, round2(total), err
}

func (m *Module) respondComponents(c *gin.Context, courseID int64) {
	comps, total, err := m.loadComponents(courseID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"course_id": courseID, "total_weight": total, "components": comps})
}

func (m *Module) getGradeComponents(c *gin.Context) {
	id, _, ok := m.courseForGrades(c)
	if !ok {
		return
	}
	m.respondComponents(c, id)
}

// replaceComponents upserts the given list (by id) and removes components that are no longer listed.
func (m *Module) replaceComponents(courseID int64, list []gradeComponent) error {
	return m.db.Transaction(func(tx *gorm.DB) error {
		keep := []int64{}
		for i, comp := range list {
			var id int64
			if comp.ID > 0 {
				res := tx.Exec(`UPDATE grade_components SET name = ?, weight = ?, max_score = ?, is_required = ?, position = ?, note = ? WHERE id = ? AND course_id = ?`,
					strings.TrimSpace(comp.Name), comp.Weight, comp.MaxScore, comp.IsRequired, i, comp.Note, comp.ID, courseID)
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected > 0 {
					keep = append(keep, comp.ID)
					continue
				}
			}
			if err := tx.Raw(`INSERT INTO grade_components (course_id, name, weight, max_score, is_required, position, note) VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
				courseID, strings.TrimSpace(comp.Name), comp.Weight, comp.MaxScore, comp.IsRequired, i, comp.Note).Scan(&id).Error; err != nil {
				return err
			}
			keep = append(keep, id)
		}
		if len(keep) == 0 {
			return tx.Exec("DELETE FROM grade_components WHERE course_id = ?", courseID).Error
		}
		return tx.Exec("DELETE FROM grade_components WHERE course_id = ? AND id NOT IN ?", courseID, keep).Error
	})
}

func (m *Module) putGradeComponents(c *gin.Context) {
	id, _, ok := m.courseForGrades(c)
	if !ok {
		return
	}
	var b struct {
		Components []gradeComponent `json:"components"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	v := httpx.NewValidationError()
	names := map[string]bool{}
	total := 0.0
	for i, comp := range b.Components {
		key := "components." + strconv.Itoa(i)
		n := strings.TrimSpace(comp.Name)
		switch {
		case n == "":
			v.Add(key+".name", "Vui lòng nhập tên thành phần.")
		case len(n) > 150:
			v.Add(key+".name", "Tên thành phần tối đa 150 ký tự.")
		case names[strings.ToLower(n)]:
			v.Add(key+".name", "Tên thành phần bị trùng.")
		}
		names[strings.ToLower(n)] = true
		if comp.Weight < 0 || comp.Weight > 100 {
			v.Add(key+".weight", "Trọng số phải từ 0 đến 100.")
		}
		if comp.MaxScore <= 0 || comp.MaxScore > 999 {
			v.Add(key+".max_score", "Điểm tối đa phải lớn hơn 0.")
		}
		total += comp.Weight
	}
	if total > 100.0001 {
		v.Add("components", "Tổng trọng số không được vượt quá 100%.")
	}
	if v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	if err := m.replaceComponents(id, b.Components); err != nil {
		httpx.Fail(c, err)
		return
	}
	m.respondComponents(c, id)
}

func (m *Module) applyGradePreset(c *gin.Context) {
	id, _, ok := m.courseForGrades(c)
	if !ok {
		return
	}
	var b struct {
		Preset string `json:"preset"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	list, ok := gradePresets[b.Preset]
	if !ok {
		httpx.Fail(c, httpx.NewValidationError().Add("preset", "The selected preset is invalid."))
		return
	}
	// Keep ids of components with the same name so existing scores survive a preset change.
	existing, _, _ := m.loadComponents(id)
	byName := map[string]int64{}
	for _, e := range existing {
		byName[strings.ToLower(e.Name)] = e.ID
	}
	out := make([]gradeComponent, len(list))
	for i, comp := range list {
		comp.ID = byName[strings.ToLower(comp.Name)]
		out[i] = comp
	}
	if err := m.replaceComponents(id, out); err != nil {
		httpx.Fail(c, err)
		return
	}
	m.respondComponents(c, id)
}

// ---- per class section ---------------------------------------------------------

func (m *Module) sectionForGrades(c *gin.Context) (secID, courseID int64, ok bool) {
	secID, ok = idParam(c, "id")
	if !ok {
		return
	}
	var s struct {
		CourseID   int64
		LecturerID *int64
	}
	res := m.db.Raw("SELECT course_id, lecturer_id FROM class_sections WHERE id = ?", secID).Scan(&s)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return 0, 0, false
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return 0, 0, false
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return 0, 0, false
	}
	isLecturer := s.LecturerID != nil && *s.LecturerID == uid
	if !isLecturer && !m.canUseCourse(uid, roles, s.CourseID) && !hasRole(roles, auth.RoleAcademicManager) {
		httpx.Forbidden(c, "")
		return 0, 0, false
	}
	return secID, s.CourseID, true
}

func (m *Module) gradeMatrix(secID, courseID int64) (gin.H, error) {
	var sec struct {
		ID          int64
		Code        string
		CourseTitle string
	}
	if err := m.db.Raw(`SELECT s.id, s.code::text AS code, c.title AS course_title FROM class_sections s JOIN courses c ON c.id = s.course_id WHERE s.id = ?`, secID).Scan(&sec).Error; err != nil {
		return nil, err
	}
	comps, _, err := m.loadComponents(courseID)
	if err != nil {
		return nil, err
	}
	type stu struct {
		EnrollmentID int64   `json:"enrollment_id"`
		UserID       int64   `json:"user_id"`
		Name         string  `json:"name"`
		StudentCode  *string `json:"student_code"`
		Email        string  `json:"email"`
	}
	var students []stu
	if err := m.db.Raw(`SELECT e.id AS enrollment_id, u.id AS user_id, u.name, u.student_code::text AS student_code, u.email::text AS email
		FROM enrollments e JOIN users u ON u.id = e.user_id WHERE e.class_section_id = ? ORDER BY u.name`, secID).Scan(&students).Error; err != nil {
		return nil, err
	}
	type entryRow struct {
		EnrollmentID int64
		ComponentID  int64
		Score        *float64
	}
	var entries []entryRow
	if err := m.db.Raw(`SELECT g.enrollment_id, g.grade_component_id AS component_id, g.score::float8 AS score FROM grade_entries g
		JOIN enrollments e ON e.id = g.enrollment_id WHERE e.class_section_id = ?`, secID).Scan(&entries).Error; err != nil {
		return nil, err
	}
	scores := map[[2]int64]*float64{}
	for _, e := range entries {
		scores[[2]int64{e.EnrollmentID, e.ComponentID}] = e.Score
	}
	rows := make([]gin.H, 0, len(students))
	for _, s := range students {
		ents := gin.H{}
		prov, complete := 0.0, true
		for _, comp := range comps {
			sc := scores[[2]int64{s.EnrollmentID, comp.ID}]
			ents[strconv.FormatInt(comp.ID, 10)] = gin.H{"score": sc}
			if sc != nil {
				prov += *sc / comp.MaxScore * comp.Weight / 100 * 10
			} else if comp.IsRequired {
				complete = false
			}
		}
		prov = round2(prov)
		row := gin.H{"enrollment_id": s.EnrollmentID, "user_id": s.UserID, "name": s.Name, "student_code": s.StudentCode, "email": s.Email,
			"entries": ents, "provisional_total": prov, "complete": complete && len(comps) > 0, "total": nil, "letter": nil, "gpa4": nil, "passed": nil}
		if complete && len(comps) > 0 {
			letter, gpa, passed := letterOf(prov)
			row["total"], row["letter"], row["gpa4"], row["passed"] = prov, letter, gpa, passed
		}
		rows = append(rows, row)
	}
	return gin.H{"section": gin.H{"id": sec.ID, "code": sec.Code, "course_title": sec.CourseTitle, "course_id": courseID}, "components": comps, "students": rows}, nil
}

func (m *Module) getSectionGrades(c *gin.Context) {
	secID, courseID, ok := m.sectionForGrades(c)
	if !ok {
		return
	}
	out, err := m.gradeMatrix(secID, courseID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (m *Module) putSectionGrades(c *gin.Context) {
	secID, courseID, ok := m.sectionForGrades(c)
	if !ok {
		return
	}
	var b struct {
		Entries []struct {
			UserID      int64    `json:"user_id"`
			ComponentID int64    `json:"component_id"`
			Score       *float64 `json:"score"`
		} `json:"entries"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if len(b.Entries) == 0 || len(b.Entries) > 5000 {
		httpx.Fail(c, httpx.NewValidationError().Add("entries", "Không có điểm nào để lưu."))
		return
	}
	uid := auth.UserID(c)
	comps, _, err := m.loadComponents(courseID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	maxOf := map[int64]float64{}
	for _, comp := range comps {
		maxOf[comp.ID] = comp.MaxScore
	}
	v := httpx.NewValidationError()
	for i, e := range b.Entries {
		key := "entries." + strconv.Itoa(i)
		mx, known := maxOf[e.ComponentID]
		switch {
		case !known:
			v.Add(key+".component_id", "Thành phần điểm không hợp lệ.")
		case e.Score != nil && (*e.Score < 0 || *e.Score > mx):
			v.Add(key+".score", "Điểm phải từ 0 đến "+strconv.FormatFloat(mx, 'f', -1, 64)+".")
		}
	}
	if v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	err = m.db.Transaction(func(tx *gorm.DB) error {
		for _, e := range b.Entries {
			var enr int64
			if err := tx.Raw("SELECT id FROM enrollments WHERE class_section_id = ? AND user_id = ?", secID, e.UserID).Scan(&enr).Error; err != nil {
				return err
			}
			if enr == 0 {
				return httpx.NewValidationError().Add("entries", "Có sinh viên không thuộc lớp học phần này.")
			}
			if err := tx.Exec(`INSERT INTO grade_entries (enrollment_id, grade_component_id, score, graded_by, graded_at) VALUES (?, ?, ?, ?, now())
				ON CONFLICT (enrollment_id, grade_component_id) DO UPDATE SET score = EXCLUDED.score, graded_by = EXCLUDED.graded_by, graded_at = now()`,
				enr, e.ComponentID, e.Score, uid).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.gradeMatrix(secID, courseID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
