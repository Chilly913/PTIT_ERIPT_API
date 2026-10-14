package catalog

import (
	"context"
	"strings"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
)

// where accumulates SQL conditions and their arguments.
type where struct {
	conds []string
	args  []any
}

func (w *where) add(cond string, args ...any) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

func (w *where) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

// likeTerm escapes a search term for LIKE (SearchQuery::like): \ % _ are literal.
func likeTerm(term string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(strings.TrimSpace(term))
}

// foldLike is the accent/case-insensitive LIKE of SearchQuery::like
// (learning_search_fold, served by the trigram indexes on courses).
func foldLike(col string) string {
	return "learning_search_fold(" + col + ") LIKE '%' || learning_search_fold(?) || '%'"
}

// findCourse loads the full course row.
func (m *Module) findCourse(ctx context.Context, id int64) (courseRow, error) {
	var rows []courseRow
	if err := m.db.WithContext(ctx).Raw(`SELECT `+courseCols+` FROM courses c WHERE c.id = ?`, id).Scan(&rows).Error; err != nil {
		return courseRow{}, fmtErr("find course", err)
	}
	if len(rows) == 0 {
		return courseRow{}, learning.ErrCourseNotFound
	}
	return rows[0], nil
}

// instructors loads users as {id, name, avatar} keyed by id.
func (m *Module) users(ctx context.Context, ids []int64) (map[int64]*userBrief, error) {
	out := map[int64]*userBrief{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID         int64   `gorm:"column:id"`
		Name       string  `gorm:"column:name"`
		AvatarPath *string `gorm:"column:avatar_path"`
	}
	if err := m.db.WithContext(ctx).Raw(`SELECT id, name, avatar_path FROM users WHERE id IN ?`, uniq(ids)).Scan(&rows).Error; err != nil {
		return nil, fmtErr("load users", err)
	}
	for _, r := range rows {
		out[r.ID] = &userBrief{ID: r.ID, Name: r.Name, Avatar: m.media.Public(r.AvatarPath)}
	}
	return out, nil
}

// categoryBriefs loads categories as {id, name, slug} keyed by id.
func (m *Module) categoryBriefs(ctx context.Context, ids []int64) (map[int64]*categoryBrief, error) {
	out := map[int64]*categoryBrief{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []categoryBrief
	if err := m.db.WithContext(ctx).Raw(`SELECT id, name, slug FROM categories WHERE id IN ?`, uniq(ids)).Scan(&rows).Error; err != nil {
		return nil, fmtErr("load categories", err)
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}

// courseRelations loads instructor and category of the given courses.
func (m *Module) courseRelations(ctx context.Context, rows []courseRow) (map[int64]*userBrief, map[int64]*categoryBrief, error) {
	var uids, cids []int64
	for _, r := range rows {
		uids = append(uids, r.InstructorID)
		if r.CategoryID != nil {
			cids = append(cids, *r.CategoryID)
		}
	}
	us, err := m.users(ctx, uids)
	if err != nil {
		return nil, nil, err
	}
	cs, err := m.categoryBriefs(ctx, cids)
	if err != nil {
		return nil, nil, err
	}
	return us, cs, nil
}

func categoryOf(cs map[int64]*categoryBrief, id *int64) *categoryBrief {
	if id == nil {
		return nil
	}
	return cs[*id]
}

// courseCounts returns lessons_count and enrollments_count of one course.
func (m *Module) courseCounts(ctx context.Context, id int64) (lessons, enrollments int64, err error) {
	var r struct {
		Lessons     int64 `gorm:"column:lessons"`
		Enrollments int64 `gorm:"column:enrollments"`
	}
	err = m.db.WithContext(ctx).Raw(`SELECT
  (SELECT count(*) FROM lessons WHERE course_id = ?) AS lessons,
  (SELECT count(*) FROM enrollments WHERE course_id = ?) AS enrollments`, id, id).Scan(&r).Error
	if err != nil {
		return 0, 0, fmtErr("course counts", err)
	}
	return r.Lessons, r.Enrollments, nil
}

// courseWithCounts builds the update / my-courses shape for one course.
func (m *Module) courseWithCounts(ctx context.Context, row courseRow) (courseWithCounts, error) {
	lessons, enrollments, err := m.courseCounts(ctx, row.ID)
	if err != nil {
		return courseWithCounts{}, err
	}
	us, cs, err := m.courseRelations(ctx, []courseRow{row})
	if err != nil {
		return courseWithCounts{}, err
	}
	return courseWithCounts{courseAttrs: m.courseAttrs(row), LessonsCount: lessons, EnrollmentsCount: enrollments,
		Instructor: us[row.InstructorID], Category: categoryOf(cs, row.CategoryID)}, nil
}

// lessonsOf loads lessons matching cond, ordered like the `lessons` relation.
func (m *Module) lessonsOf(ctx context.Context, cond string, args ...any) ([]lessonRow, error) {
	var rows []lessonRow
	err := m.db.WithContext(ctx).Raw(`SELECT `+lessonCols+` FROM lessons l WHERE `+cond+` ORDER BY l.position, l.id`, args...).Scan(&rows).Error
	if err != nil {
		return nil, fmtErr("load lessons", err)
	}
	return rows, nil
}

// findLesson loads one full lesson row.
func (m *Module) findLesson(ctx context.Context, id int64) (lessonRow, error) {
	rows, err := m.lessonsOf(ctx, "l.id = ?", id)
	if err != nil {
		return lessonRow{}, err
	}
	if len(rows) == 0 {
		return lessonRow{}, learning.ErrLessonNotFound
	}
	return rows[0], nil
}

// sectionsOf loads the sections of a course ordered by position.
func (m *Module) sectionsOf(ctx context.Context, courseID int64) ([]sectionRow, error) {
	var rows []sectionRow
	err := m.db.WithContext(ctx).Raw(`SELECT `+sectionCols+` FROM sections s WHERE s.course_id = ? ORDER BY s.position, s.id`, courseID).Scan(&rows).Error
	if err != nil {
		return nil, fmtErr("load sections", err)
	}
	return rows, nil
}

func (m *Module) findSection(ctx context.Context, id int64) (sectionRow, error) {
	var rows []sectionRow
	if err := m.db.WithContext(ctx).Raw(`SELECT `+sectionCols+` FROM sections s WHERE s.id = ?`, id).Scan(&rows).Error; err != nil {
		return sectionRow{}, fmtErr("find section", err)
	}
	if len(rows) == 0 {
		return sectionRow{}, learning.ErrSectionNotFound
	}
	return rows[0], nil
}

// ---------------------------------------------------------------------------
// lesson content relations

// attachmentsOf loads attachments of the lessons, keyed by lesson id.
func (m *Module) attachmentsOf(ctx context.Context, lessonIDs []int64) (map[int64][]attachmentJSON, error) {
	out := map[int64][]attachmentJSON{}
	if len(lessonIDs) == 0 {
		return out, nil
	}
	var rows []attachmentRow
	err := m.db.WithContext(ctx).Raw(`SELECT id, lesson_id, original_name, file_path, size_bytes, mime_type, created_at, updated_at
FROM lesson_attachments WHERE lesson_id IN ? ORDER BY id`, uniq(lessonIDs)).Scan(&rows).Error
	if err != nil {
		return nil, fmtErr("load attachments", err)
	}
	for _, r := range rows {
		out[r.LessonID] = append(out[r.LessonID], attachmentOf(r))
	}
	return out, nil
}

// hasOne loads the first child row per lesson of a 1:1 relation.
func hasOne[T any](m *Module, ctx context.Context, table, cols string, lessonIDs []int64, lessonID func(*T) int64) (map[int64]*T, error) {
	out := map[int64]*T{}
	if len(lessonIDs) == 0 {
		return out, nil
	}
	var rows []T
	err := m.db.WithContext(ctx).Raw(`SELECT `+cols+` FROM `+table+` WHERE lesson_id IN ? ORDER BY id`, uniq(lessonIDs)).Scan(&rows).Error
	if err != nil {
		return nil, fmtErr("load "+table, err)
	}
	for i := range rows {
		id := lessonID(&rows[i])
		if _, seen := out[id]; !seen {
			out[id] = &rows[i]
		}
	}
	return out, nil
}

func (m *Module) virtualClassesOf(ctx context.Context, ids []int64) (map[int64]*virtualClassJSON, error) {
	return hasOne(m, ctx, "virtual_classes", virtualClassCols, ids, func(v *virtualClassJSON) int64 { return v.LessonID })
}

func (m *Module) scormPackagesOf(ctx context.Context, ids []int64) (map[int64]*scormPackageJSON, error) {
	out, err := hasOne(m, ctx, "scorm_packages", scormPackageCols, ids, func(v *scormPackageJSON) int64 { return v.LessonID })
	if err != nil {
		return nil, err
	}
	for _, p := range out {
		p.EntryURL = m.scormEntryURL(p.EntryURL)
	}
	return out, nil
}

// scormEntryURL resolves entry_path: object keys (legacy "scorm/<uuid>/...")
// become /minio/<bucket>/<key>; absolute URLs (H5P embeds) are kept.
func (m *Module) scormEntryURL(p *string) *string {
	if p == nil || strings.HasPrefix(*p, "http") || strings.HasPrefix(*p, "/minio/") {
		return p
	}
	return m.media.Public(p)
}

func (m *Module) assignmentsOf(ctx context.Context, ids []int64) (map[int64]*assignmentJSON, error) {
	return hasOne(m, ctx, "lesson_assignments", assignmentCols, ids, func(v *assignmentJSON) int64 { return v.LessonID })
}

func (m *Module) offlineSessionsOf(ctx context.Context, ids []int64) (map[int64]*offlineSessionJSON, error) {
	return hasOne(m, ctx, "offline_sessions", offlineSessionCols, ids, func(v *offlineSessionJSON) int64 {
		if v.LessonID == nil {
			return 0
		}
		return *v.LessonID
	})
}

func (m *Module) quizzesOf(ctx context.Context, ids []int64) (map[int64]*quizRow, error) {
	return hasOne(m, ctx, "quizzes", quizCols, ids, func(v *quizRow) int64 {
		if v.LessonID == nil {
			return 0
		}
		return *v.LessonID
	})
}

// exists reports whether a row with id exists in table (exists:table,id).
func (m *Module) exists(ctx context.Context, table string, id int64) (bool, error) {
	var n int64
	if err := m.db.WithContext(ctx).Raw(`SELECT count(*) FROM `+table+` WHERE id = ?`, id).Scan(&n).Error; err != nil {
		return false, fmtErr("exists "+table, err)
	}
	return n > 0, nil
}

func uniq(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func lessonIDs(rows []lessonRow) []int64 {
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}
