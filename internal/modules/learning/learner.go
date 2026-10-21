package learning

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// ---- enrolment ---------------------------------------------------------------

// enroll self-enrols the caller in a published free course (paid courses need the payment module).
func (m *Module) enroll(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid := auth.UserID(c)
	if cr.Status != "published" {
		httpx.NotFound(c, "")
		return
	}
	if cr.Price > 0 {
		httpx.Abort(c, http.StatusPaymentRequired, "Payment is not available yet for paid courses.")
		return
	}
	res := m.db.Exec(`INSERT INTO enrollments (user_id, course_id, enrollment_source) VALUES (?, ?, 'marketplace') ON CONFLICT (user_id, course_id) DO NOTHING`, uid, cr.ID)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Enrolled.", "already_enrolled": res.RowsAffected == 0})
}

func (m *Module) enrollments(c *gin.Context) {
	uid := auth.UserID(c)
	rows, err := m.jsonRows(`SELECT jsonb_build_object('id', e.id, 'course_id', e.course_id, 'enrollment_source', e.enrollment_source,
		'enrolled_at', e.enrolled_at, 'lessons_count', (SELECT count(*) FROM lessons l WHERE l.course_id = c.id),
		'completed_lessons', (SELECT count(*) FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id WHERE p.user_id = e.user_id AND l.course_id = c.id AND p.completed_at IS NOT NULL),
		'progress', CASE WHEN (SELECT count(*) FROM lessons l WHERE l.course_id = c.id) = 0 THEN 0 ELSE
			round(100.0 * (SELECT count(*) FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id WHERE p.user_id = e.user_id AND l.course_id = c.id AND p.completed_at IS NOT NULL)
			/ (SELECT count(*) FROM lessons l WHERE l.course_id = c.id)) END,
		'certificate_enabled', c.certificate_template_id IS NOT NULL,
		'certificate_credential', (SELECT uc.credential_id FROM user_certificates uc WHERE uc.user_id = e.user_id AND uc.course_id = c.id),
		'window_status', 'open', 'is_planned', false, 'starts_at', NULL, 'ends_at', NULL, 'term', NULL,
		'course', jsonb_build_object('id', c.id, 'title', c.title, 'slug', c.slug, 'thumbnail', c.thumbnail_path, 'price', c.price, 'status', c.status,
			'category', CASE WHEN cat.id IS NULL THEN NULL ELSE jsonb_build_object('id', cat.id, 'name', cat.name) END,
			'instructor', jsonb_build_object('id', u.id, 'name', u.name, 'avatar', u.avatar_path)))
		FROM enrollments e JOIN `+courseFrom+` ON c.id = e.course_id WHERE e.user_id = ? ORDER BY e.enrolled_at DESC`, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (m *Module) dashboard(c *gin.Context) {
	uid := auth.UserID(c)
	courses, err := m.jsonRows(`SELECT jsonb_build_object('id', c.id, 'slug', c.slug,
		'last_activity_at', (SELECT max(p.last_watched_at) FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id WHERE p.user_id = e.user_id AND l.course_id = c.id))
		FROM enrollments e JOIN courses c ON c.id = e.course_id WHERE e.user_id = ? ORDER BY e.enrolled_at DESC`, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	// Continue with the first unfinished lesson of the most recently active course.
	cont, err := m.jsonOne(`SELECT jsonb_build_object('course', jsonb_build_object('id', c.id, 'title', c.title, 'slug', c.slug),
		'lesson_id', nl.id, 'lesson_title', nl.title,
		'progress_percent', CASE WHEN tot.n = 0 THEN 0 ELSE round(100.0 * done.n / tot.n) END)
		FROM enrollments e JOIN courses c ON c.id = e.course_id
		JOIN LATERAL (SELECT count(*) AS n FROM lessons l WHERE l.course_id = c.id) tot ON true
		JOIN LATERAL (SELECT count(*) AS n FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id WHERE p.user_id = e.user_id AND l.course_id = c.id AND p.completed_at IS NOT NULL) done ON true
		JOIN LATERAL (SELECT l.id, l.title FROM lessons l WHERE l.course_id = c.id
			AND NOT EXISTS (SELECT 1 FROM lesson_progress p WHERE p.user_id = e.user_id AND p.lesson_id = l.id AND p.completed_at IS NOT NULL)
			ORDER BY l.position, l.id LIMIT 1) nl ON true
		WHERE e.user_id = ? AND c.status = 'published'
		ORDER BY coalesce((SELECT max(p.last_watched_at) FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id WHERE p.user_id = e.user_id AND l.course_id = c.id), e.enrolled_at) DESC LIMIT 1`, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	exams, err := m.jsonRows(`SELECT jsonb_build_object('id', e.id, 'title', e.title, 'starts_at', e.starts_at,
		'window', CASE WHEN e.starts_at IS NOT NULL AND e.starts_at > now() THEN 'upcoming' WHEN e.ends_at IS NOT NULL AND e.ends_at < now() THEN 'ended' ELSE 'ongoing' END,
		'course', (SELECT jsonb_build_object('title', c.title) FROM courses c WHERE c.id = e.course_id))
		FROM exams e WHERE e.status IN ('scheduled', 'active') AND (e.ends_at IS NULL OR e.ends_at > now()) AND (
			(e.type = 'standalone' AND EXISTS (SELECT 1 FROM exam_enrollments x WHERE x.exam_id = e.id AND x.user_id = ?)) OR
			(e.type = 'course_final' AND EXISTS (SELECT 1 FROM enrollments en WHERE en.course_id = e.course_id AND en.user_id = ?)))
		ORDER BY e.starts_at NULLS FIRST, e.id LIMIT 8`, uid, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	// Assignments of enrolled courses that the learner has not submitted yet; overdue ones stay listed.
	pending, err := m.jsonRows(`SELECT jsonb_build_object('assignment_id', a.id, 'lesson_id', l.id, 'lesson_title', l.title, 'due_at', a.due_at,
		'overdue', a.due_at IS NOT NULL AND a.due_at < now(),
		'course', jsonb_build_object('id', c.id, 'slug', c.slug, 'title', c.title))
		FROM lesson_assignments a
		JOIN lessons l ON l.id = a.lesson_id
		JOIN courses c ON c.id = l.course_id
		JOIN enrollments en ON en.course_id = c.id AND en.user_id = ?
		WHERE (a.available_from IS NULL OR a.available_from <= now())
			AND NOT EXISTS (SELECT 1 FROM assignment_submissions s WHERE s.lesson_assignment_id = a.id AND s.user_id = ?)
		ORDER BY a.due_at NULLS LAST, a.id LIMIT 8`, uid, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	certs, err := m.jsonRows("SELECT "+certificateJSON+certificateFrom+" WHERE uc.user_id = ? ORDER BY uc.issued_at DESC LIMIT 5", uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var cl any
	if cont != nil {
		cl = cont
	}
	c.JSON(http.StatusOK, gin.H{"courses": courses, "continue_learning": cl, "upcoming_exams": exams, "pending_assignments": pending, "recent_certificates": certs})
}

func (m *Module) pointsSummary(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"balance": 0, "streak_days": 0})
}

// ---- progress ------------------------------------------------------------------

func (m *Module) courseProgress(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok {
		return
	}
	lessons, err := m.jsonRows(`SELECT jsonb_build_object('id', l.id, 'title', l.title, 'order', l.position, 'duration', l.duration_seconds,
		'completed', coalesce(p.completed_at IS NOT NULL, false), 'watched_seconds', coalesce(p.watched_seconds, 0))
		FROM lessons l LEFT JOIN lesson_progress p ON p.lesson_id = l.id AND p.user_id = ?
		WHERE l.course_id = ? ORDER BY l.position, l.id`, uid, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	reasons, _, err := m.lockReasons(cr.ID, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	bypass, err := m.bypassGate(c, cr)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	total, done := len(lessons), 0
	for i, rawLesson := range lessons {
		var l struct {
			ID        int64 `json:"id"`
			Completed bool  `json:"completed"`
		}
		_ = json.Unmarshal(rawLesson, &l)
		if l.Completed {
			done++
		}
		var obj map[string]any
		_ = json.Unmarshal(rawLesson, &obj)
		reason := reasons[l.ID]
		if bypass {
			reason = ""
		}
		obj["locked"], obj["locked_reason"] = reason != "", reason
		if b, err := json.Marshal(obj); err == nil {
			lessons[i] = b
		}
	}
	pct := 0.0
	if total > 0 {
		pct = float64(done*1000/total) / 10
	}
	c.JSON(http.StatusOK, gin.H{"total_lessons": total, "completed_lessons": done, "percent": pct, "lessons": lessons})
}

func (m *Module) lessonDetail(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	lid, ok := idParam(c, "lid")
	if !ok {
		return
	}
	var preview bool
	res := m.db.Raw("SELECT is_preview FROM lessons WHERE id = ? AND course_id = ?", lid, cr.ID).Scan(&preview)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	if !preview {
		uid, ok := m.canLearn(c, cr)
		if !ok || !m.gate(c, cr, uid, lid) {
			return
		}
	} else if uid := auth.UserID(c); uid != 0 {
		// Free previews stay open to everyone, but an enrolled learner follows the course schedule like any other lesson.
		if isEnrolled, err := m.enrolled(uid, cr.ID); err != nil {
			httpx.Fail(c, err)
			return
		} else if isEnrolled && !m.gate(c, cr, uid, lid) {
			return
		}
	}
	out, err := m.lessonByID(cr.ID, lid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "", out)
}

func (m *Module) lessonProgress(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok {
		return
	}
	lid, ok := idParam(c, "lid")
	if !ok {
		return
	}
	out, err := m.progressJSON(uid, lid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "", out)
}

func (m *Module) progressJSON(uid, lid int64) (json.RawMessage, error) {
	return m.jsonOne(`SELECT jsonb_build_object('lesson_id', ?::bigint, 'progress_percent', coalesce(p.progress_percent, 0),
		'last_position', coalesce(p.last_position, 0), 'completed', coalesce(p.completed_at IS NOT NULL, false),
		'watched_seconds', coalesce(p.watched_seconds, 0))
		FROM (SELECT 1) one LEFT JOIN lesson_progress p ON p.user_id = ? AND p.lesson_id = ?`, lid, uid, lid)
}

func (m *Module) saveLessonProgress(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok {
		return
	}
	lid, ok := idParam(c, "lid")
	if !ok {
		return
	}
	var n int64
	if err := m.db.Table("lessons").Where("id = ? AND course_id = ?", lid, cr.ID).Count(&n).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if n == 0 {
		httpx.NotFound(c, "")
		return
	}
	if !m.gate(c, cr, uid, lid) {
		return
	}
	var b struct {
		WatchedSeconds *int  `json:"watched_seconds"`
		LastPosition   *int  `json:"last_position"`
		Completed      *bool `json:"completed"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	watched, pos := 0, 0
	hasPos := b.LastPosition != nil || b.WatchedSeconds != nil
	if b.WatchedSeconds != nil && *b.WatchedSeconds > 0 {
		watched = *b.WatchedSeconds
	}
	if b.LastPosition != nil && *b.LastPosition > 0 {
		pos = *b.LastPosition
	} else {
		pos = watched
	}
	completed := b.Completed != nil && *b.Completed
	err := m.db.Exec(`INSERT INTO lesson_progress (user_id, lesson_id, progress_percent, last_position, watched_seconds, completed_at, last_watched_at)
		VALUES (?, ?, ?, ?, ?, CASE WHEN ? THEN now() END, now())
		ON CONFLICT (user_id, lesson_id) DO UPDATE SET
			watched_seconds = greatest(lesson_progress.watched_seconds, EXCLUDED.watched_seconds),
			last_position = CASE WHEN ? THEN EXCLUDED.last_position ELSE lesson_progress.last_position END,
			progress_percent = CASE WHEN ? THEN 100 WHEN ?::boolean IS FALSE AND ? THEN 0 ELSE lesson_progress.progress_percent END,
			completed_at = CASE WHEN ? THEN coalesce(lesson_progress.completed_at, now()) WHEN ?::boolean IS NOT NULL THEN NULL ELSE lesson_progress.completed_at END,
			last_watched_at = now()`,
		uid, lid, map[bool]int{true: 100, false: 0}[completed], pos, watched, completed,
		hasPos, completed, b.Completed, b.Completed != nil, completed, b.Completed).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.progressJSON(uid, lid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "", out)
}

// ---- notes ---------------------------------------------------------------------

const noteJSON = `jsonb_build_object('id', n.id, 'lesson_id', n.lesson_id, 'content', n.content, 'position_seconds', n.position_seconds,
  'created_at', n.created_at, 'updated_at', n.updated_at)`

func (m *Module) noteContext(c *gin.Context) (uid, lid int64, ok bool) {
	cr, ok := m.course(c)
	if !ok {
		return 0, 0, false
	}
	uid, ok = m.canLearn(c, cr)
	if !ok {
		return 0, 0, false
	}
	lid, ok = idParam(c, "lid")
	if !ok {
		return 0, 0, false
	}
	var n int64
	if err := m.db.Table("lessons").Where("id = ? AND course_id = ?", lid, cr.ID).Count(&n).Error; err != nil {
		httpx.Fail(c, err)
		return 0, 0, false
	}
	if n == 0 {
		httpx.NotFound(c, "")
		return 0, 0, false
	}
	return uid, lid, true
}

func (m *Module) listNotes(c *gin.Context) {
	uid, lid, ok := m.noteContext(c)
	if !ok {
		return
	}
	rows, err := m.jsonRows("SELECT "+noteJSON+" FROM lesson_notes n WHERE n.user_id = ? AND n.lesson_id = ? ORDER BY n.position_seconds, n.id", uid, lid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (m *Module) createNote(c *gin.Context) {
	uid, lid, ok := m.noteContext(c)
	if !ok {
		return
	}
	var b struct {
		Content         string `json:"content" binding:"required,max=5000"`
		PositionSeconds int    `json:"position_seconds" binding:"min=0"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	var id int64
	if err := m.db.Raw("INSERT INTO lesson_notes (user_id, lesson_id, content, position_seconds) VALUES (?, ?, ?, ?) RETURNING id", uid, lid, strings.TrimSpace(b.Content), b.PositionSeconds).Scan(&id).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+noteJSON+" FROM lesson_notes n WHERE n.id = ?", id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "", out)
}

func (m *Module) updateNote(c *gin.Context) {
	uid, _, ok := m.noteContext(c)
	if !ok {
		return
	}
	nid, ok := idParam(c, "nid")
	if !ok {
		return
	}
	var b struct {
		Content string `json:"content" binding:"required,max=5000"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	res := m.db.Exec("UPDATE lesson_notes SET content = ? WHERE id = ? AND user_id = ?", strings.TrimSpace(b.Content), nid, uid)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	out, err := m.jsonOne("SELECT "+noteJSON+" FROM lesson_notes n WHERE n.id = ?", nid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "", out)
}

func (m *Module) deleteNote(c *gin.Context) {
	uid, _, ok := m.noteContext(c)
	if !ok {
		return
	}
	nid, ok := idParam(c, "nid")
	if !ok {
		return
	}
	res := m.db.Exec("DELETE FROM lesson_notes WHERE id = ? AND user_id = ?", nid, uid)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

// ---- reviews -------------------------------------------------------------------

const reviewJSON = `jsonb_build_object('id', r.id, 'user_id', r.user_id, 'course_id', r.course_id, 'rating', r.rating, 'comment', r.comment,
  'created_at', r.created_at, 'updated_at', r.updated_at, 'user', jsonb_build_object('id', ru.id, 'name', ru.name, 'avatar', ru.avatar_path))`

func (m *Module) listReviews(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	page, per := pageParams(c, 10, 50)
	var total int64
	if err := m.db.Table("reviews").Where("course_id = ?", cr.ID).Count(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows("SELECT "+reviewJSON+" FROM reviews r JOIN users ru ON ru.id = r.user_id WHERE r.course_id = ? ORDER BY r.created_at DESC LIMIT ? OFFSET ?", cr.ID, per, (page-1)*per)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	legacyPage(c, rows, total, page, per)
}

type reviewBody struct {
	Rating  int     `json:"rating" binding:"required,min=1,max=5"`
	Comment *string `json:"comment" binding:"omitempty,max=2000"`
}

func (m *Module) createReview(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	isEnrolled, err := m.enrolled(uid, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !isEnrolled && !hasRole(roles, auth.RoleAdmin) {
		httpx.Forbidden(c, "Only enrolled students can review this course.")
		return
	}
	var b reviewBody
	if !httpx.BindJSON(c, &b) {
		return
	}
	var id int64
	res := m.db.Raw(`INSERT INTO reviews (user_id, course_id, rating, comment) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, course_id) DO NOTHING RETURNING id`, uid, cr.ID, b.Rating, nullStr(b.Comment)).Scan(&id)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if id == 0 {
		httpx.Abort(c, http.StatusConflict, "You have already reviewed this course.")
		return
	}
	out, err := m.jsonOne("SELECT "+reviewJSON+" FROM reviews r JOIN users ru ON ru.id = r.user_id WHERE r.id = ?", id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "review", out)
}

func (m *Module) ownReview(c *gin.Context) (id int64, ok bool) {
	cr, ok := m.course(c)
	if !ok {
		return 0, false
	}
	rid, ok := idParam(c, "rid")
	if !ok {
		return 0, false
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return 0, false
	}
	var owner int64
	res := m.db.Raw("SELECT user_id FROM reviews WHERE id = ? AND course_id = ?", rid, cr.ID).Scan(&owner)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return 0, false
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return 0, false
	}
	if owner != uid && !hasRole(roles, auth.RoleAdmin) {
		httpx.Forbidden(c, "")
		return 0, false
	}
	return rid, true
}

func (m *Module) updateReview(c *gin.Context) {
	rid, ok := m.ownReview(c)
	if !ok {
		return
	}
	var b reviewBody
	if !httpx.BindJSON(c, &b) {
		return
	}
	if err := m.db.Exec("UPDATE reviews SET rating = ?, comment = ? WHERE id = ?", b.Rating, nullStr(b.Comment), rid).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+reviewJSON+" FROM reviews r JOIN users ru ON ru.id = r.user_id WHERE r.id = ?", rid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "review", out)
}

func (m *Module) deleteReview(c *gin.Context) {
	rid, ok := m.ownReview(c)
	if !ok {
		return
	}
	if err := m.db.Exec("DELETE FROM reviews WHERE id = ?", rid).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

// ---- Q&A -----------------------------------------------------------------------

// qaJSON builds a question with reactions and replies for the caller (? = caller id, used three times in order).
const reactionCounts = `'like_count', (SELECT count(*) FROM qa_reactions x WHERE x.%[1]s = %[2]s AND x.kind = 'like'),
  'dislike_count', (SELECT count(*) FROM qa_reactions x WHERE x.%[1]s = %[2]s AND x.kind = 'dislike'),
  'my_reaction', (SELECT x.kind FROM qa_reactions x WHERE x.%[1]s = %[2]s AND x.user_id = ?)`

func qaJSON() string {
	reply := `jsonb_build_object('id', rp.id, 'course_qa_id', rp.course_qa_id, 'user_id', rp.user_id, 'content', rp.content, 'created_at', rp.created_at,
  'user', jsonb_build_object('id', ru.id, 'name', ru.name, 'avatar', ru.avatar_path), ` + fmt.Sprintf(reactionCounts, "course_qa_reply_id", "rp.id") + `)`
	return `jsonb_build_object('id', q.id, 'user_id', q.user_id, 'course_id', q.course_id, 'lesson_id', q.lesson_id, 'subject', q.subject,
  'content', q.content, 'created_at', q.created_at,
  'user', jsonb_build_object('id', qu.id, 'name', qu.name, 'avatar', qu.avatar_path), ` + fmt.Sprintf(reactionCounts, "course_qa_id", "q.id") + `,
  'replies', (SELECT coalesce(jsonb_agg(` + reply + ` ORDER BY rp.created_at, rp.id), '[]'::jsonb)
    FROM course_qa_replies rp JOIN users ru ON ru.id = rp.user_id WHERE rp.course_qa_id = q.id))`
}

func (m *Module) listQAs(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid := auth.UserID(c)
	page, per := pageParams(c, 10, 50)
	conds, args := []string{"q.course_id = ?"}, []any{cr.ID}
	if lid := strings.TrimSpace(c.Query("lesson_id")); lid != "" {
		conds = append(conds, "q.lesson_id = ?")
		args = append(args, lid)
	}
	where := " WHERE " + strings.Join(conds, " AND ")
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM course_qas q"+where, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	// The caller id appears once per reaction sub-select: question (1) + reply (1).
	qargs := append([]any{uid, uid}, args...)
	qargs = append(qargs, per, (page-1)*per)
	rows, err := m.jsonRows("SELECT "+qaJSON()+" FROM course_qas q JOIN users qu ON qu.id = q.user_id"+where+" ORDER BY q.created_at DESC, q.id DESC LIMIT ? OFFSET ?", qargs...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	legacyPage(c, rows, total, page, per)
}

func (m *Module) qaByID(id, uid int64) (json.RawMessage, error) {
	return m.jsonOne("SELECT "+qaJSON()+" FROM course_qas q JOIN users qu ON qu.id = q.user_id WHERE q.id = ?", uid, uid, id)
}

func (m *Module) createQA(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok {
		return
	}
	var b struct {
		LessonID *int64 `json:"lesson_id"`
		Subject  string `json:"subject" binding:"required,max=255"`
		Content  string `json:"content" binding:"required,max=5000"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	var id int64
	if err := m.db.Raw(`INSERT INTO course_qas (user_id, course_id, lesson_id, subject, content)
		VALUES (?, ?, (SELECT id FROM lessons WHERE id = ? AND course_id = ?), ?, ?) RETURNING id`,
		uid, cr.ID, b.LessonID, cr.ID, strings.TrimSpace(b.Subject), strings.TrimSpace(b.Content)).Scan(&id).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.qaByID(id, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "", out)
}

func (m *Module) createReply(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok {
		return
	}
	qid, ok := idParam(c, "qid")
	if !ok {
		return
	}
	var b struct {
		Content string `json:"content" binding:"required,max=5000"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	var n int64
	if err := m.db.Table("course_qas").Where("id = ? AND course_id = ?", qid, cr.ID).Count(&n).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if n == 0 {
		httpx.NotFound(c, "")
		return
	}
	var id int64
	if err := m.db.Raw("INSERT INTO course_qa_replies (course_qa_id, user_id, content) VALUES (?, ?, ?) RETURNING id", qid, uid, strings.TrimSpace(b.Content)).Scan(&id).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne(`SELECT jsonb_build_object('id', rp.id, 'course_qa_id', rp.course_qa_id, 'user_id', rp.user_id, 'content', rp.content, 'created_at', rp.created_at,
		'user', jsonb_build_object('id', ru.id, 'name', ru.name, 'avatar', ru.avatar_path), 'like_count', 0, 'dislike_count', 0, 'my_reaction', NULL)
		FROM course_qa_replies rp JOIN users ru ON ru.id = rp.user_id WHERE rp.id = ?`, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "", out)
}

// reactQA toggles a like/dislike on a question or reply and returns the fresh counts.
func (m *Module) reactQA(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok {
		return
	}
	var b struct {
		Type string `json:"reactable_type" binding:"required,oneof=qa reply"`
		ID   int64  `json:"reactable_id" binding:"required,min=1"`
		Kind string `json:"kind" binding:"required,oneof=like dislike"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	col, table := "course_qa_id", "course_qas"
	check := "id = ? AND course_id = ?"
	if b.Type == "reply" {
		col, table = "course_qa_reply_id", "course_qa_replies"
		check = "id = ? AND course_qa_id IN (SELECT id FROM course_qas WHERE course_id = ?)"
	}
	var n int64
	if err := m.db.Table(table).Where(check, b.ID, cr.ID).Count(&n).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if n == 0 {
		httpx.NotFound(c, "")
		return
	}
	var existing string
	_ = m.db.Raw("SELECT kind FROM qa_reactions WHERE user_id = ? AND "+col+" = ?", uid, b.ID).Scan(&existing).Error
	switch {
	case existing == b.Kind:
		_ = m.db.Exec("DELETE FROM qa_reactions WHERE user_id = ? AND "+col+" = ?", uid, b.ID).Error
	case existing != "":
		_ = m.db.Exec("UPDATE qa_reactions SET kind = ? WHERE user_id = ? AND "+col+" = ?", b.Kind, uid, b.ID).Error
	default:
		if err := m.db.Exec("INSERT INTO qa_reactions (user_id, "+col+", kind) VALUES (?, ?, ?)", uid, b.ID, b.Kind).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	var out struct {
		Like, Dislike int64
		My            *string
	}
	if err := m.db.Raw(`SELECT count(*) FILTER (WHERE kind = 'like') AS "like", count(*) FILTER (WHERE kind = 'dislike') AS dislike,
		(SELECT kind FROM qa_reactions WHERE user_id = ? AND `+col+` = ?) AS my FROM qa_reactions WHERE `+col+` = ?`, uid, b.ID, b.ID).Scan(&out).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"like_count": out.Like, "dislike_count": out.Dislike, "my_reaction": out.My})
}
