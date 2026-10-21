package learning

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Exams: course-final and standalone exams, question paper (fixed + random rules), candidates, results.
// Contracts follow eript-web/src/features/exams/types.ts.

const examJSON = `jsonb_build_object('id', e.id, 'title', e.title, 'description', e.description, 'type', e.type,
  'course_id', e.course_id, 'course_title', (SELECT title FROM courses WHERE id = e.course_id), 'status', e.status,
  'duration', e.duration, 'pass_score', e.pass_score, 'max_attempts', e.max_attempts, 'variant_count', e.variant_count,
  'shuffle_questions', e.shuffle_questions, 'shuffle_answers', e.shuffle_answers, 'proctoring_enabled', e.proctoring_enabled,
  'starts_at', e.starts_at, 'ends_at', e.ends_at, 'room', e.room,
  'question_count', (SELECT count(*) FROM quiz_questions qq JOIN quizzes z ON z.id = qq.quiz_id WHERE z.exam_id = e.id)
      + coalesce((SELECT sum((r->>'count')::int) FROM quizzes z, jsonb_array_elements(coalesce(z.settings->'random_rules', '[]'::jsonb)) r WHERE z.exam_id = e.id), 0),
  'attempts_count', (SELECT count(*) FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = e.id),
  'candidates_count', CASE WHEN e.type = 'course_final' THEN (SELECT count(*) FROM enrollments en WHERE en.course_id = e.course_id)
      ELSE (SELECT count(*) FROM exam_enrollments x WHERE x.exam_id = e.id) END,
  'review_options', coalesce(e.review_options, '{"show_score": true, "show_answers": false, "show_correct_answers": false, "show_explanation": false, "when": "immediately"}'::jsonb),
  'proctoring', jsonb_build_object('enabled', e.proctoring_enabled,
      'require_face', coalesce((SELECT (z.settings->'proctoring'->>'require_face')::boolean FROM quizzes z WHERE z.exam_id = e.id), false),
      'require_fullscreen', coalesce((SELECT (z.settings->'proctoring'->>'require_fullscreen')::boolean FROM quizzes z WHERE z.exam_id = e.id), false),
      'block_copy_paste', coalesce((SELECT (z.settings->'proctoring'->>'block_copy_paste')::boolean FROM quizzes z WHERE z.exam_id = e.id), false),
      'max_violations', coalesce((SELECT (z.settings->'proctoring'->>'max_violations')::int FROM quizzes z WHERE z.exam_id = e.id), 3),
      'auto_submit', coalesce((SELECT (z.settings->'proctoring'->>'auto_submit')::boolean FROM quizzes z WHERE z.exam_id = e.id), false)))`

var examStatuses = map[string]bool{"draft": true, "scheduled": true, "active": true, "closed": true, "archived": true}

type examRef struct {
	ID        int64
	Type      string
	CourseID  *int64
	CreatedBy *int64
	Status    string
}

func (m *Module) loadExam(c *gin.Context) (examRef, bool) {
	id, ok := idParam(c, "id")
	if !ok {
		return examRef{}, false
	}
	var e examRef
	res := m.db.Raw("SELECT id, type, course_id, created_by, status FROM exams WHERE id = ?", id).Scan(&e)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return e, false
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return e, false
	}
	return e, true
}

// manageExam resolves the exam and requires its owner (course instructor or creator) or an admin.
func (m *Module) manageExam(c *gin.Context) (examRef, int64, bool) {
	e, ok := m.loadExam(c)
	if !ok {
		return e, 0, false
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return e, 0, false
	}
	if hasRole(roles, auth.RoleAdmin) {
		return e, uid, true
	}
	if e.CreatedBy != nil && *e.CreatedBy == uid {
		return e, uid, true
	}
	if e.CourseID != nil {
		var inst int64
		_ = m.db.Raw("SELECT instructor_id FROM courses WHERE id = ?", *e.CourseID).Scan(&inst)
		if inst == uid {
			return e, uid, true
		}
	}
	httpx.Forbidden(c, "")
	return e, 0, false
}

type examBody struct {
	Title            *string        `json:"title"`
	Description      *string        `json:"description"`
	Duration         *int           `json:"duration"`
	PassScore        *int           `json:"pass_score"`
	MaxAttempts      *int           `json:"max_attempts"`
	VariantCount     *int           `json:"variant_count"`
	StartsAt         *string        `json:"starts_at"`
	EndsAt           *string        `json:"ends_at"`
	Room             *string        `json:"room"`
	ShuffleQuestions *bool          `json:"shuffle_questions"`
	ShuffleAnswers   *bool          `json:"shuffle_answers"`
	Proctoring       map[string]any `json:"proctoring"`
	ReviewOptions    map[string]any `json:"review_options"`
}

func validateExam(b examBody, keys map[string]json.RawMessage, create bool) *httpx.ValidationError {
	v := httpx.NewValidationError()
	if _, ok := keys["title"]; ok || create {
		if b.Title == nil || strings.TrimSpace(*b.Title) == "" {
			v.Add("title", "The title field is required.")
		} else if len(strings.TrimSpace(*b.Title)) > 255 {
			v.Add("title", "The title may not be greater than 255 characters.")
		}
	}
	if b.Duration != nil && *b.Duration < 0 {
		v.Add("duration", "The duration must be at least 0.")
	}
	if b.PassScore != nil && (*b.PassScore < 0 || *b.PassScore > 100) {
		v.Add("pass_score", "The pass score must be between 0 and 100.")
	}
	if b.MaxAttempts != nil && (*b.MaxAttempts < 1 || *b.MaxAttempts > 99) {
		v.Add("max_attempts", "The max attempts must be between 1 and 99.")
	}
	if b.VariantCount != nil && (*b.VariantCount < 1 || *b.VariantCount > 26) {
		v.Add("variant_count", "The variant count must be between 1 and 26.")
	}
	if !whenValid(b.StartsAt) {
		v.Add("starts_at", "The starts at is not a valid date.")
	}
	if !whenValid(b.EndsAt) {
		v.Add("ends_at", "The ends at is not a valid date.")
	}
	if s, e := parseWhen(b.StartsAt), parseWhen(b.EndsAt); s != nil && e != nil && e.(time.Time).Before(s.(time.Time)) {
		v.Add("ends_at", "The ends at must be after the starts at.")
	}
	return v
}

func (m *Module) createExam(c *gin.Context, courseID *int64, kind string) {
	uid := auth.UserID(c)
	body, _ := c.GetRawData()
	var b examBody
	if err := json.Unmarshal(body, &b); err != nil {
		httpx.Abort(c, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	if v := validateExam(b, present(c, body), true); v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	pass := 80
	if b.PassScore != nil {
		pass = *b.PassScore
	}
	var id int64
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`INSERT INTO exams (course_id, type, title, description, duration, pass_score, created_by)
			VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, courseID, kind, strings.TrimSpace(*b.Title), nullStr(b.Description), b.Duration, pass, uid).Scan(&id).Error; err != nil {
			return err
		}
		return m.ensureQuiz(tx, id)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+examJSON+" FROM exams e WHERE e.id = ?", id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "", out)
}

func (m *Module) createCourseExam(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	m.createExam(c, &cr.ID, "course_final")
}

func (m *Module) createStandaloneExam(c *gin.Context) {
	_, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !hasRole(roles, auth.RoleAdmin, auth.RoleAcademicManager, auth.RoleInstructor) {
		httpx.Forbidden(c, "")
		return
	}
	m.createExam(c, nil, "standalone")
}

// ensureQuiz makes sure the exam has its question paper row.
func (m *Module) ensureQuiz(tx *gorm.DB, examID int64) error {
	return tx.Exec(`INSERT INTO quizzes (scope, exam_id, title, time_limit, pass_score)
		SELECT 'exam', e.id, e.title, e.duration, e.pass_score FROM exams e WHERE e.id = ?
		ON CONFLICT (exam_id) WHERE exam_id IS NOT NULL DO NOTHING`, examID).Error
}

func (m *Module) listExams(c *gin.Context, where string, args []any) {
	page, per := pageParams(c, 12, 100)
	conds, qargs := []string{where}, args
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		conds = append(conds, "e.title ILIKE ?")
		qargs = append(qargs, likeArg(s))
	}
	if st := c.Query("status"); st != "" {
		conds = append(conds, "e.status = ?")
		qargs = append(qargs, st)
	}
	w := " WHERE " + strings.Join(conds, " AND ")
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM exams e"+w, qargs...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows("SELECT "+examJSON+" FROM exams e"+w+" ORDER BY e.created_at DESC, e.id DESC LIMIT ? OFFSET ?", append(qargs, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, total, page, per)
}

func (m *Module) listCourseExams(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	m.listExams(c, "e.course_id = ?", []any{cr.ID})
}

func (m *Module) listStandaloneExams(c *gin.Context) {
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !hasRole(roles, auth.RoleAdmin, auth.RoleAcademicManager, auth.RoleInstructor) {
		httpx.Forbidden(c, "")
		return
	}
	if hasRole(roles, auth.RoleAdmin, auth.RoleAcademicManager) {
		m.listExams(c, "e.type = 'standalone'", nil)
		return
	}
	m.listExams(c, "e.type = 'standalone' AND e.created_by = ?", []any{uid})
}

func (m *Module) updateExam(c *gin.Context) {
	e, _, ok := m.manageExam(c)
	if !ok {
		return
	}
	body, _ := c.GetRawData()
	var b examBody
	if err := json.Unmarshal(body, &b); err != nil {
		httpx.Abort(c, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	keys := present(c, body)
	if v := validateExam(b, keys, false); v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	if e.Status == "archived" {
		httpx.Abort(c, http.StatusConflict, "Kỳ thi đã lưu trữ, không thể sửa.")
		return
	}
	sets, args := []string{}, []any{}
	add := func(col string, val any) { sets, args = append(sets, col+" = ?"), append(args, val) }
	has := func(k string) bool { _, ok := keys[k]; return ok }
	if has("title") {
		add("title", strings.TrimSpace(*b.Title))
	}
	if has("description") {
		add("description", nullStr(b.Description))
	}
	if has("duration") {
		add("duration", b.Duration)
	}
	if has("pass_score") && b.PassScore != nil {
		add("pass_score", *b.PassScore)
	}
	if has("max_attempts") && b.MaxAttempts != nil {
		add("max_attempts", *b.MaxAttempts)
	}
	if has("variant_count") && b.VariantCount != nil {
		add("variant_count", *b.VariantCount)
	}
	if has("starts_at") {
		add("starts_at", parseWhen(b.StartsAt))
	}
	if has("ends_at") {
		add("ends_at", parseWhen(b.EndsAt))
	}
	if has("room") {
		add("room", nullStr(b.Room))
	}
	if has("shuffle_questions") && b.ShuffleQuestions != nil {
		add("shuffle_questions", *b.ShuffleQuestions)
	}
	if has("shuffle_answers") && b.ShuffleAnswers != nil {
		add("shuffle_answers", *b.ShuffleAnswers)
	}
	if has("review_options") && b.ReviewOptions != nil {
		add("review_options", mustJSON(b.ReviewOptions))
		sets[len(sets)-1] = "review_options = ?::jsonb"
	}
	if has("proctoring") && b.Proctoring != nil {
		if en, ok := b.Proctoring["enabled"].(bool); ok {
			add("proctoring_enabled", en)
		}
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if len(sets) > 0 {
			if err := tx.Exec("UPDATE exams SET "+strings.Join(sets, ", ")+" WHERE id = ?", append(args, e.ID)...).Error; err != nil {
				return err
			}
		}
		if err := m.ensureQuiz(tx, e.ID); err != nil {
			return err
		}
		if b.Proctoring != nil {
			if err := tx.Exec(`UPDATE quizzes SET settings = jsonb_set(coalesce(settings, '{}'::jsonb), '{proctoring}', ?::jsonb) WHERE exam_id = ?`, mustJSON(b.Proctoring), e.ID).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`UPDATE quizzes z SET title = x.title, time_limit = x.duration, pass_score = x.pass_score FROM exams x WHERE x.id = z.exam_id AND x.id = ?`, e.ID).Error
	})
	if err != nil {
		if strings.Contains(err.Error(), "ck_exams_window") {
			err = httpx.NewValidationError().Add("ends_at", "The ends at must be after the starts at.")
		}
		httpx.Fail(c, err)
		return
	}
	m.respondExamDetail(c, e.ID, http.StatusOK)
}

func (m *Module) deleteExam(c *gin.Context) {
	e, _, ok := m.manageExam(c)
	if !ok {
		return
	}
	var n int64
	_ = m.db.Raw("SELECT count(*) FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = ?", e.ID).Scan(&n)
	if n > 0 {
		httpx.Abort(c, http.StatusConflict, "Kỳ thi đã có lượt làm bài, chỉ có thể lưu trữ.")
		return
	}
	if err := m.db.Exec("DELETE FROM exams WHERE id = ?", e.ID).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

func (m *Module) setExamStatus(c *gin.Context) {
	e, _, ok := m.manageExam(c)
	if !ok {
		return
	}
	var b struct {
		Status string `json:"status"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if !examStatuses[b.Status] {
		httpx.Fail(c, httpx.NewValidationError().Add("status", "The selected status is invalid."))
		return
	}
	if (b.Status == "scheduled" || b.Status == "active") && e.Status != b.Status {
		var cnt int64
		_ = m.db.Raw(`SELECT (SELECT count(*) FROM quiz_questions qq JOIN quizzes z ON z.id = qq.quiz_id WHERE z.exam_id = ?)
			+ coalesce((SELECT sum((r->>'count')::int) FROM quizzes z, jsonb_array_elements(coalesce(z.settings->'random_rules', '[]'::jsonb)) r WHERE z.exam_id = ?), 0)`, e.ID, e.ID).Scan(&cnt)
		if cnt == 0 {
			httpx.Abort(c, http.StatusUnprocessableEntity, "Hãy soạn đề thi (thêm câu hỏi) trước khi lên lịch hoặc mở kỳ thi.")
			return
		}
	}
	if err := m.db.Exec("UPDATE exams SET status = ? WHERE id = ?", b.Status, e.ID).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	m.respondExamDetail(c, e.ID, http.StatusOK)
}

// ---- detail + paper -------------------------------------------------------------

type randomRule struct {
	BankID     int64    `json:"bank_id"`
	GroupID    *int64   `json:"group_id"`
	Difficulty *int     `json:"difficulty"`
	Count      int      `json:"count"`
	Points     *float64 `json:"points"`
	BankName   string   `json:"bank_name,omitempty"`
	GroupName  *string  `json:"group_name,omitempty"`
	Available  *int     `json:"available,omitempty"`
}

func (m *Module) respondExamDetail(c *gin.Context, examID int64, status int) {
	out, err := m.jsonOne("SELECT "+examJSON+" FROM exams e WHERE e.id = ?", examID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if out == nil {
		httpx.NotFound(c, "")
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		httpx.Fail(c, err)
		return
	}
	quiz, err := m.quizView(examID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	q, _ := json.Marshal(quiz)
	obj["quiz"] = q
	c.JSON(status, obj)
}

func (m *Module) quizView(examID int64) (gin.H, error) {
	var z struct {
		ID        int64
		TimeLimit *int
		PassScore int
		Settings  []byte
	}
	if err := m.db.Raw("SELECT id, time_limit, pass_score, settings FROM quizzes WHERE exam_id = ?", examID).Scan(&z).Error; err != nil {
		return nil, err
	}
	qs, err := m.jsonRows(`SELECT jsonb_build_object('question_id', q.id, 'position', qq.position, 'code', q.code, 'type', q.type, 'difficulty', q.difficulty,
		'content', q.content, 'default_score', q.default_score::float8, 'points', qq.points::float8, 'effective_points', CASE WHEN q.type = 'passage' THEN (SELECT coalesce(sum(c.default_score), 0) FROM questions c WHERE c.parent_question_id = q.id)::float8 ELSE coalesce(qq.points, q.default_score)::float8 END)
		FROM quiz_questions qq JOIN questions q ON q.id = qq.question_id WHERE qq.quiz_id = ? ORDER BY qq.position, q.id`, z.ID)
	if err != nil {
		return nil, err
	}
	var settings struct {
		RandomRules []randomRule `json:"random_rules"`
	}
	if len(z.Settings) > 0 {
		_ = json.Unmarshal(z.Settings, &settings)
	}
	warnings := []string{}
	random, totalPoints := 0, 0.0
	for _, raw := range qs {
		var q struct {
			Effective float64 `json:"effective_points"`
		}
		_ = json.Unmarshal(raw, &q)
		totalPoints += q.Effective
	}
	for i := range settings.RandomRules {
		r := &settings.RandomRules[i]
		var name string
		_ = m.db.Raw("SELECT name FROM question_banks WHERE id = ?", r.BankID).Scan(&name)
		r.BankName = name
		if r.GroupID != nil {
			var gn string
			_ = m.db.Raw("SELECT name FROM question_groups WHERE id = ?", *r.GroupID).Scan(&gn)
			r.GroupName = &gn
		}
		cond, args := "question_bank_id = ? AND parent_question_id IS NULL", []any{r.BankID}
		if r.GroupID != nil {
			cond, args = cond+" AND question_group_id = ?", append(args, *r.GroupID)
		}
		if r.Difficulty != nil {
			cond, args = cond+" AND difficulty = ?", append(args, *r.Difficulty)
		}
		var avail int64
		_ = m.db.Raw("SELECT count(*) FROM questions WHERE "+cond, args...).Scan(&avail)
		a := int(avail)
		r.Available = &a
		if a < r.Count {
			warnings = append(warnings, "Ngân hàng \""+name+"\" chỉ có "+strconv.Itoa(a)+" câu phù hợp, nhưng quy tắc cần "+strconv.Itoa(r.Count)+".")
		}
		random += r.Count
		p := 1.0
		if r.Points != nil {
			p = *r.Points
		}
		totalPoints += p * float64(r.Count)
	}
	if settings.RandomRules == nil {
		settings.RandomRules = []randomRule{}
	}
	return gin.H{
		"id": z.ID, "time_limit": z.TimeLimit, "pass_score": z.PassScore, "questions": qs, "random_rules": settings.RandomRules,
		"summary": gin.H{"fixed_count": len(qs), "random_count": random, "total_questions": len(qs) + random, "total_points": totalPoints, "warnings": warnings},
	}, nil
}

func (m *Module) examDetail(c *gin.Context) {
	e, _, ok := m.manageExam(c)
	if !ok {
		return
	}
	if err := m.ensureQuiz(m.db, e.ID); err != nil {
		httpx.Fail(c, err)
		return
	}
	m.respondExamDetail(c, e.ID, http.StatusOK)
}

// canUseBank reports whether the actor may draw questions from the course owning the question/bank.
func (m *Module) canUseCourse(uid int64, roles []string, courseID int64) bool {
	if hasRole(roles, auth.RoleAdmin) {
		return true
	}
	var inst int64
	_ = m.db.Raw("SELECT instructor_id FROM courses WHERE id = ?", courseID).Scan(&inst)
	return inst == uid
}

func (m *Module) saveExamPaper(c *gin.Context) {
	e, uid, ok := m.manageExam(c)
	if !ok {
		return
	}
	var b struct {
		Questions []struct {
			QuestionID int64    `json:"question_id"`
			Points     *float64 `json:"points"`
		} `json:"questions"`
		RandomRules []randomRule `json:"random_rules"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if e.Status == "archived" {
		httpx.Abort(c, http.StatusConflict, "Kỳ thi đã lưu trữ, không thể sửa đề.")
		return
	}
	var attempts int64
	_ = m.db.Raw("SELECT count(*) FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = ?", e.ID).Scan(&attempts)
	if attempts > 0 {
		httpx.Abort(c, http.StatusConflict, "Kỳ thi đã có lượt làm bài, không thể thay đổi đề.")
		return
	}
	_, roles, _ := m.actor(c)
	v := httpx.NewValidationError()
	seen := map[int64]bool{}
	for i, q := range b.Questions {
		if seen[q.QuestionID] {
			v.Add("questions."+strconv.Itoa(i), "Câu hỏi bị trùng.")
			continue
		}
		seen[q.QuestionID] = true
		var cid, child int64
		_ = m.db.Raw("SELECT course_id FROM questions WHERE id = ?", q.QuestionID).Scan(&cid)
		_ = m.db.Raw("SELECT count(*) FROM questions WHERE id = ? AND parent_question_id IS NOT NULL", q.QuestionID).Scan(&child)
		if cid == 0 || !m.canUseCourse(uid, roles, cid) {
			v.Add("questions."+strconv.Itoa(i), "The selected question is invalid.")
		} else if child > 0 {
			v.Add("questions."+strconv.Itoa(i), "Câu hỏi con chỉ được chọn cùng đoạn văn của nó.")
		}
		if q.Points != nil && *q.Points < 0 {
			v.Add("questions."+strconv.Itoa(i), "The points must be at least 0.")
		}
	}
	for i, r := range b.RandomRules {
		var cid int64
		_ = m.db.Raw("SELECT course_id FROM question_banks WHERE id = ?", r.BankID).Scan(&cid)
		if cid == 0 || !m.canUseCourse(uid, roles, cid) {
			v.Add("random_rules."+strconv.Itoa(i), "The selected bank is invalid.")
		}
		if r.Count < 1 || r.Count > 500 {
			v.Add("random_rules."+strconv.Itoa(i), "The count must be between 1 and 500.")
		}
	}
	if v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	rules := b.RandomRules
	if rules == nil {
		rules = []randomRule{}
	}
	for i := range rules {
		rules[i].BankName, rules[i].GroupName, rules[i].Available = "", nil, nil
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := m.ensureQuiz(tx, e.ID); err != nil {
			return err
		}
		var qid int64
		if err := tx.Raw("SELECT id FROM quizzes WHERE exam_id = ?", e.ID).Scan(&qid).Error; err != nil {
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
		return tx.Exec(`UPDATE quizzes SET settings = jsonb_set(coalesce(settings, '{}'::jsonb), '{random_rules}', ?::jsonb) WHERE id = ?`, mustJSON(rules), qid).Error
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	m.respondExamDetail(c, e.ID, http.StatusOK)
}

// ---- candidates ------------------------------------------------------------------

func (m *Module) examCandidates(c *gin.Context) {
	e, _, ok := m.manageExam(c)
	if !ok {
		return
	}
	page, per := pageParams(c, 15, 100)
	var from string
	var args []any
	if e.Type == "course_final" {
		from = `enrollments x JOIN users u ON u.id = x.user_id WHERE x.course_id = ?`
		args = []any{*e.CourseID}
	} else {
		from = `exam_enrollments x JOIN users u ON u.id = x.user_id WHERE x.exam_id = ?`
		args = []any{e.ID}
	}
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		from += " AND (u.name ILIKE ? OR u.email::text ILIKE ? OR u.student_code::text ILIKE ?)"
		args = append(args, likeArg(s), likeArg(s), likeArg(s))
	}
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM "+from, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	src := "'exam'"
	if e.Type == "course_final" {
		src = "'course'"
	}
	rows, err := m.jsonRows(`SELECT jsonb_build_object('user_id', u.id, 'name', u.name, 'email', u.email, 'student_code', u.student_code, 'source', `+src+`,
		'enrolled_at', x.enrolled_at, 'attempts_used', (SELECT count(*) FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = `+strconv.FormatInt(e.ID, 10)+` AND a.user_id = u.id))
		FROM `+from+` ORDER BY u.name LIMIT ? OFFSET ?`, append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, total, page, per)
}

func (m *Module) enrollExam(c *gin.Context) {
	e, uid, ok := m.manageExam(c)
	if !ok {
		return
	}
	if e.Type != "standalone" {
		httpx.Abort(c, http.StatusUnprocessableEntity, "Kỳ thi cuối khoá dùng danh sách học viên của khoá học.")
		return
	}
	var b struct {
		Emails       []string `json:"emails"`
		StudentCodes []string `json:"student_codes"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	ids := append(append([]string{}, b.Emails...), b.StudentCodes...)
	if len(ids) == 0 || len(ids) > 1000 {
		httpx.Fail(c, httpx.NewValidationError().Add("emails", "Nhập từ 1 đến 1000 email hoặc mã sinh viên."))
		return
	}
	enrolled, already, notFound := 0, 0, []string{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		var target int64
		_ = m.db.Raw("SELECT id FROM users WHERE deleted_at IS NULL AND (email = ? OR student_code = ?) LIMIT 1", id, id).Scan(&target)
		if target == 0 {
			notFound = append(notFound, id)
			continue
		}
		res := m.db.Exec("INSERT INTO exam_enrollments (exam_id, user_id, enrolled_by) VALUES (?, ?, ?) ON CONFLICT (exam_id, user_id) DO NOTHING", e.ID, target, uid)
		if res.Error != nil {
			httpx.Fail(c, res.Error)
			return
		}
		if res.RowsAffected == 0 {
			already++
		} else {
			enrolled++
			m.notify(target, "enrollment", "Bạn được thêm vào kỳ thi", "Bạn đã được ghi danh vào một kỳ thi mới.", "")
		}
	}
	c.JSON(http.StatusOK, gin.H{"enrolled": enrolled, "already_enrolled": already, "not_found": notFound})
}

func (m *Module) unenrollExam(c *gin.Context) {
	e, _, ok := m.manageExam(c)
	if !ok {
		return
	}
	uid, ok := idParam(c, "uid")
	if !ok {
		return
	}
	if e.Type != "standalone" {
		httpx.Abort(c, http.StatusUnprocessableEntity, "Kỳ thi cuối khoá dùng danh sách học viên của khoá học.")
		return
	}
	res := m.db.Exec("DELETE FROM exam_enrollments WHERE exam_id = ? AND user_id = ?", e.ID, uid)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

// ---- results ---------------------------------------------------------------------

func (m *Module) examResults(c *gin.Context) {
	e, _, ok := m.manageExam(c)
	if !ok {
		return
	}
	page, per := pageParams(c, 15, 100)
	var users, args = "", []any{}
	if e.Type == "course_final" {
		users, args = "SELECT user_id FROM enrollments WHERE course_id = ?", []any{*e.CourseID}
	} else {
		users, args = "SELECT user_id FROM exam_enrollments WHERE exam_id = ?", []any{e.ID}
	}
	cond := ""
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		cond = " AND (u.name ILIKE ? OR u.email::text ILIKE ? OR u.student_code::text ILIKE ?)"
		args = append(args, likeArg(s), likeArg(s), likeArg(s))
	}
	base := `WITH cand AS (SELECT u.id, u.name, u.email, u.student_code FROM users u WHERE u.id IN (` + users + `)` + cond + `),
	  stat AS (SELECT cand.*,
	    (SELECT count(*) FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = ? AND a.user_id = cand.id) AS attempts,
	    (SELECT max(a.score) FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = ? AND a.user_id = cand.id AND a.status IN ('submitted','force_stopped')) AS best,
	    (SELECT a.score FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = ? AND a.user_id = cand.id AND a.status IN ('submitted','force_stopped') ORDER BY a.completed_at DESC LIMIT 1) AS last,
	    (SELECT a.status FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = ? AND a.user_id = cand.id ORDER BY a.started_at DESC LIMIT 1) AS status,
	    (SELECT bool_or(a.passed) FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = ? AND a.user_id = cand.id) AS passed,
	    (SELECT extract(epoch FROM a.completed_at - a.started_at)::int FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = ? AND a.user_id = cand.id AND a.completed_at IS NOT NULL ORDER BY a.completed_at DESC LIMIT 1) AS secs,
	    (SELECT count(*) FROM exam_violations v JOIN quiz_attempts a ON a.id = v.attempt_id JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = ? AND v.user_id = cand.id) AS violations,
	    (SELECT max(a.completed_at) FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id WHERE z.exam_id = ? AND a.user_id = cand.id) AS submitted
	  FROM cand) `
	eargs := append(append([]any{}, args...), e.ID, e.ID, e.ID, e.ID, e.ID, e.ID, e.ID, e.ID)
	var sum struct {
		Candidates, Attempted, Passed int64
		Avg, Hi, Lo                   *float64
	}
	if err := m.db.Raw(base+`SELECT count(*) AS candidates, count(*) FILTER (WHERE best IS NOT NULL) AS attempted, count(*) FILTER (WHERE passed) AS passed,
		avg(best)::float8 AS avg, max(best)::float8 AS hi, min(best)::float8 AS lo FROM stat`, eargs...).Scan(&sum).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	dist, err := m.jsonRows(base+`SELECT jsonb_build_object('from', b.n * 10, 'to', b.n * 10 + 10,
		'count', (SELECT count(*) FROM stat WHERE best IS NOT NULL AND (CASE WHEN best >= 100 THEN 9 ELSE floor(best / 10)::int END) = b.n))
		FROM generate_series(0, 9) AS b(n) ORDER BY b.n`, eargs...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows(base+`SELECT jsonb_build_object('user', jsonb_build_object('id', id, 'name', name, 'email', email, 'student_code', student_code),
		'attempts_used', attempts, 'best_score', best::float8, 'last_score', last::float8, 'status', coalesce(status, 'not_started'),
		'passed', coalesce(passed, false), 'duration_seconds', secs, 'violations_count', violations, 'last_submitted_at', submitted)
		FROM stat ORDER BY name LIMIT ? OFFSET ?`, append(eargs, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var passRate any
	if sum.Attempted > 0 {
		passRate = float64(sum.Passed) * 100 / float64(sum.Attempted)
	}
	c.JSON(http.StatusOK, gin.H{
		"data": rows,
		"meta": gin.H{"page": page, "per_page": per, "total": sum.Candidates, "last_page": lastPage(sum.Candidates, per)},
		"summary": gin.H{
			"candidates": sum.Candidates, "attempted": sum.Attempted, "not_attempted": sum.Candidates - sum.Attempted,
			"average_score": sum.Avg, "highest_score": sum.Hi, "lowest_score": sum.Lo, "pass_count": sum.Passed, "pass_rate": passRate,
			"distribution": dist,
		},
	})
}
