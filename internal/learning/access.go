// Package learning holds what the catalog, study and interaction modules share:
// course lookup and the legacy course access rules (App\Support\Authorize,
// CoursePolicy, the controllers' authorizeLessonAccess).
package learning

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/rbac"
)

// ErrCourseNotFound is returned when no course matches.
var ErrCourseNotFound = errors.New("learning: course not found")

// CourseRef is the minimum needed for access decisions.
type CourseRef struct {
	ID           int64  `gorm:"column:id"`
	InstructorID int64  `gorm:"column:instructor_id"` // legacy courses.user_id
	Status       string `gorm:"column:status"`
	Price        string `gorm:"column:price"` // numeric as text, avoids float rounding
}

// FindCourseRef resolves a {course} route parameter like Course::resolveRouteBinding:
// all digits = id, anything else = slug.
func FindCourseRef(ctx context.Context, db *gorm.DB, key string) (CourseRef, error) {
	q := `SELECT id, instructor_id, status, price::text AS price FROM courses WHERE `
	var args any = key
	if id, err := strconv.ParseInt(key, 10, 64); err == nil && key != "" && strings.Trim(key, "0123456789") == "" {
		q += `id = ?`
		args = id
	} else {
		q += `slug = ?`
	}
	var rows []CourseRef
	if err := db.WithContext(ctx).Raw(q, args).Scan(&rows).Error; err != nil {
		return CourseRef{}, fmt.Errorf("learning: find course: %w", err)
	}
	if len(rows) == 0 {
		return CourseRef{}, ErrCourseNotFound
	}
	return rows[0], nil
}

// IsEnrolled reports whether the user has an enrollment row for the course.
func IsEnrolled(ctx context.Context, db *gorm.DB, userID, courseID int64) (bool, error) {
	var n int64
	err := db.WithContext(ctx).Raw(
		`SELECT count(*) FROM enrollments WHERE user_id = ? AND course_id = ?`, userID, courseID).Scan(&n).Error
	if err != nil {
		return false, fmt.Errorf("learning: is enrolled: %w", err)
	}
	return n > 0, nil
}

// Caller is the authenticated user with roles and permissions resolved
// (rbac.Service.Caller / UserRolesPermissions).
type Caller struct {
	UserID int64
	Roles  []string
	Perms  []string
}

// CallerFrom resolves the request's caller (memoized per request by rbac).
// ok=false when the request is unauthenticated.
func CallerFrom(c *gin.Context, svc *rbac.Service) (Caller, bool, error) {
	roles, perms, ok, err := svc.Caller(c)
	if err != nil || !ok {
		return Caller{}, ok, err
	}
	return Caller{UserID: auth.UserID(c), Roles: roles, Perms: perms}, true, nil
}

// IsAdmin is Authorize::isAdmin (hasRole('admin')).
func (c Caller) IsAdmin() bool { return rbac.HasAnyOf(c.Roles, rbac.RoleAdmin) }

// Allows is Authorize::allows: admin, or any of the permissions.
func (c Caller) Allows(perms ...string) bool {
	return c.IsAdmin() || rbac.HasAnyOf(c.Perms, perms...)
}

// IsOwner is "admin or course owner" (the isOwner of authorizeLessonAccess).
func (c Caller) IsOwner(course CourseRef) bool {
	return c.IsAdmin() || course.InstructorID == c.UserID
}

// OwnsCourse is Authorize::ownsCourse: the permission (default manage_courses)
// and then admin or owner. Used by CoursePolicy update/delete/publish.
func (c Caller) OwnsCourse(course CourseRef, perms ...string) bool {
	if len(perms) == 0 {
		perms = []string{rbac.PermManageCourses}
	}
	if !c.Allows(perms...) {
		return false
	}
	return c.IsOwner(course)
}

// CanViewCourseAdmin is CoursePolicy::view.
func (c Caller) CanViewCourseAdmin(course CourseRef) bool {
	return c.IsOwner(course) || c.Allows(rbac.PermManageCourses)
}

// CanAccessLessons is authorizeLessonAccess: owner/admin or enrolled.
// Callers answer 403 {"message":"Enrollment required"} when false.
func CanAccessLessons(ctx context.Context, db *gorm.DB, c Caller, course CourseRef) (bool, error) {
	if c.IsOwner(course) {
		return true, nil
	}
	return IsEnrolled(ctx, db, c.UserID, course.ID)
}
