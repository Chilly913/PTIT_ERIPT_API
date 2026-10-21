package learning

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Homework: instructors configure one assignment per lesson, learners submit text and/or one file,
// instructors grade. Submissions are stored outside the public uploads folder and downloaded through
// an authenticated endpoint.

// storageRoot is the base folder for uploaded files (UPLOAD_DIR, default ./uploads), shared with the content module.
func storageRoot() string {
	if d := strings.TrimSpace(os.Getenv("UPLOAD_DIR")); d != "" {
		return d
	}
	return "uploads"
}

const assignmentJSON = `jsonb_build_object('id', a.id, 'lesson_id', a.lesson_id, 'instructions', a.instructions,
  'max_file_size', a.max_file_size_kb, 'allowed_extensions', array_to_string(a.allowed_extensions, ','),
  'available_from', a.available_from, 'submission_open_at', a.submission_open_at, 'due_at', a.due_at)`

var extRe = regexp.MustCompile(`^[a-z0-9]{1,10}$`)

// courseLesson resolves :ref and :lid and makes sure the lesson belongs to the course.
func (m *Module) courseLesson(c *gin.Context) (cr courseRef, lid int64, ok bool) {
	cr, ok = m.course(c)
	if !ok {
		return
	}
	lid, ok = idParam(c, "lid")
	if !ok {
		return
	}
	var n int64
	if err := m.db.Table("lessons").Where("id = ? AND course_id = ?", lid, cr.ID).Count(&n).Error; err != nil {
		httpx.Fail(c, err)
		return cr, 0, false
	}
	if n == 0 {
		httpx.NotFound(c, "")
		return cr, 0, false
	}
	return cr, lid, true
}

func (m *Module) saveAssignment(c *gin.Context) {
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
	var b struct {
		Instructions      string  `json:"instructions"`
		MaxFileSize       int     `json:"max_file_size"`
		AllowedExtensions *string `json:"allowed_extensions"`
		AvailableFrom     *string `json:"available_from"`
		SubmissionOpenAt  *string `json:"submission_open_at"`
		DueAt             *string `json:"due_at"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	v := httpx.NewValidationError()
	if strings.TrimSpace(b.Instructions) == "" {
		v.Add("instructions", "The instructions field is required.")
	}
	if b.MaxFileSize <= 0 {
		b.MaxFileSize = 10240
	}
	if b.MaxFileSize > 204800 {
		v.Add("max_file_size", "The max file size may not be greater than 200 MB.")
	}
	exts := []string{}
	if b.AllowedExtensions != nil {
		for _, e := range strings.FieldsFunc(*b.AllowedExtensions, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
			if !extRe.MatchString(e) {
				v.Add("allowed_extensions", "The allowed extensions are invalid.")
				break
			}
			exts = append(exts, e)
		}
	}
	if len(exts) == 0 {
		exts = []string{"pdf", "doc", "docx", "zip"}
	}
	for field, val := range map[string]*string{"available_from": b.AvailableFrom, "submission_open_at": b.SubmissionOpenAt, "due_at": b.DueAt} {
		if !whenValid(val) {
			v.Add(field, "The date is not valid.")
		}
	}
	af, so, du := parseWhen(b.AvailableFrom), parseWhen(b.SubmissionOpenAt), parseWhen(b.DueAt)
	if t1, ok1 := af.(time.Time); ok1 {
		if t2, ok2 := so.(time.Time); ok2 && t2.Before(t1) {
			v.Add("submission_open_at", "The submission open date must be after the available date.")
		}
	}
	if t2, ok2 := so.(time.Time); ok2 {
		if t3, ok3 := du.(time.Time); ok3 && t3.Before(t2) {
			v.Add("due_at", "The due date must be after the submission open date.")
		}
	}
	if v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO lesson_assignments (lesson_id, instructions, max_file_size_kb, allowed_extensions, available_from, submission_open_at, due_at)
			VALUES (?, ?, ?, ?::text[], ?, ?, ?)
			ON CONFLICT (lesson_id) DO UPDATE SET instructions = EXCLUDED.instructions, max_file_size_kb = EXCLUDED.max_file_size_kb,
				allowed_extensions = EXCLUDED.allowed_extensions, available_from = EXCLUDED.available_from,
				submission_open_at = EXCLUDED.submission_open_at, due_at = EXCLUDED.due_at`,
			lid, strings.TrimSpace(b.Instructions), b.MaxFileSize, "{"+strings.Join(exts, ",")+"}", af, so, du).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE lessons SET type = 'assignment' WHERE id = ?", lid).Error
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+assignmentJSON+" FROM lesson_assignments a WHERE a.lesson_id = ?", lid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "assignment", out)
}

const submissionJSON = `jsonb_build_object('id', s.id, 'lesson_assignment_id', s.lesson_assignment_id, 'user_id', s.user_id,
  'file_url', CASE WHEN s.file_path = '' THEN NULL ELSE '/bff/courses/' || l.course_id || '/assignments/submissions/' || s.id || '/file' END,
  'student_note', s.student_note, 'grade', s.grade::text, 'feedback', s.feedback, 'submitted_at', s.submitted_at)`

func (m *Module) submitAssignment(c *gin.Context) {
	cr, lid, ok := m.courseLesson(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok || !m.gate(c, cr, uid, lid) {
		return
	}
	var a struct {
		ID               int64
		MaxKB            int
		Exts             string
		AvailableFrom    *time.Time
		SubmissionOpenAt *time.Time
		DueAt            *time.Time
	}
	res := m.db.Raw(`SELECT id, max_file_size_kb AS max_kb, array_to_string(allowed_extensions, ',') AS exts, available_from, submission_open_at, due_at
		FROM lesson_assignments WHERE lesson_id = ?`, lid).Scan(&a)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	now := time.Now()
	switch {
	case a.AvailableFrom != nil && now.Before(*a.AvailableFrom):
		httpx.Abort(c, http.StatusForbidden, "Bài tập sẽ mở lúc "+fmtWhen(*a.AvailableFrom))
		return
	case a.SubmissionOpenAt != nil && now.Before(*a.SubmissionOpenAt):
		httpx.Abort(c, http.StatusForbidden, "Chưa đến thời gian nộp bài (mở lúc "+fmtWhen(*a.SubmissionOpenAt)+")")
		return
	case a.DueAt != nil && now.After(*a.DueAt):
		httpx.Abort(c, http.StatusForbidden, "Đã quá hạn nộp bài ("+fmtWhen(*a.DueAt)+")")
		return
	}
	note := strings.TrimSpace(c.PostForm("student_note"))
	if len(note) > 10000 {
		httpx.Fail(c, httpx.NewValidationError().Add("student_note", "The student note is too long."))
		return
	}
	filePath, savedAbs := "", ""
	if fh, err := c.FormFile("file"); err == nil {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(fh.Filename), "."))
		allowed := false
		for _, e := range strings.Split(a.Exts, ",") {
			allowed = allowed || e == ext
		}
		switch {
		case !allowed:
			httpx.Fail(c, httpx.NewValidationError().Add("file", "Chỉ nhận tệp: "+strings.ReplaceAll(a.Exts, ",", ", ")))
			return
		case fh.Size > int64(a.MaxKB)*1024:
			httpx.Fail(c, httpx.NewValidationError().Add("file", fmt.Sprintf("Tệp quá lớn (tối đa %d KB).", a.MaxKB)))
			return
		}
		dir := filepath.Join(storageRoot(), "private", "assignments", strconv.FormatInt(a.ID, 10))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			httpx.Fail(c, err)
			return
		}
		name := fmt.Sprintf("%d-%d.%s", uid, now.UnixNano(), ext)
		src, err := fh.Open()
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		defer src.Close()
		dst, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		_, cerr := io.Copy(dst, src)
		dst.Close()
		if cerr != nil {
			httpx.Fail(c, cerr)
			return
		}
		filePath = filepath.ToSlash(filepath.Join("private", "assignments", strconv.FormatInt(a.ID, 10), name))
		savedAbs = filepath.Join(dir, name)
		// remember the original name in the note-less case
		if note == "" {
			note = "Tệp nộp: " + fh.Filename
		}
	}
	if filePath == "" && note == "" {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "Hãy nhập nội dung hoặc đính kèm tệp."))
		return
	}
	var oldPath string
	_ = m.db.Raw("SELECT file_path FROM assignment_submissions WHERE lesson_assignment_id = ? AND user_id = ?", a.ID, uid).Scan(&oldPath)
	var sid int64
	err := m.db.Raw(`INSERT INTO assignment_submissions (lesson_assignment_id, user_id, file_path, student_note, submitted_at)
		VALUES (?, ?, ?, NULLIF(?, ''), now())
		ON CONFLICT (lesson_assignment_id, user_id) DO UPDATE SET file_path = EXCLUDED.file_path, student_note = EXCLUDED.student_note,
			submitted_at = now(), grade = NULL, feedback = NULL
		RETURNING id`, a.ID, uid, filePath, note).Scan(&sid).Error
	if err != nil {
		if savedAbs != "" {
			_ = os.Remove(savedAbs)
		}
		httpx.Fail(c, err)
		return
	}
	if oldPath != "" && oldPath != filePath {
		_ = os.Remove(filepath.Join(storageRoot(), filepath.FromSlash(oldPath)))
	}
	// Handing in the homework completes the lesson.
	_ = m.db.Exec(`INSERT INTO lesson_progress (user_id, lesson_id, progress_percent, completed_at, last_watched_at)
		VALUES (?, ?, 100, now(), now())
		ON CONFLICT (user_id, lesson_id) DO UPDATE SET progress_percent = 100, completed_at = coalesce(lesson_progress.completed_at, now()), last_watched_at = now()`, uid, lid)
	out, err := m.jsonOne("SELECT "+submissionJSON+" FROM assignment_submissions s JOIN lesson_assignments a ON a.id = s.lesson_assignment_id JOIN lessons l ON l.id = a.lesson_id WHERE s.id = ?", sid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "submission", out)
}

// mySubmission returns the caller's own submission (or null).
func (m *Module) mySubmission(c *gin.Context) {
	cr, lid, ok := m.courseLesson(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok {
		return
	}
	out, err := m.jsonOne("SELECT "+submissionJSON+` FROM assignment_submissions s JOIN lesson_assignments a ON a.id = s.lesson_assignment_id
		JOIN lessons l ON l.id = a.lesson_id WHERE a.lesson_id = ? AND s.user_id = ?`, lid, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if out == nil {
		c.JSON(http.StatusOK, gin.H{"submission": nil})
		return
	}
	raw(c, http.StatusOK, "submission", out)
}

// submissions is the instructor's inbox: every assignment of the course with its submissions.
func (m *Module) submissions(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	var title string
	_ = m.db.Raw("SELECT title FROM courses WHERE id = ?", cr.ID).Scan(&title)
	rows, err := m.jsonRows(`SELECT jsonb_build_object('lesson_id', l.id, 'lesson_title', l.title, 'assignment_id', a.id, 'due_at', a.due_at,
		'submissions_count', (SELECT count(*) FROM assignment_submissions s WHERE s.lesson_assignment_id = a.id),
		'ungraded_count', (SELECT count(*) FROM assignment_submissions s WHERE s.lesson_assignment_id = a.id AND s.grade IS NULL),
		'submissions', coalesce((SELECT jsonb_agg(`+submissionJSON+` || jsonb_build_object('user', jsonb_build_object('id', u.id, 'name', u.name, 'email', u.email))
			ORDER BY s.submitted_at DESC) FROM assignment_submissions s JOIN users u ON u.id = s.user_id WHERE s.lesson_assignment_id = a.id), '[]'::jsonb))
		FROM lesson_assignments a JOIN lessons l ON l.id = a.lesson_id WHERE l.course_id = ? ORDER BY l.position, l.id`, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var sums struct{ Submissions, Ungraded int64 }
	_ = m.db.Raw(`SELECT count(*) AS submissions, count(*) FILTER (WHERE s.grade IS NULL) AS ungraded FROM assignment_submissions s
		JOIN lesson_assignments a ON a.id = s.lesson_assignment_id JOIN lessons l ON l.id = a.lesson_id WHERE l.course_id = ?`, cr.ID).Scan(&sums)
	c.JSON(http.StatusOK, gin.H{
		"course":      gin.H{"id": cr.ID, "title": title},
		"assignments": rows,
		"summary":     gin.H{"assignments": len(rows), "submissions": sums.Submissions, "ungraded": sums.Ungraded},
	})
}

func (m *Module) gradeSubmission(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	sid, ok := idParam(c, "sid")
	if !ok {
		return
	}
	var b struct {
		Grade    *float64 `json:"grade"`
		Feedback *string  `json:"feedback"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if b.Grade == nil || *b.Grade < 0 || *b.Grade > 10 {
		httpx.Fail(c, httpx.NewValidationError().Add("grade", "The grade must be between 0 and 10."))
		return
	}
	var info struct {
		UserID int64
		Title  string
	}
	res := m.db.Raw(`SELECT s.user_id, l.title FROM assignment_submissions s JOIN lesson_assignments a ON a.id = s.lesson_assignment_id
		JOIN lessons l ON l.id = a.lesson_id WHERE s.id = ? AND l.course_id = ?`, sid, cr.ID).Scan(&info)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	if err := m.db.Exec("UPDATE assignment_submissions SET grade = ?, feedback = ? WHERE id = ?", *b.Grade, nullStr(b.Feedback), sid).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	m.notify(info.UserID, "announcement", "Bài tập đã được chấm", fmt.Sprintf("Bài \"%s\" của bạn được %.1f/10 điểm.", info.Title, *b.Grade), fmt.Sprintf("/learn/%d", cr.ID))
	out, err := m.jsonOne("SELECT "+submissionJSON+" FROM assignment_submissions s JOIN lesson_assignments a ON a.id = s.lesson_assignment_id JOIN lessons l ON l.id = a.lesson_id WHERE s.id = ?", sid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "submission", out)
}

// downloadSubmission streams a submitted file to the instructor or to the learner who submitted it.
func (m *Module) downloadSubmission(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	sid, ok := idParam(c, "sid")
	if !ok {
		return
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var s struct {
		UserID   int64
		FilePath string
	}
	res := m.db.Raw(`SELECT s.user_id, s.file_path FROM assignment_submissions s JOIN lesson_assignments a ON a.id = s.lesson_assignment_id
		JOIN lessons l ON l.id = a.lesson_id WHERE s.id = ? AND l.course_id = ?`, sid, cr.ID).Scan(&s)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 || s.FilePath == "" {
		httpx.NotFound(c, "")
		return
	}
	if s.UserID != uid && cr.InstructorID != uid && !hasRole(roles, auth.RoleAdmin) {
		httpx.Forbidden(c, "")
		return
	}
	root, _ := filepath.Abs(storageRoot())
	abs := filepath.Join(root, filepath.FromSlash(s.FilePath))
	if !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		httpx.NotFound(c, "")
		return
	}
	c.FileAttachment(abs, filepath.Base(abs))
}
