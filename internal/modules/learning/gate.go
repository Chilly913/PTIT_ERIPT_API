package learning

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Availability rules for learners: the course window, the section window, and (for sequential courses)
// the order in which lessons unlock. Course owners and admins are never locked out.

func fmtWhen(t time.Time) string { return t.Local().Format("15:04 02/01/2006") }

type gateLesson struct {
	ID          int64
	SecOpensAt  *time.Time
	SecClosesAt *time.Time
	Done        bool
}

// lockReasons returns, per lesson id, why the learner cannot open it yet ("" = open).
func (m *Module) lockReasons(courseID, uid int64) (map[int64]string, []int64, error) {
	var course struct {
		OpensAt      *time.Time
		ClosesAt     *time.Time
		LearningMode string
	}
	if err := m.db.Raw("SELECT opens_at, closes_at, learning_mode FROM courses WHERE id = ?", courseID).Scan(&course).Error; err != nil {
		return nil, nil, err
	}
	var lessons []gateLesson
	err := m.db.Raw(`SELECT l.id, s.opens_at AS sec_opens_at, s.closes_at AS sec_closes_at,
			EXISTS (SELECT 1 FROM lesson_progress p WHERE p.user_id = ? AND p.lesson_id = l.id AND p.completed_at IS NOT NULL) AS done
		FROM lessons l LEFT JOIN sections s ON s.id = l.section_id
		WHERE l.course_id = ? ORDER BY coalesce(s.position, 2147483647), l.position, l.id`, uid, courseID).Scan(&lessons).Error
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	courseReason := ""
	switch {
	case course.OpensAt != nil && course.OpensAt.After(now):
		courseReason = "Khóa học sẽ mở lúc " + fmtWhen(*course.OpensAt)
	case course.ClosesAt != nil && course.ClosesAt.Before(now):
		courseReason = "Khóa học đã đóng từ " + fmtWhen(*course.ClosesAt)
	}
	out := make(map[int64]string, len(lessons))
	order := make([]int64, 0, len(lessons))
	prevDone := true
	for _, l := range lessons {
		order = append(order, l.ID)
		reason := courseReason
		if reason == "" {
			switch {
			case l.SecOpensAt != nil && l.SecOpensAt.After(now):
				reason = "Chương này sẽ mở lúc " + fmtWhen(*l.SecOpensAt)
			case l.SecClosesAt != nil && l.SecClosesAt.Before(now):
				reason = "Chương này đã đóng từ " + fmtWhen(*l.SecClosesAt)
			}
		}
		if reason == "" && course.LearningMode == "sequential" && !prevDone {
			reason = "Hãy hoàn thành bài học trước để mở bài này"
		}
		out[l.ID] = reason
		prevDone = prevDone && l.Done
	}
	return out, order, nil
}

// bypassGate reports whether the caller may open any lesson regardless of schedule.
func (m *Module) bypassGate(c *gin.Context, cr courseRef) (bool, error) {
	uid, roles, err := m.actor(c)
	if err != nil {
		return false, err
	}
	return cr.InstructorID == uid || hasRole(roles, auth.RoleAdmin), nil
}

// gate answers 403 with the reason when the learner may not open the lesson yet.
func (m *Module) gate(c *gin.Context, cr courseRef, uid, lessonID int64) bool {
	bypass, err := m.bypassGate(c, cr)
	if err != nil {
		httpx.Fail(c, err)
		return false
	}
	if bypass {
		return true
	}
	reasons, _, err := m.lockReasons(cr.ID, uid)
	if err != nil {
		httpx.Fail(c, err)
		return false
	}
	if r := reasons[lessonID]; r != "" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": r, "locked": true})
		return false
	}
	return true
}
