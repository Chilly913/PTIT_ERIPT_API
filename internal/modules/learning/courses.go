package learning

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/textutil"
)

// courseJSON builds the course object shared by the catalog, the studio and the admin queue
// (a superset of the web app's CourseSummary, Course and CourseDetail head fields).
// The query must alias courses as c, the instructor as u and the category as cat.
const courseJSON = `jsonb_build_object(
  'id', c.id, 'user_id', c.instructor_id, 'category_id', c.category_id, 'title', c.title, 'slug', c.slug,
  'description', c.description, 'learning_outcomes', c.learning_outcomes, 'benefits', c.benefits,
  'requirements', c.requirements, 'level', c.level, 'trailer_url', c.trailer_url, 'price', c.price,
  'status', c.status, 'is_featured', c.is_featured, 'reject_reason', c.reject_reason, 'thumbnail', c.thumbnail_path,
  'published_at', c.published_at, 'created_at', c.created_at, 'updated_at', c.updated_at,
  'course_mode', c.course_mode, 'certificate_template_id', c.certificate_template_id,
  'opens_at', c.opens_at, 'closes_at', c.closes_at, 'learning_mode', c.learning_mode,
  'lessons_count', (SELECT count(*) FROM lessons l WHERE l.course_id = c.id),
  'enrollments_count', (SELECT count(*) FROM enrollments e WHERE e.course_id = c.id),
  'reviews_count', (SELECT count(*) FROM reviews r WHERE r.course_id = c.id),
  'reviews_avg_rating', (SELECT round(avg(r.rating)::numeric, 2) FROM reviews r WHERE r.course_id = c.id),
  'avg_rating', coalesce((SELECT round(avg(r.rating)::numeric, 2) FROM reviews r WHERE r.course_id = c.id), 0),
  'instructor', jsonb_build_object('id', u.id, 'name', u.name, 'avatar', u.avatar_path, 'email', u.email),
  'category', CASE WHEN cat.id IS NULL THEN NULL ELSE jsonb_build_object('id', cat.id, 'name', cat.name, 'slug', cat.slug) END
)`

const courseFrom = `courses c JOIN users u ON u.id = c.instructor_id LEFT JOIN categories cat ON cat.id = c.category_id`

// lessonOutline is the lesson summary shown in curricula.
const lessonOutline = `jsonb_build_object('id', l.id, 'section_id', l.section_id, 'course_id', l.course_id, 'title', l.title,
  'type', l.type, 'duration', l.duration_seconds, 'is_preview', l.is_preview, 'order', l.position)`

// lessonFull is the studio / learner lesson object (alias l). Content fields are included.
const lessonFull = `jsonb_build_object('id', l.id, 'course_id', l.course_id, 'section_id', l.section_id, 'title', l.title,
  'type', l.type, 'description', l.description, 'video_url', l.video_path, 'duration', l.duration_seconds,
  'order', l.position, 'is_preview', l.is_preview, 'video_status', l.video_status, 'video_size', l.video_size_bytes::text,
  'attachments', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', a.id, 'lesson_id', a.lesson_id, 'original_name', a.original_name,
      'file_path', a.file_path, 'file_size', a.size_bytes::text, 'mime_type', a.mime_type, 'url', a.file_path) ORDER BY a.id), '[]'::jsonb)
    FROM lesson_attachments a WHERE a.lesson_id = l.id),
  'virtual_class', (SELECT jsonb_build_object('id', v.id, 'lesson_id', v.lesson_id, 'provider', v.provider, 'meeting_id', v.meeting_id,
      'meeting_password', v.meeting_password, 'join_url', v.join_url, 'start_url', v.start_url, 'start_at', v.start_at,
      'duration', v.duration_minutes) FROM virtual_classes v WHERE v.lesson_id = l.id),
  'scorm_package', (SELECT jsonb_build_object('id', p.id, 'uuid', p.uuid, 'version', p.version,
      'entry_url', '/bff/scorm/' || p.uuid::text || '/' || p.entry_path, 'title', p.title)
      FROM scorm_packages p WHERE p.lesson_id = l.id),
  'assignment', (SELECT jsonb_build_object('id', a.id, 'lesson_id', a.lesson_id, 'instructions', a.instructions,
      'max_file_size', a.max_file_size_kb, 'allowed_extensions', array_to_string(a.allowed_extensions, ','),
      'available_from', a.available_from, 'submission_open_at', a.submission_open_at, 'due_at', a.due_at)
      FROM lesson_assignments a WHERE a.lesson_id = l.id), 'offline_session', NULL, 'quiz', (SELECT jsonb_build_object('id', z.id, 'title', z.title, 'description', z.description, 'time_limit', z.time_limit, 'pass_score', z.pass_score) FROM quizzes z WHERE z.lesson_id = l.id))`

var thumbRe = regexp.MustCompile(`^(https?://\S+|/uploads/[\w./-]+)$`)

// ---- catalog ----------------------------------------------------------------

func (m *Module) categories(c *gin.Context) {
	rows, err := m.jsonRows(`SELECT jsonb_build_object('id', cat.id, 'parent_id', cat.parent_id, 'name', cat.name, 'slug', cat.slug,
		'icon', cat.icon, 'sort_order', cat.sort_order,
		'courses_count', (SELECT count(*) FROM courses c WHERE c.category_id = cat.id AND c.status = 'published'))
		FROM categories cat ORDER BY cat.parent_id NULLS FIRST, cat.sort_order, cat.name`)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, rows)
}

// courseFilters turns the shared list query parameters into SQL conditions.
func courseFilters(c *gin.Context, conds []string, args []any) ([]string, []any) {
	if s := strings.TrimSpace(c.Query("search")); s != "" {
		conds = append(conds, "(learning_search_fold(c.title) ILIKE learning_search_fold(?) OR learning_search_fold(c.description) ILIKE learning_search_fold(?))")
		args = append(args, likeArg(s), likeArg(s))
	}
	if slug := strings.TrimSpace(c.Query("category")); slug != "" {
		conds = append(conds, "c.category_id IN (SELECT id FROM categories WHERE slug = ? OR parent_id = (SELECT id FROM categories WHERE slug = ?))")
		args = append(args, slug, slug)
	}
	if lv := c.Query("level"); lv == "beginner" || lv == "intermediate" || lv == "advanced" {
		conds = append(conds, "c.level = ?")
		args = append(args, lv)
	}
	switch c.Query("pricing") {
	case "free":
		conds = append(conds, "c.price = 0")
	case "paid":
		conds = append(conds, "c.price > 0")
	}
	if c.Query("featured") == "1" {
		conds = append(conds, "c.is_featured")
	}
	if id, err := strconv.ParseInt(c.Query("instructor_id"), 10, 64); err == nil && id > 0 {
		conds = append(conds, "c.instructor_id = ?")
		args = append(args, id)
	}
	return conds, args
}

const (
	enrollCount = "(SELECT count(*) FROM enrollments e WHERE e.course_id = c.id)"
	avgRating   = "coalesce((SELECT avg(r.rating) FROM reviews r WHERE r.course_id = c.id), 0)"
)

var courseSorts = map[string]string{
	"newest":     "coalesce(c.published_at, c.created_at) DESC, c.id DESC",
	"title":      "c.title ASC, c.id DESC",
	"popular":    enrollCount + " DESC, c.id DESC",
	"learners":   enrollCount + " DESC, c.id DESC",
	"rating":     avgRating + " DESC, c.id DESC",
	"price_asc":  "c.price ASC, c.id DESC",
	"price_desc": "c.price DESC, c.id DESC",
}

func courseOrder(c *gin.Context) string {
	if o, ok := courseSorts[c.Query("sort")]; ok {
		return o
	}
	return courseSorts["newest"]
}

func (m *Module) listCourses(c *gin.Context) {
	page, per := pageParams(c, 12, 50)
	conds, args := courseFilters(c, []string{"c.status = 'published'"}, nil)
	where := " WHERE " + strings.Join(conds, " AND ")
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM "+courseFrom+where, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows("SELECT "+courseJSON+" FROM "+courseFrom+where+" ORDER BY "+courseOrder(c)+" LIMIT ? OFFSET ?", append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	legacyPage(c, rows, total, page, per)
}

func (m *Module) showCourse(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	manage := cr.InstructorID == uid || hasRole(roles, auth.RoleAdmin)
	if cr.Status != "published" && !manage {
		httpx.NotFound(c, "")
		return
	}
	isEnrolled, err := m.enrolled(uid, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	access := manage || isEnrolled
	var reviewed int64
	if uid != 0 {
		_ = m.db.Table("reviews").Where("user_id = ? AND course_id = ?", uid, cr.ID).Count(&reviewed).Error
	}
	sql := `SELECT ` + courseJSON + ` || jsonb_build_object(
		'sections', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', s.id, 'course_id', s.course_id, 'title', s.title,
			'description', s.description, 'position', s.position,
			'lessons', (SELECT coalesce(jsonb_agg(` + lessonOutline + ` ORDER BY l.position, l.id), '[]'::jsonb) FROM lessons l WHERE l.section_id = s.id))
			ORDER BY s.position, s.id), '[]'::jsonb) FROM sections s WHERE s.course_id = c.id),
		'lessons', (SELECT coalesce(jsonb_agg(` + lessonOutline + ` || jsonb_build_object('video_url', CASE WHEN l.is_preview OR ?::boolean THEN l.video_path END)
			ORDER BY l.position, l.id), '[]'::jsonb) FROM lessons l WHERE l.course_id = c.id),
		'is_enrolled', ?::boolean, 'has_reviewed', ?::boolean,
		'latest_reviews', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', r.id, 'user_id', r.user_id, 'course_id', r.course_id,
			'rating', r.rating, 'comment', r.comment, 'created_at', r.created_at, 'updated_at', r.updated_at,
			'user', jsonb_build_object('id', ru.id, 'name', ru.name, 'avatar', ru.avatar_path)) ORDER BY r.created_at DESC), '[]'::jsonb)
			FROM (SELECT * FROM reviews WHERE course_id = c.id ORDER BY created_at DESC LIMIT 5) r JOIN users ru ON ru.id = r.user_id))
		FROM ` + courseFrom + ` WHERE c.id = ?`
	out, err := m.jsonOne(sql, access, isEnrolled, reviewed > 0, cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "", out)
}

// ---- studio -----------------------------------------------------------------

func (m *Module) myCourses(c *gin.Context) {
	uid, _, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	page, per := pageParams(c, 12, 100)
	conds, args := courseFilters(c, []string{"c.instructor_id = ?"}, []any{uid})
	if st := c.Query("status"); st != "" {
		conds = append(conds, "c.status = ?")
		args = append(args, st)
	}
	where := " WHERE " + strings.Join(conds, " AND ")
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM "+courseFrom+where, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows("SELECT "+courseJSON+" FROM "+courseFrom+where+" ORDER BY "+courseOrder(c)+" LIMIT ? OFFSET ?", append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, total, page, per)
}

type courseBody struct {
	Title            *string   `json:"title"`
	Description      *string   `json:"description"`
	CategoryID       *int64    `json:"category_id"`
	Level            *string   `json:"level"`
	Price            *float64  `json:"price"`
	Thumbnail        *string   `json:"thumbnail"`
	TrailerURL       *string   `json:"trailer_url"`
	LearningOutcomes *[]string `json:"learning_outcomes"`
	Requirements     *[]string `json:"requirements"`
	Benefits         *[]string `json:"benefits"`
	Status           *string   `json:"status"`
	OpensAt          *string   `json:"opens_at"`
	ClosesAt         *string   `json:"closes_at"`
	LearningMode     *string   `json:"learning_mode"`
	// IssueCertificate switches the completion certificate of the course on (default template) or off.
	IssueCertificate *bool `json:"issue_certificate"`
}

// present tells which keys the request actually carried (so PUT can be partial and null can clear).
func present(c *gin.Context, body []byte) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(body, &m)
	return m
}

func cleanList(in *[]string) []string {
	out := []string{}
	if in == nil {
		return out
	}
	for _, s := range *in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func validateCourse(b courseBody, keys map[string]json.RawMessage, creating bool) *httpx.ValidationError {
	v := httpx.NewValidationError()
	_, hasTitle := keys["title"]
	if creating || hasTitle {
		if b.Title == nil || strings.TrimSpace(*b.Title) == "" {
			v.Add("title", "The title field is required.")
		} else if len([]rune(*b.Title)) > 255 {
			v.Add("title", "The title must not be greater than 255 characters.")
		}
	}
	if b.Level != nil && *b.Level != "" && *b.Level != "beginner" && *b.Level != "intermediate" && *b.Level != "advanced" {
		v.Add("level", "The selected level is invalid.")
	}
	if b.Price != nil && *b.Price < 0 {
		v.Add("price", "The price must be at least 0.")
	}
	if b.Thumbnail != nil && strings.TrimSpace(*b.Thumbnail) != "" && !thumbRe.MatchString(strings.TrimSpace(*b.Thumbnail)) {
		v.Add("thumbnail", "The thumbnail must be an image URL or an uploaded file.")
	}
	if b.TrailerURL != nil && len(*b.TrailerURL) > 2048 {
		v.Add("trailer_url", "The trailer url must not be greater than 2048 characters.")
	}
	return v
}

func (m *Module) uniqueCourseSlug(base string, self int64) (string, error) {
	if base == "" {
		base = "khoa-hoc"
	}
	slug := base
	for i := 2; ; i++ {
		var n int64
		if err := m.db.Table("courses").Where("slug = ? AND id <> ?", slug, self).Count(&n).Error; err != nil {
			return "", err
		}
		if n == 0 {
			return slug, nil
		}
		slug = base + "-" + strconv.Itoa(i)
	}
}

func nullStr(s *string) any {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	return strings.TrimSpace(*s)
}

func (m *Module) courseByID(id int64) (json.RawMessage, error) {
	return m.jsonOne("SELECT "+courseJSON+" FROM "+courseFrom+" WHERE c.id = ?", id)
}

func (m *Module) createCourse(c *gin.Context) {
	uid := auth.UserID(c)
	body, _ := c.GetRawData()
	var b courseBody
	if err := json.Unmarshal(body, &b); err != nil {
		httpx.Abort(c, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	if v := validateCourse(b, present(c, body), true); v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	if v := validateSchedule(present(c, body), b.OpensAt, b.ClosesAt, b.LearningMode); v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	title := strings.TrimSpace(*b.Title)
	slug, err := m.uniqueCourseSlug(textutil.Slugify(title), 0)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	price := 0.0
	if b.Price != nil {
		price = *b.Price
	}
	var id int64
	err = m.db.Raw(`INSERT INTO courses (instructor_id, category_id, title, slug, description, level, thumbnail_path, trailer_url, price,
			learning_outcomes, benefits, requirements, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?::jsonb, ?::jsonb, 'draft') RETURNING id`,
		uid, b.CategoryID, title, slug, nullStr(b.Description), nullStr(b.Level), nullStr(b.Thumbnail), nullStr(b.TrailerURL), price,
		mustJSON(cleanList(b.LearningOutcomes)), mustJSON(cleanList(b.Benefits)), mustJSON(cleanList(b.Requirements))).Scan(&id).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := m.applyCourseSchedule(id, present(c, body), b); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.courseByID(id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "course", out)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (m *Module) updateCourse(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	_, roles, _ := m.actor(c)
	body, _ := c.GetRawData()
	var b courseBody
	if err := json.Unmarshal(body, &b); err != nil {
		httpx.Abort(c, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	keys := present(c, body)
	if v := validateCourse(b, keys, false); v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	if v := validateSchedule(keys, b.OpensAt, b.ClosesAt, b.LearningMode); v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	sets := []string{}
	args := []any{}
	add := func(col string, val any) { sets = append(sets, col+" = ?"); args = append(args, val) }
	if _, ok := keys["opens_at"]; ok {
		add("opens_at", parseWhen(b.OpensAt))
	}
	if _, ok := keys["closes_at"]; ok {
		add("closes_at", parseWhen(b.ClosesAt))
	}
	if _, ok := keys["learning_mode"]; ok && b.LearningMode != nil {
		add("learning_mode", *b.LearningMode)
	}
	if _, ok := keys["issue_certificate"]; ok && b.IssueCertificate != nil {
		if *b.IssueCertificate {
			id, err := m.defaultCertificateTemplate()
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			add("certificate_template_id", id)
		} else {
			add("certificate_template_id", nil)
		}
	}
	if _, ok := keys["title"]; ok {
		add("title", strings.TrimSpace(*b.Title))
	}
	if _, ok := keys["description"]; ok {
		add("description", nullStr(b.Description))
	}
	if _, ok := keys["category_id"]; ok {
		add("category_id", b.CategoryID)
	}
	if _, ok := keys["level"]; ok {
		add("level", nullStr(b.Level))
	}
	if _, ok := keys["price"]; ok && b.Price != nil {
		add("price", *b.Price)
	}
	if _, ok := keys["thumbnail"]; ok {
		add("thumbnail_path", nullStr(b.Thumbnail))
	}
	if _, ok := keys["trailer_url"]; ok {
		add("trailer_url", nullStr(b.TrailerURL))
	}
	for key, col := range map[string]string{"learning_outcomes": "learning_outcomes", "benefits": "benefits", "requirements": "requirements"} {
		if _, ok := keys[key]; ok {
			var list *[]string
			switch key {
			case "learning_outcomes":
				list = b.LearningOutcomes
			case "benefits":
				list = b.Benefits
			default:
				list = b.Requirements
			}
			sets = append(sets, col+" = ?::jsonb")
			args = append(args, mustJSON(cleanList(list)))
		}
	}
	if _, ok := keys["status"]; ok && b.Status != nil {
		st := *b.Status
		allowed := st == "draft" || st == "closed"
		if hasRole(roles, auth.RoleAdmin) {
			allowed = allowed || st == "published" || st == "rejected" || st == "pending_review"
		}
		if !allowed {
			httpx.Fail(c, httpx.NewValidationError().Add("status", "The selected status is invalid."))
			return
		}
		add("status", st)
		if st == "published" {
			sets = append(sets, "published_at = coalesce(published_at, now())")
		}
	}
	if len(sets) > 0 {
		args = append(args, cr.ID)
		if err := m.db.Exec("UPDATE courses SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	out, err := m.courseByID(cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "course", out)
}

func (m *Module) deleteCourse(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	var n int64
	if err := m.db.Table("enrollments").Where("course_id = ?", cr.ID).Count(&n).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if n > 0 {
		httpx.Abort(c, http.StatusConflict, "This course has enrolled students and cannot be deleted.")
		return
	}
	if err := m.db.Exec("DELETE FROM courses WHERE id = ?", cr.ID).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

// publishCourse submits a draft or rejected course for admin review.
func (m *Module) publishCourse(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	if cr.Status != "draft" && cr.Status != "rejected" {
		httpx.Abort(c, http.StatusConflict, "Only draft or rejected courses can be submitted for review.")
		return
	}
	var lessons int64
	if err := m.db.Table("lessons").Where("course_id = ?", cr.ID).Count(&lessons).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if lessons == 0 {
		httpx.Fail(c, httpx.NewValidationError().Add("course", "The course must have at least one lesson before it can be submitted."))
		return
	}
	if err := m.db.Exec("UPDATE courses SET status = 'pending_review', reject_reason = NULL WHERE id = ?", cr.ID).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.courseByID(cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "course", out)
}

// ---- admin review queue --------------------------------------------------------

func (m *Module) adminCourses(c *gin.Context) {
	page, per := pageParams(c, 10, 100)
	conds, args := []string{}, []any{}
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		conds = append(conds, "(learning_search_fold(c.title) ILIKE learning_search_fold(?) OR u.name ILIKE ?)")
		args = append(args, likeArg(s), likeArg(s))
	}
	if st := c.Query("status"); st != "" {
		conds = append(conds, "c.status = ?")
		args = append(args, st)
	}
	if id, err := strconv.ParseInt(c.Query("category_id"), 10, 64); err == nil && id > 0 {
		conds = append(conds, "c.category_id = ?")
		args = append(args, id)
	}
	if id, err := strconv.ParseInt(c.Query("instructor_id"), 10, 64); err == nil && id > 0 {
		conds = append(conds, "c.instructor_id = ?")
		args = append(args, id)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM "+courseFrom+where, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows("SELECT "+courseJSON+" FROM "+courseFrom+where+
		" ORDER BY (c.status = 'pending_review') DESC, c.updated_at DESC, c.id DESC LIMIT ? OFFSET ?", append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, total, page, per)
}

func (m *Module) approveCourse(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	if err := m.db.Exec("UPDATE courses SET status = 'published', published_at = coalesce(published_at, now()), reject_reason = NULL WHERE id = ?", cr.ID).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	m.notify(cr.InstructorID, "course_approved", "Khóa học đã được duyệt", "Khóa học của bạn đã được duyệt và xuất bản.", "/studio/courses/"+strconv.FormatInt(cr.ID, 10))
	out, err := m.courseByID(cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "course", out)
}

func (m *Module) rejectCourse(c *gin.Context) {
	cr, ok := m.course(c)
	if !ok {
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	reason := strings.TrimSpace(b.Reason)
	if reason == "" {
		httpx.Fail(c, httpx.NewValidationError().Add("reason", "The reason field is required."))
		return
	}
	if err := m.db.Exec("UPDATE courses SET status = 'rejected', reject_reason = ? WHERE id = ?", reason, cr.ID).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	m.notify(cr.InstructorID, "course_rejected", "Khóa học bị từ chối", reason, "/studio/courses/"+strconv.FormatInt(cr.ID, 10))
	out, err := m.courseByID(cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "course", out)
}

// parseWhen reads an RFC 3339 or datetime-local ("2006-01-02T15:04") timestamp; nil/empty/invalid -> nil (SQL NULL).
func parseWhen(s *string) any {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, strings.TrimSpace(*s), time.Local); err == nil {
			return t
		}
	}
	return nil
}

func whenValid(s *string) bool {
	return s == nil || strings.TrimSpace(*s) == "" || parseWhen(s) != nil
}

// validateSchedule checks the open/close window and learning mode of a course.
func validateSchedule(keys map[string]json.RawMessage, opens, closes, mode *string) *httpx.ValidationError {
	v := httpx.NewValidationError()
	if _, ok := keys["opens_at"]; ok && !whenValid(opens) {
		v.Add("opens_at", "The opens at is not a valid date.")
	}
	if _, ok := keys["closes_at"]; ok && !whenValid(closes) {
		v.Add("closes_at", "The closes at is not a valid date.")
	}
	if _, ok := keys["learning_mode"]; ok && mode != nil && *mode != "free" && *mode != "sequential" {
		v.Add("learning_mode", "The selected learning mode is invalid.")
	}
	if o, c := parseWhen(opens), parseWhen(closes); o != nil && c != nil && !c.(time.Time).After(o.(time.Time)) {
		v.Add("closes_at", "The closes at must be after the opens at.")
	}
	return v
}

// applyCourseSchedule stores the schedule fields a create request carried.
func (m *Module) applyCourseSchedule(id int64, keys map[string]json.RawMessage, b courseBody) error {
	_, a := keys["opens_at"]
	_, z := keys["closes_at"]
	_, l := keys["learning_mode"]
	if !a && !z && !l {
		return nil
	}
	mode := "free"
	if b.LearningMode != nil {
		mode = *b.LearningMode
	}
	return m.db.Exec("UPDATE courses SET opens_at = ?, closes_at = ?, learning_mode = ? WHERE id = ?", parseWhen(b.OpensAt), parseWhen(b.ClosesAt), mode, id).Error
}
