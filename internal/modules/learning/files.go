package learning

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Files attached to a file_upload answer. They are private: stored under UPLOAD_DIR/private/answers/<attempt>/ and only
// downloadable through an authenticated endpoint by the learner or a grader of that attempt.

// attemptMeta is an attempt together with what is needed to find its questions and to check who may grade it.
type attemptMeta struct {
	ID            int64
	UserID        int64
	QuizID        int64
	Status        string
	GradingStatus string
	Answers       []byte
	Grading       []byte
	IDsRaw        string
	Score         float64
	Passed        bool
	StartedAt     time.Time
	CompletedAt   *time.Time
	GradedAt      *time.Time
	ExamID        *int64
	LessonID      *int64
	CourseID      *int64
	PassScore     int
	Settings      []byte
	Review        []byte // exam review options (what the learner may see afterwards)
	Title         string
	CourseTitle   *string
	InstructorID  *int64
	CreatedBy     *int64
	StudentName   string
	StudentEmail  string
	StudentCode   *string
}

const attemptMetaSQL = `SELECT a.id, a.user_id, a.quiz_id, a.status, a.grading_status, a.answers, a.grading, a.question_ids::text AS ids_raw,
	a.score::float8 AS score, a.passed, a.started_at, a.completed_at, a.graded_at,
	z.exam_id, z.lesson_id, coalesce(z.course_id, e.course_id, l.course_id) AS course_id, z.pass_score, z.settings, e.review_options AS review, coalesce(e.title, z.title) AS title,
	c.title AS course_title, c.instructor_id AS instructor_id, e.created_by AS created_by,
	u.name AS student_name, u.email::text AS student_email, u.student_code::text AS student_code
	FROM quiz_attempts a JOIN quizzes z ON z.id = a.quiz_id
	LEFT JOIN exams e ON e.id = z.exam_id LEFT JOIN lessons l ON l.id = z.lesson_id
	LEFT JOIN courses c ON c.id = coalesce(z.course_id, e.course_id, l.course_id)
	JOIN users u ON u.id = a.user_id`

func (m *Module) loadAttempt(id int64) (*attemptMeta, error) {
	var a attemptMeta
	res := m.db.Raw(attemptMetaSQL+" WHERE a.id = ?", id).Scan(&a)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	return &a, nil
}

// isGrader reports whether uid may grade the attempt: an admin, the instructor of its course, or the creator of its exam.
func (a *attemptMeta) isGrader(uid int64, roles []string) bool {
	return hasRole(roles, auth.RoleAdmin) || (a.InstructorID != nil && *a.InstructorID == uid) || (a.CreatedBy != nil && *a.CreatedBy == uid)
}

// questions loads the questions of the attempt as they were drawn for the learner.
func (m *Module) attemptQuestions(a *attemptMeta) ([]quizQuestion, error) {
	if a.ExamID != nil {
		return m.paperQuestions(examTake{QuizID: a.QuizID, Settings: a.Settings}, parseIDs(a.IDsRaw))
	}
	return m.quizQuestions(a.QuizID)
}

func cleanFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if r := []rune(name); len(r) > 200 {
		name = string(r[len(r)-200:])
	}
	if name == "" || name == "." {
		name = "tep"
	}
	return name
}

func (m *Module) uploadAttemptFile(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	uid := auth.UserID(c)
	a, err := m.loadAttempt(id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if a == nil || a.UserID != uid {
		httpx.NotFound(c, "")
		return
	}
	if a.Status != "in_progress" {
		httpx.Abort(c, http.StatusConflict, "Bài làm này đã được nộp.")
		return
	}
	qid, err := strconv.ParseInt(c.PostForm("question_id"), 10, 64)
	if err != nil {
		httpx.Fail(c, httpx.NewValidationError().Add("question_id", "The question id is invalid."))
		return
	}
	qs, err := m.attemptQuestions(a)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var target *quizQuestion
	for i := range qs {
		if qs[i].ID == qid && qs[i].Type == "file_upload" {
			target = &qs[i]
		}
	}
	if target == nil {
		httpx.Fail(c, httpx.NewValidationError().Add("question_id", "This question does not accept files."))
		return
	}
	rules := uploadRulesOf(target.Metadata)
	fh, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "The file field is required."))
		return
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(fh.Filename), "."))
	allowed := false
	for _, e := range rules.Ext {
		allowed = allowed || e == ext
	}
	switch {
	case !allowed:
		httpx.Fail(c, httpx.NewValidationError().Add("file", "Chỉ nhận tệp: "+strings.Join(rules.Ext, ", ")))
		return
	case fh.Size <= 0:
		httpx.Fail(c, httpx.NewValidationError().Add("file", "Tệp rỗng."))
		return
	case fh.Size > int64(rules.MaxSizeMB)<<20:
		httpx.Fail(c, httpx.NewValidationError().Add("file", fmt.Sprintf("Tệp quá lớn (tối đa %d MB).", rules.MaxSizeMB)))
		return
	}
	var have int64
	if err := m.db.Raw("SELECT count(*) FROM attempt_files WHERE attempt_id = ? AND question_id = ?", id, qid).Scan(&have).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if have >= int64(rules.MaxFiles) {
		httpx.Fail(c, httpx.NewValidationError().Add("file", fmt.Sprintf("Chỉ được nộp tối đa %d tệp cho câu này.", rules.MaxFiles)))
		return
	}
	rel := filepath.ToSlash(filepath.Join("private", "answers", strconv.FormatInt(id, 10), fmt.Sprintf("%d-%d.%s", qid, time.Now().UnixNano(), ext)))
	abs := filepath.Join(storageRoot(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := c.SaveUploadedFile(fh, abs); err != nil {
		httpx.Fail(c, err)
		return
	}
	name := cleanFileName(fh.Filename)
	var fid int64
	if err := m.db.Raw(`INSERT INTO attempt_files (attempt_id, question_id, original_name, stored_path, size_bytes) VALUES (?, ?, ?, ?, ?) RETURNING id`,
		id, qid, name, rel, fh.Size).Scan(&fid).Error; err != nil {
		_ = os.Remove(abs)
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": fid, "name": name, "size": fh.Size})
}

func (m *Module) deleteAttemptFile(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	fid, ok := idParam(c, "fid")
	if !ok {
		return
	}
	var f struct {
		StoredPath string
		Status     string
	}
	res := m.db.Raw(`SELECT f.stored_path, a.status FROM attempt_files f JOIN quiz_attempts a ON a.id = f.attempt_id
		WHERE f.id = ? AND a.id = ? AND a.user_id = ?`, fid, id, auth.UserID(c)).Scan(&f)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	if f.Status != "in_progress" {
		httpx.Abort(c, http.StatusConflict, "Bài làm này đã được nộp.")
		return
	}
	if err := m.db.Exec("DELETE FROM attempt_files WHERE id = ?", fid).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	_ = os.Remove(filepath.Join(storageRoot(), filepath.FromSlash(f.StoredPath)))
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

func (m *Module) downloadAttemptFile(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	fid, ok := idParam(c, "fid")
	if !ok {
		return
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	a, err := m.loadAttempt(id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if a == nil {
		httpx.NotFound(c, "")
		return
	}
	if a.UserID != uid && !a.isGrader(uid, roles) {
		httpx.Forbidden(c, "")
		return
	}
	var f struct {
		StoredPath   string
		OriginalName string
	}
	res := m.db.Raw("SELECT stored_path, original_name FROM attempt_files WHERE id = ? AND attempt_id = ?", fid, id).Scan(&f)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	root, _ := filepath.Abs(storageRoot())
	abs := filepath.Join(root, filepath.FromSlash(f.StoredPath))
	if !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		httpx.NotFound(c, "")
		return
	}
	c.FileAttachment(abs, f.OriginalName)
}
