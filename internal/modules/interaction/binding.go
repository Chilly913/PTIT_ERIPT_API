package interaction

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
)

// Context keys of the resolved route models.
const (
	ctxCourse = "interaction.course"
	ctxLesson = "interaction.lesson"
	ctxReview = "interaction.review"
	ctxQa     = "interaction.qa"
)

// Laravel's ModelNotFoundException message, rendered as the 404 body. Course
// binding uses firstOrFail (no id in the message); implicit bindings of the
// other models append the route value.
const msgCourseNotFound = `No query results for model [App\Models\Course].`

func modelNotFound(model, key string) string {
	return `No query results for model [App\Models\` + model + `] ` + key
}

// parseID parses a route key the way an implicit binding lookup by primary
// key would match: digits only, else no row.
func parseID(s string) (int64, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}

// lessonRef is the part of a lesson the handlers need.
type lessonRef struct {
	ID       int64  `gorm:"column:id"`
	CourseID int64  `gorm:"column:course_id"`
	Type     string `gorm:"column:type"`
	Title    string `gorm:"column:title"`
}

func (m *Module) bindCourse(c *gin.Context) {
	ref, err := learning.FindCourseRef(c.Request.Context(), m.db, c.Param("course"))
	if errors.Is(err, learning.ErrCourseNotFound) {
		httpx.NotFound(c, msgCourseNotFound)
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Set(ctxCourse, ref)
	c.Next()
}

func (m *Module) bindLesson(c *gin.Context) {
	key := c.Param("lesson")
	var rows []lessonRef
	if id, ok := parseID(key); ok {
		if err := m.db.WithContext(c.Request.Context()).Raw(
			`SELECT id, course_id, type, title FROM lessons WHERE id = ?`, id).Scan(&rows).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	if len(rows) == 0 {
		httpx.NotFound(c, modelNotFound("Lesson", key))
		return
	}
	c.Set(ctxLesson, rows[0])
	c.Next()
}

func (m *Module) bindReview(c *gin.Context) {
	key := c.Param("review")
	var rows []reviewRow
	if id, ok := parseID(key); ok {
		if err := m.db.WithContext(c.Request.Context()).Raw(
			`SELECT `+reviewCols+` FROM reviews WHERE id = ?`, id).Scan(&rows).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	if len(rows) == 0 {
		httpx.NotFound(c, modelNotFound("Review", key))
		return
	}
	c.Set(ctxReview, rows[0])
	c.Next()
}

func (m *Module) bindQa(c *gin.Context) {
	key := c.Param("qa")
	var rows []qaRow
	if id, ok := parseID(key); ok {
		if err := m.db.WithContext(c.Request.Context()).Raw(
			`SELECT `+qaCols+` FROM course_qas WHERE id = ?`, id).Scan(&rows).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	if len(rows) == 0 {
		httpx.NotFound(c, modelNotFound("CourseQa", key))
		return
	}
	c.Set(ctxQa, rows[0])
	c.Next()
}

func courseOf(c *gin.Context) learning.CourseRef { return c.MustGet(ctxCourse).(learning.CourseRef) }
func lessonOf(c *gin.Context) lessonRef          { return c.MustGet(ctxLesson).(lessonRef) }
func reviewOf(c *gin.Context) reviewRow          { return c.MustGet(ctxReview).(reviewRow) }
func qaOf(c *gin.Context) qaRow                  { return c.MustGet(ctxQa).(qaRow) }

// caller resolves the authenticated caller; false when a response was written.
func (m *Module) caller(c *gin.Context) (learning.Caller, bool) {
	who, ok, err := learning.CallerFrom(c, m.roles)
	switch {
	case err != nil:
		httpx.Fail(c, err)
		return who, false
	case !ok:
		httpx.Unauthenticated(c)
		return who, false
	}
	return who, true
}

// forbidden renders the controllers' 403 {"message":"Forbidden"}.
func forbidden(c *gin.Context) { httpx.Abort(c, http.StatusForbidden, "Forbidden") }

// notFound renders the controllers' 404 {"message":"Not found"}.
func notFound(c *gin.Context) { httpx.Abort(c, http.StatusNotFound, "Not found") }
