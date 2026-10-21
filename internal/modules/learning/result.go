package learning

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// What a learner sees of an attempt after submitting it: the list of their attempts and the result of one, including
// the grades and comments a grader added later. The result is built from the stored answers (scoreAttempt), so it
// matches what was shown at submission plus the manual grades.

// myAttempts lists the caller's submitted attempts of a lesson quiz (?lesson_id=) or an exam (?exam_id=).
func (m *Module) myAttempts(c *gin.Context) {
	lesson, exam := c.Query("lesson_id"), c.Query("exam_id")
	if (lesson == "") == (exam == "") {
		httpx.Fail(c, httpx.NewValidationError().Add("lesson_id", "Provide either lesson_id or exam_id."))
		return
	}
	cond, arg := "z.lesson_id = ?", lesson
	if exam != "" {
		cond, arg = "z.exam_id = ?", exam
	}
	rows, err := m.jsonRows(`SELECT jsonb_build_object('id', a.id, 'status', a.status, 'score', a.score::float8, 'passed', a.passed,
		'grading_status', a.grading_status, 'submitted_at', a.completed_at, 'graded_at', a.graded_at)
		FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id
		WHERE a.user_id = ? AND a.status IN ('submitted', 'force_stopped') AND `+cond+` ORDER BY a.completed_at DESC NULLS LAST, a.id DESC LIMIT 50`, auth.UserID(c), arg)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

// attemptResult returns the review of one of the caller's submitted attempts.
func (m *Module) attemptResult(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	a, err := m.loadAttempt(id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if a == nil || a.UserID != auth.UserID(c) {
		httpx.NotFound(c, "")
		return
	}
	if a.Status != "submitted" && a.Status != "force_stopped" {
		httpx.Abort(c, http.StatusConflict, "Bài làm chưa được nộp.")
		return
	}
	qs, err := m.attemptQuestions(a)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	answers := map[string]any{}
	if len(a.Answers) > 0 {
		_ = json.Unmarshal(a.Answers, &answers)
	}
	doc := parseGrading(a.Grading)
	sc := scoreAttempt(qs, answers, a.ID, doc.Questions)
	out := gin.H{
		"id": a.ID, "title": a.Title, "kind": map[bool]string{true: "exam", false: "quiz"}[a.ExamID != nil],
		"review_available": true, "grading_status": a.GradingStatus, "pass_score": a.PassScore,
		"score": sc.Score, "points_earned": sc.Earned, "points_max": sc.Max, "pending_manual": sc.Pending,
		"overall_feedback": doc.OverallFeedback, "submitted_at": a.CompletedAt, "graded_at": a.GradedAt,
		"questions": sc.Review,
	}
	if sc.Pending {
		out["passed"] = nil
	} else {
		out["passed"] = a.Passed
	}
	if a.ExamID != nil {
		applyExamReview(out, a.Review) // the instructor decides what an exam candidate may see afterwards
	}
	c.JSON(http.StatusOK, out)
}
