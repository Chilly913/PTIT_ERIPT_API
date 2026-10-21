package learning

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Certificates: an instructor switches "issue a certificate" on for a course; a learner who has completed
// every lesson claims it and gets a credential id that anyone can verify at /certificates/:credential.

const defaultCertificateName = "Chứng nhận hoàn thành khóa học"

// defaultCertificateTemplate returns the id of the shared default template, creating it on first use.
func (m *Module) defaultCertificateTemplate() (int64, error) {
	var id int64
	if err := m.db.Raw("SELECT id FROM certificate_templates WHERE name = ? ORDER BY id LIMIT 1", defaultCertificateName).Scan(&id).Error; err != nil {
		return 0, err
	}
	if id != 0 {
		return id, nil
	}
	err := m.db.Raw("INSERT INTO certificate_templates (name) VALUES (?) RETURNING id", defaultCertificateName).Scan(&id).Error
	return id, err
}

func newCredentialID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "ERIPT-" + strings.ToUpper(hex.EncodeToString(b))
}

const certificateJSON = `jsonb_build_object('id', uc.id, 'credential_id', uc.credential_id, 'issued_at', uc.issued_at,
	'course', CASE WHEN c.id IS NULL THEN NULL ELSE jsonb_build_object('id', c.id, 'slug', c.slug, 'title', c.title,
		'instructor', (SELECT i.name FROM users i WHERE i.id = c.instructor_id)) END,
	'user', jsonb_build_object('name', u.name))`

const certificateFrom = ` FROM user_certificates uc JOIN users u ON u.id = uc.user_id LEFT JOIN courses c ON c.id = uc.course_id `

// claimCertificate issues the certificate of a course to the caller once every lesson is completed.
func (m *Module) claimCertificate(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid, _, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if en, err := m.enrolled(uid, cr.ID); err != nil || !en {
		httpx.Forbidden(c, "You are not enrolled in this course.")
		return
	}
	var tpl *int64
	_ = m.db.Raw("SELECT certificate_template_id FROM courses WHERE id = ?", cr.ID).Scan(&tpl).Error
	if tpl == nil {
		httpx.Abort(c, http.StatusUnprocessableEntity, "Khóa học này không cấp chứng chỉ.")
		return
	}
	var prog struct{ Total, Done int64 }
	if err := m.db.Raw(`SELECT count(*) AS total, count(p.completed_at) AS done FROM lessons l
		LEFT JOIN lesson_progress p ON p.lesson_id = l.id AND p.user_id = ? WHERE l.course_id = ?`, uid, cr.ID).Scan(&prog).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if prog.Total == 0 || prog.Done < prog.Total {
		httpx.Abort(c, http.StatusUnprocessableEntity, "Bạn cần hoàn thành tất cả bài học trước khi nhận chứng chỉ.")
		return
	}
	if err := m.db.Exec(`INSERT INTO user_certificates (user_id, course_id, certificate_template_id, credential_id)
		VALUES (?, ?, ?, ?) ON CONFLICT (user_id, course_id) WHERE course_id IS NOT NULL DO NOTHING`, uid, cr.ID, *tpl, newCredentialID()).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+certificateJSON+certificateFrom+" WHERE uc.user_id = ? AND uc.course_id = ?", uid, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "certificate", out)
}

// myCertificates lists the caller's certificates.
func (m *Module) myCertificates(c *gin.Context) {
	uid, _, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows("SELECT "+certificateJSON+certificateFrom+" WHERE uc.user_id = ? ORDER BY uc.issued_at DESC", uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"certificates": rows})
}

// showCertificate is public: it lets anyone verify a credential id.
func (m *Module) showCertificate(c *gin.Context) {
	out, err := m.jsonOne("SELECT "+certificateJSON+certificateFrom+" WHERE uc.credential_id = ?", strings.TrimSpace(c.Param("credential")))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if out == nil {
		httpx.NotFound(c, "")
		return
	}
	raw(c, http.StatusOK, "certificate", out)
}

// siteStats: GET /site-stats (public) — headline figures for the home page; only published courses count.
func (m *Module) siteStats(c *gin.Context) {
	out, err := m.jsonOne(`SELECT jsonb_build_object(
		'courses', (SELECT count(*) FROM courses WHERE status = 'published'),
		'instructors', (SELECT count(DISTINCT instructor_id) FROM courses WHERE status = 'published'),
		'lessons', (SELECT count(*) FROM lessons l JOIN courses c ON c.id = l.course_id WHERE c.status = 'published'),
		'learners', (SELECT count(DISTINCT e.user_id) FROM enrollments e JOIN courses c ON c.id = e.course_id WHERE c.status = 'published'))`)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "", out)
}
