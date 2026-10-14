package catalog

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/rbac"
)

const defaultPerPage = 12

// queryFilled is $request->filled($key) for a query parameter.
func queryFilled(c *gin.Context, key string) (string, bool) {
	v := strings.TrimSpace(c.Query(key))
	return v, v != ""
}

// pageParams reads `page` (paginator) and `per_page`
// (max(1, min(100, $request->integer('per_page', 12)))).
func pageParams(c *gin.Context) httpx.PageParams {
	p := httpx.ParsePage(c, defaultPerPage, 100)
	per := int64(defaultPerPage)
	if raw, ok := c.GetQuery("per_page"); ok {
		per = phpInt(normalize("per_page", raw))
	}
	p.PerPage = int(max(1, min(100, per)))
	return p
}

// pricingFilter is the shared `pricing=paid|free` filter.
func pricingFilter(c *gin.Context, w *where) {
	switch strings.TrimSpace(c.Query("pricing")) {
	case "paid":
		w.add("c.price > 0")
	case "free":
		w.add("(c.price IS NULL OR c.price <= 0)")
	}
}

// courseListRow is a course with the withCount/withAvg columns.
type courseListRow struct {
	courseRow
	LessonsCount     int64    `gorm:"column:lessons_count"`
	EnrollmentsCount int64    `gorm:"column:enrollments_count"`
	ReviewsCount     int64    `gorm:"column:reviews_count"`
	ReviewsAvgRating *float64 `gorm:"column:reviews_avg_rating"`
}

const (
	lessonsCountCol     = `(SELECT count(*) FROM lessons x WHERE x.course_id = c.id) AS lessons_count`
	enrollmentsCountCol = `(SELECT count(*) FROM enrollments x WHERE x.course_id = c.id) AS enrollments_count`
	reviewsCountCol     = `(SELECT count(*) FROM reviews x WHERE x.course_id = c.id) AS reviews_count`
	reviewsAvgCol       = `(SELECT avg(x.rating)::float8 FROM reviews x WHERE x.course_id = c.id) AS reviews_avg_rating`
)

// listCourses is GET /courses (public catalog).
func (m *Module) listCourses(c *gin.Context) {
	ctx := c.Request.Context()
	w := &where{}
	if s, ok := queryFilled(c, "status"); ok {
		w.add("c.status = ?", s)
	} else {
		w.add("c.status = 'published'")
	}
	if key, ok := queryFilled(c, "category"); ok {
		ids, err := m.categoryFilterIDs(ctx, key)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if len(ids) == 0 {
			w.add("1 = 0") // unknown category: no results instead of ignoring the filter
		} else {
			w.add("c.category_id IN ?", ids)
		}
	}
	if s, ok := queryFilled(c, "search"); ok {
		t := likeTerm(s)
		w.add("("+foldLike("c.title")+" OR "+foldLike("c.description")+")", t, t)
	}
	if s, ok := queryFilled(c, "featured"); ok {
		w.add("c.is_featured = ?", phpBool(s))
	}
	if s, ok := queryFilled(c, "instructor_id"); ok {
		w.add("c.instructor_id = ?", phpInt(s))
	}
	if s, ok := queryFilled(c, "level"); ok {
		w.add("c.level = ?", s)
	}
	pricingFilter(c, w)

	order := "c.is_featured DESC, c.created_at DESC"
	switch c.DefaultQuery("sort", "newest") {
	case "popular":
		order = "c.is_featured DESC, enrollments_count DESC"
	case "price_asc":
		order = "c.price ASC"
	case "price_desc":
		order = "c.price DESC"
	case "rating":
		order = "reviews_avg_rating DESC NULLS LAST, enrollments_count DESC"
	}
	order += ", c.id DESC" // deterministic pages (MySQL left ties unordered)

	p := pageParams(c)
	db := m.db.WithContext(ctx)
	var total int64
	if err := db.Raw(`SELECT count(*) FROM courses c`+w.sql(), w.args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, fmtErr("count courses", err))
		return
	}
	var rows []courseListRow
	if total > 0 {
		q := `SELECT ` + courseCols + `, ` + lessonsCountCol + `, ` + enrollmentsCountCol + `, ` + reviewsCountCol + `, ` + reviewsAvgCol +
			` FROM courses c` + w.sql() + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
		if err := db.Raw(q, append(w.args, p.Limit(), p.Offset())...).Scan(&rows).Error; err != nil {
			httpx.Fail(c, fmtErr("list courses", err))
			return
		}
	}
	base := make([]courseRow, len(rows))
	for i, r := range rows {
		base[i] = r.courseRow
	}
	us, cs, err := m.courseRelations(ctx, base)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]courseListItem, len(rows))
	for i, r := range rows {
		var avg *float64
		if r.ReviewsAvgRating != nil {
			v := round1(*r.ReviewsAvgRating)
			avg = &v
		}
		items[i] = courseListItem{
			courseAttrs: m.courseAttrs(r.courseRow), LessonsCount: r.LessonsCount, EnrollmentsCount: r.EnrollmentsCount,
			ReviewsCount: r.ReviewsCount, ReviewsAvgRating: avg,
			Instructor: us[r.InstructorID], Category: categoryOf(cs, r.CategoryID),
		}
	}
	httpx.Paginate(c, items, total, p)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// categoryFilterIDs is the `category` filter: id when numeric, else slug;
// the category plus its children and grandchildren (categoryAndDescendantIds).
// Empty when the category does not exist.
func (m *Module) categoryFilterIDs(ctx context.Context, key string) ([]int64, error) {
	db := m.db.WithContext(ctx)
	var ids []int64
	if isNumeric(key) {
		if id, ok := learning.RouteID(strings.TrimPrefix(key, "+")); ok {
			if err := db.Raw(`SELECT id FROM categories WHERE id = ?`, id).Scan(&ids).Error; err != nil {
				return nil, fmtErr("find category", err)
			}
		}
	} else if err := db.Raw(`SELECT id FROM categories WHERE slug = ?`, key).Scan(&ids).Error; err != nil {
		return nil, fmtErr("find category", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var children []int64
	if err := db.Raw(`SELECT id FROM categories WHERE parent_id = ?`, ids[0]).Scan(&children).Error; err != nil {
		return nil, fmtErr("category children", err)
	}
	ids = append(ids, children...)
	if len(children) > 0 {
		var grand []int64
		if err := db.Raw(`SELECT id FROM categories WHERE parent_id IN ?`, children).Scan(&grand).Error; err != nil {
			return nil, fmtErr("category grandchildren", err)
		}
		ids = append(ids, grand...)
	}
	return ids, nil
}

// myCourses is GET /my-courses: the caller's own (instructor) courses.
func (m *Module) myCourses(c *gin.Context) {
	ctx := c.Request.Context()
	uid := auth.UserID(c)
	w := &where{}
	w.add("c.instructor_id = ?", uid)
	if s, ok := queryFilled(c, "status"); ok {
		w.add("c.status = ?", s)
	}
	if s, ok := queryFilled(c, "course_mode"); ok {
		w.add("c.course_mode = ?", s)
	}
	if s, ok := queryFilled(c, "category_id"); ok {
		w.add("c.category_id = ?", phpInt(s))
	}
	pricingFilter(c, w)
	if s, ok := queryFilled(c, "search"); ok {
		t := likeTerm(s)
		w.add("("+foldLike("c.title")+" OR "+foldLike("c.slug")+
			" OR EXISTS (SELECT 1 FROM categories cat WHERE cat.id = c.category_id AND "+foldLike("cat.name")+"))", t, t, t)
	}
	var order string
	switch c.DefaultQuery("sort", "newest") {
	case "price_asc":
		order = "c.price ASC, c.id DESC"
	case "price_desc":
		order = "c.price DESC, c.id DESC"
	case "title":
		order = "c.title ASC, c.id DESC"
	case "learners":
		order = "enrollments_count DESC, c.id DESC"
	default:
		order = "c.created_at DESC, c.id DESC"
	}

	p := pageParams(c)
	db := m.db.WithContext(ctx)
	var total int64
	if err := db.Raw(`SELECT count(*) FROM courses c`+w.sql(), w.args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, fmtErr("count my courses", err))
		return
	}
	var rows []courseListRow
	if total > 0 {
		q := `SELECT ` + courseCols + `, ` + lessonsCountCol + `, ` + enrollmentsCountCol +
			` FROM courses c` + w.sql() + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
		if err := db.Raw(q, append(w.args, p.Limit(), p.Offset())...).Scan(&rows).Error; err != nil {
			httpx.Fail(c, fmtErr("list my courses", err))
			return
		}
	}
	base := make([]courseRow, len(rows))
	for i, r := range rows {
		base[i] = r.courseRow
	}
	us, cs, err := m.courseRelations(ctx, base)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]courseWithCounts, len(rows))
	for i, r := range rows {
		items[i] = courseWithCounts{courseAttrs: m.courseAttrs(r.courseRow), LessonsCount: r.LessonsCount,
			EnrollmentsCount: r.EnrollmentsCount, Instructor: us[r.InstructorID], Category: categoryOf(cs, r.CategoryID)}
	}
	httpx.Paginate(c, items, total, p)
}

// showCourse is GET /courses/{course}: course detail with lessons, section
// outline, counts, the caller's enrollment/review state, latest reviews and
// career-path suggestions.
func (m *Module) showCourse(c *gin.Context) {
	ctx := c.Request.Context()
	row, err := m.findCourse(ctx, boundCourse(c).ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	who, authed, err := m.caller(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	db := m.db.WithContext(ctx)

	out := courseDetail{courseAttrs: m.courseAttrs(row)}
	var counts struct {
		Enrollments int64    `gorm:"column:enrollments"`
		Reviews     int64    `gorm:"column:reviews"`
		Lessons     int64    `gorm:"column:lessons"`
		AvgRating   *float64 `gorm:"column:avg_rating"`
	}
	if err := db.Raw(`SELECT
  (SELECT count(*) FROM enrollments WHERE course_id = ?) AS enrollments,
  (SELECT count(*) FROM reviews WHERE course_id = ?) AS reviews,
  (SELECT count(*) FROM lessons WHERE course_id = ?) AS lessons,
  (SELECT avg(rating)::float8 FROM reviews WHERE course_id = ?) AS avg_rating`, row.ID, row.ID, row.ID, row.ID).Scan(&counts).Error; err != nil {
		httpx.Fail(c, fmtErr("course counts", err))
		return
	}
	out.EnrollmentsCount, out.ReviewsCount, out.LessonsCount = counts.Enrollments, counts.Reviews, counts.Lessons
	if counts.AvgRating != nil {
		out.AvgRating = round1(*counts.AvgRating)
	}

	us, cs, err := m.courseRelations(ctx, []courseRow{row})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out.Instructor, out.Category = us[row.InstructorID], categoryOf(cs, row.CategoryID)

	lessons, err := m.lessonsOf(ctx, "l.course_id = ?", row.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out.Lessons = make([]lessonJSON, len(lessons))
	for i, l := range lessons {
		out.Lessons[i] = m.lessonJSON(l)
	}
	if out.Sections, err = m.sectionOutlines(ctx, row.ID, lessons); err != nil {
		httpx.Fail(c, err)
		return
	}

	if authed {
		if out.IsEnrolled, err = learning.IsEnrolled(ctx, m.db, who.UserID, row.ID); err != nil {
			httpx.Fail(c, err)
			return
		}
		var n int64
		if err := db.Raw(`SELECT count(*) FROM reviews WHERE course_id = ? AND user_id = ?`, row.ID, who.UserID).Scan(&n).Error; err != nil {
			httpx.Fail(c, fmtErr("has reviewed", err))
			return
		}
		out.HasReviewed = n > 0
	}
	if out.LatestReviews, err = m.latestReviews(ctx, row.ID); err != nil {
		httpx.Fail(c, err)
		return
	}
	var uid int64
	if authed {
		uid = who.UserID
	}
	if out.PathSuggestions, err = m.pathSuggestions(ctx, row, uid); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// sectionOutlines builds sections (by position) with their lesson outline.
func (m *Module) sectionOutlines(ctx context.Context, courseID int64, lessons []lessonRow) ([]sectionOutline, error) {
	sections, err := m.sectionsOf(ctx, courseID)
	if err != nil {
		return nil, err
	}
	bySection := map[int64][]lessonOutline{}
	for _, l := range lessons {
		if l.SectionID != nil {
			bySection[*l.SectionID] = append(bySection[*l.SectionID], lessonOutlineOf(l))
		}
	}
	out := make([]sectionOutline, len(sections))
	for i, s := range sections {
		ls := bySection[s.ID]
		if ls == nil {
			ls = []lessonOutline{}
		}
		out[i] = sectionOutline{sectionJSON: s.json(), Lessons: ls}
	}
	return out, nil
}

func (m *Module) latestReviews(ctx context.Context, courseID int64) ([]reviewJSON, error) {
	var rows []reviewJSON
	err := m.db.WithContext(ctx).Raw(`SELECT id, user_id, course_id, rating, comment, created_at, updated_at
FROM reviews WHERE course_id = ? ORDER BY created_at DESC, id DESC LIMIT 5`, courseID).Scan(&rows).Error
	if err != nil {
		return nil, fmtErr("latest reviews", err)
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.UserID
	}
	us, err := m.users(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].User = us[rows[i].UserID]
	}
	if rows == nil {
		rows = []reviewJSON{}
	}
	return rows, nil
}

// pathSuggestions is CourseController::buildPathSuggestions: for every
// published career path containing this (non-core) course, the remaining
// published courses the caller does not own yet.
func (m *Module) pathSuggestions(ctx context.Context, course courseRow, userID int64) ([]pathSuggestionJSON, error) {
	out := []pathSuggestionJSON{}
	if course.CourseMode == "core" {
		return out, nil
	}
	db := m.db.WithContext(ctx)
	var paths []struct {
		ID    int64  `gorm:"column:id"`
		Title string `gorm:"column:title"`
		Slug  string `gorm:"column:slug"`
		Price string `gorm:"column:price"`
	}
	if err := db.Raw(`SELECT cp.id, cp.title, cp.slug, cp.price::text AS price
FROM career_path_courses cpc JOIN career_paths cp ON cp.id = cpc.career_path_id
WHERE cpc.course_id = ? AND cp.status = 'published' ORDER BY cpc.id`, course.ID).Scan(&paths).Error; err != nil {
		return nil, fmtErr("career paths", err)
	}
	if len(paths) == 0 {
		return out, nil
	}
	pathIDs := make([]int64, len(paths))
	for i, p := range paths {
		pathIDs[i] = p.ID
	}
	var items []struct {
		CareerPathID  int64   `gorm:"column:career_path_id"`
		CourseID      int64   `gorm:"column:course_id"`
		Title         *string `gorm:"column:title"`
		Slug          *string `gorm:"column:slug"`
		ThumbnailPath *string `gorm:"column:thumbnail_path"`
		Price         *string `gorm:"column:price"`
		Status        *string `gorm:"column:status"`
	}
	if err := db.Raw(`SELECT cpc.career_path_id, cpc.course_id, c.title, c.slug, c.thumbnail_path, c.price::text AS price, c.status
FROM career_path_courses cpc LEFT JOIN courses c ON c.id = cpc.course_id
WHERE cpc.career_path_id IN ? ORDER BY cpc.career_path_id, cpc.sort_order, cpc.id`, pathIDs).Scan(&items).Error; err != nil {
		return nil, fmtErr("career path courses", err)
	}
	enrolled := map[int64]bool{}
	if userID != 0 {
		var all []int64
		for _, it := range items {
			all = append(all, it.CourseID)
		}
		var ids []int64
		if len(all) > 0 {
			if err := db.Raw(`SELECT course_id FROM enrollments WHERE user_id = ? AND course_id IN ?`, userID, uniq(all)).Scan(&ids).Error; err != nil {
				return nil, fmtErr("path enrollments", err)
			}
		}
		for _, id := range ids {
			enrolled[id] = true
		}
	}
	for _, p := range paths {
		s := pathSuggestionJSON{ID: p.ID, Title: p.Title, Slug: p.Slug, PathPrice: intPrice(p.Price), RemainingCourses: []pathCourseBrief{}}
		for _, it := range items {
			if it.CareerPathID != p.ID {
				continue
			}
			s.TotalCount++
			if it.Status == nil || *it.Status != "published" {
				continue
			}
			if enrolled[it.CourseID] || it.CourseID == course.ID {
				s.OwnedCount++ // the course being viewed counts as about to be owned
				continue
			}
			price := intPrice(deref(it.Price))
			s.RemainingCourses = append(s.RemainingCourses, pathCourseBrief{ID: it.CourseID, Title: deref(it.Title),
				Slug: deref(it.Slug), Thumbnail: m.media.Public(it.ThumbnailPath), Price: price})
			s.RemainingTotalPrice += price
		}
		if len(s.RemainingCourses) == 0 {
			continue
		}
		s.RemainingCount = len(s.RemainingCourses)
		out = append(out, s)
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---------------------------------------------------------------------------
// course write

// storeCourse is POST /courses.
func (m *Module) storeCourse(c *gin.Context) {
	ctx := c.Request.Context()
	who, _, err := m.caller(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !who.Allows(rbac.PermManageCourses, rbac.PermManageLessons, rbac.PermManageExams, rbac.PermManageGrades, rbac.PermViewDashboard) {
		httpx.Forbidden(c, "Unauthorized")
		return
	}
	in, err := readInput(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v := newValidator(in)
	v.field("title").required().string().max(255)
	v.field("description").nullableRule().string()
	v.field("price").required().numericRule().min(0)
	v.field("category_id").nullableRule().integer().exists(m.existsIn(ctx, "categories"))
	v.field("thumbnail").nullableRule().url().max(2048)
	v.field("thumbnail_file").nullableRule().image().max(5120)
	v.field("is_featured").nullableRule().boolean()
	v.field("certificate_template_id").nullableRule().integer().exists(m.existsIn(ctx, "certificate_templates"))
	if err := v.err(); err != nil {
		httpx.Fail(c, err)
		return
	}

	slug, err := m.uniqueSlug(ctx, in.str("title"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var thumb *string
	if s := in.str("thumbnail"); s != "" {
		k := m.media.StorageKey(s)
		thumb = &k
	}
	if fh := fileOf(in, "thumbnail_file"); fh != nil {
		key, err := m.putUpload(ctx, fh, "courses/thumbnails")
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		thumb = &key
	}
	var id int64
	err = m.db.WithContext(ctx).Raw(`INSERT INTO courses
 (instructor_id, title, slug, description, price, category_id, thumbnail_path, status, is_featured, certificate_template_id)
 VALUES (?, ?, ?, ?, ?, ?, ?, 'draft', ?, ?) RETURNING id`,
		who.UserID, in.str("title"), slug, nullableStr(in, "description"), roundPrice(in.get("price")),
		nullableInt(in, "category_id"), thumb, isFeatured(in), nullableInt(in, "certificate_template_id"),
	).Scan(&id).Error
	if err != nil {
		httpx.Fail(c, fmtErr("create course", err))
		return
	}
	row, err := m.findCourse(ctx, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	full, err := m.courseWithCounts(ctx, row)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, gin.H{"message": "Course created", "course": courseCreated{
		courseAttrs: full.courseAttrs, LessonsCount: full.LessonsCount, EnrollmentsCount: full.EnrollmentsCount,
		Instructor: full.Instructor,
	}})
}

// uniqueSlug is Str::slug($title) with "-1", "-2"... appended until unused.
func (m *Module) uniqueSlug(ctx context.Context, title string) (string, error) {
	base := slugify(title)
	if base == "" {
		base = "course" // the schema requires a non-empty slug (legacy stored "")
	}
	slug := base
	for i := 1; ; i++ {
		taken, err := m.slugTaken(ctx, slug)
		if err != nil {
			return "", err
		}
		if !taken {
			return slug, nil
		}
		slug = base + "-" + strconv.Itoa(i)
	}
}

func (m *Module) slugTaken(ctx context.Context, slug string) (bool, error) {
	var n int64
	if err := m.db.WithContext(ctx).Raw(`SELECT count(*) FROM courses WHERE slug = ?`, slug).Scan(&n).Error; err != nil {
		return false, fmtErr("slug lookup", err)
	}
	return n > 0, nil
}

func (m *Module) existsIn(ctx context.Context, table string) func(int64) (bool, error) {
	return func(id int64) (bool, error) { return m.exists(ctx, table, id) }
}

// roundPrice stores the price like the legacy decimal(12,0) column (rounded).
func roundPrice(v any) float64 { return math.Round(toFloat(v)) }

func isFeatured(in *input) bool { return phpBool(in.get("is_featured")) }

func nullableStr(in *input, key string) *string {
	if s, ok := in.get(key).(string); ok {
		return &s
	}
	return nil
}

func nullableInt(in *input, key string) *int64 {
	v := in.get(key)
	if v == nil {
		return nil
	}
	n := phpInt(v)
	return &n
}

// listFields may arrive JSON-encoded inside multipart bodies.
var listFields = []string{"learning_outcomes", "benefits", "requirements"}

// updateCourse is PUT /courses/{course} (also POST with _method=PUT).
func (m *Module) updateCourse(c *gin.Context) {
	ctx := c.Request.Context()
	course := boundCourse(c)
	if !m.ownerOnly(c, course, "Forbidden") {
		return
	}
	in, err := readInput(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	for _, f := range listFields {
		if s, ok := in.vals[f].(string); ok {
			dec := json.NewDecoder(strings.NewReader(s))
			dec.UseNumber()
			var decoded any
			if dec.Decode(&decoded) == nil && !dec.More() {
				in.vals[f] = decoded
			}
		}
	}

	v := newValidator(in)
	v.field("title").sometimes().string().max(255)
	v.field("description").nullableRule().string()
	for _, f := range listFields {
		v.field(f).nullableRule().array()
		keys, vals := arrayItems(in.get(f))
		for i, k := range keys {
			v.value(f+"."+k, vals[i], true).string().max(500)
		}
	}
	v.field("level").nullableRule().string().in("beginner", "intermediate", "advanced")
	v.field("trailer_url").nullableRule().string().max(2048)
	v.field("price").sometimes().numericRule().min(0)
	v.field("category_id").nullableRule().integer().exists(m.existsIn(ctx, "categories"))
	v.field("thumbnail").nullableRule().url().max(2048)
	v.field("thumbnail_file").nullableRule().image().max(5120)
	v.field("status").sometimes().in("draft", "published", "closed", "pending_review", "rejected")
	v.field("is_featured").nullableRule().boolean()
	v.field("certificate_template_id").nullableRule().integer().exists(m.existsIn(ctx, "certificate_templates"))
	if err := v.err(); err != nil {
		httpx.Fail(c, err)
		return
	}

	set := map[string]any{}
	if in.has("title") {
		set["title"] = in.str("title")
	}
	if in.has("description") {
		set["description"] = nullableStr(in, "description")
	}
	for _, f := range listFields {
		if in.has(f) {
			_, vals := arrayItems(in.get(f))
			if vals == nil {
				vals = []any{}
			}
			b, err := json.Marshal(vals)
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			set[f] = string(b)
		}
	}
	for _, f := range []string{"level", "trailer_url"} {
		if in.has(f) {
			set[f] = nullableStr(in, f)
		}
	}
	if in.has("price") {
		set["price"] = roundPrice(in.get("price"))
	}
	if in.has("category_id") {
		set["category_id"] = nullableInt(in, "category_id")
	}
	if in.has("thumbnail") {
		var thumb *string
		if s := in.str("thumbnail"); s != "" {
			k := m.media.StorageKey(s)
			thumb = &k
		}
		set["thumbnail_path"] = thumb
	}
	if fh := fileOf(in, "thumbnail_file"); fh != nil {
		key, err := m.putUpload(ctx, fh, "courses/thumbnails")
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		set["thumbnail_path"] = key
	}
	if in.has("status") {
		set["status"] = in.str("status")
	}
	if in.has("is_featured") {
		set["is_featured"] = isFeatured(in)
	}
	if in.has("certificate_template_id") {
		set["certificate_template_id"] = nullableInt(in, "certificate_template_id")
	}
	if err := m.updateRow(ctx, "courses", course.ID, set, map[string]string{
		"learning_outcomes": "?::jsonb", "benefits": "?::jsonb", "requirements": "?::jsonb",
	}); err != nil {
		httpx.Fail(c, err)
		return
	}

	row, err := m.findCourse(ctx, course.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	full, err := m.courseWithCounts(ctx, row)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"message": "Course updated", "course": full})
}

// updateRow runs UPDATE table SET <set> WHERE id = ?; casts overrides the
// placeholder of a column (e.g. "?::jsonb"). Nothing to set is a no-op.
func (m *Module) updateRow(ctx context.Context, table string, id int64, set map[string]any, casts map[string]string) error {
	if len(set) == 0 {
		return nil
	}
	cols := make([]string, 0, len(set))
	for k := range set {
		cols = append(cols, k)
	}
	sortStrings(cols)
	parts := make([]string, len(cols))
	args := make([]any, 0, len(cols)+1)
	for i, k := range cols {
		ph := "?"
		if p, ok := casts[k]; ok {
			ph = p
		}
		parts[i] = k + " = " + ph
		args = append(args, set[k])
	}
	args = append(args, id)
	if err := m.db.WithContext(ctx).Exec(`UPDATE `+table+` SET `+strings.Join(parts, ", ")+` WHERE id = ?`, args...).Error; err != nil {
		return fmtErr("update "+table, err)
	}
	return nil
}

// destroyCourse is DELETE /courses/{course}.
func (m *Module) destroyCourse(c *gin.Context) {
	course := boundCourse(c)
	if !m.ownerOnly(c, course, "Forbidden") {
		return
	}
	if err := m.db.WithContext(c.Request.Context()).Exec(`DELETE FROM courses WHERE id = ?`, course.ID).Error; err != nil {
		httpx.Fail(c, fmtErr("delete course", err))
		return
	}
	httpx.Message(c, http.StatusOK, "Course deleted")
}

// publishCourse is POST /courses/{course}/publish: submit for review.
func (m *Module) publishCourse(c *gin.Context) {
	ctx := c.Request.Context()
	course := boundCourse(c)
	if !m.ownerOnly(c, course, "Forbidden") {
		return
	}
	if course.Status == "pending_review" || course.Status == "published" {
		httpx.Message(c, http.StatusUnprocessableEntity, "Course has already been submitted or published")
		return
	}
	lessons, _, err := m.courseCounts(ctx, course.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if lessons == 0 {
		httpx.Message(c, http.StatusUnprocessableEntity, "Course must have at least 1 lesson before publishing")
		return
	}
	if err := m.updateRow(ctx, "courses", course.ID, map[string]any{"status": "pending_review"}, nil); err != nil {
		httpx.Fail(c, err)
		return
	}
	row, err := m.findCourse(ctx, course.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"message": "Course submitted for review", "course": m.courseAttrs(row)})
}
