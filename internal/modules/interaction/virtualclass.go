package interaction

import (
	"errors"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// liveLessonTypes keep their type when a virtual class is attached.
var liveLessonTypes = []string{"virtual_class", "zoom", "meet", "live"}

// showVirtualClass is VirtualClassController@show. Like Laravel any
// authenticated user may read it (no enrollment check) and every field,
// including meeting_password/start_url, is returned (decision: no hiding).
func (m *Module) showVirtualClass(c *gin.Context) {
	lesson := lessonOf(c)
	if lesson.CourseID != courseOf(c).ID {
		notFound(c)
		return
	}
	var rows []virtualClassJSON
	if err := m.db.WithContext(c.Request.Context()).Raw(
		`SELECT `+virtualClassCols+` FROM virtual_classes WHERE lesson_id = ?`, lesson.ID).Scan(&rows).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if len(rows) == 0 {
		httpx.Abort(c, http.StatusNotFound, "No virtual class set for this lesson")
		return
	}
	httpx.OK(c, rows[0])
}

// storeVirtualClass is VirtualClassController@store (updateOrCreate by lesson).
// Optional fields that are absent keep their stored value on update.
func (m *Module) storeVirtualClass(c *gin.Context) {
	ctx := c.Request.Context()
	who, ok := m.caller(c)
	if !ok {
		return
	}
	course, lesson := courseOf(c), lessonOf(c)
	if !who.IsOwner(course) {
		forbidden(c)
		return
	}
	if lesson.CourseID != course.ID {
		notFound(c)
		return
	}
	in, ok := bindInput(c)
	if !ok {
		return
	}
	if err := validate(in, nil,
		rules("provider", "required|string|in:zoom,google_meet,jitsi,other"),
		rules("meeting_id", "nullable|string|max:255"),
		rules("meeting_password", "nullable|string|max:255"),
		rules("join_url", "required|url"),
		rules("start_url", "nullable|url"),
		rules("start_at", "required|date"),
		rules("duration", "required|integer|min:1"),
	); err != nil {
		httpx.Fail(c, err)
		return
	}
	startAt, _ := parseDate(*in.str("start_at"))
	duration, _ := in.int("duration")
	if duration > 1<<31-1 {
		httpx.Fail(c, httpx.NewValidationError().AddRule("duration", "max.numeric", map[string]string{"max": "2147483647"}))
		return
	}

	var out virtualClassJSON
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []virtualClassJSON
		if err := tx.Raw(`INSERT INTO virtual_classes
  (lesson_id, provider, meeting_id, meeting_password, join_url, start_url, start_at, duration_minutes)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (lesson_id) DO UPDATE SET
  provider = EXCLUDED.provider,
  meeting_id = CASE WHEN ? THEN EXCLUDED.meeting_id ELSE virtual_classes.meeting_id END,
  meeting_password = CASE WHEN ? THEN EXCLUDED.meeting_password ELSE virtual_classes.meeting_password END,
  join_url = EXCLUDED.join_url,
  start_url = CASE WHEN ? THEN EXCLUDED.start_url ELSE virtual_classes.start_url END,
  start_at = EXCLUDED.start_at,
  duration_minutes = EXCLUDED.duration_minutes
RETURNING `+virtualClassCols,
			lesson.ID, *in.str("provider"), in.str("meeting_id"), in.str("meeting_password"),
			*in.str("join_url"), in.str("start_url"), startAt, duration,
			in.has("meeting_id"), in.has("meeting_password"), in.has("start_url"),
		).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.New("interaction: upsert virtual_classes returned no row")
		}
		out = rows[0]
		if !slices.Contains(liveLessonTypes, lesson.Type) {
			return tx.Exec(`UPDATE lessons SET type = 'virtual_class' WHERE id = ?`, lesson.ID).Error
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{
		"message":       "Virtual class updated successfully",
		"virtual_class": out,
	})
}

// destroyVirtualClass is VirtualClassController@destroy. Deviation: Laravel
// skipped the lesson/course check here, letting the owner of any course delete
// another course's virtual class; Go answers 404 "Not found" like store does.
func (m *Module) destroyVirtualClass(c *gin.Context) {
	who, ok := m.caller(c)
	if !ok {
		return
	}
	course := courseOf(c)
	if !who.IsOwner(course) {
		forbidden(c)
		return
	}
	if lessonOf(c).CourseID != course.ID {
		notFound(c)
		return
	}
	if err := m.db.WithContext(c.Request.Context()).Exec(
		`DELETE FROM virtual_classes WHERE lesson_id = ?`, lessonOf(c).ID).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"message": "Virtual class details removed"})
}
