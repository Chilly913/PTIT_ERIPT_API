package learning

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// ErrLessonNotFound is returned when no lesson matches.
var ErrLessonNotFound = errors.New("learning: lesson not found")

// ErrSectionNotFound is returned when no section matches.
var ErrSectionNotFound = errors.New("learning: section not found")

// LessonRef is the minimum needed to bind a {lesson} route parameter and to
// decide access (LessonController::show/videoUrl).
type LessonRef struct {
	ID        int64   `gorm:"column:id"`
	CourseID  int64   `gorm:"column:course_id"`
	SectionID *int64  `gorm:"column:section_id"`
	Type      string  `gorm:"column:type"`
	IsPreview bool    `gorm:"column:is_preview"`
	VideoPath *string `gorm:"column:video_path"` // legacy video_url
}

// FindLessonRef resolves a {lesson} route parameter like Laravel implicit
// binding (Lesson::where('id', $value)->firstOrFail()). The binding is not
// scoped: callers compare CourseID with the route's course and answer
// 404 {"message":"Not found"} on mismatch, like the legacy controllers.
func FindLessonRef(ctx context.Context, db *gorm.DB, key string) (LessonRef, error) {
	id, ok := RouteID(key)
	if !ok {
		return LessonRef{}, ErrLessonNotFound
	}
	var rows []LessonRef
	err := db.WithContext(ctx).Raw(
		`SELECT id, course_id, section_id, type, is_preview, video_path FROM lessons WHERE id = ?`, id).Scan(&rows).Error
	if err != nil {
		return LessonRef{}, fmt.Errorf("learning: find lesson: %w", err)
	}
	if len(rows) == 0 {
		return LessonRef{}, ErrLessonNotFound
	}
	return rows[0], nil
}

// SectionRef is a bound {section} route parameter.
type SectionRef struct {
	ID       int64 `gorm:"column:id"`
	CourseID int64 `gorm:"column:course_id"`
}

// FindSectionRef resolves a {section} route parameter (implicit binding by id).
func FindSectionRef(ctx context.Context, db *gorm.DB, key string) (SectionRef, error) {
	id, ok := RouteID(key)
	if !ok {
		return SectionRef{}, ErrSectionNotFound
	}
	var rows []SectionRef
	if err := db.WithContext(ctx).Raw(`SELECT id, course_id FROM sections WHERE id = ?`, id).Scan(&rows).Error; err != nil {
		return SectionRef{}, fmt.Errorf("learning: find section: %w", err)
	}
	if len(rows) == 0 {
		return SectionRef{}, ErrSectionNotFound
	}
	return rows[0], nil
}

// RouteID parses an implicit-binding id. MySQL compared `id = 'abc'` as 0 (no
// row), so anything that is not a positive integer simply matches nothing.
func RouteID(key string) (int64, bool) {
	if key == "" || strings.Trim(key, "0123456789") != "" {
		return 0, false
	}
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// ModelNotFoundMessage is the 404 message Laravel renders for a failed route
// model binding (ModelNotFoundException -> NotFoundHttpException):
//   - implicit binding:         "No query results for model [App\Models\Lesson] 12"
//   - Course::resolveRouteBinding (firstOrFail, no ids): "No query results for model [App\Models\Course]."
func ModelNotFoundMessage(model string, ids ...string) string {
	msg := `No query results for model [App\Models\` + model + `]`
	if len(ids) == 0 {
		return msg + "."
	}
	return msg + " " + strings.Join(ids, ", ")
}

// CourseNotFoundMessage is the 404 message of a failed {course} binding.
var CourseNotFoundMessage = ModelNotFoundMessage("Course")

// FormatSizeMB renders a byte count the way the legacy upload code stored it:
// round($bytes / 1024 / 1024, 2) . 'MB' (e.g. "12.5MB", "3MB"). nil stays nil.
// Used for lessons.video_size and lesson_attachments.file_size.
func FormatSizeMB(bytes *int64) *string {
	if bytes == nil {
		return nil
	}
	mb := math.Round(float64(*bytes)/1024/1024*100) / 100
	s := strconv.FormatFloat(mb, 'f', -1, 64) + "MB"
	return &s
}
