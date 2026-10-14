package study

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Defaults of AssignmentController@store.
const (
	defaultMaxFileSizeKB     = 10240
	defaultAllowedExtensions = "pdf,doc,docx,zip"
)

// findAssignment loads the lesson's assignment (nil when none).
func (m *Module) findAssignment(c *gin.Context, lessonID int64) (*assignmentRow, error) {
	var rows []assignmentRow
	err := m.db.WithContext(c.Request.Context()).Raw(`SELECT `+assignmentCols+` FROM lesson_assignments
		WHERE lesson_id = ?`, lessonID).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("study: find assignment: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// showAssignment is GET .../assignment. Legacy checks only that the lesson
// belongs to the course (no enrollment check).
func (m *Module) showAssignment(c *gin.Context) {
	course, lesson, ok := m.bindCourseLesson(c)
	if !ok || !requireLessonInCourse(c, course, lesson) {
		return
	}
	a, err := m.findAssignment(c, lesson.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if a == nil {
		httpx.Abort(c, http.StatusNotFound, "No assignment found")
		return
	}
	httpx.OK(c, a.JSON())
}

// saveAssignment is POST .../assignment (create or update, instructor).
func (m *Module) saveAssignment(c *gin.Context) {
	course, lesson, ok := m.bindCourseLesson(c)
	if !ok {
		return
	}
	cl, ok := m.caller(c)
	if !ok || !requireOwner(c, cl, course) || !requireLessonInCourse(c, course, lesson) {
		return
	}
	in, ok := readInput(c)
	if !ok {
		return
	}
	r := newRules(in)
	instructions := r.stringField("instructions", true, false, 0)
	maxSize := r.intField("max_file_size", false, i64(1), i64(maxInt32))
	exts := r.stringField("allowed_extensions", false, false, 255)
	availableFrom, _ := r.dateField("available_from")
	openAt, openOK := r.dateField("submission_open_at")
	if openOK && openAt != nil && availableFrom != nil && openAt.Before(*availableFrom) {
		r.afterOrEqual("submission_open_at", "available_from")
	}
	dueAt, dueOK := r.dateField("due_at")
	if dueOK && dueAt != nil && openAt != nil && dueAt.Before(*openAt) {
		r.afterOrEqual("due_at", "submission_open_at")
	}
	if !r.ok(c) {
		return
	}

	size := int64(defaultMaxFileSizeKB)
	if maxSize != nil {
		size = *maxSize
	}
	extList := parseExtensions(defaultAllowedExtensions)
	if exts != nil {
		if l := parseExtensions(*exts); len(l) > 0 {
			extList = l
		}
	}

	var saved assignmentRow
	err := m.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var rows []assignmentRow
		err := tx.Raw(`INSERT INTO lesson_assignments
				(lesson_id, instructions, max_file_size_kb, allowed_extensions, available_from, submission_open_at, due_at)
			VALUES (?, ?, ?, ?::text[], ?, ?, ?)
			ON CONFLICT (lesson_id) DO UPDATE SET
				instructions       = EXCLUDED.instructions,
				max_file_size_kb   = EXCLUDED.max_file_size_kb,
				allowed_extensions = EXCLUDED.allowed_extensions,
				available_from     = EXCLUDED.available_from,
				submission_open_at = EXCLUDED.submission_open_at,
				due_at             = EXCLUDED.due_at
			RETURNING `+assignmentCols,
			lesson.ID, *instructions, size, pgTextArray(extList), availableFrom, openAt, dueAt).Scan(&rows).Error
		if err != nil {
			return fmt.Errorf("study: save assignment: %w", err)
		}
		if len(rows) == 0 {
			return errors.New("study: save assignment returned no row")
		}
		saved = rows[0]
		if lesson.Type != "assignment" {
			if err := tx.Exec(`UPDATE lessons SET type = 'assignment' WHERE id = ?`, lesson.ID).Error; err != nil {
				return fmt.Errorf("study: set lesson type: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"message": "Assignment saved successfully", "assignment": saved.JSON()})
}

// pgTextArray renders a text[] literal ({"a","b"}).
func pgTextArray(items []string) string {
	b := []byte{'{'}
	for i, s := range items {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendQuote(b, s) // extensions are plain ASCII; Go quoting matches PG for them
	}
	return string(append(b, '}'))
}

// submitAssignment is POST .../assignment/submit (multipart `file`, `student_note`).
// Legacy checks neither the lesson's course nor the enrollment, nor the
// assignment's own size/extension/date settings.
func (m *Module) submitAssignment(c *gin.Context) {
	course, lesson, ok := m.bindCourseLesson(c)
	if !ok {
		return
	}
	cl, ok := m.caller(c)
	if !ok {
		return
	}
	a, err := m.findAssignment(c, lesson.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if a == nil {
		httpx.Abort(c, http.StatusNotFound, "No assignment for this lesson")
		return
	}
	in, ok := readInput(c)
	if !ok {
		return
	}
	r := newRules(in)
	fh := r.fileField("file", maxUploadKB)
	note := r.stringField("student_note", false, false, 0)
	if !r.ok(c) {
		return
	}
	up, err := m.upload(c, fh, fmt.Sprintf("courses/%d/lessons/%d/assignments/submissions/%d", course.ID, lesson.ID, cl.UserID))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var rows []submissionRow
	err = m.db.WithContext(c.Request.Context()).Raw(`INSERT INTO assignment_submissions AS s
			(lesson_assignment_id, user_id, file_path, student_note, submitted_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (lesson_assignment_id, user_id) DO UPDATE SET
			file_path    = EXCLUDED.file_path,
			student_note = EXCLUDED.student_note,
			submitted_at = EXCLUDED.submitted_at
		RETURNING `+submissionCols, a.ID, cl.UserID, up.key, note, m.now()).Scan(&rows).Error
	if err == nil && len(rows) == 0 {
		err = errors.New("insert returned no row")
	}
	if err != nil {
		m.discard(c, up.key)
		httpx.Fail(c, fmt.Errorf("study: submit assignment: %w", err))
		return
	}
	httpx.Created(c, gin.H{"message": "Assignment submitted successfully", "submission": m.submissionJSON(rows[0])})
}

const submissionWithUser = submissionCols + `, u.name AS u_name, u.email::text AS u_email`

// lessonSubmissions is GET .../assignment/submissions (instructor).
func (m *Module) lessonSubmissions(c *gin.Context) {
	course, lesson, ok := m.bindCourseLesson(c)
	if !ok {
		return
	}
	cl, ok := m.caller(c)
	if !ok || !requireOwner(c, cl, course) || !requireLessonInCourse(c, course, lesson) {
		return
	}
	a, err := m.findAssignment(c, lesson.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if a == nil {
		httpx.Abort(c, http.StatusNotFound, "Assignment not found")
		return
	}
	var rows []submissionRow
	err = m.db.WithContext(c.Request.Context()).Raw(`SELECT `+submissionWithUser+`
		FROM assignment_submissions s LEFT JOIN users u ON u.id = s.user_id
		WHERE s.lesson_assignment_id = ?
		ORDER BY s.submitted_at DESC, s.id DESC`, a.ID).Scan(&rows).Error
	if err != nil {
		httpx.Fail(c, fmt.Errorf("study: list submissions: %w", err))
		return
	}
	out := make([]submissionUserJSON, 0, len(rows))
	for _, s := range rows {
		out = append(out, m.submissionUserJSON(s))
	}
	httpx.OK(c, out)
}

// inboxAssignment is one entry of the course grading inbox.
type inboxAssignment struct {
	LessonID         int64                `json:"lesson_id"`
	LessonTitle      string               `json:"lesson_title"`
	AssignmentID     int64                `json:"assignment_id"`
	DueAt            httpx.Time           `json:"due_at"`
	SubmissionsCount int                  `json:"submissions_count"`
	UngradedCount    int                  `json:"ungraded_count"`
	Submissions      []submissionUserJSON `json:"submissions"`
}

// courseSubmissions is GET /courses/{course}/assignments/submissions.
func (m *Module) courseSubmissions(c *gin.Context) {
	course, ok := m.bindCourse(c)
	if !ok {
		return
	}
	cl, ok := m.caller(c)
	if !ok || !requireOwner(c, cl, course) {
		return
	}
	db := m.db.WithContext(c.Request.Context())
	var title string
	if err := db.Raw(`SELECT title FROM courses WHERE id = ?`, course.ID).Scan(&title).Error; err != nil {
		httpx.Fail(c, fmt.Errorf("study: course title: %w", err))
		return
	}
	var lessons []struct {
		LessonID     int64      `gorm:"column:lesson_id"`
		LessonTitle  string     `gorm:"column:lesson_title"`
		AssignmentID int64      `gorm:"column:assignment_id"`
		DueAt        *time.Time `gorm:"column:due_at"`
	}
	err := db.Raw(`SELECT l.id AS lesson_id, l.title AS lesson_title, a.id AS assignment_id, a.due_at
		FROM lessons l JOIN lesson_assignments a ON a.lesson_id = l.id
		WHERE l.course_id = ? AND l.type = 'assignment'
		ORDER BY l.position, l.id`, course.ID).Scan(&lessons).Error
	if err != nil {
		httpx.Fail(c, fmt.Errorf("study: course assignments: %w", err))
		return
	}
	ids := make([]int64, 0, len(lessons))
	for _, l := range lessons {
		ids = append(ids, l.AssignmentID)
	}
	byAssignment := map[int64][]submissionRow{}
	if len(ids) > 0 {
		var subs []submissionRow
		err = db.Raw(`SELECT `+submissionWithUser+`
			FROM assignment_submissions s LEFT JOIN users u ON u.id = s.user_id
			WHERE s.lesson_assignment_id IN ?
			ORDER BY s.submitted_at DESC, s.id DESC`, ids).Scan(&subs).Error
		if err != nil {
			httpx.Fail(c, fmt.Errorf("study: course submissions: %w", err))
			return
		}
		for _, s := range subs {
			byAssignment[s.LessonAssignmentID] = append(byAssignment[s.LessonAssignmentID], s)
		}
	}
	assignments := make([]inboxAssignment, 0, len(lessons))
	totalSubs, totalUngraded := 0, 0
	for _, l := range lessons {
		item := inboxAssignment{
			LessonID: l.LessonID, LessonTitle: l.LessonTitle, AssignmentID: l.AssignmentID,
			DueAt: tptr(l.DueAt), Submissions: []submissionUserJSON{},
		}
		for _, s := range byAssignment[l.AssignmentID] {
			item.Submissions = append(item.Submissions, m.submissionUserJSON(s))
			if s.Grade == nil {
				item.UngradedCount++
			}
		}
		item.SubmissionsCount = len(item.Submissions)
		totalSubs += item.SubmissionsCount
		totalUngraded += item.UngradedCount
		assignments = append(assignments, item)
	}
	httpx.OK(c, gin.H{
		"course":      gin.H{"id": course.ID, "title": title},
		"assignments": assignments,
		"summary": gin.H{
			"assignments": len(assignments),
			"submissions": totalSubs,
			"ungraded":    totalUngraded,
		},
	})
}

// gradeSubmission is PUT /courses/{course}/assignments/submissions/{submission}.
func (m *Module) gradeSubmission(c *gin.Context) {
	course, ok := m.bindCourse(c)
	if !ok {
		return
	}
	db := m.db.WithContext(c.Request.Context())
	key := c.Param("submission")
	var found []struct {
		ID             int64  `gorm:"column:id"`
		LessonCourseID *int64 `gorm:"column:course_id"`
	}
	if id, ok := parseID(key); ok {
		err := db.Raw(`SELECT s.id, l.course_id
			FROM assignment_submissions s
			LEFT JOIN lesson_assignments a ON a.id = s.lesson_assignment_id
			LEFT JOIN lessons l ON l.id = a.lesson_id
			WHERE s.id = ?`, id).Scan(&found).Error
		if err != nil {
			httpx.Fail(c, fmt.Errorf("study: find submission: %w", err))
			return
		}
	}
	if len(found) == 0 {
		modelNotFound(c, "AssignmentSubmission", key)
		return
	}
	cl, ok := m.caller(c)
	if !ok || !requireOwner(c, cl, course) {
		return
	}
	if found[0].LessonCourseID == nil || *found[0].LessonCourseID != course.ID {
		// abort_unless(..., 404) renders an empty message.
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"message": ""})
		return
	}
	in, ok := readInput(c)
	if !ok {
		return
	}
	r := newRules(in)
	grade := r.numberField("grade", true, 0, 10)
	feedback := r.stringField("feedback", false, false, 2000)
	if !r.ok(c) {
		return
	}
	// feedback ?? $submission->feedback: a null/absent feedback keeps the old one.
	var rows []submissionRow
	err := db.Raw(`UPDATE assignment_submissions s SET grade = ?, feedback = COALESCE(?, s.feedback)
		WHERE s.id = ?
		RETURNING `+submissionCols+`, (SELECT name FROM users WHERE id = s.user_id) AS u_name,
			(SELECT email::text FROM users WHERE id = s.user_id) AS u_email`,
		strconv.FormatFloat(*grade, 'f', -1, 64), feedback, found[0].ID).Scan(&rows).Error
	if err == nil && len(rows) == 0 {
		err = errors.New("update returned no row")
	}
	if err != nil {
		httpx.Fail(c, fmt.Errorf("study: grade submission: %w", err))
		return
	}
	httpx.OK(c, gin.H{"message": "Đã chấm điểm bài tập.", "submission": m.submissionUserJSON(rows[0])})
}
