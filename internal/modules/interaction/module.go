// Package interaction ports the Laravel course community and lesson add-on
// endpoints: reviews (ReviewController), Q&A with reactions (CourseQaController),
// virtual classes (VirtualClassController) and SCORM/H5P packages with SCORM
// tracking (ScormPackageController, ScormController).
package interaction

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/media"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/rbac"
)

// Module implements app.Module.
type Module struct {
	log    *slog.Logger
	db     *gorm.DB
	roles  *rbac.Service
	tokens *auth.TokenService
	store  ObjectStore
	media  media.Resolver
	bucket string
	limits ScormLimits
}

// Option customises the module (mainly for tests).
type Option func(*Module)

// WithStore overrides the object store (default a.Storage).
func WithStore(s ObjectStore) Option { return func(m *Module) { m.store = s } }

// WithScormLimits overrides the SCORM package extraction limits.
func WithScormLimits(l ScormLimits) Option { return func(m *Module) { m.limits = l } }

// New builds the module. roles resolves the caller's roles/permissions,
// tokens guards the authenticated routes.
func New(a *app.App, roles *rbac.Service, tokens *auth.TokenService, opts ...Option) *Module {
	m := &Module{log: a.Log, db: a.DB, roles: roles, tokens: tokens, limits: DefaultScormLimits}
	if a.Storage != nil {
		m.store = minioStore{a.Storage}
	}
	for _, o := range opts {
		o(m)
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	m.media = media.NewResolver(m.store, a.Cfg.App.URL)
	switch {
	case m.store != nil:
		m.bucket = m.store.Bucket()
	case a.Cfg.Storage.Bucket != "":
		m.bucket = a.Cfg.Storage.Bucket
	default:
		m.bucket = "lms-videos" // config('filesystems.disks.minio.bucket') default
	}
	return m
}

// Register implements app.Module.
//
// Middleware order follows Laravel: auth:sanctum runs first (Authenticate has
// priority over SubstituteBindings), then the route-model bindings (404), then
// Spatie's permission middleware (403), then the controller.
func (m *Module) Register(api *gin.RouterGroup) {
	course := m.bindCourse
	lesson := m.bindLesson
	submitReviews := m.roles.RequirePermission(rbac.PermSubmitReviews)
	manageLessons := m.roles.RequirePermission(rbac.PermManageLessons, rbac.PermManageCourses)

	// Public.
	api.GET("/courses/:course/reviews", course, m.listReviews)

	g := api.Group("", m.tokens.Required())

	// Reviews (permission:submit_reviews).
	g.POST("/courses/:course/reviews", course, submitReviews, m.storeReview)
	g.PUT("/courses/:course/reviews/:review", course, m.bindReview, submitReviews, m.updateReview)
	g.DELETE("/courses/:course/reviews/:review", course, m.bindReview, submitReviews, m.destroyReview)

	// Q&A.
	g.GET("/courses/:course/qas", course, m.listQas)
	g.POST("/courses/:course/qas", course, m.storeQa)
	g.GET("/courses/:course/qas/:qa", course, m.bindQa, m.showQa)
	g.POST("/courses/:course/qas/:qa/replies", course, m.bindQa, m.storeReply)
	g.POST("/courses/:course/qa-reactions", course, m.react)

	// Virtual class.
	g.GET("/courses/:course/lessons/:lesson/virtual-class", course, lesson, m.showVirtualClass)
	g.POST("/courses/:course/lessons/:lesson/virtual-class", course, lesson, manageLessons, m.storeVirtualClass)
	g.DELETE("/courses/:course/lessons/:lesson/virtual-class", course, lesson, manageLessons, m.destroyVirtualClass)

	// SCORM / H5P.
	g.GET("/courses/:course/lessons/:lesson/scorm-package", course, lesson, m.showScormPackage)
	g.POST("/courses/:course/lessons/:lesson/scorm-package", course, lesson, manageLessons, m.storeScormPackage)
	g.DELETE("/courses/:course/lessons/:lesson/scorm-package", course, lesson, manageLessons, m.destroyScormPackage)
	g.POST("/courses/:course/lessons/:lesson/scorm/track", course, lesson, m.trackScorm)
}

var _ app.Module = (*Module)(nil)
