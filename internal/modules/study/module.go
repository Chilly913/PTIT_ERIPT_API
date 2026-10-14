// Package study ports the learner-side lesson endpoints of the Laravel API:
// lesson progress (CourseManagement + UserManagement LessonProgressController),
// GET /enrollments, lesson notes, lesson attachments and lesson assignments
// (AssignmentController), including the completion side effects that live in
// the new schema (course certificate row, career-path progress).
package study

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/media"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/rbac"
)

// maxUploadKB is the `max:51200` rule of the attachment and submission uploads (50 MB).
const maxUploadKB = 51200

// Module implements app.Module.
type Module struct {
	log    *slog.Logger
	db     *gorm.DB
	roles  *rbac.Service
	tokens *auth.TokenService
	store  media.ObjectStore
	media  media.Resolver
	appURL string
	now    func() time.Time
}

// Option customises the module (mainly for tests).
type Option func(*Module)

// WithStore overrides the object store (default a.Storage).
func WithStore(s media.ObjectStore) Option { return func(m *Module) { m.store = s } }

// WithClock overrides the clock (progress timestamps, upload names, term windows).
func WithClock(now func() time.Time) Option { return func(m *Module) { m.now = now } }

// New builds the module.
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

// Register implements app.Module. Every route is behind auth:sanctum.
func (m *Module) Register(api *gin.RouterGroup) {
	g := api.Group("", m.tokens.Required())
	lesson := "/courses/:course/lessons/:lesson"

	// routes/api/course.php — learning experience (enrolled student / owner).
	g.GET(lesson+"/progress", m.showProgress)
	g.PUT(lesson+"/progress", m.upsertProgress)
	g.GET(lesson+"/notes", m.listNotes)
	g.POST(lesson+"/notes", m.createNote)
	g.PUT(lesson+"/notes/:note", m.updateNote)
	g.DELETE(lesson+"/notes/:note", m.deleteNote)
	g.GET(lesson+"/attachments", m.listAttachments)
	g.GET(lesson+"/attachments/:attachment/download", m.downloadAttachment)
	g.GET(lesson+"/assignment", m.showAssignment)
	g.POST(lesson+"/assignment/submit", m.submitAssignment)

	// routes/api/course.php — permission:manage_lessons|manage_courses.
	w := g.Group("", m.roles.RequirePermission(rbac.PermManageLessons, rbac.PermManageCourses))
	w.POST(lesson+"/attachments", m.uploadAttachment)
	w.DELETE(lesson+"/attachments/:attachment", m.deleteAttachment)
	w.POST(lesson+"/assignment", m.saveAssignment)
	w.GET(lesson+"/assignment/submissions", m.lessonSubmissions)
	w.GET("/courses/:course/assignments/submissions", m.courseSubmissions)
	w.PUT("/courses/:course/assignments/submissions/:submission", m.gradeSubmission)

	// routes/api/user.php — UserManagement\LessonProgressController.
	g.POST(lesson+"/progress", m.updateProgress)
	g.GET("/courses/:course/progress", m.courseProgress)
	g.GET("/enrollments", m.enrollments)
}

var _ app.Module = (*Module)(nil)
