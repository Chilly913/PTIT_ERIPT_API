package study

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

const progressCols = `user_id, lesson_id, progress_percent, last_position, watched_seconds,
	completed_at, last_watched_at, created_at, updated_at`

// findProgress loads one lesson_progress row; lock=true takes FOR UPDATE.
func findProgress(db *gorm.DB, userID, lessonID int64, lock bool) (*progressRow, error) {
	q := db.Table("lesson_progress").Select(progressCols).Where("user_id = ? AND lesson_id = ?", userID, lessonID)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []progressRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("study: find progress: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// saveProgress upserts all mutable columns and returns the stored row.
func saveProgress(db *gorm.DB, p progressRow) (progressRow, error) {
	var out []progressRow
	err := db.Raw(`INSERT INTO lesson_progress
			(user_id, lesson_id, progress_percent, last_position, watched_seconds, completed_at, last_watched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, lesson_id) DO UPDATE SET
			progress_percent = EXCLUDED.progress_percent,
			last_position    = EXCLUDED.last_position,
			watched_seconds  = EXCLUDED.watched_seconds,
			completed_at     = EXCLUDED.completed_at,
			last_watched_at  = EXCLUDED.last_watched_at
		RETURNING `+progressCols,
		p.UserID, p.LessonID, p.ProgressPercent, p.LastPosition, p.WatchedSeconds, p.CompletedAt, p.LastWatchedAt).
		Scan(&out).Error
	if err != nil {
		return progressRow{}, fmt.Errorf("study: save progress: %w", err)
	}
	if len(out) == 0 {
		return progressRow{}, errors.New("study: save progress returned no row")
	}
	return out[0], nil
}

// showProgress is GET /courses/{course}/lessons/{lesson}/progress
// (LessonProgress::firstOrCreate with zero defaults).
func (m *Module) showProgress(c *gin.Context) {
	cl, _, lesson, ok := m.lessonGate(c)
	if !ok {
		return
	}
	db := m.db.WithContext(c.Request.Context())
	if err := db.Exec(`INSERT INTO lesson_progress (user_id, lesson_id) VALUES (?, ?)
		ON CONFLICT (user_id, lesson_id) DO NOTHING`, cl.UserID, lesson.ID).Error; err != nil {
		httpx.Fail(c, fmt.Errorf("study: create progress: %w", err))
		return
	}
	p, err := findProgress(db, cl.UserID, lesson.ID, false)
	if err == nil && p == nil {
		err = errors.New("study: progress row missing")
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, p.JSON())
}

// upsertInput is the validated PUT .../progress payload (all nullable).
type upsertInput struct {
	ProgressPercent *int64
	LastPosition    *int64
	WatchedSeconds  *int64
	Completed       *bool
}

// applyUpsert is the fill() of LessonProgressController@upsert: missing
// values keep the stored ones (0 for a new row); `completed` defaults to
// progress_percent >= 100; completed_at is kept while completed, else cleared.
func applyUpsert(prev *progressRow, in upsertInput, userID, lessonID int64, now time.Time) progressRow {
	p := progressRow{UserID: userID, LessonID: lessonID}
	if prev != nil {
		p = *prev
	}
	if in.ProgressPercent != nil {
		p.ProgressPercent = int(*in.ProgressPercent)
	}
	if in.LastPosition != nil {
		p.LastPosition = int(*in.LastPosition)
	}
	if in.WatchedSeconds != nil {
		p.WatchedSeconds = int(*in.WatchedSeconds)
	}
	completed := p.ProgressPercent >= 100
	if in.Completed != nil {
		completed = *in.Completed
	}
	p.LastWatchedAt = &now
	switch {
	case !completed:
		p.CompletedAt = nil
	case p.CompletedAt == nil:
		p.CompletedAt = &now
	}
	return p
}

// upsertProgress is PUT /courses/{course}/lessons/{lesson}/progress.
func (m *Module) upsertProgress(c *gin.Context) {
	cl, _, lesson, ok := m.lessonGate(c)
	if !ok {
		return
	}
	in, ok := readInput(c)
	if !ok {
		return
	}
	r := newRules(in)
	data := upsertInput{
		ProgressPercent: r.intField("progress_percent", false, i64(0), i64(100)),
		LastPosition:    r.intField("last_position", false, i64(0), i64(maxInt32)),
		WatchedSeconds:  r.intField("watched_seconds", false, i64(0), i64(maxInt32)),
		Completed:       r.boolField("completed", true),
	}
	if !r.ok(c) {
		return
	}

	var saved progressRow
	err := m.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		prev, err := findProgress(tx, cl.UserID, lesson.ID, true)
		if err != nil {
			return err
		}
		saved, err = saveProgress(tx, applyUpsert(prev, data, cl.UserID, lesson.ID, m.now()))
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"message": "Lesson progress saved successfully.", "progress": saved.JSON()})
}

// maxInt32 caps integer inputs stored in `integer` columns (legacy had no
// upper bound and failed with a database error instead).
const maxInt32 = 1<<31 - 1

// autoCompleted is UserManagement updateProgress's rule: a lesson with a
// duration counts as completed once 90% of it was watched.
func autoCompleted(durationSeconds int, watchedSeconds int64) bool {
	return durationSeconds > 0 && float64(watchedSeconds) >= float64(durationSeconds)*0.9
}

// updateProgress is POST /courses/{course}/lessons/{lesson}/progress
// (UserManagement\LessonProgressController@updateProgress).
func (m *Module) updateProgress(c *gin.Context) {
	course, lesson, ok := m.bindCourseLesson(c)
	if !ok {
		return
	}
	cl, ok := m.caller(c)
	if !ok || !requireLessonInCourse(c, course, lesson) || !m.requireAccess(c, cl, course) {
		return
	}
	in, ok := readInput(c)
	if !ok {
		return
	}
	r := newRules(in)
	watched := r.intField("watched_seconds", true, i64(0), i64(maxInt32))
	completedIn := r.boolField("completed", false)
	if !r.ok(c) {
		return
	}
	completed := autoCompleted(lesson.DurationSeconds, *watched)
	if completedIn != nil {
		completed = *completedIn
	}

	now := m.now()
	ctx := c.Request.Context()
	var saved progressRow
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		prev, err := findProgress(tx, cl.UserID, lesson.ID, true)
		if err != nil {
			return err
		}
		p := progressRow{UserID: cl.UserID, LessonID: lesson.ID}
		if prev != nil {
			p = *prev
		}
		// updateOrCreate(watched_seconds, completed, last_watched_at); the new
		// schema stores `completed` as completed_at.
		p.WatchedSeconds = int(*watched)
		p.LastWatchedAt = &now
		switch {
		case !completed:
			p.CompletedAt = nil
		case p.CompletedAt == nil:
			p.CompletedAt = &now
		}
		saved, err = saveProgress(tx, p)
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	if completed {
		// Hook point: course/path completion side effects (certificate row,
		// career-path progress). Failures are logged, the progress is saved.
		if err := m.onLessonCompleted(ctx, cl.UserID, course.ID); err != nil {
			logx.FromContext(ctx).ErrorContext(ctx, "lesson completion side effects failed",
				"user_id", cl.UserID, "course_id", course.ID, "error", err.Error())
		}
	}
	httpx.OK(c, gin.H{"message": "Progress saved", "progress": saved.JSON()})
}

// courseLessonProgress is one entry of GET /courses/{course}/progress.
type courseLessonProgress struct {
	ID             int64      `json:"id"`
	Title          string     `json:"title"`
	Order          int        `json:"order"`
	Duration       int        `json:"duration"`
	Completed      bool       `json:"completed"`
	WatchedSeconds int        `json:"watched_seconds"`
	LastWatchedAt  httpx.Time `json:"last_watched_at"`
}

// courseProgress is GET /courses/{course}/progress.
func (m *Module) courseProgress(c *gin.Context) {
	course, ok := m.bindCourse(c)
	if !ok {
		return
	}
	cl, ok := m.caller(c)
	if !ok || !m.requireAccess(c, cl, course) {
		return
	}
	var rows []struct {
		ID             int64      `gorm:"column:id"`
		Title          string     `gorm:"column:title"`
		Position       int        `gorm:"column:position"`
		Duration       int        `gorm:"column:duration_seconds"`
		CompletedAt    *time.Time `gorm:"column:completed_at"`
		WatchedSeconds *int       `gorm:"column:watched_seconds"`
		LastWatchedAt  *time.Time `gorm:"column:last_watched_at"`
	}
	err := m.db.WithContext(c.Request.Context()).Raw(`
		SELECT l.id, l.title, l.position, l.duration_seconds,
		       lp.completed_at, lp.watched_seconds, lp.last_watched_at
		FROM lessons l
		LEFT JOIN lesson_progress lp ON lp.lesson_id = l.id AND lp.user_id = ?
		WHERE l.course_id = ?
		ORDER BY l.position, l.id`, cl.UserID, course.ID).Scan(&rows).Error
	if err != nil {
		httpx.Fail(c, fmt.Errorf("study: course progress: %w", err))
		return
	}
	if len(rows) == 0 {
		httpx.OK(c, gin.H{"total_lessons": 0, "completed_lessons": 0, "percent": 0, "lessons": []any{}})
		return
	}
	lessons := make([]courseLessonProgress, 0, len(rows))
	var done int64
	for _, r := range rows {
		item := courseLessonProgress{
			ID: r.ID, Title: r.Title, Order: r.Position, Duration: r.Duration,
			Completed: r.CompletedAt != nil, LastWatchedAt: tptr(r.LastWatchedAt),
		}
		if r.WatchedSeconds != nil {
			item.WatchedSeconds = *r.WatchedSeconds
		}
		if item.Completed {
			done++
		}
		lessons = append(lessons, item)
	}
	httpx.OK(c, gin.H{
		"total_lessons":     len(rows),
		"completed_lessons": done,
		"percent":           phpFloat(roundPercent(done, int64(len(rows)))),
		"lessons":           lessons,
	})
}

// completionCounts returns (lessons in course, lessons completed by user).
func completionCounts(ctx context.Context, db *gorm.DB, userID, courseID int64) (total, done int64, err error) {
	var row struct {
		Total int64 `gorm:"column:total"`
		Done  int64 `gorm:"column:done"`
	}
	err = db.WithContext(ctx).Raw(`
		SELECT count(*) AS total, count(lp.completed_at) AS done
		FROM lessons l
		LEFT JOIN lesson_progress lp ON lp.lesson_id = l.id AND lp.user_id = ?
		WHERE l.course_id = ?`, userID, courseID).Scan(&row).Error
	if err != nil {
		return 0, 0, fmt.Errorf("study: completion counts: %w", err)
	}
	return row.Total, row.Done, nil
}
