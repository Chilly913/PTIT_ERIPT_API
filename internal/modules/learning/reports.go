package learning

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Course progress report for instructors: one row per learner plus class-wide totals.

type reportRow struct {
	UserID               int64      `json:"user_id"`
	Name                 string     `json:"name"`
	Email                string     `json:"email"`
	EnrolledAt           time.Time  `json:"enrolled_at"`
	TotalLessons         int        `json:"total_lessons"`
	CompletedLessons     int        `json:"completed_lessons"`
	Percent              int        `json:"percent" gorm:"-"`
	LastActivity         *time.Time `json:"last_activity"`
	TotalAssignments     int        `json:"total_assignments"`
	SubmittedAssignments int        `json:"submitted_assignments"`
	AvgGrade             *float64   `json:"avg_grade"`
	TotalQuizzes         int        `json:"total_quizzes"`
	PassedQuizzes        int        `json:"passed_quizzes"`
	QuizAvg              *float64   `json:"quiz_avg"`
	ExamBest             *float64   `json:"exam_best"`
	TotalSessions        int        `json:"total_sessions"`
	AttendedSessions     int        `json:"attended_sessions"`
	HasCertificate       bool       `json:"has_certificate"`
	AtRisk               bool       `json:"at_risk" gorm:"-"`
	ClassID              *int64     `json:"class_id"`
	ClassCode            *string    `json:"class_code"`
	ClassName            *string    `json:"class_name"`
	YearID               *int64     `json:"year_id"`
	YearName             *string    `json:"year_name"`
}

// classRow is the per-class aggregate shown above the learner table.
type classRow struct {
	ClassID *int64        `json:"class_id"`
	Code    string        `json:"code"`
	Name    string        `json:"name"`
	Summary reportSummary `json:"summary"`
}

type reportSummary struct {
	Students         int      `json:"students"`
	AvgPercent       int      `json:"avg_percent"`
	Completed        int      `json:"completed"`
	Certificates     int      `json:"certificates"`
	AssignmentRate   int      `json:"assignment_rate"` // % of (learner, assignment) pairs submitted
	AttendanceRate   int      `json:"attendance_rate"` // % of (learner, past session) pairs attended
	AtRisk           int      `json:"at_risk"`
	TotalLessons     int      `json:"total_lessons"`
	TotalAssignments int      `json:"total_assignments"`
	TotalQuizzes     int      `json:"total_quizzes"`
	TotalSessions    int      `json:"total_sessions"`
	AvgQuizScore     *float64 `json:"avg_quiz_score"`
	AvgAssignGrade   *float64 `json:"avg_assignment_grade"`
}

const reportSelect = `
SELECT u.administrative_class_id AS class_id, ac.code::text AS class_code, ac.name AS class_name, ay.id AS year_id, ay.name AS year_name,
	u.id AS user_id, u.name, u.email::text AS email, en.enrolled_at,
	(SELECT count(*) FROM lessons l WHERE l.course_id = en.course_id) AS total_lessons,
	(SELECT count(*) FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id
		WHERE l.course_id = en.course_id AND p.user_id = en.user_id AND p.completed_at IS NOT NULL) AS completed_lessons,
	GREATEST(
		(SELECT max(coalesce(p.last_watched_at, p.updated_at)) FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id WHERE l.course_id = en.course_id AND p.user_id = en.user_id),
		(SELECT max(s.submitted_at) FROM assignment_submissions s JOIN lesson_assignments a ON a.id = s.lesson_assignment_id JOIN lessons l ON l.id = a.lesson_id WHERE l.course_id = en.course_id AND s.user_id = en.user_id),
		(SELECT max(t.started_at) FROM quiz_attempts t JOIN quizzes q ON q.id = t.quiz_id LEFT JOIN lessons l ON l.id = q.lesson_id WHERE (l.course_id = en.course_id OR q.course_id = en.course_id) AND t.user_id = en.user_id)
	) AS last_activity,
	(SELECT count(*) FROM lesson_assignments a JOIN lessons l ON l.id = a.lesson_id WHERE l.course_id = en.course_id) AS total_assignments,
	(SELECT count(*) FROM assignment_submissions s JOIN lesson_assignments a ON a.id = s.lesson_assignment_id JOIN lessons l ON l.id = a.lesson_id
		WHERE l.course_id = en.course_id AND s.user_id = en.user_id) AS submitted_assignments,
	(SELECT avg(s.grade) FROM assignment_submissions s JOIN lesson_assignments a ON a.id = s.lesson_assignment_id JOIN lessons l ON l.id = a.lesson_id
		WHERE l.course_id = en.course_id AND s.user_id = en.user_id AND s.grade IS NOT NULL) AS avg_grade,
	(SELECT count(*) FROM quizzes q JOIN lessons l ON l.id = q.lesson_id WHERE l.course_id = en.course_id) AS total_quizzes,
	(SELECT count(DISTINCT t.quiz_id) FROM quiz_attempts t JOIN quizzes q ON q.id = t.quiz_id JOIN lessons l ON l.id = q.lesson_id
		WHERE l.course_id = en.course_id AND t.user_id = en.user_id AND t.completed_at IS NOT NULL AND t.passed) AS passed_quizzes,
	(SELECT avg(b) FROM (SELECT max(t.score) AS b FROM quiz_attempts t JOIN quizzes q ON q.id = t.quiz_id JOIN lessons l ON l.id = q.lesson_id
		WHERE l.course_id = en.course_id AND t.user_id = en.user_id AND t.completed_at IS NOT NULL GROUP BY t.quiz_id) x) AS quiz_avg,
	(SELECT max(t.score) FROM quiz_attempts t JOIN quizzes q ON q.id = t.quiz_id JOIN exams e ON e.id = q.exam_id
		WHERE e.course_id = en.course_id AND t.user_id = en.user_id AND t.completed_at IS NOT NULL) AS exam_best,
	(SELECT count(*) FROM offline_sessions o WHERE o.course_id = en.course_id AND o.start_at <= now()) AS total_sessions,
	(SELECT count(*) FROM offline_session_attendances a JOIN offline_sessions o ON o.id = a.offline_session_id
		WHERE o.course_id = en.course_id AND o.start_at <= now() AND a.user_id = en.user_id AND a.status IN ('present', 'late', 'excused')) AS attended_sessions,
	EXISTS (SELECT 1 FROM user_certificates uc WHERE uc.user_id = en.user_id AND uc.course_id = en.course_id) AS has_certificate
FROM enrollments en JOIN users u ON u.id = en.user_id
LEFT JOIN administrative_classes ac ON ac.id = u.administrative_class_id
LEFT JOIN terms tm ON tm.id = en.term_id
LEFT JOIN academic_years ay ON ay.id = tm.academic_year_id
WHERE en.course_id = ?`

func pct(n, d int) int {
	if d <= 0 {
		return 0
	}
	return int(math.Round(100 * float64(n) / float64(d)))
}

// atRisk marks learners who barely started or went quiet (no activity for 14 days) while the course is unfinished.
func atRisk(r reportRow, now time.Time) bool {
	if r.TotalLessons == 0 || r.Percent >= 100 {
		return false
	}
	if r.LastActivity == nil {
		return now.Sub(r.EnrolledAt) > 7*24*time.Hour
	}
	return now.Sub(*r.LastActivity) > 14*24*time.Hour
}

func summarize(rows []reportRow, now time.Time) reportSummary {
	s := reportSummary{Students: len(rows)}
	var sumPct, subs, subTotal, att, attTotal, qN, gN int
	var qSum, gSum float64
	for i := range rows {
		r := &rows[i]
		r.Percent = pct(r.CompletedLessons, r.TotalLessons)
		r.AtRisk = atRisk(*r, now)
		sumPct += r.Percent
		if r.Percent >= 100 && r.TotalLessons > 0 {
			s.Completed++
		}
		if r.HasCertificate {
			s.Certificates++
		}
		if r.AtRisk {
			s.AtRisk++
		}
		subs += r.SubmittedAssignments
		subTotal += r.TotalAssignments
		att += r.AttendedSessions
		attTotal += r.TotalSessions
		if r.QuizAvg != nil {
			qSum += *r.QuizAvg
			qN++
		}
		if r.AvgGrade != nil {
			gSum += *r.AvgGrade
			gN++
		}
		s.TotalLessons, s.TotalAssignments, s.TotalQuizzes, s.TotalSessions = r.TotalLessons, r.TotalAssignments, r.TotalQuizzes, r.TotalSessions
	}
	if len(rows) > 0 {
		s.AvgPercent = int(math.Round(float64(sumPct) / float64(len(rows))))
	}
	s.AssignmentRate, s.AttendanceRate = pct(subs, subTotal), pct(att, attTotal)
	if qN > 0 {
		v := math.Round(qSum/float64(qN)*10) / 10
		s.AvgQuizScore = &v
	}
	if gN > 0 {
		v := math.Round(gSum/float64(gN)*10) / 10
		s.AvgAssignGrade = &v
	}
	return s
}

// reportFilter limits the report to some classes and/or academic years (comma-separated ids; "none" = not set).
type reportFilter struct{ Classes, Years []string }

func filterFromQuery(c *gin.Context) reportFilter {
	split := func(v string) []string {
		var out []string
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
		return out
	}
	return reportFilter{Classes: split(c.Query("class_id")), Years: split(c.Query("year_id"))}
}

func idsCondition(col string, vals []string) (string, []any) {
	if len(vals) == 0 {
		return "", nil
	}
	var ids []string
	none := false
	for _, v := range vals {
		if v == "none" {
			none = true
		} else {
			ids = append(ids, v)
		}
	}
	switch {
	case none && len(ids) > 0:
		return " AND (" + col + " IS NULL OR " + col + "::text IN ?)", []any{ids}
	case none:
		return " AND " + col + " IS NULL", nil
	default:
		return " AND " + col + "::text IN ?", []any{ids}
	}
}

func (m *Module) courseReport(courseID int64, f reportFilter) ([]reportRow, reportSummary, error) {
	q, args := reportSelect, []any{courseID}
	for _, part := range []struct {
		col  string
		vals []string
	}{{"u.administrative_class_id", f.Classes}, {"ay.id", f.Years}} {
		cond, a := idsCondition(part.col, part.vals)
		q += cond
		args = append(args, a...)
	}
	var rows []reportRow
	if err := m.db.Raw(q+" ORDER BY u.name, u.id", args...).Scan(&rows).Error; err != nil {
		return nil, reportSummary{}, err
	}
	return rows, summarize(rows, time.Now()), nil
}

// byClass groups report rows by the learner's class (rows without a class are grouped together).
func byClass(rows []reportRow, now time.Time) []classRow {
	order := []int64{}
	groups := map[int64][]reportRow{}
	meta := map[int64]classRow{}
	for _, r := range rows {
		var key int64
		if r.ClassID != nil {
			key = *r.ClassID
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
			cr := classRow{ClassID: r.ClassID, Code: "—", Name: "Chưa có lớp"}
			if r.ClassCode != nil {
				cr.Code = *r.ClassCode
			}
			if r.ClassName != nil {
				cr.Name = *r.ClassName
			}
			meta[key] = cr
		}
		groups[key] = append(groups[key], r)
	}
	out := make([]classRow, 0, len(order))
	for _, k := range order {
		cr := meta[k]
		cr.Summary = summarize(groups[k], now)
		out = append(out, cr)
	}
	return out
}

// showCourseReport: GET /courses/:ref/report
func (m *Module) showCourseReport(c *gin.Context) {
	cr, filter, ok := m.reportAccess(c)
	if !ok {
		return
	}
	rows, sum, err := m.courseReport(cr.ID, filter)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if rows == nil {
		rows = []reportRow{}
	}
	restrictSQL, restrictArgs := "", []any{}
	if restricted := m.advisorRestricted(c, cr); restricted {
		restrictSQL, restrictArgs = idsCondition("u.administrative_class_id", filter.Classes)
	}
	classes, err := m.jsonRows(`SELECT jsonb_build_object('value', coalesce(ac.id::text, 'none'), 'label', coalesce(ac.code::text || ' – ' || ac.name, 'Chưa có lớp'), 'count', count(*))
		FROM enrollments en JOIN users u ON u.id = en.user_id LEFT JOIN administrative_classes ac ON ac.id = u.administrative_class_id
		WHERE en.course_id = ?`+restrictSQL+` GROUP BY ac.id, ac.code, ac.name ORDER BY ac.code NULLS LAST`, append([]any{cr.ID}, restrictArgs...)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	years, err := m.jsonRows(`SELECT jsonb_build_object('value', coalesce(ay.id::text, 'none'), 'label', coalesce(ay.name, 'Chưa gán năm học'), 'count', count(*))
		FROM enrollments en JOIN users u ON u.id = en.user_id LEFT JOIN terms tm ON tm.id = en.term_id LEFT JOIN academic_years ay ON ay.id = tm.academic_year_id
		WHERE en.course_id = ?`+restrictSQL+` GROUP BY ay.id, ay.name ORDER BY ay.name DESC NULLS LAST`, append([]any{cr.ID}, restrictArgs...)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"summary": sum, "students": rows, "by_class": byClass(rows, time.Now()), "filters": gin.H{"classes": classes, "years": years}})
}

// exportCourseReport: GET /courses/:ref/report/export, an Excel workbook of the same table.
func (m *Module) exportCourseReport(c *gin.Context) {
	cr, filter, ok := m.reportAccess(c)
	if !ok {
		return
	}
	rows, _, err := m.courseReport(cr.ID, filter)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetName(0)
	head := []any{"Họ tên", "Email", "Lớp", "Năm học", "Ngày vào lớp", "Bài học hoàn thành", "Tiến độ (%)", "Hoạt động gần nhất", "Bài tập đã nộp", "Điểm bài tập TB",
		"Quiz đạt", "Điểm quiz TB", "Điểm thi cao nhất", "Buổi có mặt", "Tổng buổi đã diễn ra", "Chứng chỉ", "Cần chú ý"}
	_ = f.SetSheetRow(sheet, "A1", &head)
	day := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Local().Format("02/01/2006")
	}
	num := func(v *float64) any {
		if v == nil {
			return ""
		}
		return math.Round(*v*100) / 100
	}
	yes := func(b bool) string {
		if b {
			return "Có"
		}
		return ""
	}
	for i, r := range rows {
		line := []any{r.Name, r.Email, deref(r.ClassCode), deref(r.YearName), day(&r.EnrolledAt), fmt.Sprintf("%d/%d", r.CompletedLessons, r.TotalLessons), r.Percent, day(r.LastActivity),
			fmt.Sprintf("%d/%d", r.SubmittedAssignments, r.TotalAssignments), num(r.AvgGrade), fmt.Sprintf("%d/%d", r.PassedQuizzes, r.TotalQuizzes), num(r.QuizAvg),
			num(r.ExamBest), r.AttendedSessions, r.TotalSessions, yes(r.HasCertificate), yes(r.AtRisk)}
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		_ = f.SetSheetRow(sheet, cell, &line)
	}
	if st, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}}); err == nil {
		_ = f.SetCellStyle(sheet, "A1", "Q1", st)
	}
	_ = f.SetColWidth(sheet, "A", "B", 28)
	_ = f.SetColWidth(sheet, "C", "Q", 16)
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	buf, err := f.WriteToBuffer()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	sendFile(c, fmt.Sprintf("bao-cao-khoa-%d.xlsx", cr.ID), "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf.Bytes())
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// showReportStudent: GET /courses/:ref/report/students/:uid, everything behind one row of the report.
func (m *Module) showReportStudent(c *gin.Context) {
	cr, filter, ok := m.reportAccess(c)
	if !ok {
		return
	}
	uid, ok := idParam(c, "uid")
	if !ok {
		return
	}
	if m.advisorRestricted(c, cr) {
		var n int64
		_ = m.db.Raw("SELECT count(*) FROM users WHERE id = ? AND administrative_class_id::text IN ?", uid, filter.Classes).Scan(&n).Error
		if n == 0 {
			httpx.NotFound(c, "")
			return
		}
	}
	head, err := m.jsonOne(`SELECT jsonb_build_object('id', u.id, 'name', u.name, 'email', u.email, 'student_code', u.student_code, 'enrolled_at', en.enrolled_at,
		'class', CASE WHEN ac.id IS NULL THEN NULL ELSE ac.code::text || ' – ' || ac.name END)
		FROM enrollments en JOIN users u ON u.id = en.user_id LEFT JOIN administrative_classes ac ON ac.id = u.administrative_class_id
		WHERE en.course_id = ? AND en.user_id = ?`, cr.ID, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if head == nil {
		httpx.NotFound(c, "")
		return
	}
	lessons, err := m.jsonRows(`SELECT jsonb_build_object('id', l.id, 'title', l.title, 'type', l.type, 'section', s.title, 'completed_at', p.completed_at,
		'percent', coalesce(p.progress_percent, 0), 'watched_seconds', coalesce(p.watched_seconds, 0), 'last_at', p.last_watched_at)
		FROM lessons l LEFT JOIN sections s ON s.id = l.section_id LEFT JOIN lesson_progress p ON p.lesson_id = l.id AND p.user_id = ?
		WHERE l.course_id = ? ORDER BY coalesce(s.position, 2147483647), l.position, l.id`, uid, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	assignments, err := m.jsonRows(`SELECT jsonb_build_object('id', a.id, 'title', l.title, 'due_at', a.due_at, 'submitted_at', s.submitted_at, 'grade', s.grade)
		FROM lesson_assignments a JOIN lessons l ON l.id = a.lesson_id LEFT JOIN assignment_submissions s ON s.lesson_assignment_id = a.id AND s.user_id = ?
		WHERE l.course_id = ? ORDER BY l.position, a.id`, uid, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	quizzes, err := m.jsonRows(`SELECT jsonb_build_object('quiz', q.title, 'attempts', count(t.id), 'best', max(t.score), 'passed', coalesce(bool_or(t.passed), false), 'last_at', max(t.completed_at))
		FROM quizzes q JOIN lessons l ON l.id = q.lesson_id LEFT JOIN quiz_attempts t ON t.quiz_id = q.id AND t.user_id = ? AND t.completed_at IS NOT NULL
		WHERE l.course_id = ? GROUP BY q.id, q.title, l.position ORDER BY l.position`, uid, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	sessions, err := m.jsonRows(`SELECT jsonb_build_object('id', o.id, 'title', coalesce(o.title, o.location), 'start_at', o.start_at, 'status', a.status)
		FROM offline_sessions o LEFT JOIN offline_session_attendances a ON a.offline_session_id = o.id AND a.user_id = ?
		WHERE o.course_id = ? AND o.start_at <= now() ORDER BY o.start_at`, uid, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"student": head, "lessons": lessons, "assignments": assignments, "quizzes": quizzes, "sessions": sessions})
}

// reportAccess resolves the course for a report request. The course owner and admins see everything. Academic advisors
// and academic managers may read the report only for the classes they advise, so a class_id filter is mandatory for them.
func (m *Module) reportAccess(c *gin.Context) (courseRef, reportFilter, bool) {
	cr, ok := m.course(c)
	if !ok {
		return cr, reportFilter{}, false
	}
	f := filterFromQuery(c)
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return cr, f, false
	}
	if cr.InstructorID == uid || hasRole(roles, auth.RoleAdmin) {
		return cr, f, true
	}
	if !hasRole(roles, auth.RoleAdvisor, auth.RoleAcademicManager) || len(f.Classes) == 0 {
		httpx.Forbidden(c, "")
		return cr, f, false
	}
	if !hasRole(roles, auth.RoleAcademicManager) {
		var n int64
		for _, id := range f.Classes {
			if id == "none" {
				httpx.Forbidden(c, "")
				return cr, f, false
			}
		}
		_ = m.db.Raw("SELECT count(*) FROM administrative_classes WHERE id::text IN ? AND advisor_id = ?", f.Classes, uid).Scan(&n).Error
		if int(n) != len(f.Classes) {
			httpx.Forbidden(c, "")
			return cr, f, false
		}
	}
	return cr, f, true
}

// advisorRestricted reports whether the caller sees the report only through a class filter (not owner/admin).
func (m *Module) advisorRestricted(c *gin.Context, cr courseRef) bool {
	uid, roles, _ := m.actor(c)
	return cr.InstructorID != uid && !hasRole(roles, auth.RoleAdmin)
}
