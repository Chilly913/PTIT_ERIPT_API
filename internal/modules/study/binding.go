package study

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
)

// Messages written by the legacy controllers.
const (
	msgNotFoundLower      = "Not found" // controllers' own 404 (lesson not in course, ...)
	msgForbiddenPlain     = "Forbidden"
	msgEnrollmentRequired = "Enrollment required"
)

// modelNotFound renders the 404 of a failed implicit route binding exactly like
// Laravel's ModelNotFoundException (non-debug JSON).
func modelNotFound(c *gin.Context, model, key string) {
	msg := "No query results for model [App\\Models\\" + model + "]"
	if key == "" {
		msg += "."
	} else {
		msg += " " + key
	}
	httpx.Abort(c, http.StatusNotFound, msg)
}

// parseID parses an integer route key; ok=false means no row can match.
func parseID(s string) (int64, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}

// bindCourse resolves {course} (Course::resolveRouteBinding uses firstOrFail,
// so the 404 message carries no key).
func (m *Module) bindCourse(c *gin.Context) (learning.CourseRef, bool) {
	ref, err := learning.FindCourseRef(c.Request.Context(), m.db, c.Param("course"))
	if errors.Is(err, learning.ErrCourseNotFound) {
		modelNotFound(c, "Course", "")
		return ref, false
	}
	if err != nil {
		httpx.Fail(c, err)
		return ref, false
	}
	return ref, true
}

// lessonRef is the part of a lesson the study endpoints need.
type lessonRef struct {
	ID              int64  `gorm:"column:id"`
	CourseID        int64  `gorm:"column:course_id"`
	Title           string `gorm:"column:title"`
	Type            string `gorm:"column:type"`
	Position        int    `gorm:"column:position"`
	DurationSeconds int    `gorm:"column:duration_seconds"`
}

// bindLesson resolves {lesson} by id (implicit binding, not scoped to the course).
func (m *Module) bindLesson(c *gin.Context) (lessonRef, bool) {
	key := c.Param("lesson")
	var rows []lessonRef
	if id, ok := parseID(key); ok {
		err := m.db.WithContext(c.Request.Context()).Raw(
			`SELECT id, course_id, title, type, position, duration_seconds FROM lessons WHERE id = ?`, id).
			Scan(&rows).Error
		if err != nil {
			httpx.Fail(c, fmt.Errorf("study: find lesson: %w", err))
			return lessonRef{}, false
		}
	}
	if len(rows) == 0 {
		modelNotFound(c, "Lesson", key)
		return lessonRef{}, false
	}
	return rows[0], true
}

// bindCourseLesson resolves {course} then {lesson} (Laravel binds in route order).
func (m *Module) bindCourseLesson(c *gin.Context) (learning.CourseRef, lessonRef, bool) {
	course, ok := m.bindCourse(c)
	if !ok {
		return course, lessonRef{}, false
	}
	lesson, ok := m.bindLesson(c)
	return course, lesson, ok
}

// caller resolves the authenticated user with roles/permissions.
func (m *Module) caller(c *gin.Context) (learning.Caller, bool) {
	cl, ok, err := learning.CallerFrom(c, m.roles)
	if err != nil {
		httpx.Fail(c, err)
		return cl, false
	}
	if !ok {
		httpx.Unauthenticated(c)
		return cl, false
	}
	return cl, true
}

// requireLessonInCourse answers 404 {"message":"Not found"} when the lesson
// belongs to another course.
func requireLessonInCourse(c *gin.Context, course learning.CourseRef, lesson lessonRef) bool {
	if lesson.CourseID != course.ID {
		httpx.Abort(c, http.StatusNotFound, msgNotFoundLower)
		return false
	}
	return true
}

// requireAccess is the controllers' authorizeLessonAccess minus the lesson
// check: owner/admin or enrolled, else 403 "Enrollment required".
func (m *Module) requireAccess(c *gin.Context, cl learning.Caller, course learning.CourseRef) bool {
	ok, err := learning.CanAccessLessons(c.Request.Context(), m.db, cl, course)
	if err != nil {
		httpx.Fail(c, err)
		return false
	}
	if !ok {
		httpx.Abort(c, http.StatusForbidden, msgEnrollmentRequired)
		return false
	}
	return true
}

// requireOwner is the instructor endpoints' "admin or course owner" check,
// 403 {"message":"Forbidden"} otherwise.
func requireOwner(c *gin.Context, cl learning.Caller, course learning.CourseRef) bool {
	if !cl.IsOwner(course) {
		httpx.Abort(c, http.StatusForbidden, msgForbiddenPlain)
		return false
	}
	return true
}

// lessonGate binds course+lesson, then applies authorizeLessonAccess
// (404 "Not found" / 403 "Enrollment required").
func (m *Module) lessonGate(c *gin.Context) (learning.Caller, learning.CourseRef, lessonRef, bool) {
	course, lesson, ok := m.bindCourseLesson(c)
	if !ok {
		return learning.Caller{}, course, lesson, false
	}
	cl, ok := m.caller(c)
	if !ok || !requireLessonInCourse(c, course, lesson) || !m.requireAccess(c, cl, course) {
		return cl, course, lesson, false
	}
	return cl, course, lesson, true
}
