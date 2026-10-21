package learning

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Class management for academic advisors, academic managers and admins.
// A class (lớp hành chính) belongs to a cohort and a curriculum; its students are users.administrative_class_id.
// Courses (môn học) are placed in the curriculum per term number and assigned to the whole class with assign-course.

// classScope returns the SQL condition that limits classes to what the caller may see, plus its arguments.
// Admins and academic managers see every class; advisors see their own; instructors see the classes that have
// students enrolled in one of their courses.
func (m *Module) classScope(c *gin.Context) (string, []any, bool) {
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return "", nil, false
	}
	if hasRole(roles, auth.RoleAdmin, auth.RoleAcademicManager) {
		return "TRUE", nil, true
	}
	return `(ac.advisor_id = ? OR EXISTS (SELECT 1 FROM users su JOIN enrollments se ON se.user_id = su.id JOIN courses sc ON sc.id = se.course_id
		WHERE su.administrative_class_id = ac.id AND sc.instructor_id = ?))`, []any{uid, uid}, true
}

// canManageClass: admins, academic managers and the class advisor may change a class.
func (m *Module) canManageClass(c *gin.Context, classID int64) bool {
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return false
	}
	if hasRole(roles, auth.RoleAdmin, auth.RoleAcademicManager) {
		return true
	}
	var n int64
	_ = m.db.Raw("SELECT count(*) FROM administrative_classes WHERE id = ? AND advisor_id = ?", classID, uid).Scan(&n).Error
	if n == 0 {
		httpx.Forbidden(c, "")
		return false
	}
	return true
}

const classJSON = `jsonb_build_object('id', ac.id, 'code', ac.code, 'name', ac.name, 'status', ac.status, 'capacity', ac.capacity,
	'expected_graduation_year', ac.expected_graduation_year, 'description', ac.description,
	'cohort', CASE WHEN co.id IS NULL THEN NULL ELSE jsonb_build_object('id', co.id, 'code', co.code, 'name', co.name, 'start_year', co.start_year) END,
	'program', jsonb_build_object('id', p.id, 'code', p.code, 'name', p.name),
	'advisor', CASE WHEN ad.id IS NULL THEN NULL ELSE jsonb_build_object('id', ad.id, 'name', ad.name, 'email', ad.email) END,
	'curriculum', CASE WHEN cu.id IS NULL THEN NULL ELSE jsonb_build_object('id', cu.id, 'code', cu.code, 'name', cu.name) END,
	'student_count', (SELECT count(*) FROM users s WHERE s.administrative_class_id = ac.id AND s.deleted_at IS NULL))`

const classFrom = ` FROM administrative_classes ac
	JOIN programs p ON p.id = ac.program_id
	LEFT JOIN cohorts co ON co.id = ac.cohort_id
	LEFT JOIN users ad ON ad.id = ac.advisor_id
	LEFT JOIN curricula cu ON cu.id = ac.curriculum_id `

// listClasses: GET /classes?q=&cohort_id=&program_id=&year=&status=  (cohort_id/program_id/year accept comma-separated lists)
func (m *Module) listClasses(c *gin.Context) {
	scope, args, ok := m.classScope(c)
	if !ok {
		return
	}
	conds := scope
	list := func(param, col string) {
		if v := strings.TrimSpace(c.Query(param)); v != "" {
			ids := strings.Split(v, ",")
			conds += " AND " + col + " IN ?"
			args = append(args, ids)
		}
	}
	list("cohort_id", "ac.cohort_id::text")
	list("program_id", "ac.program_id::text")
	list("year", "co.start_year::text")
	list("status", "ac.status")
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		conds += " AND (ac.code::text ILIKE ? OR ac.name ILIKE ?)"
		args = append(args, likeArg(s), likeArg(s))
	}
	page, per := pageParams(c, 20, 100)
	var total int64
	if err := m.db.Raw("SELECT count(*)"+classFrom+"WHERE "+conds, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows("SELECT "+classJSON+classFrom+"WHERE "+conds+" ORDER BY co.start_year DESC NULLS LAST, ac.code LIMIT ? OFFSET ?", append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, total, page, per)
}

// classFilters: GET /classes/filters — options for the filter dropdowns (cohorts, programs, years) among visible classes.
func (m *Module) classFilters(c *gin.Context) {
	scope, args, ok := m.classScope(c)
	if !ok {
		return
	}
	cohorts, err := m.jsonRows(`SELECT DISTINCT ON (co.id) jsonb_build_object('value', co.id::text, 'label', co.code || ' – ' || co.name, 'year', co.start_year)`+classFrom+`WHERE co.id IS NOT NULL AND `+scope+` ORDER BY co.id`, args...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	programs, err := m.jsonRows(`SELECT DISTINCT ON (p.id) jsonb_build_object('value', p.id::text, 'label', p.code || ' – ' || p.name)`+classFrom+`WHERE `+scope+` ORDER BY p.id`, args...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	years, err := m.jsonRows(`SELECT DISTINCT ON (co.start_year) jsonb_build_object('value', co.start_year::text, 'label', 'Khóa nhập học ' || co.start_year)`+classFrom+`WHERE co.id IS NOT NULL AND `+scope+` ORDER BY co.start_year DESC`, args...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"cohorts": cohorts, "programs": programs, "years": years})
}

// showClass: GET /classes/:id — header, students and the curriculum courses of the class by term.
func (m *Module) showClass(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	scope, args, ok := m.classScope(c)
	if !ok {
		return
	}
	head, err := m.jsonOne("SELECT "+classJSON+classFrom+"WHERE ac.id = ? AND "+scope, append([]any{id}, args...)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if head == nil {
		httpx.NotFound(c, "")
		return
	}
	students, err := m.jsonRows(`SELECT jsonb_build_object('id', u.id, 'name', u.name, 'email', u.email, 'student_code', u.student_code,
		'courses', (SELECT count(*) FROM enrollments e WHERE e.user_id = u.id),
		'progress', coalesce((SELECT round(avg(100.0 * (SELECT count(*) FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id WHERE p.user_id = u.id AND l.course_id = e.course_id AND p.completed_at IS NOT NULL)
			/ nullif((SELECT count(*) FROM lessons l WHERE l.course_id = e.course_id), 0))) FROM enrollments e WHERE e.user_id = u.id), 0))
		FROM users u WHERE u.administrative_class_id = ? AND u.deleted_at IS NULL ORDER BY u.name, u.id`, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	courses, err := m.jsonRows(`SELECT jsonb_build_object('course_id', c.id, 'slug', c.slug, 'title', c.title, 'status', c.status, 'term_number', cc.term_number,
		'is_required', cc.is_required, 'credits', cc.credits,
		'term', CASE WHEN t.id IS NULL THEN NULL ELSE jsonb_build_object('id', t.id, 'name', t.name, 'start_date', t.start_date, 'end_date', t.end_date, 'year', ay.name) END,
		'enrolled', (SELECT count(*) FROM enrollments e JOIN users s ON s.id = e.user_id WHERE e.course_id = c.id AND s.administrative_class_id = ac.id),
		'completed', (SELECT count(*) FROM enrollments e JOIN users s ON s.id = e.user_id WHERE e.course_id = c.id AND s.administrative_class_id = ac.id
			AND (SELECT count(*) FROM lessons l WHERE l.course_id = c.id) > 0
			AND (SELECT count(*) FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id WHERE p.user_id = s.id AND l.course_id = c.id AND p.completed_at IS NOT NULL) >= (SELECT count(*) FROM lessons l WHERE l.course_id = c.id)))
		FROM administrative_classes ac
		JOIN curriculum_courses cc ON cc.curriculum_id = ac.curriculum_id
		JOIN courses c ON c.id = cc.course_id
		LEFT JOIN administrative_class_terms act ON act.administrative_class_id = ac.id AND act.term_number = cc.term_number
		LEFT JOIN terms t ON t.id = act.term_id
		LEFT JOIN academic_years ay ON ay.id = t.academic_year_id
		WHERE ac.id = ? ORDER BY cc.term_number, cc.position, c.title`, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	// courses enrolled by the class that are outside the curriculum (assigned ad hoc)
	extra, err := m.jsonRows(`SELECT jsonb_build_object('course_id', c.id, 'slug', c.slug, 'title', c.title, 'status', c.status, 'term_number', NULL, 'is_required', false, 'credits', NULL, 'term', NULL,
		'enrolled', (SELECT count(*) FROM enrollments e JOIN users s ON s.id = e.user_id WHERE e.course_id = c.id AND s.administrative_class_id = ?), 'completed', 0)
		FROM courses c WHERE EXISTS (SELECT 1 FROM enrollments e JOIN users s ON s.id = e.user_id WHERE e.course_id = c.id AND s.administrative_class_id = ?)
		AND NOT EXISTS (SELECT 1 FROM administrative_classes ac JOIN curriculum_courses cc ON cc.curriculum_id = ac.curriculum_id WHERE ac.id = ? AND cc.course_id = c.id) ORDER BY c.title`, id, id, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	termMap, err := m.jsonRows(`SELECT jsonb_build_object('term_number', act.term_number, 'term_id', t.id, 'name', t.name, 'year', ay.name, 'start_date', t.start_date, 'end_date', t.end_date)
		FROM administrative_class_terms act JOIN terms t ON t.id = act.term_id JOIN academic_years ay ON ay.id = t.academic_year_id
		WHERE act.administrative_class_id = ? ORDER BY act.term_number`, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"class": head, "students": students, "courses": append(courses, extra...), "term_map": termMap})
}

// addClassStudents: POST /classes/:id/students {identifiers: [email|student code]} moves users into the class.
func (m *Module) addClassStudents(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok || !m.canManageClass(c, id) {
		return
	}
	var b struct {
		Identifiers []string `json:"identifiers"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if len(b.Identifiers) == 0 || len(b.Identifiers) > 500 {
		httpx.Fail(c, httpx.NewValidationError().Add("identifiers", "Nhập từ 1 đến 500 email hoặc mã sinh viên."))
		return
	}
	added, already := 0, 0
	moved := []string{}
	notFound := []string{}
	for _, raw := range b.Identifiers {
		ident := strings.TrimSpace(raw)
		if ident == "" {
			continue
		}
		var u struct {
			ID    int64
			Class *int64 `gorm:"column:administrative_class_id"`
		}
		if err := m.db.Raw("SELECT id, administrative_class_id FROM users WHERE deleted_at IS NULL AND (email = ? OR student_code = ?) LIMIT 1", ident, ident).Scan(&u).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
		switch {
		case u.ID == 0:
			notFound = append(notFound, ident)
		case u.Class != nil && *u.Class == id:
			already++
		default:
			if u.Class != nil {
				moved = append(moved, ident)
			}
			if err := m.db.Exec("UPDATE users SET administrative_class_id = ? WHERE id = ?", id, u.ID).Error; err != nil {
				httpx.Fail(c, err)
				return
			}
			added++
		}
	}
	c.JSON(http.StatusOK, gin.H{"added": added, "already": already, "moved_from_other_class": moved, "not_found": notFound})
}

// removeClassStudent: DELETE /classes/:id/students/:uid — takes the user out of the class (enrollments are kept).
func (m *Module) removeClassStudent(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok || !m.canManageClass(c, id) {
		return
	}
	uid, ok := idParam(c, "uid")
	if !ok {
		return
	}
	res := m.db.Exec("UPDATE users SET administrative_class_id = NULL WHERE id = ? AND administrative_class_id = ?", uid, id)
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

// assignClassCourse: POST /classes/:id/assign-course {course_id, term_number?} enrols every student of the class in the course.
// With term_number the enrolment is tied to the real term the class maps that curriculum term to.
func (m *Module) assignClassCourse(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok || !m.canManageClass(c, id) {
		return
	}
	var b struct {
		CourseID   int64 `json:"course_id"`
		TermNumber *int  `json:"term_number"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var course struct {
		ID           int64
		InstructorID int64
		Status       string
	}
	if err := m.db.Raw("SELECT id, instructor_id, status FROM courses WHERE id = ?", b.CourseID).Scan(&course).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if course.ID == 0 {
		httpx.Fail(c, httpx.NewValidationError().Add("course_id", "Khóa học không tồn tại."))
		return
	}
	if course.Status != "published" && course.InstructorID != uid && !hasRole(roles, auth.RoleAdmin) {
		httpx.Fail(c, httpx.NewValidationError().Add("course_id", "Chỉ gán được khóa học đã xuất bản."))
		return
	}
	var info struct {
		CohortID *int64
		TermID   *int64
	}
	_ = m.db.Raw(`SELECT ac.cohort_id AS cohort_id, (SELECT act.term_id FROM administrative_class_terms act WHERE act.administrative_class_id = ac.id AND act.term_number = ?) AS term_id
		FROM administrative_classes ac WHERE ac.id = ?`, b.TermNumber, id).Scan(&info).Error
	res := m.db.Exec(`INSERT INTO enrollments (user_id, course_id, enrollment_source, cohort_id, term_id)
		SELECT u.id, ?, 'academic', ?, ? FROM users u WHERE u.administrative_class_id = ? AND u.deleted_at IS NULL
		ON CONFLICT (user_id, course_id) DO NOTHING`, b.CourseID, info.CohortID, info.TermID, id)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	// Enrolments that already existed without a term pick up the class's term (and cohort) so reports can group by school year.
	if info.TermID != nil {
		_ = m.db.Exec(`UPDATE enrollments SET term_id = ?, cohort_id = coalesce(cohort_id, ?) WHERE course_id = ? AND term_id IS NULL
			AND user_id IN (SELECT id FROM users WHERE administrative_class_id = ? AND deleted_at IS NULL)`, info.TermID, info.CohortID, b.CourseID, id).Error
	}
	var total int64
	_ = m.db.Raw("SELECT count(*) FROM users WHERE administrative_class_id = ? AND deleted_at IS NULL", id).Scan(&total).Error
	c.JSON(http.StatusOK, gin.H{"enrolled": res.RowsAffected, "already": total - res.RowsAffected, "students": total})
}
