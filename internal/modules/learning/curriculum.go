package learning

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

const sectionJSON = `jsonb_build_object('id', s.id, 'course_id', s.course_id, 'title', s.title, 'description', s.description,
  'position', s.position, 'opens_at', s.opens_at, 'closes_at', s.closes_at, 'lessons', coalesce((SELECT jsonb_agg(` + lessonFull + ` ORDER BY l.position, l.id) FROM lessons l WHERE l.section_id = s.id), '[]'::jsonb))`

var lessonTypes = map[string]bool{
	"video": true, "audio": true, "file": true, "page": true, "document": true, "quiz": true, "assignment": true, "forum": true,
	"survey": true, "scorm": true, "h5p": true, "virtual_class": true, "zoom": true, "meet": true, "live": true, "offline": true,
}

// ---- sections -----------------------------------------------------------------

func (m *Module) studioSections(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	rows, err := m.jsonRows("SELECT "+sectionJSON+" FROM sections s WHERE s.course_id = ? ORDER BY s.position, s.id", cr.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

type titleBody struct {
	Title string `json:"title" binding:"required,max=255"`
}

func (m *Module) createSection(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	var b titleBody
	if !httpx.BindJSON(c, &b) {
		return
	}
	var id int64
	err := m.db.Raw(`INSERT INTO sections (course_id, title, position)
		VALUES (?, ?, coalesce((SELECT max(position) FROM sections WHERE course_id = ?), 0) + 1) RETURNING id`,
		cr.ID, strings.TrimSpace(b.Title), cr.ID).Scan(&id).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+sectionJSON+" FROM sections s WHERE s.id = ?", id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "data", out)
}

// sectionOwner resolves a section id to its course and checks the caller may manage it.
func (m *Module) sectionOwner(c *gin.Context) (secID int64, ok bool) {
	secID, ok = idParam(c, "id")
	if !ok {
		return 0, false
	}
	var inst int64
	res := m.db.Raw("SELECT c.instructor_id FROM sections s JOIN courses c ON c.id = s.course_id WHERE s.id = ?", secID).Scan(&inst)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return 0, false
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return 0, false
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return 0, false
	}
	if inst != uid && !hasRole(roles, auth.RoleAdmin) {
		httpx.Forbidden(c, "")
		return 0, false
	}
	return secID, true
}

func (m *Module) updateSection(c *gin.Context) {
	id, ok := m.sectionOwner(c)
	if !ok {
		return
	}
	body, _ := c.GetRawData()
	var b struct {
		Title    *string `json:"title"`
		OpensAt  *string `json:"opens_at"`
		ClosesAt *string `json:"closes_at"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		httpx.Abort(c, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	keys := present(c, body)
	v := httpx.NewValidationError()
	if _, has := keys["title"]; has && (b.Title == nil || strings.TrimSpace(*b.Title) == "" || len(strings.TrimSpace(*b.Title)) > 255) {
		v.Add("title", "The title field is required.")
	}
	if _, has := keys["opens_at"]; has && !whenValid(b.OpensAt) {
		v.Add("opens_at", "The opens at is not a valid date.")
	}
	if _, has := keys["closes_at"]; has && !whenValid(b.ClosesAt) {
		v.Add("closes_at", "The closes at is not a valid date.")
	}
	if v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	sets, args := []string{}, []any{}
	if _, has := keys["title"]; has {
		sets, args = append(sets, "title = ?"), append(args, strings.TrimSpace(*b.Title))
	}
	if _, has := keys["opens_at"]; has {
		sets, args = append(sets, "opens_at = ?"), append(args, parseWhen(b.OpensAt))
	}
	if _, has := keys["closes_at"]; has {
		sets, args = append(sets, "closes_at = ?"), append(args, parseWhen(b.ClosesAt))
	}
	if len(sets) > 0 {
		if err := m.db.Exec("UPDATE sections SET "+strings.Join(sets, ", ")+" WHERE id = ?", append(args, id)...).Error; err != nil {
			if strings.Contains(err.Error(), "ck_sections_window") {
				err = httpx.NewValidationError().Add("closes_at", "The closes at must be after the opens at.")
			}
			httpx.Fail(c, err)
			return
		}
	}
	out, err := m.jsonOne("SELECT "+sectionJSON+" FROM sections s WHERE s.id = ?", id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "data", out)
}

func (m *Module) deleteSection(c *gin.Context) {
	id, ok := m.sectionOwner(c)
	if !ok {
		return
	}
	if err := m.db.Exec("DELETE FROM sections WHERE id = ?", id).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

func (m *Module) reorderSections(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	var b struct {
		Sections []struct {
			ID       int64 `json:"id"`
			Position int   `json:"position"`
		} `json:"sections"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		for _, s := range b.Sections {
			if s.Position < 0 {
				continue
			}
			if err := tx.Exec("UPDATE sections SET position = ? WHERE id = ? AND course_id = ?", s.Position, s.ID, cr.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Reordered."})
}

// ---- lessons ------------------------------------------------------------------

func (m *Module) lessonByID(courseID, id int64) (json.RawMessage, error) {
	return m.jsonOne("SELECT "+lessonFull+" FROM lessons l WHERE l.id = ? AND l.course_id = ?", id, courseID)
}

func (m *Module) createLesson(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	var b struct {
		Title     string `json:"title" binding:"required,max=255"`
		SectionID *int64 `json:"section_id"`
		Type      string `json:"type"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if b.Type == "" {
		b.Type = "video"
	}
	if !lessonTypes[b.Type] {
		httpx.Fail(c, httpx.NewValidationError().Add("type", "The selected type is invalid."))
		return
	}
	if b.SectionID != nil {
		var n int64
		if err := m.db.Table("sections").Where("id = ? AND course_id = ?", *b.SectionID, cr.ID).Count(&n).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
		if n == 0 {
			httpx.Fail(c, httpx.NewValidationError().Add("section_id", "The selected section is invalid."))
			return
		}
	}
	var id int64
	err := m.db.Raw(`INSERT INTO lessons (course_id, section_id, title, type, position)
		VALUES (?, ?, ?, ?, coalesce((SELECT max(position) FROM lessons WHERE course_id = ?), 0) + 1) RETURNING id`,
		cr.ID, b.SectionID, strings.TrimSpace(b.Title), b.Type, cr.ID).Scan(&id).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.lessonByID(cr.ID, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "lesson", out)
}

func (m *Module) updateLesson(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	lid, ok := idParam(c, "lid")
	if !ok {
		return
	}
	body, _ := c.GetRawData()
	keys := present(c, body)
	var b struct {
		Title       *string `json:"title"`
		Type        *string `json:"type"`
		Description *string `json:"description"`
		VideoURL    *string `json:"video_url"`
		Duration    *int    `json:"duration"`
		IsPreview   *bool   `json:"is_preview"`
		SectionID   *int64  `json:"section_id"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		httpx.Abort(c, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	v := httpx.NewValidationError()
	sets, args := []string{}, []any{}
	add := func(col string, val any) { sets = append(sets, col+" = ?"); args = append(args, val) }
	if _, has := keys["title"]; has {
		if b.Title == nil || strings.TrimSpace(*b.Title) == "" || len([]rune(*b.Title)) > 255 {
			v.Add("title", "The title must be between 1 and 255 characters.")
		} else {
			add("title", strings.TrimSpace(*b.Title))
		}
	}
	if _, has := keys["type"]; has && b.Type != nil {
		if !lessonTypes[*b.Type] {
			v.Add("type", "The selected type is invalid.")
		} else {
			add("type", *b.Type)
		}
	}
	if _, has := keys["description"]; has {
		add("description", nullStr(b.Description))
	}
	if _, has := keys["video_url"]; has {
		if b.VideoURL != nil && len(*b.VideoURL) > 2048 {
			v.Add("video_url", "The video url must not be greater than 2048 characters.")
		} else {
			add("video_path", nullStr(b.VideoURL))
			add("video_status", map[bool]string{true: "ready", false: "pending"}[b.VideoURL != nil && strings.TrimSpace(*b.VideoURL) != ""])
		}
	}
	if _, has := keys["duration"]; has && b.Duration != nil {
		if *b.Duration < 0 {
			v.Add("duration", "The duration must be at least 0.")
		} else {
			add("duration_seconds", *b.Duration)
		}
	}
	if _, has := keys["is_preview"]; has && b.IsPreview != nil {
		add("is_preview", *b.IsPreview)
	}
	if _, has := keys["section_id"]; has {
		if b.SectionID != nil {
			var n int64
			_ = m.db.Table("sections").Where("id = ? AND course_id = ?", *b.SectionID, cr.ID).Count(&n).Error
			if n == 0 {
				v.Add("section_id", "The selected section is invalid.")
			}
		}
		add("section_id", b.SectionID)
	}
	if v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	if len(sets) > 0 {
		args = append(args, lid, cr.ID)
		res := m.db.Exec("UPDATE lessons SET "+strings.Join(sets, ", ")+" WHERE id = ? AND course_id = ?", args...)
		if res.Error != nil {
			httpx.Fail(c, res.Error)
			return
		}
		if res.RowsAffected == 0 {
			httpx.NotFound(c, "")
			return
		}
	}
	out, err := m.lessonByID(cr.ID, lid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "lesson", out)
}

func (m *Module) deleteLesson(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	lid, ok := idParam(c, "lid")
	if !ok {
		return
	}
	res := m.db.Exec("DELETE FROM lessons WHERE id = ? AND course_id = ?", lid, cr.ID)
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

func (m *Module) reorderLessons(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	var b struct {
		Order []int64 `json:"order"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		for i, id := range b.Order {
			if err := tx.Exec("UPDATE lessons SET position = ? WHERE id = ? AND course_id = ?", i+1, id, cr.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Reordered."})
}

// ---- students (roster) ----------------------------------------------------------

const (
	studentDone  = "(SELECT count(*) FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id WHERE p.user_id = e.user_id AND l.course_id = e.course_id AND p.completed_at IS NOT NULL)"
	studentTotal = "(SELECT count(*) FROM lessons l WHERE l.course_id = e.course_id)"
	studentFrom  = "enrollments e JOIN users u ON u.id = e.user_id"
)

// studentWhere builds the roster filter (q, progress) shared by the list and the CSV export.
func studentWhere(c *gin.Context, courseID int64) (string, []any) {
	done, total := studentDone, studentTotal
	conds, args := []string{"e.course_id = ?"}, []any{courseID}
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		conds = append(conds, "(u.name ILIKE ? OR u.email::text ILIKE ? OR u.student_code::text ILIKE ?)")
		args = append(args, likeArg(s), likeArg(s), likeArg(s))
	}
	switch c.Query("progress") {
	case "not_started":
		conds = append(conds, done+" = 0")
	case "in_progress":
		conds = append(conds, done+" > 0 AND "+done+" < "+total)
	case "completed":
		conds = append(conds, total+" > 0 AND "+done+" >= "+total)
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// exportStudents streams the roster as a UTF-8 CSV (with BOM so Excel reads Vietnamese correctly).
func (m *Module) exportStudents(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	where, args := studentWhere(c, cr.ID)
	rows, err := m.db.Raw(`SELECT u.name, u.email::text AS email, coalesce(u.student_code::text, '') AS code, e.enrollment_source AS source,
		to_char(e.enrolled_at, 'YYYY-MM-DD') AS enrolled, `+studentDone+` AS done, `+studentTotal+` AS total
		FROM `+studentFrom+where+` ORDER BY e.enrolled_at DESC`, args...).Rows()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	defer rows.Close()
	header := []string{"Họ tên", "Email", "Mã sinh viên", "Nguồn", "Ngày ghi danh", "Bài đã học", "Tổng số bài", "Tiến độ (%)"}
	sources := map[string]string{"marketplace": "Tự đăng ký", "manual": "Thêm thủ công", "academic": "Lớp học phần", "excel_import": "Nhập từ Excel"}
	records := [][]string{}
	for rows.Next() {
		var name, email, code, source, enrolled string
		var done, total int
		if err := rows.Scan(&name, &email, &code, &source, &enrolled, &done, &total); err != nil {
			httpx.Fail(c, err)
			return
		}
		pct := 0
		if total > 0 {
			pct = done * 100 / total
		}
		if v, ok := sources[source]; ok {
			source = v
		}
		records = append(records, []string{name, email, code, source, enrolled, strconv.Itoa(done), strconv.Itoa(total), strconv.Itoa(pct)})
	}
	if c.Query("format") == "csv" {
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="hoc-vien-khoa-%d.csv"`, cr.ID))
		c.Status(http.StatusOK)
		_, _ = c.Writer.WriteString("ï»¿")
		w := csv.NewWriter(c.Writer)
		_ = w.Write(header)
		_ = w.WriteAll(records)
		return
	}
	f := excelize.NewFile()
	defer f.Close()
	sheet := "Học viên"
	_ = f.SetSheetName("Sheet1", sheet)
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Color: []string{"#E8EEF7"}, Pattern: 1}})
	for i, h := range header {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	_ = f.SetCellStyle(sheet, "A1", "H1", bold)
	for r, rec := range records {
		for i, v := range rec {
			cell, _ := excelize.CoordinatesToCellName(i+1, r+2)
			if i >= 5 {
				n, _ := strconv.Atoi(v)
				_ = f.SetCellValue(sheet, cell, n)
			} else {
				_ = f.SetCellValue(sheet, cell, v)
			}
		}
	}
	for col, w := range map[string]float64{"A": 28, "B": 32, "C": 16, "D": 18, "E": 16, "F": 12, "G": 12, "H": 12} {
		_ = f.SetColWidth(sheet, col, col, w)
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="hoc-vien-khoa-%d.xlsx"`, cr.ID))
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf.Bytes())
}

func (m *Module) listStudents(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	page, per := pageParams(c, 10, 100)
	where, args := studentWhere(c, cr.ID)
	done, total := studentDone, studentTotal
	from := studentFrom
	var count int64
	if err := m.db.Raw("SELECT count(*) FROM "+from+where, args...).Scan(&count).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	order := "e.enrolled_at DESC"
	if strings.TrimPrefix(c.Query("sort"), "-") == "name" {
		order = "u.name " + map[bool]string{true: "DESC", false: "ASC"}[strings.HasPrefix(c.Query("sort"), "-")]
	}
	rows, err := m.jsonRows(`SELECT jsonb_build_object('enrollment_id', e.id,
		'user', jsonb_build_object('id', u.id, 'name', u.name, 'email', u.email, 'avatar', u.avatar_path, 'student_code', u.student_code),
		'source', e.enrollment_source, 'enrolled_at', e.enrolled_at,
		'completed_lessons', `+done+`, 'total_lessons', `+total+`,
		'progress_percent', CASE WHEN `+total+` = 0 THEN 0 ELSE round(100.0 * `+done+` / `+total+`) END,
		'progress_state', CASE WHEN `+done+` = 0 THEN 'not_started' WHEN `+done+` >= `+total+` THEN 'completed' ELSE 'in_progress' END,
		'last_activity_at', (SELECT max(p.last_watched_at) FROM lesson_progress p JOIN lessons l ON l.id = p.lesson_id WHERE p.user_id = e.user_id AND l.course_id = e.course_id))
		FROM `+from+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, count, page, per)
}

func (m *Module) addStudents(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	var b struct {
		Identifiers []string `json:"identifiers"`
	}
	if fh, err := c.FormFile("file"); err == nil {
		ids, ferr := identifiersFromXLSX(fh)
		if ferr != nil {
			httpx.Fail(c, httpx.NewValidationError().Add("file", ferr.Error()))
			return
		}
		b.Identifiers = ids
	} else if !httpx.BindJSON(c, &b) {
		return
	}
	if len(b.Identifiers) == 0 || len(b.Identifiers) > 500 {
		httpx.Fail(c, httpx.NewValidationError().Add("identifiers", "Provide between 1 and 500 emails or student codes."))
		return
	}
	enrolled, already := 0, 0
	notFound := []string{}
	for _, raw := range b.Identifiers {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		var uid int64
		if err := m.db.Raw("SELECT id FROM users WHERE deleted_at IS NULL AND (email = ? OR student_code = ?) LIMIT 1", id, id).Scan(&uid).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
		if uid == 0 {
			notFound = append(notFound, id)
			continue
		}
		res := m.db.Exec(`INSERT INTO enrollments (user_id, course_id, enrollment_source) VALUES (?, ?, 'manual') ON CONFLICT (user_id, course_id) DO NOTHING`, uid, cr.ID)
		if res.Error != nil {
			httpx.Fail(c, res.Error)
			return
		}
		if res.RowsAffected == 0 {
			already++
		} else {
			enrolled++
		}
	}
	c.JSON(http.StatusOK, gin.H{"enrolled": enrolled, "already": already, "not_found": notFound})
}

func (m *Module) removeStudent(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	uid, err := strconv.ParseInt(c.Param("uid"), 10, 64)
	if err != nil || uid < 1 {
		httpx.NotFound(c, "")
		return
	}
	res := m.db.Exec("DELETE FROM enrollments WHERE course_id = ? AND user_id = ?", cr.ID, uid)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Removed."})
}

// identifiersFromXLSX reads emails / student codes from the first column of an uploaded Excel file.
func identifiersFromXLSX(fh *multipart.FileHeader) ([]string, error) {
	if !strings.HasSuffix(strings.ToLower(fh.Filename), ".xlsx") {
		return nil, fmt.Errorf("Chỉ hỗ trợ tệp Excel (.xlsx).")
	}
	if fh.Size > 5<<20 {
		return nil, fmt.Errorf("Tệp quá lớn (tối đa 5 MB).")
	}
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	x, err := excelize.OpenReader(f)
	if err != nil {
		return nil, fmt.Errorf("Không đọc được tệp Excel.")
	}
	defer x.Close()
	rows, err := x.GetRows(x.GetSheetName(0))
	if err != nil {
		return nil, err
	}
	var ids []string
	for i, r := range rows {
		if len(r) == 0 {
			continue
		}
		v := strings.TrimSpace(r[0])
		l := strings.ToLower(v)
		if v == "" || (i == 0 && (strings.Contains(l, "email") || strings.Contains(l, "mã") || strings.Contains(l, "ma sinh"))) {
			continue
		}
		ids = append(ids, v)
	}
	return ids, nil
}

// studentTemplate serves a sample Excel file for adding students in bulk.
func (m *Module) studentTemplate(c *gin.Context) {
	if _, _, ok := m.manageCourse(c); !ok {
		return
	}
	f := excelize.NewFile()
	defer f.Close()
	sheet := "Học viên"
	_ = f.SetSheetName("Sheet1", sheet)
	_ = f.SetCellValue(sheet, "A1", "Email hoặc mã sinh viên")
	_ = f.SetCellValue(sheet, "A2", "sv001@example.com")
	_ = f.SetCellValue(sheet, "A3", "B21DCCN001")
	_ = f.SetColWidth(sheet, "A", "A", 34)
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	_ = f.SetCellStyle(sheet, "A1", "A1", bold)
	buf, err := f.WriteToBuffer()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="mau-them-hoc-vien.xlsx"`)
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf.Bytes())
}
