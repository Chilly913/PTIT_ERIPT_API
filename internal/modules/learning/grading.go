package learning

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Manual grading of the questions a machine cannot score (essay, file upload).
//
// An attempt with such questions is closed with grading_status = 'pending'. A grader (admin, the instructor of the
// course, or the creator of the exam) lists the pending attempts, reads each answer, scores every manual question and
// saves; the attempt is then re-scored from the stored answers plus those grades (scoreAttempt), so the final score,
// pass/fail and lesson completion come out the same way as for an auto-graded attempt.

type gradingDoc struct {
	Questions       map[string]manualGrade `json:"questions"`
	OverallFeedback string                 `json:"overall_feedback,omitempty"`
}

func parseGrading(raw []byte) gradingDoc {
	var d gradingDoc
	_ = json.Unmarshal(raw, &d)
	if d.Questions == nil {
		d.Questions = map[string]manualGrade{}
	}
	return d
}

// gradableAttempt loads an attempt and requires the caller to be one of its graders.
func (m *Module) gradableAttempt(c *gin.Context) (*attemptMeta, int64, bool) {
	id, ok := idParam(c, "id")
	if !ok {
		return nil, 0, false
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return nil, 0, false
	}
	a, err := m.loadAttempt(id)
	if err != nil {
		httpx.Fail(c, err)
		return nil, 0, false
	}
	if a == nil || !a.isGrader(uid, roles) {
		httpx.NotFound(c, "") // do not reveal that the attempt exists
		return nil, 0, false
	}
	return a, uid, true
}

func (m *Module) listGrading(c *gin.Context) {
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	page, per := pageParams(c, 15, 100)
	conds := []string{"a.status IN ('submitted', 'force_stopped')", "a.grading_status <> 'none'"}
	var args []any
	switch c.DefaultQuery("status", "pending") {
	case "graded":
		conds = append(conds, "a.grading_status = 'graded'")
	case "all":
	default:
		conds = append(conds, "a.grading_status = 'pending'")
	}
	if !hasRole(roles, auth.RoleAdmin) {
		conds = append(conds, "(c.instructor_id = ? OR e.created_by = ?)")
		args = append(args, uid, uid)
	}
	if v := c.Query("course_id"); v != "" {
		conds = append(conds, "c.id = ?")
		args = append(args, v)
	}
	if v := c.Query("exam_id"); v != "" {
		conds = append(conds, "z.exam_id = ?")
		args = append(args, v)
	}
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		conds = append(conds, "(u.name ILIKE ? OR u.email::text ILIKE ? OR u.student_code::text ILIKE ?)")
		args = append(args, likeArg(s), likeArg(s), likeArg(s))
	}
	from := ` FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id
		LEFT JOIN exams e ON e.id = z.exam_id LEFT JOIN lessons l ON l.id = z.lesson_id
		LEFT JOIN courses c ON c.id = coalesce(z.course_id, e.course_id, l.course_id)
		JOIN users u ON u.id = a.user_id WHERE ` + strings.Join(conds, " AND ")
	var total int64
	if err := m.db.Raw("SELECT count(*)"+from, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows(`SELECT jsonb_build_object('id', a.id, 'grading_status', a.grading_status, 'score', a.score::float8, 'passed', a.passed,
		'submitted_at', a.completed_at, 'graded_at', a.graded_at,
		'kind', CASE WHEN z.exam_id IS NOT NULL THEN 'exam' ELSE 'quiz' END, 'title', coalesce(e.title, z.title), 'course_title', c.title,
		'student', jsonb_build_object('id', u.id, 'name', u.name, 'email', u.email::text, 'student_code', u.student_code::text))`+from+
		" ORDER BY a.completed_at DESC NULLS LAST, a.id DESC LIMIT ? OFFSET ?", append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, total, page, per)
}

// gradingView builds the grader's view of an attempt: every question with the learner's answer, the auto score and,
// for questions that need a human, the grade given so far.
func (m *Module) gradingView(a *attemptMeta) (gin.H, error) {
	qs, err := m.attemptQuestions(a)
	if err != nil {
		return nil, err
	}
	answers := map[string]any{}
	if len(a.Answers) > 0 {
		_ = json.Unmarshal(a.Answers, &answers)
	}
	doc := parseGrading(a.Grading)
	sc := scoreAttempt(qs, answers, a.ID, doc.Questions)
	files, err := m.attemptFiles(a.ID)
	if err != nil {
		return nil, err
	}
	for i, x := range qs {
		if x.Type == "file_upload" {
			list := files[x.ID]
			if list == nil {
				list = []gin.H{}
			}
			sc.Review[i]["files"] = list
		}
	}
	return gin.H{
		"id": a.ID, "title": a.Title, "course_title": a.CourseTitle, "kind": map[bool]string{true: "exam", false: "quiz"}[a.ExamID != nil],
		"grading_status": a.GradingStatus, "status": a.Status, "pass_score": a.PassScore,
		"score": sc.Score, "points_earned": sc.Earned, "points_max": sc.Max, "passed": a.Passed, "pending": sc.Pending,
		"submitted_at": a.CompletedAt, "graded_at": a.GradedAt, "overall_feedback": doc.OverallFeedback,
		"student":   gin.H{"id": a.UserID, "name": a.StudentName, "email": a.StudentEmail, "student_code": a.StudentCode},
		"questions": sc.Review,
	}, nil
}

func (m *Module) showGrading(c *gin.Context) {
	a, _, ok := m.gradableAttempt(c)
	if !ok {
		return
	}
	if a.Status == "in_progress" || a.Status == "paused" {
		httpx.Abort(c, http.StatusConflict, "Bài làm chưa được nộp.")
		return
	}
	out, err := m.gradingView(a)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (m *Module) putGrading(c *gin.Context) {
	a, uid, ok := m.gradableAttempt(c)
	if !ok {
		return
	}
	if a.Status != "submitted" && a.Status != "force_stopped" {
		httpx.Abort(c, http.StatusConflict, "Bài làm chưa được nộp.")
		return
	}
	var b struct {
		Questions       map[string]manualGrade `json:"questions"`
		OverallFeedback *string                `json:"overall_feedback"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	qs, err := m.attemptQuestions(a)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	manualQ := map[string]quizQuestion{}
	for _, q := range qs {
		if q.Type == "essay" || q.Type == "file_upload" {
			manualQ[strconv.FormatInt(q.ID, 10)] = q
		}
	}
	doc := parseGrading(a.Grading)
	v := httpx.NewValidationError()
	for key, g := range b.Questions {
		q, isManual := manualQ[key]
		switch {
		case !isManual:
			v.Add("questions."+key, "This question is not graded by hand.")
		case math.IsNaN(g.Score) || g.Score < 0 || g.Score > q.Points+1e-9:
			v.Add("questions."+key, "The score must be between 0 and "+strconv.FormatFloat(q.Points, 'f', -1, 64)+".")
		case len([]rune(g.Comment)) > 2000:
			v.Add("questions."+key, "The comment may not be greater than 2000 characters.")
		default:
			doc.Questions[key] = manualGrade{Score: math.Round(g.Score*100) / 100, Comment: strings.TrimSpace(g.Comment)}
		}
	}
	if b.OverallFeedback != nil {
		doc.OverallFeedback = strings.TrimSpace(*b.OverallFeedback)
		if len([]rune(doc.OverallFeedback)) > 4000 {
			v.Add("overall_feedback", "The overall feedback may not be greater than 4000 characters.")
		}
	}
	if v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	answers := map[string]any{}
	if len(a.Answers) > 0 {
		_ = json.Unmarshal(a.Answers, &answers)
	}
	sc := scoreAttempt(qs, answers, a.ID, doc.Questions)
	passed := sc.Score >= float64(a.PassScore) && !sc.Pending
	status := "graded"
	if sc.Pending {
		status = "pending"
	}
	j, _ := json.Marshal(doc)
	err = m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE quiz_attempts SET score = ?, passed = ?, grading_status = ?, grading = ?::jsonb, graded_by = ?, graded_at = now() WHERE id = ?`,
			sc.Score, passed, status, string(j), uid, a.ID).Error; err != nil {
			return err
		}
		if passed && a.LessonID != nil { // a lesson quiz completes its lesson once passed, as when it is auto-graded
			return tx.Exec(`INSERT INTO lesson_progress (user_id, lesson_id, progress_percent, completed_at, last_watched_at) VALUES (?, ?, 100, now(), now())
				ON CONFLICT (user_id, lesson_id) DO UPDATE SET progress_percent = 100, completed_at = coalesce(lesson_progress.completed_at, now()), last_watched_at = now()`,
				a.UserID, *a.LessonID).Error
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if status == "graded" && a.GradingStatus != "graded" {
		m.notify(a.UserID, "announcement", "Bài làm đã được chấm", fmt.Sprintf("Bài \"%s\" của bạn đã được chấm: %.2f/100 điểm.", a.Title, sc.Score), "/attempts/"+strconv.FormatInt(a.ID, 10))
	}
	fresh, err := m.loadAttempt(a.ID)
	if err != nil || fresh == nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.gradingView(fresh)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
