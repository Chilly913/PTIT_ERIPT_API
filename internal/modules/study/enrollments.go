package study

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// ---------------------------------------------------------------------------
// JSON shapes of GET /enrollments

type idName struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type instructorJSON struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	Avatar *string `json:"avatar"`
}

// enrollCourseJSON is course:id,user_id,title,thumbnail,price,status,category_id
// with category:id,name and instructor:id,name,avatar.
type enrollCourseJSON struct {
	ID         int64           `json:"id"`
	UserID     int64           `json:"user_id"`
	Title      string          `json:"title"`
	Thumbnail  *string         `json:"thumbnail"`
	Price      int64           `json:"price"`
	Status     string          `json:"status"`
	CategoryID *int64          `json:"category_id"`
	Category   *idName         `json:"category"`
	Instructor *instructorJSON `json:"instructor"`
}

// termJSON is the `term` object of termWindow().
type termJSON struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Code         string  `json:"code"`
	StartDate    *string `json:"start_date"`
	EndDate      *string `json:"end_date"`
	IsCurrent    bool    `json:"is_current"`
	Status       string  `json:"status"`
	DisplayName  string  `json:"display_name"`
	AcademicYear *idName `json:"academic_year"`
}

// termWindow is LessonProgressController::termWindow.
type termWindow struct {
	Term         *termJSON
	StartsAt     *string
	EndsAt       *string
	WindowStatus string
}

type enrollmentJSON struct {
	ID               int64             `json:"id"`
	UserID           int64             `json:"user_id"`
	CourseID         int64             `json:"course_id"`
	TermID           *int64            `json:"term_id"`
	CohortID         *int64            `json:"cohort_id"`
	ClassSectionID   *int64            `json:"class_section_id"`
	OrderID          *int64            `json:"order_id"`
	EnrollmentSource string            `json:"enrollment_source"`
	EnrolledAt       httpx.Time        `json:"enrolled_at"`
	CreatedAt        httpx.Time        `json:"created_at"`
	UpdatedAt        httpx.Time        `json:"updated_at"`
	Course           *enrollCourseJSON `json:"course"`
	Term             *termJSON         `json:"term"`
	Progress         any               `json:"progress"`
	LessonsCount     int64             `json:"lessons_count"`
	CompletedLessons int64             `json:"completed_lessons"`
	StartsAt         *string           `json:"starts_at"`
	EndsAt           *string           `json:"ends_at"`
	WindowStatus     string            `json:"window_status"`
	IsPlanned        bool              `json:"is_planned"`
}

// plannedJSON is an upcoming curriculum course without enrollment (negative id).
type plannedJSON struct {
	ID               int64             `json:"id"`
	UserID           int64             `json:"user_id"`
	CourseID         int64             `json:"course_id"`
	TermID           int64             `json:"term_id"`
	CohortID         *int64            `json:"cohort_id"`
	ClassSectionID   *int64            `json:"class_section_id"`
	EnrollmentSource string            `json:"enrollment_source"`
	EnrolledAt       *string           `json:"enrolled_at"`
	Course           *enrollCourseJSON `json:"course"`
	IsPlanned        bool              `json:"is_planned"`
	Progress         int               `json:"progress"`
	LessonsCount     int               `json:"lessons_count"`
	CompletedLessons int               `json:"completed_lessons"`
	Term             *termJSON         `json:"term"`
	StartsAt         *string           `json:"starts_at"`
	EndsAt           *string           `json:"ends_at"`
	WindowStatus     string            `json:"window_status"`
}

// ---------------------------------------------------------------------------
// term helpers (App\Models\Term)

type termRow struct {
	ID        int64     `gorm:"column:id"`
	Name      string    `gorm:"column:name"`
	Code      string    `gorm:"column:code"`
	StartDate time.Time `gorm:"column:start_date"`
	EndDate   time.Time `gorm:"column:end_date"`
	IsCurrent bool      `gorm:"column:is_current"`
	Status    string    `gorm:"column:status"`
	YearID    *int64    `gorm:"column:year_id"`
	YearName  *string   `gorm:"column:year_name"`
}

// windowStatus is Term::windowStatus: expired / upcoming / current by date.
func windowStatus(start, end, now time.Time) string {
	today := dateOnly(now)
	if dateOnly(end).Before(today) {
		return "expired"
	}
	if dateOnly(start).After(today) {
		return "upcoming"
	}
	return "current"
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

var hocKyRe = regexp.MustCompile(`(?i)^(Học kỳ\s+\d+)\s*\(([^)]+)\)$`)

// termDisplayName is Term::displayName, e.g. "Học kỳ 1 - Năm học 2025-2026".
func termDisplayName(name string, year *string) string {
	name = strings.TrimSpace(name)
	if m := hocKyRe.FindStringSubmatch(name); m != nil {
		return m[1] + " - Năm học " + m[2]
	}
	if year != nil && *year != "" && !strings.Contains(name, *year) {
		return name + " - Năm học " + *year
	}
	return name
}

func (m *Module) termWindow(t *termRow) termWindow {
	if t == nil {
		return termWindow{WindowStatus: "current"}
	}
	start, end := t.StartDate.Format(httpx.DateLayout), t.EndDate.Format(httpx.DateLayout)
	tj := &termJSON{
		ID: t.ID, Name: t.Name, Code: t.Code, StartDate: &start, EndDate: &end,
		IsCurrent: t.IsCurrent, Status: t.Status, DisplayName: termDisplayName(t.Name, t.YearName),
	}
	if t.YearID != nil {
		tj.AcademicYear = &idName{ID: *t.YearID, Name: deref(t.YearName)}
	}
	return termWindow{Term: tj, StartsAt: &start, EndsAt: &end, WindowStatus: windowStatus(t.StartDate, t.EndDate, m.now())}
}

// ---------------------------------------------------------------------------
// loaders

func (m *Module) loadTerms(ctx context.Context, ids []int64) (map[int64]*termRow, error) {
	out := map[int64]*termRow{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []termRow
	err := m.db.WithContext(ctx).Raw(`SELECT t.id, t.name, t.code::text AS code, t.start_date, t.end_date,
			t.is_current, t.status, y.id AS year_id, y.name AS year_name
		FROM terms t LEFT JOIN academic_years y ON y.id = t.academic_year_id
		WHERE t.id IN ?`, ids).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("study: load terms: %w", err)
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}

func (m *Module) loadCourses(ctx context.Context, ids []int64) (map[int64]*enrollCourseJSON, error) {
	out := map[int64]*enrollCourseJSON{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID           int64   `gorm:"column:id"`
		InstructorID int64   `gorm:"column:instructor_id"`
		Title        string  `gorm:"column:title"`
		Thumbnail    *string `gorm:"column:thumbnail_path"`
		Price        int64   `gorm:"column:price"`
		Status       string  `gorm:"column:status"`
		CategoryID   *int64  `gorm:"column:category_id"`
		CatName      *string `gorm:"column:cat_name"`
		UName        *string `gorm:"column:u_name"`
		UAvatar      *string `gorm:"column:u_avatar"`
		UID          *int64  `gorm:"column:u_id"`
	}
	err := m.db.WithContext(ctx).Raw(`SELECT c.id, c.instructor_id, c.title, c.thumbnail_path,
			trunc(c.price)::bigint AS price, c.status, c.category_id,
			cat.name AS cat_name, u.id AS u_id, u.name AS u_name, u.avatar_path AS u_avatar
		FROM courses c
		LEFT JOIN categories cat ON cat.id = c.category_id
		LEFT JOIN users u ON u.id = c.instructor_id
		WHERE c.id IN ?`, ids).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("study: load courses: %w", err)
	}
	for _, r := range rows {
		cj := &enrollCourseJSON{
			ID: r.ID, UserID: r.InstructorID, Title: r.Title, Thumbnail: m.media.Public(r.Thumbnail),
			Price: r.Price, Status: r.Status, CategoryID: r.CategoryID,
		}
		if r.CategoryID != nil && r.CatName != nil {
			cj.Category = &idName{ID: *r.CategoryID, Name: *r.CatName}
		}
		if r.UID != nil {
			cj.Instructor = &instructorJSON{ID: *r.UID, Name: deref(r.UName), Avatar: m.media.Public(r.UAvatar)}
		}
		out[r.ID] = cj
	}
	return out, nil
}

// lessonCounts returns per course (lessons, lessons completed by the user).
func (m *Module) lessonCounts(ctx context.Context, userID int64, courseIDs []int64) (map[int64][2]int64, error) {
	out := map[int64][2]int64{}
	if len(courseIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		CourseID int64 `gorm:"column:course_id"`
		Total    int64 `gorm:"column:total"`
		Done     int64 `gorm:"column:done"`
	}
	err := m.db.WithContext(ctx).Raw(`SELECT l.course_id, count(*) AS total, count(lp.completed_at) AS done
		FROM lessons l
		LEFT JOIN lesson_progress lp ON lp.lesson_id = l.id AND lp.user_id = ?
		WHERE l.course_id IN ?
		GROUP BY l.course_id`, userID, courseIDs).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("study: lesson counts: %w", err)
	}
	for _, r := range rows {
		out[r.CourseID] = [2]int64{r.Total, r.Done}
	}
	return out, nil
}

// enrollmentProgress is enrollmentCourseProgress: progress is a float
// (round(.., 1)) when the course has lessons, else integer 0.
func enrollmentProgress(total, done int64) any {
	if total <= 0 {
		return 0
	}
	return phpFloat(roundPercent(done, total))
}

// ---------------------------------------------------------------------------
// handler

// enrollments is GET /enrollments: the caller's enrollments (newest first)
// with course, term window and progress, followed by the upcoming curriculum
// courses of the caller's administrative class that are not enrolled yet.
func (m *Module) enrollments(c *gin.Context) {
	cl, ok := m.caller(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	db := m.db.WithContext(ctx)

	var rows []struct {
		ID               int64     `gorm:"column:id"`
		UserID           int64     `gorm:"column:user_id"`
		CourseID         int64     `gorm:"column:course_id"`
		TermID           *int64    `gorm:"column:term_id"`
		CohortID         *int64    `gorm:"column:cohort_id"`
		ClassSectionID   *int64    `gorm:"column:class_section_id"`
		OrderID          *int64    `gorm:"column:order_id"`
		EnrollmentSource string    `gorm:"column:enrollment_source"`
		EnrolledAt       time.Time `gorm:"column:enrolled_at"`
		CreatedAt        time.Time `gorm:"column:created_at"`
		UpdatedAt        time.Time `gorm:"column:updated_at"`
		WindowTermID     *int64    `gorm:"column:window_term_id"`
	}
	// term ?? classSection.term (a dangling term_id cannot exist: FK SET NULL).
	err := db.Raw(`SELECT e.id, e.user_id, e.course_id, e.term_id, e.cohort_id, e.class_section_id, e.order_id,
			e.enrollment_source, e.enrolled_at, e.created_at, e.updated_at,
			COALESCE(e.term_id, cs.term_id) AS window_term_id
		FROM enrollments e
		LEFT JOIN class_sections cs ON cs.id = e.class_section_id
		WHERE e.user_id = ?
		ORDER BY e.enrolled_at DESC, e.id DESC`, cl.UserID).Scan(&rows).Error
	if err != nil {
		httpx.Fail(c, fmt.Errorf("study: enrollments: %w", err))
		return
	}

	planned, err := m.plannedRows(ctx, cl.UserID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	enrolled := map[int64]bool{}
	var courseIDs, termIDs []int64
	for _, r := range rows {
		enrolled[r.CourseID] = true
		courseIDs = append(courseIDs, r.CourseID)
		if r.WindowTermID != nil {
			termIDs = append(termIDs, *r.WindowTermID)
		}
	}
	// Keep the first curriculum row per course that is not enrolled.
	var kept []plannedRow
	for _, p := range planned.rows {
		if enrolled[p.CourseID] {
			continue
		}
		if tid, ok := planned.termMap[p.TermNumber]; ok {
			termIDs = append(termIDs, tid)
		}
		kept = append(kept, p)
		courseIDs = append(courseIDs, p.CourseID)
	}

	courses, err := m.loadCourses(ctx, courseIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	terms, err := m.loadTerms(ctx, termIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	counts, err := m.lessonCounts(ctx, cl.UserID, courseIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	out := make([]any, 0, len(rows)+len(kept))
	for _, r := range rows {
		var t *termRow
		if r.WindowTermID != nil {
			t = terms[*r.WindowTermID]
		}
		w := m.termWindow(t)
		cnt := counts[r.CourseID]
		course := courses[r.CourseID]
		item := enrollmentJSON{
			ID: r.ID, UserID: r.UserID, CourseID: r.CourseID, TermID: r.TermID, CohortID: r.CohortID,
			ClassSectionID: r.ClassSectionID, OrderID: r.OrderID, EnrollmentSource: r.EnrollmentSource,
			EnrolledAt: httpx.NewTime(r.EnrolledAt), CreatedAt: httpx.NewTime(r.CreatedAt), UpdatedAt: httpx.NewTime(r.UpdatedAt),
			Course: course, Term: w.Term, StartsAt: w.StartsAt, EndsAt: w.EndsAt, WindowStatus: w.WindowStatus,
		}
		if course == nil {
			item.Progress = 0
		} else {
			item.Progress = enrollmentProgress(cnt[0], cnt[1])
			item.LessonsCount, item.CompletedLessons = cnt[0], cnt[1]
		}
		out = append(out, item)
	}

	seen := map[int64]bool{}
	for _, p := range kept {
		course := courses[p.CourseID]
		if course == nil || seen[p.CourseID] {
			continue
		}
		tid, ok := planned.termMap[p.TermNumber]
		if !ok {
			continue
		}
		t := terms[tid]
		if t == nil || windowStatus(t.StartDate, t.EndDate, m.now()) != "upcoming" {
			continue
		}
		seen[p.CourseID] = true
		w := m.termWindow(t)
		out = append(out, plannedJSON{
			ID: -p.CourseID, UserID: cl.UserID, CourseID: p.CourseID, TermID: t.ID, CohortID: planned.cohortID,
			EnrollmentSource: "academic", Course: course, IsPlanned: true,
			Term: w.Term, StartsAt: w.StartsAt, EndsAt: w.EndsAt, WindowStatus: w.WindowStatus,
		})
	}
	httpx.OK(c, out)
}

type plannedRow struct {
	CourseID   int64 `gorm:"column:course_id"`
	TermNumber int   `gorm:"column:term_number"`
}

type plannedSet struct {
	rows     []plannedRow
	termMap  map[int]int64 // term_number -> term_id
	cohortID *int64
}

// plannedRows loads the curriculum of the user's administrative class
// (ordered by term_number, position) and the class's term map.
func (m *Module) plannedRows(ctx context.Context, userID int64) (plannedSet, error) {
	set := plannedSet{termMap: map[int]int64{}}
	db := m.db.WithContext(ctx)
	var cls []struct {
		ID           int64  `gorm:"column:id"`
		CurriculumID *int64 `gorm:"column:curriculum_id"`
		CohortID     *int64 `gorm:"column:cohort_id"`
	}
	err := db.Raw(`SELECT ac.id, ac.curriculum_id, ac.cohort_id
		FROM users u JOIN administrative_classes ac ON ac.id = u.administrative_class_id
		WHERE u.id = ?`, userID).Scan(&cls).Error
	if err != nil {
		return set, fmt.Errorf("study: administrative class: %w", err)
	}
	if len(cls) == 0 || cls[0].CurriculumID == nil {
		return set, nil
	}
	set.cohortID = cls[0].CohortID
	var tm []struct {
		TermNumber int   `gorm:"column:term_number"`
		TermID     int64 `gorm:"column:term_id"`
	}
	if err := db.Raw(`SELECT term_number, term_id FROM administrative_class_terms
		WHERE administrative_class_id = ?`, cls[0].ID).Scan(&tm).Error; err != nil {
		return set, fmt.Errorf("study: class term map: %w", err)
	}
	for _, t := range tm {
		set.termMap[t.TermNumber] = t.TermID
	}
	if err := db.Raw(`SELECT course_id, term_number FROM curriculum_courses
		WHERE curriculum_id = ? ORDER BY term_number, position, id`, *cls[0].CurriculumID).Scan(&set.rows).Error; err != nil {
		return set, fmt.Errorf("study: curriculum courses: %w", err)
	}
	return set, nil
}
