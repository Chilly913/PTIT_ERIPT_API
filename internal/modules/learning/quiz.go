package learning

import (
	"encoding/json"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// "Bài kiểm tra nhanh": a quiz attached to a lesson (separate from exams). Instructors pick questions from
// the question banks of the course; learners take it inside the lesson page with autosave and auto grading.
// Answer formats: choice -> option id, multiple -> [ids], ordering -> [ids], matching -> {optionId: target},
// numerical/short/essay -> string.

type quizRow struct {
	ID        int64
	Title     string
	TimeLimit *int
	PassScore int
}

func (m *Module) lessonQuiz(lessonID int64) (*quizRow, error) {
	var q quizRow
	res := m.db.Raw("SELECT id, title, time_limit, pass_score FROM quizzes WHERE lesson_id = ?", lessonID).Scan(&q)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	return &q, nil
}

// ---- studio ---------------------------------------------------------------------

func (m *Module) getLessonQuiz(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	lid, ok := idParam(c, "lid")
	if !ok {
		return
	}
	var n int64
	if err := m.db.Table("lessons").Where("id = ? AND course_id = ?", lid, cr.ID).Count(&n).Error; err != nil || n == 0 {
		httpx.NotFound(c, "")
		return
	}
	m.respondLessonQuiz(c, lid)
}

func (m *Module) respondLessonQuiz(c *gin.Context, lid int64) {
	q, err := m.lessonQuiz(lid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if q == nil {
		c.JSON(http.StatusOK, gin.H{"quiz": nil})
		return
	}
	qs, err := m.jsonRows(`SELECT jsonb_build_object('question_id', x.id, 'position', qq.position, 'code', x.code, 'type', x.type, 'difficulty', x.difficulty,
		'content', x.content, 'default_score', x.default_score::float8, 'points', qq.points::float8, 'effective_points', CASE WHEN x.type = 'passage' THEN (SELECT coalesce(sum(c.default_score), 0) FROM questions c WHERE c.parent_question_id = x.id)::float8 ELSE coalesce(qq.points, x.default_score)::float8 END)
		FROM quiz_questions qq JOIN questions x ON x.id = qq.question_id WHERE qq.quiz_id = ? ORDER BY qq.position, x.id`, q.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"quiz": gin.H{"id": q.ID, "title": q.Title, "time_limit": q.TimeLimit, "pass_score": q.PassScore, "questions": qs}})
}

func (m *Module) saveLessonQuiz(c *gin.Context) {
	cr, uid, ok := m.manageCourse(c)
	if !ok {
		return
	}
	lid, ok := idParam(c, "lid")
	if !ok {
		return
	}
	var title string
	res := m.db.Raw("SELECT title FROM lessons WHERE id = ? AND course_id = ?", lid, cr.ID).Scan(&title)
	if res.Error != nil || res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	var b struct {
		Title     *string `json:"title"`
		TimeLimit *int    `json:"time_limit"`
		PassScore *int    `json:"pass_score"`
		Questions []struct {
			QuestionID int64    `json:"question_id"`
			Points     *float64 `json:"points"`
		} `json:"questions"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	v := httpx.NewValidationError()
	pass := 80
	if b.PassScore != nil {
		pass = *b.PassScore
	}
	if pass < 0 || pass > 100 {
		v.Add("pass_score", "The pass score must be between 0 and 100.")
	}
	if b.TimeLimit != nil && (*b.TimeLimit < 0 || *b.TimeLimit > 600) {
		v.Add("time_limit", "The time limit must be between 0 and 600 minutes.")
	}
	_, roles, _ := m.actor(c)
	seen := map[int64]bool{}
	for i, q := range b.Questions {
		key := "questions." + strconv.Itoa(i)
		var cid, child int64
		_ = m.db.Raw("SELECT course_id FROM questions WHERE id = ?", q.QuestionID).Scan(&cid)
		_ = m.db.Raw("SELECT count(*) FROM questions WHERE id = ? AND parent_question_id IS NOT NULL", q.QuestionID).Scan(&child)
		switch {
		case child > 0:
			v.Add(key, "Câu hỏi con chỉ được chọn cùng đoạn văn của nó.")
		case seen[q.QuestionID]:
			v.Add(key, "Câu hỏi bị trùng.")
		case cid == 0 || !m.canUseCourse(uid, roles, cid):
			v.Add(key, "The selected question is invalid.")
		case q.Points != nil && *q.Points < 0:
			v.Add(key, "The points must be at least 0.")
		}
		seen[q.QuestionID] = true
	}
	if v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	qt := title
	if b.Title != nil && strings.TrimSpace(*b.Title) != "" {
		qt = strings.TrimSpace(*b.Title)
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var qid int64
		if err := tx.Raw(`INSERT INTO quizzes (scope, course_id, lesson_id, title, time_limit, pass_score) VALUES ('lesson', ?, ?, ?, ?, ?)
			ON CONFLICT (lesson_id) WHERE lesson_id IS NOT NULL DO UPDATE SET title = EXCLUDED.title, time_limit = EXCLUDED.time_limit, pass_score = EXCLUDED.pass_score
			RETURNING id`, cr.ID, lid, qt, b.TimeLimit, pass).Scan(&qid).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM quiz_questions WHERE quiz_id = ?", qid).Error; err != nil {
			return err
		}
		for i, q := range b.Questions {
			if err := tx.Exec("INSERT INTO quiz_questions (quiz_id, question_id, position, points) VALUES (?, ?, ?, ?)", qid, q.QuestionID, i, q.Points).Error; err != nil {
				return err
			}
		}
		return tx.Exec("UPDATE lessons SET type = 'quiz' WHERE id = ?", lid).Error
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	m.respondLessonQuiz(c, lid)
}

// ---- taking ---------------------------------------------------------------------

type qa struct {
	ID         int64
	Content    string
	SubContent *string
	IsCorrect  bool
	CorrectPos *int
	Position   int
}

type quizQuestion struct {
	ID         int64
	Type       string
	Content    string
	Points     float64
	Explain    *string
	Feedback   *string
	Metadata   []byte
	BankID     *int64
	GroupID    *int64
	Difficulty int
	ParentID   *int64
	Answers    []qa `gorm:"-"`
}

const questionCols = `x.id, x.type, x.content, x.explanation AS explain, x.feedback, x.metadata, x.parent_question_id AS parent_id`

// expandPassages puts the sub-questions of every passage right after it (they are picked, drawn and graded with
// their passage, never alone) and loads the answers of every question. A passage itself carries no points.
func (m *Module) expandPassages(qs []quizQuestion) ([]quizQuestion, error) {
	out := make([]quizQuestion, 0, len(qs))
	for _, q := range qs {
		out = append(out, q)
		if q.Type != "passage" {
			continue
		}
		out[len(out)-1].Points = 0
		var kids []quizQuestion
		if err := m.db.Raw("SELECT "+questionCols+", x.default_score::float8 AS points FROM questions x WHERE x.parent_question_id = ? ORDER BY x.id", q.ID).Scan(&kids).Error; err != nil {
			return nil, err
		}
		out = append(out, kids...)
	}
	for i := range out {
		if err := m.db.Raw(`SELECT id, content, sub_content, is_correct, correct_position AS correct_pos, position FROM question_answers WHERE question_id = ? ORDER BY position, id`, out[i].ID).Scan(&out[i].Answers).Error; err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (m *Module) quizQuestions(quizID int64) ([]quizQuestion, error) {
	var qs []quizQuestion
	err := m.db.Raw(`SELECT `+questionCols+`, coalesce(qq.points, x.default_score)::float8 AS points
		FROM quiz_questions qq JOIN questions x ON x.id = qq.question_id WHERE qq.quiz_id = ? ORDER BY qq.position, x.id`, quizID).Scan(&qs).Error
	if err != nil {
		return nil, err
	}
	return m.expandPassages(qs)
}

type attemptRow struct {
	ID        int64
	QuizID    int64
	UserID    int64
	Status    string
	Answers   []byte
	StartedAt time.Time
}

func remaining(started time.Time, limit *int) *int {
	if limit == nil || *limit <= 0 {
		return nil
	}
	left := int(time.Until(started.Add(time.Duration(*limit) * time.Minute)).Seconds())
	if left < 0 {
		left = 0
	}
	return &left
}

// presentQuestion builds the learner's view of a question; it never contains the correct answers.
// Shuffles are stable per attempt (seeded by the attempt and the question).
func presentQuestion(q quizQuestion, seed int64) gin.H {
	out := gin.H{"id": q.ID, "type": q.Type, "content": q.Content, "points": q.Points}
	if q.ParentID != nil {
		out["parent_id"] = *q.ParentID // sub-question: shown under its passage
	}
	if t, ok := questionTypes[q.Type]; ok {
		t.present(q, rand.New(rand.NewSource(seed+q.ID)), out)
	}
	return out
}

func (m *Module) takeQuiz(c *gin.Context) {
	cr, lid, ok := m.courseLesson(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok || !m.gate(c, cr, uid, lid) {
		return
	}
	q, err := m.lessonQuiz(lid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if q == nil {
		httpx.NotFound(c, "")
		return
	}
	qs, err := m.quizQuestions(q.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if len(qs) == 0 {
		httpx.Abort(c, http.StatusUnprocessableEntity, "Bài kiểm tra nhanh chưa có câu hỏi.")
		return
	}
	var at attemptRow
	res := m.db.Raw(`SELECT id, quiz_id, user_id, status, answers, started_at FROM quiz_attempts
		WHERE quiz_id = ? AND user_id = ? AND status = 'in_progress' ORDER BY started_at DESC LIMIT 1`, q.ID, uid).Scan(&at)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	resumed := res.RowsAffected > 0
	// An unfinished attempt whose time ran out is closed (zero score unless saved answers earn points).
	if resumed {
		if left := remaining(at.StartedAt, q.TimeLimit); left != nil && *left == 0 {
			if _, err := m.gradeAttempt(at, q, qs, nil); err != nil {
				httpx.Fail(c, err)
				return
			}
			resumed = false
		}
	}
	if !resumed {
		ids := make([]string, len(qs))
		for i, x := range qs {
			ids[i] = strconv.FormatInt(x.ID, 10)
		}
		if err := m.db.Raw(`INSERT INTO quiz_attempts (quiz_id, user_id, question_ids, ip_address, user_agent) VALUES (?, ?, ?::bigint[], NULLIF(?, '')::inet, ?) RETURNING id, quiz_id, user_id, status, started_at`,
			q.ID, uid, "{"+strings.Join(ids, ",")+"}", c.ClientIP(), c.Request.UserAgent()).Scan(&at).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	presented := make([]gin.H, len(qs))
	for i, x := range qs {
		presented[i] = presentQuestion(x, at.ID)
	}
	saved := gin.H{}
	if len(at.Answers) > 0 {
		_ = json.Unmarshal(at.Answers, &saved)
	}
	c.JSON(http.StatusOK, gin.H{
		"attempt":   gin.H{"id": at.ID, "status": at.Status, "remaining_time": remaining(at.StartedAt, q.TimeLimit)},
		"quiz":      gin.H{"id": q.ID, "title": q.Title, "description": nil, "time_limit": q.TimeLimit, "pass_score": q.PassScore},
		"questions": presented, "saved_answers": saved, "resumed": resumed,
	})
}

func (m *Module) autoSaveAttempt(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var b struct {
		Answers map[string]any `json:"answers"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	j, _ := json.Marshal(b.Answers)
	res := m.db.Exec("UPDATE quiz_attempts SET answers = ?::jsonb, auto_saved_at = now() WHERE id = ? AND user_id = ? AND status = 'in_progress'", string(j), id, auth.UserID(c))
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"saved": true})
}

// ---- grading --------------------------------------------------------------------

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(x, ",", ".")), 64)
		return f, err == nil
	}
	return 0, false
}

func idsOf(v any) []int64 {
	var out []int64
	switch x := v.(type) {
	case float64:
		out = []int64{int64(x)}
	case []any:
		for _, e := range x {
			if f, ok := e.(float64); ok {
				out = append(out, int64(f))
			}
		}
	}
	return out
}

// manualGrade is a grader's score for one question that needs a human (essay, file upload).
type manualGrade struct {
	Score   float64 `json:"score"`
	Comment string  `json:"comment,omitempty"`
}

type attemptScore struct {
	Earned, Max, Score float64
	Pending            bool // some manual question has no grade yet
	Review             []gin.H
}

// scoreAttempt grades every question of an attempt. Questions that need a human use the grades in manual;
// without one they stay pending. It is pure, so closing an attempt and re-grading it later share one code path.
func scoreAttempt(qs []quizQuestion, answers map[string]any, seed int64, manual map[string]manualGrade) attemptScore {
	out := attemptScore{Review: make([]gin.H, 0, len(qs))}
	for _, x := range qs {
		out.Max += x.Points
		key := strconv.FormatInt(x.ID, 10)
		ans := answers[key]
		if x.Type == "passage" {
			out.Review = append(out.Review, presentQuestion(x, seed))
			continue
		}
		res := gradeQuestion(x, ans)
		sc := 0.0
		graded, hasGrade := manual[key]
		switch {
		case res.Manual && hasGrade:
			sc = math.Round(math.Min(math.Max(graded.Score, 0), x.Points)*100) / 100
		case res.Manual:
			out.Pending = true
		case res.full():
			sc = x.Points
		default:
			sc = math.Round(x.Points*res.Fraction*100) / 100
		}
		out.Earned += sc
		item := presentQuestion(x, seed)
		item["answer"], item["answered"], item["is_correct"], item["score"] = ans, ans != nil, nil, sc
		switch {
		case res.Manual && hasGrade:
			item["is_correct"] = sc >= x.Points-1e-9
			item["partial"] = sc > 0 && sc < x.Points-1e-9
			item["grader_comment"] = graded.Comment
			item["grade"] = graded
		case !res.Manual:
			item["is_correct"] = res.full()
			item["partial"] = !res.full() && res.Fraction > 0
			item["correct_answer"] = res.Reveal
		}
		item["explanation"], item["feedback"] = x.Explain, x.Feedback
		item["manual"] = res.Manual
		item["needs_manual"] = res.Manual && !hasGrade
		out.Review = append(out.Review, item)
	}
	if out.Max > 0 {
		out.Score = math.Round(out.Earned/out.Max*10000) / 100
	}
	return out
}

// attemptFiles lists the files uploaded for an attempt, grouped by question id.
func (m *Module) attemptFiles(attemptID int64) (map[int64][]gin.H, error) {
	var rows []struct {
		ID           int64
		QuestionID   int64
		OriginalName string
		SizeBytes    int64
	}
	if err := m.db.Raw("SELECT id, question_id, original_name, size_bytes FROM attempt_files WHERE attempt_id = ? ORDER BY id", attemptID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := map[int64][]gin.H{}
	for _, r := range rows {
		out[r.QuestionID] = append(out[r.QuestionID], gin.H{"id": r.ID, "name": r.OriginalName, "size": r.SizeBytes})
	}
	return out, nil
}

// normalizeFileAnswers makes the answer of every file question exactly the files stored for it: a learner cannot
// claim a file they did not upload, and an answer without files counts as unanswered.
func (m *Module) normalizeFileAnswers(attemptID int64, qs []quizQuestion, answers map[string]any) error {
	var fileQs []quizQuestion
	for _, q := range qs {
		if q.Type == "file_upload" {
			fileQs = append(fileQs, q)
		}
	}
	if len(fileQs) == 0 {
		return nil
	}
	files, err := m.attemptFiles(attemptID)
	if err != nil {
		return err
	}
	for _, q := range fileQs {
		key := strconv.FormatInt(q.ID, 10)
		if list := files[q.ID]; len(list) > 0 {
			answers[key] = gin.H{"files": list}
		} else {
			delete(answers, key)
		}
	}
	return nil
}

// gradeAttempt scores and closes an attempt; answers == nil uses what was autosaved.
// An attempt with manual questions is left pending until a grader scores them (see grading.go).
func (m *Module) gradeAttempt(at attemptRow, q *quizRow, qs []quizQuestion, answers map[string]any) (gin.H, error) {
	if answers == nil {
		answers = map[string]any{}
		if len(at.Answers) > 0 {
			_ = json.Unmarshal(at.Answers, &answers)
		}
	}
	if err := m.normalizeFileAnswers(at.ID, qs, answers); err != nil {
		return nil, err
	}
	sc := scoreAttempt(qs, answers, at.ID, nil)
	passed := sc.Score >= float64(q.PassScore) && !sc.Pending
	status := "none"
	if sc.Pending {
		status = "pending"
	}
	j, _ := json.Marshal(answers)
	if err := m.db.Exec(`UPDATE quiz_attempts SET status = 'submitted', completed_at = now(), answers = ?::jsonb, score = ?, passed = ?, grading_status = ? WHERE id = ?`,
		string(j), sc.Score, passed, status, at.ID).Error; err != nil {
		return nil, err
	}
	out := gin.H{"review_available": true, "score": sc.Score, "points_earned": sc.Earned, "points_max": sc.Max, "pending_manual": sc.Pending, "questions": sc.Review}
	if sc.Pending {
		out["passed"] = nil
	} else {
		out["passed"] = passed
	}
	return out, nil
}

func (m *Module) submitQuiz(c *gin.Context) {
	cr, lid, ok := m.courseLesson(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok {
		return
	}
	var b struct {
		AttemptID int64          `json:"attempt_id"`
		Answers   map[string]any `json:"answers"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	q, err := m.lessonQuiz(lid)
	if err != nil || q == nil {
		httpx.NotFound(c, "")
		return
	}
	var at attemptRow
	res := m.db.Raw(`SELECT id, quiz_id, user_id, status, answers, started_at FROM quiz_attempts WHERE id = ? AND quiz_id = ? AND user_id = ?`, b.AttemptID, q.ID, uid).Scan(&at)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	if at.Status != "in_progress" {
		httpx.Abort(c, http.StatusConflict, "Bài làm này đã được nộp.")
		return
	}
	qs, err := m.quizQuestions(q.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	auto := false
	if left := remaining(at.StartedAt, q.TimeLimit); left != nil && *left == 0 {
		auto = true
	}
	out, err := m.gradeAttempt(at, q, qs, b.Answers)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out["auto_submitted"] = auto
	completed := false
	if p, ok := out["passed"].(bool); ok && p {
		completed = true
		_ = m.db.Exec(`INSERT INTO lesson_progress (user_id, lesson_id, progress_percent, completed_at, last_watched_at) VALUES (?, ?, 100, now(), now())
			ON CONFLICT (user_id, lesson_id) DO UPDATE SET progress_percent = 100, completed_at = coalesce(lesson_progress.completed_at, now()), last_watched_at = now()`, uid, lid)
	}
	out["lesson_completed"] = completed
	c.JSON(http.StatusOK, out)
}
