package learning

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Taking exams as a candidate. Reuses the quiz engine (presentation + grading) of quiz.go; the paper of an exam is
// its fixed questions plus random draws from the question banks, fixed at the moment an attempt starts.

// attemptWithIDs is an attempt row plus its question ids (flat struct: gorm does not fill embedded pointers).
type attemptWithIDs struct {
	ID        int64
	QuizID    int64
	UserID    int64
	Status    string
	Answers   []byte
	StartedAt time.Time
	IDsRaw    string
}

func (a attemptWithIDs) row() attemptRow {
	return attemptRow{ID: a.ID, QuizID: a.QuizID, UserID: a.UserID, Status: a.Status, Answers: a.Answers, StartedAt: a.StartedAt}
}

type examTake struct {
	ID          int64
	Type        string
	CourseID    *int64
	Status      string
	Title       string
	Duration    *int
	PassScore   int
	MaxAttempts int
	StartsAt    *time.Time
	EndsAt      *time.Time
	ShuffleQ    bool
	ShuffleA    bool
	Review      []byte
	QuizID      int64
	Settings    []byte
}

// candidateExam loads the exam and checks the caller is one of its candidates.
func (m *Module) candidateExam(c *gin.Context) (examTake, int64, bool) {
	id, ok := idParam(c, "id")
	if !ok {
		return examTake{}, 0, false
	}
	uid := auth.UserID(c)
	var e examTake
	res := m.db.Raw(`SELECT e.id, e.type, e.course_id, e.status, e.title, e.duration, e.pass_score, e.max_attempts, e.starts_at, e.ends_at,
		e.shuffle_questions AS shuffle_q, e.shuffle_answers AS shuffle_a, e.review_options AS review, z.id AS quiz_id, z.settings
		FROM exams e JOIN quizzes z ON z.exam_id = e.id WHERE e.id = ?`, id).Scan(&e)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return e, 0, false
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return e, 0, false
	}
	var n int64
	if e.Type == "course_final" && e.CourseID != nil {
		_ = m.db.Raw("SELECT count(*) FROM enrollments WHERE user_id = ? AND course_id = ?", uid, *e.CourseID).Scan(&n)
	} else {
		_ = m.db.Raw("SELECT count(*) FROM exam_enrollments WHERE user_id = ? AND exam_id = ?", uid, e.ID).Scan(&n)
	}
	if n == 0 {
		httpx.Forbidden(c, "Bạn không nằm trong danh sách thí sinh của kỳ thi này.")
		return e, 0, false
	}
	return e, uid, true
}

// myExams lists the exams the caller is a candidate of.
func (m *Module) myExams(c *gin.Context) {
	uid := auth.UserID(c)
	rows, err := m.jsonRows(`SELECT jsonb_build_object('id', e.id, 'title', e.title, 'status', e.status, 'type', e.type,
		'course_title', (SELECT title FROM courses WHERE id = e.course_id), 'duration', e.duration, 'pass_score', e.pass_score,
		'max_attempts', e.max_attempts, 'starts_at', e.starts_at, 'ends_at', e.ends_at, 'room', e.room,
		'attempts_used', (SELECT count(*) FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = e.id AND a.user_id = ?),
		'best_score', (SELECT max(a.score)::float8 FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = e.id AND a.user_id = ? AND a.status = 'submitted'),
		'passed', coalesce((SELECT bool_or(a.passed) FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = e.id AND a.user_id = ?), false))
		FROM exams e WHERE e.status IN ('scheduled', 'active', 'closed') AND (
			(e.type = 'standalone' AND EXISTS (SELECT 1 FROM exam_enrollments x WHERE x.exam_id = e.id AND x.user_id = ?)) OR
			(e.type = 'course_final' AND EXISTS (SELECT 1 FROM enrollments en WHERE en.course_id = e.course_id AND en.user_id = ?)))
		ORDER BY e.starts_at NULLS LAST, e.id DESC`, uid, uid, uid, uid, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

// examPaper builds the question list for a new attempt (fixed questions + random rules).
func (m *Module) examPaper(e examTake, seed int64) ([]int64, error) {
	var fixed []int64
	if err := m.db.Raw("SELECT question_id FROM quiz_questions WHERE quiz_id = ? ORDER BY position, question_id", e.QuizID).Scan(&fixed).Error; err != nil {
		return nil, err
	}
	var settings struct {
		RandomRules []randomRule `json:"random_rules"`
	}
	if len(e.Settings) > 0 {
		_ = json.Unmarshal(e.Settings, &settings)
	}
	r := rand.New(rand.NewSource(seed))
	have := map[int64]bool{}
	for _, id := range fixed {
		have[id] = true
	}
	ids := append([]int64{}, fixed...)
	for _, rule := range settings.RandomRules {
		cond, args := "question_bank_id = ? AND parent_question_id IS NULL", []any{rule.BankID} // sub-questions come with their passage
		if rule.GroupID != nil {
			cond, args = cond+" AND question_group_id = ?", append(args, *rule.GroupID)
		}
		if rule.Difficulty != nil {
			cond, args = cond+" AND difficulty = ?", append(args, *rule.Difficulty)
		}
		var pool []int64
		if err := m.db.Raw("SELECT id FROM questions WHERE "+cond+" ORDER BY id", args...).Scan(&pool).Error; err != nil {
			return nil, err
		}
		r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		taken := 0
		for _, id := range pool {
			if taken >= rule.Count {
				break
			}
			if !have[id] {
				have[id] = true
				ids = append(ids, id)
				taken++
			}
		}
	}
	if e.ShuffleQ {
		r.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	}
	return ids, nil
}

// paperQuestions loads the questions of an attempt in order, with their points for this exam.
// A passage is followed by its sub-questions, which score by their own default points.
func (m *Module) paperQuestions(e examTake, ids []int64) ([]quizQuestion, error) {
	var settings struct {
		RandomRules []randomRule `json:"random_rules"`
	}
	if len(e.Settings) > 0 {
		_ = json.Unmarshal(e.Settings, &settings)
	}
	top := make([]quizQuestion, 0, len(ids))
	for _, id := range ids {
		var q quizQuestion
		res := m.db.Raw(`SELECT `+questionCols+`, coalesce(qq.points, x.default_score)::float8 AS points,
			x.question_bank_id AS bank_id, x.question_group_id AS group_id, x.difficulty
			FROM questions x LEFT JOIN quiz_questions qq ON qq.question_id = x.id AND qq.quiz_id = ? WHERE x.id = ?`, e.QuizID, id).Scan(&q)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		var fixed int64
		_ = m.db.Raw("SELECT count(*) FROM quiz_questions WHERE quiz_id = ? AND question_id = ?", e.QuizID, id).Scan(&fixed)
		if fixed == 0 { // random draw: points come from the matching rule
			for _, rule := range settings.RandomRules {
				if q.BankID != nil && *q.BankID == rule.BankID && rule.Points != nil {
					q.Points = *rule.Points
					break
				}
			}
		}
		top = append(top, q)
	}
	return m.expandPassages(top)
}

func (m *Module) examRemaining(e examTake, started time.Time) *int {
	if e.Duration == nil || *e.Duration <= 0 {
		return nil
	}
	end := started.Add(time.Duration(*e.Duration) * time.Minute)
	if e.EndsAt != nil && e.EndsAt.Before(end) {
		end = *e.EndsAt
	}
	left := int(time.Until(end).Seconds())
	if left < 0 {
		left = 0
	}
	return &left
}

func parseIDs(raw string) []int64 {
	raw = strings.Trim(raw, "{}")
	var out []int64
	for _, p := range strings.Split(raw, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func (m *Module) takeExam(c *gin.Context) {
	e, uid, ok := m.candidateExam(c)
	if !ok {
		return
	}
	now := time.Now()
	switch {
	case e.Status != "active":
		httpx.Abort(c, http.StatusForbidden, "Kỳ thi chưa mở hoặc đã đóng.")
		return
	case e.StartsAt != nil && now.Before(*e.StartsAt):
		httpx.Abort(c, http.StatusForbidden, "Kỳ thi sẽ bắt đầu lúc "+fmtWhen(*e.StartsAt))
		return
	case e.EndsAt != nil && now.After(*e.EndsAt):
		httpx.Abort(c, http.StatusForbidden, "Kỳ thi đã kết thúc lúc "+fmtWhen(*e.EndsAt))
		return
	}
	q := &quizRow{ID: e.QuizID, Title: e.Title, TimeLimit: e.Duration, PassScore: e.PassScore}
	var cur attemptWithIDs
	res := m.db.Raw(`SELECT id, quiz_id, user_id, status, answers, started_at, question_ids::text AS ids_raw FROM quiz_attempts
		WHERE quiz_id = ? AND user_id = ? AND status = 'in_progress' ORDER BY started_at DESC LIMIT 1`, e.QuizID, uid).Scan(&cur)
	at, idsRaw := cur.row(), cur.IDsRaw
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	resumed := res.RowsAffected > 0
	if resumed {
		// An attempt whose time ran out is closed first; it counts as used.
		if left := m.examRemaining(e, at.StartedAt); left != nil && *left == 0 {
			qs, err := m.paperQuestions(e, parseIDs(idsRaw))
			if err == nil {
				_, err = m.gradeAttempt(at, q, qs, nil)
			}
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			resumed = false
		}
	}
	if !resumed {
		var used int64
		_ = m.db.Raw("SELECT count(*) FROM quiz_attempts WHERE quiz_id = ? AND user_id = ?", e.QuizID, uid).Scan(&used)
		if used >= int64(e.MaxAttempts) {
			httpx.Abort(c, http.StatusForbidden, "Bạn đã hết số lần làm bài cho phép.")
			return
		}
		ids, err := m.examPaper(e, now.UnixNano())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if len(ids) == 0 {
			httpx.Abort(c, http.StatusUnprocessableEntity, "Kỳ thi chưa có câu hỏi.")
			return
		}
		strs := make([]string, len(ids))
		for i, id := range ids {
			strs[i] = strconv.FormatInt(id, 10)
		}
		idsRaw = "{" + strings.Join(strs, ",") + "}"
		if err := m.db.Raw(`INSERT INTO quiz_attempts (quiz_id, user_id, question_ids, ip_address, user_agent) VALUES (?, ?, ?::bigint[], NULLIF(?, '')::inet, ?)
			RETURNING id, quiz_id, user_id, status, started_at`, e.QuizID, uid, idsRaw, c.ClientIP(), c.Request.UserAgent()).Scan(&at).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	qs, err := m.paperQuestions(e, parseIDs(idsRaw))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	presented := make([]gin.H, len(qs))
	for i, x := range qs {
		if e.ShuffleA && (x.Type == "single_choice" || x.Type == "multiple_choice") {
			r := rand.New(rand.NewSource(at.ID + x.ID))
			r.Shuffle(len(x.Answers), func(a, b int) { x.Answers[a], x.Answers[b] = x.Answers[b], x.Answers[a] })
		}
		presented[i] = presentQuestion(x, at.ID)
	}
	saved := gin.H{}
	if len(at.Answers) > 0 {
		_ = json.Unmarshal(at.Answers, &saved)
	}
	c.JSON(http.StatusOK, gin.H{
		"attempt":   gin.H{"id": at.ID, "status": at.Status, "remaining_time": m.examRemaining(e, at.StartedAt)},
		"quiz":      gin.H{"id": e.QuizID, "title": e.Title, "description": nil, "time_limit": e.Duration, "pass_score": e.PassScore},
		"questions": presented, "saved_answers": saved, "resumed": resumed,
	})
}

func (m *Module) submitExam(c *gin.Context) {
	e, uid, ok := m.candidateExam(c)
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
	var cur attemptWithIDs
	res := m.db.Raw(`SELECT id, quiz_id, user_id, status, answers, started_at, question_ids::text AS ids_raw FROM quiz_attempts WHERE id = ? AND quiz_id = ? AND user_id = ?`,
		b.AttemptID, e.QuizID, uid).Scan(&cur)
	at, idsRaw := cur.row(), cur.IDsRaw
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
	qs, err := m.paperQuestions(e, parseIDs(idsRaw))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	auto := false
	if left := m.examRemaining(e, at.StartedAt); left != nil && *left == 0 {
		auto = true
	}
	out, err := m.gradeAttempt(at, &quizRow{ID: e.QuizID, Title: e.Title, TimeLimit: e.Duration, PassScore: e.PassScore}, qs, b.Answers)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out["auto_submitted"] = auto
	out["lesson_completed"] = false
	applyExamReview(out, e.Review)
	c.JSON(http.StatusOK, out)
}

// applyExamReview hides from a result what the instructor chose not to show after an exam
// (score, the answers, which ones were right, the explanations).
func applyExamReview(out gin.H, reviewJSON []byte) {
	var rv struct {
		ShowScore   *bool `json:"show_score"`
		ShowAnswers *bool `json:"show_answers"`
		ShowCorrect *bool `json:"show_correct_answers"`
		ShowExplain *bool `json:"show_explanation"`
	}
	_ = json.Unmarshal(reviewJSON, &rv)
	on := func(v *bool, def bool) bool {
		if v == nil {
			return def
		}
		return *v
	}
	if !on(rv.ShowScore, true) {
		delete(out, "score")
		delete(out, "points_earned")
		delete(out, "points_max")
		delete(out, "passed")
	}
	if items, ok := out["questions"].([]gin.H); ok {
		if !on(rv.ShowAnswers, false) {
			delete(out, "questions")
			out["review_available"] = false
			return
		}
		for _, it := range items {
			if !on(rv.ShowCorrect, false) {
				delete(it, "correct_answer")
				delete(it, "is_correct")
			}
			if !on(rv.ShowExplain, false) {
				delete(it, "explanation")
				delete(it, "feedback")
			}
		}
	}
}
