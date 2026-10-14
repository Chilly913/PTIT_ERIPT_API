// Package catalog ports the Laravel course catalog endpoints
// (CourseManagement CategoryController, CourseController, SectionController and
// LessonController): public category/course/lesson browsing, the instructor's
// "my courses" list and course/section/lesson authoring.
package catalog

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/media"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/rbac"
)

// ObjectStore is the subset of *storage.Storage the module uses (faked in tests).
type ObjectStore = media.ObjectStore

// Module implements app.Module.
type Module struct {
	log    *slog.Logger
	db     *gorm.DB
	roles  *rbac.Service
	tokens *auth.TokenService
	store  ObjectStore
	media  media.Resolver
	appURL string
	now    func() time.Time
}

// Option customises the module (mainly for tests).
type Option func(*Module)

// WithStore overrides the object store (default a.Storage).
func WithStore(s ObjectStore) Option { return func(x *Module) { x.store = s } }

// WithClock overrides the clock (upload file names, video-url expiry).
func WithClock(now func() time.Time) Option { return func(x *Module) { x.now = now } }

// New builds the module. tokens guards the authenticated routes and resolves
// the optional caller on public ones; roles answers role/permission checks.
func New(a *app.App, roles *rbac.Service, tokens *auth.TokenService, opts ...Option) *Module {
	m := &Module{log: a.Log, db: a.DB, roles: roles, tokens: tokens, appURL: a.Cfg.App.URL}
	if a.Storage != nil {
		m.store = a.Storage
	}
	for _, o := range opts {
		o(m)
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.now == nil {
		m.now = func() time.Time { return time.Now().UTC() }
	}
	m.media = media.NewResolver(m.store, m.appURL)
	return m
}

// Register implements app.Module.
func (m *Module) Register(api *gin.RouterGroup) {
	optional := m.tokens.Optional()
	required := m.tokens.Required()
	manageCourses := m.roles.RequirePermission(rbac.PermManageCourses)
	manageLessons := m.roles.RequirePermission(rbac.PermManageLessons, rbac.PermManageCourses)
	course, lesson, section, category := m.bindCourse, m.bindLesson, m.bindSection, m.bindCategory

	// Categories (public)
	api.GET("/categories", m.listCategories)
	api.GET("/categories/:category", category, m.showCategory)

	// Courses (public; a valid bearer token identifies the caller)
	api.GET("/courses/categories", m.programCategories)
	api.GET("/courses", m.listCourses)
	api.GET("/courses/:course", optional, course, m.showCourse)
	api.GET("/courses/:course/lessons", optional, course, m.listLessons)
	api.GET("/courses/:course/lessons/:lesson", optional, course, lesson, m.showLesson)
	api.GET("/courses/:course/lessons/:lesson/video-url", optional, course, lesson, m.videoURL)
	api.GET("/courses/:course/sections", optional, course, m.listSections)

	// Authenticated. Laravel order: auth:sanctum, route model binding (404),
	// then the permission middleware (403).
	api.GET("/my-courses", required, m.myCourses)

	// Course write — manage_courses
	api.POST("/courses", required, manageCourses, m.storeCourse)
	api.PUT("/courses/:course", required, course, manageCourses, m.updateCourse)
	api.DELETE("/courses/:course", required, course, manageCourses, m.destroyCourse)
	// Method spoofing (`_method=PUT` in multipart bodies, used by the course
	// editor to send thumbnail_file); see spoofedMethod.
	api.POST("/courses/:course", m.spoofed(map[string][]gin.HandlerFunc{
		http.MethodPut:    {required, course, manageCourses, m.updateCourse},
		http.MethodDelete: {required, course, manageCourses, m.destroyCourse},
	}))
	api.POST("/courses/:course/publish", required, course, manageCourses, m.publishCourse)

	api.POST("/courses/:course/sections", required, course, manageCourses, m.storeSection)
	api.GET("/sections/:section", required, section, manageCourses, m.showSection)
	api.PUT("/sections/:section", required, section, manageCourses, m.updateSection)
	api.DELETE("/sections/:section", required, section, manageCourses, m.destroySection)
	api.POST("/courses/:course/sections/reorder", required, course, manageCourses, m.reorderSections)

	// Lesson write — manage_lessons|manage_courses
	api.POST("/courses/:course/lessons", required, course, manageLessons, m.storeLesson)
	api.PUT("/courses/:course/lessons/:lesson", required, course, lesson, manageLessons, m.updateLesson)
	api.DELETE("/courses/:course/lessons/:lesson", required, course, lesson, manageLessons, m.destroyLesson)
	api.POST("/courses/:course/lessons/:lesson/upload-video", required, course, lesson, manageLessons, m.uploadVideo)
	api.POST("/courses/:course/lessons/reorder", required, course, manageLessons, m.reorderLessons)
}

var _ app.Module = (*Module)(nil)

// ---------------------------------------------------------------------------
// route model binding

const (
	ctxCourse   = "catalog.course"
	ctxLesson   = "catalog.lesson"
	ctxSection  = "catalog.section"
	ctxCategory = "catalog.category"
)

// bindCourse resolves {course} (id or slug) or answers 404 like
// Course::resolveRouteBinding.
func (m *Module) bindCourse(c *gin.Context) {
	ref, err := learning.FindCourseRef(c.Request.Context(), m.db, c.Param("course"))
	if errors.Is(err, learning.ErrCourseNotFound) {
		httpx.NotFound(c, learning.CourseNotFoundMessage)
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Set(ctxCourse, ref)
}

// bindLesson resolves {lesson} by id (implicit binding, not scoped to the course).
func (m *Module) bindLesson(c *gin.Context) {
	key := c.Param("lesson")
	ref, err := learning.FindLessonRef(c.Request.Context(), m.db, key)
	if errors.Is(err, learning.ErrLessonNotFound) {
		httpx.NotFound(c, learning.ModelNotFoundMessage("Lesson", key))
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Set(ctxLesson, ref)
}

func (m *Module) bindSection(c *gin.Context) {
	key := c.Param("section")
	ref, err := learning.FindSectionRef(c.Request.Context(), m.db, key)
	if errors.Is(err, learning.ErrSectionNotFound) {
		httpx.NotFound(c, learning.ModelNotFoundMessage("Section", key))
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Set(ctxSection, ref)
}

func (m *Module) bindCategory(c *gin.Context) {
	key := c.Param("category")
	row, ok, err := m.findCategory(c.Request.Context(), key)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !ok {
		httpx.NotFound(c, learning.ModelNotFoundMessage("Category", key))
		return
	}
	c.Set(ctxCategory, row)
}

func boundCourse(c *gin.Context) learning.CourseRef {
	v, _ := c.Get(ctxCourse)
	ref, _ := v.(learning.CourseRef)
	return ref
}

func boundLesson(c *gin.Context) learning.LessonRef {
	v, _ := c.Get(ctxLesson)
	ref, _ := v.(learning.LessonRef)
	return ref
}

func boundSection(c *gin.Context) learning.SectionRef {
	v, _ := c.Get(ctxSection)
	ref, _ := v.(learning.SectionRef)
	return ref
}

// lessonInCourse is the controllers' `$lesson->course_id !== $course->id` guard.
func lessonInCourse(c *gin.Context) (learning.CourseRef, learning.LessonRef, bool) {
	course, lesson := boundCourse(c), boundLesson(c)
	if lesson.CourseID != course.ID {
		httpx.NotFound(c, "Not found")
		return course, lesson, false
	}
	return course, lesson, true
}

// ---------------------------------------------------------------------------
// callers

// caller returns the optional caller (zero Caller, false for guests).
func (m *Module) caller(c *gin.Context) (learning.Caller, bool, error) {
	return learning.CallerFrom(c, m.roles)
}

// ownerOnly is the controllers' `!Authorize::isAdmin($user) && $course->user_id !== $user->id`
// guard: it answers 403 {"message": msg} and returns false when denied.
func (m *Module) ownerOnly(c *gin.Context, course learning.CourseRef, msg string) bool {
	who, ok, err := m.caller(c)
	if err != nil {
		httpx.Fail(c, err)
		return false
	}
	if !ok {
		httpx.Unauthenticated(c)
		return false
	}
	if !who.IsOwner(course) {
		httpx.Forbidden(c, msg)
		return false
	}
	return true
}

// lessonViewer reports whether the (optional) caller is admin/owner or enrolled.
func (m *Module) lessonViewer(c *gin.Context, course learning.CourseRef) (owner, enrolled bool, err error) {
	who, ok, err := m.caller(c)
	if err != nil || !ok {
		return false, false, err
	}
	owner = who.IsOwner(course)
	enrolled, err = learning.IsEnrolled(c.Request.Context(), m.db, who.UserID, course.ID)
	return owner, enrolled, err
}

// ---------------------------------------------------------------------------
// method spoofing

// spoofed dispatches POST requests carrying Laravel's method override
// (`_method` body field or X-HTTP-Method-Override header) to the handlers of
// that method; anything else answers 405 like an unmatched method. The chain
// runs inline: middlewares that call c.Next() (auth, rbac) see the end of the
// real chain, so Next is a no-op and the loop below continues with the next
// handler unless one aborted.
func (m *Module) spoofed(routes map[string][]gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := spoofedMethod(c)
		chain, ok := routes[method]
		if !ok {
			httpx.Abort(c, http.StatusMethodNotAllowed, httpx.MsgMethodNotAllowed)
			return
		}
		for _, h := range chain {
			h(c)
			if c.IsAborted() {
				return
			}
		}
	}
}

// spoofedMethod reads the overriding method of a POST request.
func spoofedMethod(c *gin.Context) string {
	if h := c.GetHeader("X-HTTP-Method-Override"); h != "" {
		return strings.ToUpper(strings.TrimSpace(h))
	}
	switch c.ContentType() {
	case "multipart/form-data":
		if err := c.Request.ParseMultipartForm(maxMemory); err != nil {
			return ""
		}
	case "application/x-www-form-urlencoded":
		if err := c.Request.ParseForm(); err != nil {
			return ""
		}
	default:
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(c.Request.PostFormValue("_method")))
}
