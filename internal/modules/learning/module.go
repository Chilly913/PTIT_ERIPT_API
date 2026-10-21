// Package learning serves the course catalog and the learning flow:
//
//	catalog        GET /categories, GET /courses, GET /courses/:ref (+ reviews, Q&A)
//	studio         /my-courses, course CRUD, sections, lessons, students, publish
//	admin review   /admin/courses (list, approve, reject)
//	learner        enroll, /enrollments, progress, lesson detail, notes, reviews, Q&A, dashboard
//
// JSON shapes follow the contracts used by the web app (eript-web/src/features/{site,courses,learn}).
package learning

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Module implements app.Module.
type Module struct {
	db     *gorm.DB
	tokens *auth.TokenService
	roles  auth.RoleService
}

// New builds the module; tokens and roles guard the authenticated routes.
func New(a *app.App, tokens *auth.TokenService, roles auth.RoleService) *Module {
	return &Module{db: a.DB, tokens: tokens, roles: roles}
}

// Register mounts all routes under /api.
func (m *Module) Register(api *gin.RouterGroup) {
	opt := m.tokens.Optional()
	req := m.tokens.Required()
	admin := auth.RequireRole(m.roles, auth.RoleAdmin)
	staff := auth.RequireRole(m.roles, auth.RoleAdmin, auth.RoleAcademicManager, auth.RoleInstructor)

	// Public catalog (a signed-in caller gets personal flags such as is_enrolled).
	api.GET("/categories", m.categories)
	api.GET("/site-stats", m.siteStats)
	api.GET("/courses", opt, m.listCourses)
	api.GET("/courses/:ref", opt, m.showCourse)
	api.GET("/courses/:ref/reviews", m.listReviews)
	api.GET("/courses/:ref/qas", opt, m.listQAs)
	api.GET("/certificates/:credential", m.showCertificate)
	api.GET("/my/certificates", req, m.myCertificates)
	api.POST("/courses/:ref/certificate", req, m.claimCertificate)

	// Studio (instructors manage their own courses; admins manage all).
	api.GET("/my-courses", req, m.myCourses)
	api.POST("/courses", req, staff, m.createCourse)
	api.PUT("/courses/:ref", req, m.updateCourse)
	api.DELETE("/courses/:ref", req, m.deleteCourse)
	api.POST("/courses/:ref/publish", req, m.publishCourse)
	api.GET("/courses/:ref/sections", req, m.studioSections)
	api.POST("/courses/:ref/sections", req, m.createSection)
	api.POST("/courses/:ref/sections/reorder", req, m.reorderSections)
	api.PUT("/sections/:id", req, m.updateSection)
	api.DELETE("/sections/:id", req, m.deleteSection)
	api.POST("/courses/:ref/lessons", req, m.createLesson)
	api.POST("/courses/:ref/lessons/reorder", req, m.reorderLessons)
	api.PUT("/courses/:ref/lessons/:lid", req, m.updateLesson)
	api.DELETE("/courses/:ref/lessons/:lid", req, m.deleteLesson)
	api.GET("/courses/:ref/students", req, m.listStudents)
	api.GET("/courses/:ref/students/export", req, m.exportStudents)
	api.GET("/courses/:ref/report", req, m.showCourseReport)
	api.GET("/courses/:ref/report/students/:uid", req, m.showReportStudent)
	api.GET("/classes", req, m.listClasses)
	api.GET("/classes/filters", req, m.classFilters)
	api.GET("/classes/:id", req, m.showClass)
	api.POST("/classes/:id/students", req, m.addClassStudents)
	api.DELETE("/classes/:id/students/:uid", req, m.removeClassStudent)
	api.POST("/classes/:id/assign-course", req, m.assignClassCourse)
	api.GET("/academic/terms", req, m.listTerms)
	api.GET("/curricula", req, m.listCurricula)
	api.PUT("/classes/:id/curriculum", req, m.setClassCurriculum)
	api.PUT("/classes/:id/terms", req, m.setClassTerms)
	api.POST("/classes/:id/curriculum-courses", req, m.addCurriculumCourse)
	api.DELETE("/classes/:id/curriculum-courses/:course_id", req, m.removeCurriculumCourse)
	api.GET("/courses/:ref/report/export", req, m.exportCourseReport)
	api.GET("/courses/:ref/student-template", req, m.studentTemplate)
	api.POST("/courses/:ref/students", req, m.addStudents)
	api.DELETE("/courses/:ref/students/:uid", req, m.removeStudent)
	api.GET("/courses/:ref/assignments/submissions", req, m.submissions)
	api.PUT("/courses/:ref/assignments/submissions/:sid", req, m.gradeSubmission)
	api.GET("/courses/:ref/assignments/submissions/:sid/file", req, m.downloadSubmission)
	api.POST("/courses/:ref/lessons/:lid/assignment", req, m.saveAssignment)
	api.POST("/courses/:ref/lessons/:lid/assignment/submit", req, m.submitAssignment)
	api.GET("/courses/:ref/lessons/:lid/assignment/submission", req, m.mySubmission)
	api.POST("/courses/:ref/lessons/:lid/scorm", req, m.uploadScorm)
	api.POST("/courses/:ref/lessons/:lid/scorm/track", req, m.trackScorm)
	api.GET("/scorm/:uuid/*filepath", m.serveScorm)

	// Question banks (course owners and admins).
	api.GET("/courses/:ref/question-banks", req, m.listBanks)
	api.POST("/courses/:ref/question-banks", req, m.createBank)
	api.GET("/courses/:ref/question-banks/:bid", req, m.showBank)
	api.PUT("/courses/:ref/question-banks/:bid", req, m.updateBank)
	api.DELETE("/courses/:ref/question-banks/:bid", req, m.deleteBank)
	api.POST("/courses/:ref/question-banks/:bid/import", req, m.importQuestions)
	api.GET("/courses/:ref/question-banks/:bid/export", req, m.exportBankFile)
	api.GET("/courses/:ref/question-bank-template", req, m.bankTemplate)
	api.POST("/courses/:ref/question-banks/:bid/questions", req, m.createQuestion)
	api.PUT("/courses/:ref/question-banks/:bid/questions/:qid", req, m.updateQuestion)
	api.DELETE("/courses/:ref/question-banks/:bid/questions/:qid", req, m.deleteQuestion)
	api.GET("/courses/:ref/question-groups", req, m.listGroups)
	api.POST("/courses/:ref/question-groups", req, m.createGroup)
	api.DELETE("/courses/:ref/question-groups/:gid", req, m.deleteGroup)
	api.PUT("/courses/:ref/question-groups/:gid", req, m.updateGroup)
	api.POST("/courses/:ref/question-banks/:bid/questions/bulk", req, m.bulkQuestions)

	// Exams.
	api.GET("/courses/:ref/exams", req, m.listCourseExams)
	api.POST("/courses/:ref/exams", req, m.createCourseExam)
	api.GET("/exams/standalone", req, m.listStandaloneExams)
	api.POST("/exams/standalone", req, m.createStandaloneExam)
	api.PUT("/exams/:id", req, m.updateExam)
	api.DELETE("/exams/:id", req, m.deleteExam)
	api.GET("/exams/:id/detail", req, m.examDetail)
	api.POST("/exams/:id/status", req, m.setExamStatus)
	api.POST("/exams/:id/quiz", req, m.saveExamPaper)
	api.GET("/exams/:id/enrolled-users", req, m.examCandidates)
	api.POST("/exams/:id/enroll", req, m.enrollExam)
	api.DELETE("/exams/:id/unenroll/:uid", req, m.unenrollExam)
	api.GET("/exams/:id/results", req, m.examResults)

	// Candidate side of exams.
	api.GET("/my/exams", req, m.myExams)
	api.GET("/exams/:id/take", req, m.takeExam)
	api.POST("/exams/:id/submit", req, m.submitExam)

	// Gradebook.
	api.GET("/instructor/courses/:id/grade-components", req, m.getGradeComponents)
	api.PUT("/instructor/courses/:id/grade-components", req, m.putGradeComponents)
	api.POST("/instructor/courses/:id/grade-components/preset", req, m.applyGradePreset)
	api.GET("/instructor/sections/:id/grades", req, m.getSectionGrades)
	api.PUT("/instructor/sections/:id/grades", req, m.putSectionGrades)

	// QR attendance.
	api.GET("/instructor/attendance/sessions", req, m.listSessions)
	api.POST("/instructor/attendance/sessions", req, m.createSession)
	api.GET("/instructor/sessions/:id", req, m.showSession)
	api.DELETE("/instructor/sessions/:id", req, m.deleteSession)
	api.POST("/instructor/sessions/:id/qr", req, m.issueQR)
	api.GET("/instructor/sessions/:id/attendance", req, m.sessionRoster)
	api.PUT("/instructor/sessions/:id/attendance/:uid", req, m.markAttendance)
	api.POST("/attendance/check-in", req, m.checkIn)

	// Lesson quiz ("Bài kiểm tra nhanh").
	api.GET("/courses/:ref/lessons/:lid/quiz", req, m.getLessonQuiz)
	api.POST("/courses/:ref/lessons/:lid/quiz", req, m.saveLessonQuiz)
	api.GET("/courses/:ref/lessons/:lid/quiz/take", req, m.takeQuiz)
	api.POST("/courses/:ref/lessons/:lid/quiz/:qid/submit", req, m.submitQuiz)
	api.POST("/attempts/:id/auto-save", req, m.autoSaveAttempt)

	// Files attached to a file_upload answer, and manual grading (essay, file upload).
	api.POST("/attempts/:id/files", req, m.uploadAttemptFile)
	api.DELETE("/attempts/:id/files/:fid", req, m.deleteAttemptFile)
	api.GET("/attempts/:id/files/:fid", req, m.downloadAttemptFile)
	api.GET("/my/attempts", req, m.myAttempts)
	api.GET("/attempts/:id/result", req, m.attemptResult)
	api.GET("/grading/attempts", req, m.listGrading)
	api.GET("/grading/attempts/:id", req, m.showGrading)
	api.PUT("/grading/attempts/:id", req, m.putGrading)

	// In-app notifications.
	api.GET("/notifications", req, m.notifications)
	api.GET("/notifications/unread-count", req, m.unreadCount)
	api.POST("/notifications/read-all", req, m.markAllRead)
	api.POST("/notifications/:id/read", req, m.markRead)

	// Admin review queue.
	api.GET("/admin/courses", req, admin, m.adminCourses)
	api.PUT("/admin/courses/:ref/approve", req, admin, m.approveCourse)
	api.PUT("/admin/courses/:ref/reject", req, admin, m.rejectCourse)

	// Learner.
	api.POST("/courses/:ref/enroll", req, m.enroll)
	api.GET("/enrollments", req, m.enrollments)
	api.GET("/me/dashboard", req, m.dashboard)
	api.GET("/points/summary", req, m.pointsSummary)
	api.GET("/courses/:ref/progress", req, m.courseProgress)
	api.GET("/courses/:ref/lessons/:lid", req, m.lessonDetail)
	api.GET("/courses/:ref/lessons/:lid/progress", req, m.lessonProgress)
	api.POST("/courses/:ref/lessons/:lid/progress", req, m.saveLessonProgress)
	api.GET("/courses/:ref/lessons/:lid/notes", req, m.listNotes)
	api.POST("/courses/:ref/lessons/:lid/notes", req, m.createNote)
	api.PUT("/courses/:ref/lessons/:lid/notes/:nid", req, m.updateNote)
	api.DELETE("/courses/:ref/lessons/:lid/notes/:nid", req, m.deleteNote)
	api.POST("/courses/:ref/reviews", req, m.createReview)
	api.PUT("/courses/:ref/reviews/:rid", req, m.updateReview)
	api.DELETE("/courses/:ref/reviews/:rid", req, m.deleteReview)
	api.POST("/courses/:ref/qas", req, m.createQA)
	api.POST("/courses/:ref/qas/:qid/replies", req, m.createReply)
	api.POST("/courses/:ref/qa-reactions", req, m.reactQA)
}

// ---- request helpers ------------------------------------------------------

// actor returns the caller's id (0 for guests) and role names.
func (m *Module) actor(c *gin.Context) (int64, []string, error) {
	uid := auth.UserID(c)
	if uid == 0 {
		return 0, nil, nil
	}
	roles, _, err := m.roles.UserRolesPermissions(c.Request.Context(), uid)
	return uid, roles, err
}

func hasRole(roles []string, want ...string) bool {
	for _, r := range roles {
		for _, w := range want {
			if r == w {
				return true
			}
		}
	}
	return false
}

// courseRef is a course resolved from a numeric id or a slug.
type courseRef struct {
	ID           int64
	InstructorID int64
	Status       string
	Price        float64
}

func (m *Module) course(c *gin.Context) (courseRef, bool) {
	ref := c.Param("ref")
	var rows []courseRef
	var err error
	if _, convErr := strconv.ParseInt(ref, 10, 64); convErr == nil {
		err = m.db.Raw("SELECT id, instructor_id AS instructor_id, status, price FROM courses WHERE id = ?", ref).Scan(&rows).Error
	} else {
		err = m.db.Raw("SELECT id, instructor_id AS instructor_id, status, price FROM courses WHERE slug = ?", ref).Scan(&rows).Error
	}
	if err != nil {
		httpx.Fail(c, err)
		return courseRef{}, false
	}
	if len(rows) == 0 {
		httpx.NotFound(c, "")
		return courseRef{}, false
	}
	return rows[0], true
}

// manageCourse resolves the course and requires its owner or an admin.
func (m *Module) manageCourse(c *gin.Context) (courseRef, int64, bool) {
	cr, ok := m.course(c)
	if !ok {
		return cr, 0, false
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return cr, 0, false
	}
	if cr.InstructorID != uid && !hasRole(roles, auth.RoleAdmin) {
		httpx.Forbidden(c, "")
		return cr, 0, false
	}
	return cr, uid, true
}

// enrolled reports whether uid is enrolled in the course.
func (m *Module) enrolled(uid, courseID int64) (bool, error) {
	if uid == 0 {
		return false, nil
	}
	var n int64
	err := m.db.Table("enrollments").Where("user_id = ? AND course_id = ?", uid, courseID).Count(&n).Error
	return n > 0, err
}

// canLearn lets enrolled users, the course owner and admins open lesson content.
func (m *Module) canLearn(c *gin.Context, cr courseRef) (int64, bool) {
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return 0, false
	}
	if cr.InstructorID == uid || hasRole(roles, auth.RoleAdmin) {
		return uid, true
	}
	ok, err := m.enrolled(uid, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return 0, false
	}
	if !ok {
		httpx.Forbidden(c, "You are not enrolled in this course.")
		return 0, false
	}
	return uid, true
}

func idParam(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id < 1 {
		httpx.NotFound(c, "")
		return 0, false
	}
	return id, true
}

// jsonRows runs a query whose single column is a json/jsonb value and returns each as raw JSON.
func (m *Module) jsonRows(sql string, args ...any) ([]json.RawMessage, error) {
	rows, err := m.db.Raw(sql, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(b))
	}
	return out, rows.Err()
}

// jsonOne is jsonRows for a query expected to return at most one row (nil when none).
func (m *Module) jsonOne(sql string, args ...any) (json.RawMessage, error) {
	rows, err := m.jsonRows(sql, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

func pageParams(c *gin.Context, def, max int) (page, per int) {
	p := httpx.ParsePage(c, def, max)
	return p.Page, p.PerPage
}

func lastPage(total int64, per int) int {
	last := int((total + int64(per) - 1) / int64(per))
	if last < 1 {
		last = 1
	}
	return last
}

// legacyPage renders the Laravel-style paginator the catalog endpoints use.
func legacyPage(c *gin.Context, rows []json.RawMessage, total int64, page, per int) {
	c.JSON(http.StatusOK, gin.H{"data": rows, "current_page": page, "last_page": lastPage(total, per), "per_page": per, "total": total})
}

// metaPage renders {data, meta}, the shape used by the studio lists.
func metaPage(c *gin.Context, rows []json.RawMessage, total int64, page, per int) {
	c.JSON(http.StatusOK, gin.H{"data": rows, "meta": gin.H{"page": page, "per_page": per, "total": total, "last_page": lastPage(total, per)}})
}

func raw(c *gin.Context, status int, key string, v json.RawMessage) {
	if v == nil {
		httpx.NotFound(c, "")
		return
	}
	if key == "" {
		c.Data(status, "application/json; charset=utf-8", v)
		return
	}
	c.JSON(status, gin.H{key: v})
}

func likeArg(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(s)) + "%"
}
