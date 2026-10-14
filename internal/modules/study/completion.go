package study

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"strings"

	"gorm.io/gorm"
)

// onLessonCompleted runs the side effects of UserManagement updateProgress when
// a lesson is completed:
//   - course certificate row (UserCertificate::firstOrCreate) when the course has
//     a certificate template and every lesson is completed;
//   - CareerPathFulfillmentService::refreshProgressForUserCourse (path progress,
//     completion and path certificate row).
//
// Legacy only inserts the user_certificates rows; nothing here renders a PDF or
// sends mail. Future hooks (notifications, certificate rendering, points) belong here.
func (m *Module) onLessonCompleted(ctx context.Context, userID, courseID int64) error {
	if err := m.issueCourseCertificate(ctx, userID, courseID); err != nil {
		return err
	}
	return m.refreshCareerPaths(m.db.WithContext(ctx), userID, courseID)
}

// issueCourseCertificate is the certificate block of updateProgress.
func (m *Module) issueCourseCertificate(ctx context.Context, userID, courseID int64) error {
	db := m.db.WithContext(ctx)
	var tpl []*int64
	if err := db.Raw(`SELECT certificate_template_id FROM courses WHERE id = ?`, courseID).Scan(&tpl).Error; err != nil {
		return fmt.Errorf("study: course template: %w", err)
	}
	if len(tpl) == 0 || tpl[0] == nil {
		return nil
	}
	total, done, err := completionCounts(ctx, m.db, userID, courseID)
	if err != nil {
		return err
	}
	if total == 0 || done < total {
		return nil
	}
	err = db.Exec(`INSERT INTO user_certificates (user_id, course_id, certificate_template_id, credential_id, issued_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, course_id) WHERE course_id IS NOT NULL DO NOTHING`,
		userID, courseID, *tpl[0], "CERT-"+credentialSuffix(), m.now()).Error
	if err != nil {
		return fmt.Errorf("study: issue course certificate: %w", err)
	}
	return nil
}

// refreshCareerPaths is CareerPathFulfillmentService::refreshProgressForUserCourse.
func (m *Module) refreshCareerPaths(db *gorm.DB, userID, courseID int64) error {
	var paths []struct {
		ID                    int64  `gorm:"column:id"`
		UserPathID            int64  `gorm:"column:user_path_id"`
		Status                string `gorm:"column:status"`
		CertificateTemplateID *int64 `gorm:"column:certificate_template_id"`
	}
	err := db.Raw(`
		SELECT p.id, ucp.id AS user_path_id, ucp.status, p.certificate_template_id
		FROM user_career_paths ucp
		JOIN career_paths p ON p.id = ucp.career_path_id
		WHERE ucp.user_id = ?
		  AND EXISTS (SELECT 1 FROM career_path_courses pc
		              WHERE pc.career_path_id = p.id AND pc.course_id = ? AND pc.is_required)
		ORDER BY ucp.id`, userID, courseID).Scan(&paths).Error
	if err != nil {
		return fmt.Errorf("study: career paths: %w", err)
	}
	for _, p := range paths {
		// Required courses of the path and whether each one is completed
		// (isCourseCompleted: certificate exists, or every lesson completed).
		var counts struct {
			Total int64 `gorm:"column:total"`
			Done  int64 `gorm:"column:done"`
		}
		err := db.Raw(`
			SELECT count(*) AS total,
			       count(*) FILTER (WHERE
			           EXISTS (SELECT 1 FROM user_certificates uc WHERE uc.user_id = ? AND uc.course_id = pc.course_id)
			           OR (SELECT count(*) > 0 AND count(*) = count(lp.completed_at)
			               FROM lessons l
			               LEFT JOIN lesson_progress lp ON lp.lesson_id = l.id AND lp.user_id = ?
			               WHERE l.course_id = pc.course_id)
			       ) AS done
			FROM career_path_courses pc
			WHERE pc.career_path_id = ? AND pc.is_required`, userID, userID, p.ID).Scan(&counts).Error
		if err != nil {
			return fmt.Errorf("study: career path progress: %w", err)
		}
		percent := pathPercent(counts.Done, counts.Total)
		if percent >= 100 && p.Status != "completed" {
			if p.CertificateTemplateID != nil {
				err = db.Exec(`INSERT INTO user_certificates
						(user_id, course_id, career_path_id, certificate_template_id, credential_id, issued_at)
					VALUES (?, NULL, ?, ?, ?, ?)
					ON CONFLICT (user_id, career_path_id) WHERE career_path_id IS NOT NULL DO NOTHING`,
					userID, p.ID, *p.CertificateTemplateID, "PATH-"+credentialSuffix(), m.now()).Error
				if err != nil {
					return fmt.Errorf("study: issue path certificate: %w", err)
				}
			}
			err = db.Exec(`UPDATE user_career_paths SET progress_percent = ?, status = 'completed', completed_at = ?
				WHERE id = ?`, percent, m.now(), p.UserPathID).Error
		} else {
			err = db.Exec(`UPDATE user_career_paths SET progress_percent = ? WHERE id = ?`, percent, p.UserPathID).Error
		}
		if err != nil {
			return fmt.Errorf("study: update career path: %w", err)
		}
	}
	return nil
}

// pathPercent is (int) round(done / total * 100); 0 without required courses.
func pathPercent(done, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(math.Round(float64(done) / float64(total) * 100))
}

// credentialSuffix is strtoupper(Str::random(10)).
func credentialSuffix() string {
	return strings.ToUpper(randomAlnum(10))
}

const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randomAlnum is Str::random(n).
func randomAlnum(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(b)
}
