package admin

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/textutil"
)

// Bulk import of students from an Excel file: one row per student, optionally placed into an administrative class
// (by class code), which also fills the student's program, cohort, major and unit from that class.
//
//   POST /admin/users/import           multipart: file, mode=create|update, dry_run=1, send_invite=1, default_password=...
//   GET  /admin/users/import-template  an .xlsx to fill in
//
// All-or-nothing: when any row is invalid nothing is written. A dry run reports what an import would do.

const (
	maxImportRows  = 2000
	maxImportBytes = 5 << 20
)

// importCols maps our fields to the header names accepted in the file (compared after removing accents and spaces).
var importCols = map[string][]string{
	"code":     {"ma-sinh-vien", "mssv", "ma-sv", "student-code", "msv"},
	"name":     {"ho-va-ten", "ho-ten", "ho-ten-sinh-vien", "ten", "name", "full-name"},
	"email":    {"email", "e-mail", "thu-dien-tu", "dia-chi-email"},
	"phone":    {"so-dien-thoai", "dien-thoai", "sdt", "phone"},
	"dob":      {"ngay-sinh", "ngay-thang-nam-sinh", "dob", "date-of-birth"},
	"gender":   {"gioi-tinh", "gender"},
	"class":    {"ma-lop", "lop", "ma-lop-hanh-chinh", "lop-hanh-chinh", "class", "class-code"},
	"idcard":   {"cccd", "so-cccd", "cmnd", "so-cmnd", "can-cuoc-cong-dan", "id-card"},
	"hometown": {"que-quan", "hometown"},
	"password": {"mat-khau", "password"},
}

var importHeaders = []string{"Mã sinh viên", "Họ và tên", "Email", "Số điện thoại", "Ngày sinh", "Giới tính", "Mã lớp hành chính", "CCCD", "Quê quán", "Mật khẩu"}

type importRow struct {
	Row                                                       int
	Code, Name, Email, Phone, Gender, ClassCode, IDCard, Home string
	Password                                                  string
	DOB                                                       *time.Time
	Err                                                       string

	class    *classInfo
	existing *existingUser
}

type classInfo struct {
	ID            int64
	Code          string
	Name          string
	Status        string
	InstitutionID int64
	UnitID        *int64
	ProgramID     int64
	MajorID       *int64
	CohortID      *int64
	AdvisorID     *int64
}

type existingUser struct {
	ID          int64
	Email       string
	StudentCode *string
	Deleted     bool
}

func headerKey(s string) string { return textutil.Slugify(strings.TrimSpace(s)) }

// readImportSheet turns the first sheet into rows. Dates are read raw (Excel serial numbers) so they survive any display format.
func readImportSheet(data []byte) ([]importRow, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("Không đọc được tệp. Hãy dùng tệp Excel .xlsx (tải file mẫu để xem định dạng).")
	}
	defer f.Close()
	rows, err := f.GetRows(f.GetSheetName(0), excelize.Options{RawCellValue: true})
	if err != nil || len(rows) < 2 {
		return nil, fmt.Errorf("Tệp không có dòng dữ liệu nào.")
	}
	idx := map[string]int{}
	for i, h := range rows[0] {
		k := headerKey(h)
		for field, names := range importCols {
			for _, n := range names {
				if k == n {
					if _, dup := idx[field]; !dup {
						idx[field] = i
					}
				}
			}
		}
	}
	var missing []string
	for _, f := range []struct{ key, label string }{{"code", "Mã sinh viên"}, {"name", "Họ và tên"}, {"email", "Email"}} {
		if _, ok := idx[f.key]; !ok {
			missing = append(missing, f.label)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("Dòng 1 phải là tiêu đề và có các cột: %s.", strings.Join(missing, ", "))
	}
	cell := func(r []string, key string) string {
		if i, ok := idx[key]; ok && i < len(r) {
			return strings.TrimSpace(r[i])
		}
		return ""
	}
	var out []importRow
	for n, r := range rows[1:] {
		if strings.TrimSpace(strings.Join(r, "")) == "" {
			continue
		}
		row := importRow{Row: n + 2, Code: cell(r, "code"), Name: cell(r, "name"), Email: cell(r, "email"), Phone: cell(r, "phone"),
			Gender: cell(r, "gender"), ClassCode: cell(r, "class"), IDCard: cell(r, "idcard"), Home: cell(r, "hometown"), Password: cell(r, "password")}
		if raw := cell(r, "dob"); raw != "" {
			d, ok := parseDOB(raw)
			if !ok {
				row.Err = "Ngày sinh không hợp lệ (dùng dd/mm/yyyy): " + raw
			}
			row.DOB = d
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Tệp không có dòng dữ liệu nào.")
	}
	return out, nil
}

func parseDOB(s string) (*time.Time, bool) {
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 1000 && f < 80000 { // Excel serial date
		t, err := excelize.ExcelDateToTime(f, false)
		if err != nil {
			return nil, false
		}
		return &t, true
	}
	for _, layout := range []string{"02/01/2006", "2/1/2006", "02-01-2006", "2-1-2006", "2006-01-02", "02.01.2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, true
		}
	}
	return nil, false
}

func parseGender(s string) (string, bool) {
	switch headerKey(s) {
	case "":
		return "", true
	case "nam", "male", "m":
		return "male", true
	case "nu", "female", "f":
		return "female", true
	case "khac", "other":
		return "other", true
	}
	return "", false
}

func validEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" {
		return false
	}
	at := strings.LastIndex(s, "@")
	return at > 0 && strings.Contains(s[at+1:], ".")
}

// checkRows validates the rows against each other and against the database, and attaches class and existing user.
func (m *Module) checkRows(rows []importRow, mode string) error {
	seenCode, seenEmail, seenID := map[string]int{}, map[string]int{}, map[string]int{}
	var codes, emails, classes []string
	for i := range rows {
		r := &rows[i]
		fail := func(msg string) {
			if r.Err == "" {
				r.Err = msg
			}
		}
		switch {
		case r.Code == "":
			fail("Thiếu mã sinh viên")
		case utf8.RuneCountInString(r.Code) > 50:
			fail("Mã sinh viên quá dài (tối đa 50 ký tự)")
		}
		switch {
		case r.Name == "":
			fail("Thiếu họ và tên")
		case utf8.RuneCountInString(r.Name) > 255:
			fail("Họ và tên quá dài")
		}
		switch {
		case r.Email == "":
			fail("Thiếu email")
		case !validEmail(r.Email) || len(r.Email) > 255:
			fail("Email không hợp lệ: " + r.Email)
		}
		if utf8.RuneCountInString(r.Phone) > 32 {
			fail("Số điện thoại quá dài")
		}
		if utf8.RuneCountInString(r.IDCard) > 20 {
			fail("CCCD quá dài (tối đa 20 ký tự)")
		}
		if g, ok := parseGender(r.Gender); !ok {
			fail("Giới tính không hợp lệ (Nam, Nữ hoặc Khác): " + r.Gender)
		} else {
			r.Gender = g
		}
		if r.DOB != nil && (r.DOB.After(time.Now()) || r.DOB.Year() < 1900) {
			fail("Ngày sinh không hợp lệ")
		}
		if r.Password != "" && (len(r.Password) < 8 || len(r.Password) > 72) {
			fail("Mật khẩu phải từ 8 đến 72 ký tự")
		}
		for _, d := range []struct {
			seen map[string]int
			key  string
			what string
		}{{seenCode, strings.ToLower(r.Code), "Mã sinh viên"}, {seenEmail, strings.ToLower(r.Email), "Email"}, {seenID, strings.ToLower(r.IDCard), "CCCD"}} {
			if d.key == "" {
				continue
			}
			if first, dup := d.seen[d.key]; dup {
				fail(fmt.Sprintf("%s trùng với dòng %d trong tệp", d.what, first))
			} else {
				d.seen[d.key] = r.Row
			}
		}
		if r.Code != "" {
			codes = append(codes, strings.ToLower(r.Code))
		}
		if r.Email != "" {
			emails = append(emails, strings.ToLower(r.Email))
		}
		if r.ClassCode != "" {
			classes = append(classes, strings.ToLower(r.ClassCode))
		}
	}

	classByCode := map[string]*classInfo{}
	if len(classes) > 0 {
		var cs []classInfo
		if err := m.db.Raw(`SELECT id, code::text AS code, name, status, institution_id, unit_id, program_id, major_id, cohort_id, advisor_id
			FROM administrative_classes WHERE lower(code::text) IN ?`, classes).Scan(&cs).Error; err != nil {
			return err
		}
		for i := range cs {
			classByCode[strings.ToLower(cs[i].Code)] = &cs[i]
		}
	}
	type dbUser struct {
		ID          int64
		Email       string
		StudentCode *string
		DeletedAt   *time.Time
	}
	var found []dbUser
	if len(codes) > 0 || len(emails) > 0 {
		if err := m.db.Raw(`SELECT id, email::text AS email, student_code::text AS student_code, deleted_at FROM users
			WHERE lower(student_code::text) IN ? OR lower(email::text) IN ?`, append(codes, ""), append(emails, "")).Scan(&found).Error; err != nil {
			return err
		}
	}
	byCode, byEmail := map[string]*dbUser{}, map[string]*dbUser{}
	for i := range found {
		if found[i].StudentCode != nil {
			byCode[strings.ToLower(*found[i].StudentCode)] = &found[i]
		}
		byEmail[strings.ToLower(found[i].Email)] = &found[i]
	}
	for i := range rows {
		r := &rows[i]
		if r.ClassCode != "" {
			c, ok := classByCode[strings.ToLower(r.ClassCode)]
			switch {
			case !ok:
				if r.Err == "" {
					r.Err = "Không có lớp hành chính mã " + r.ClassCode
				}
			case c.Status != "active":
				if r.Err == "" {
					r.Err = "Lớp " + c.Code + " không còn hoạt động"
				}
			default:
				r.class = c
			}
		}
		if r.Err != "" {
			continue
		}
		byC, byE := byCode[strings.ToLower(r.Code)], byEmail[strings.ToLower(r.Email)]
		switch {
		case byC == nil && byE == nil:
		case mode == "update" && byC != nil && (byE == nil || byE.ID == byC.ID):
			if byC.DeletedAt != nil {
				r.Err = "Tài khoản mã " + r.Code + " đã bị xóa, không cập nhật được"
			} else {
				r.existing = &existingUser{ID: byC.ID, Email: byC.Email, StudentCode: byC.StudentCode}
			}
		case byC != nil:
			r.Err = "Mã sinh viên " + r.Code + " đã tồn tại"
			if mode == "update" {
				r.Err = "Email " + r.Email + " đang được tài khoản khác dùng"
			}
		default:
			r.Err = "Email " + r.Email + " đã được dùng"
		}
	}
	if len(rows) > 0 {
		var idc []string
		for _, r := range rows {
			if r.IDCard != "" && r.Err == "" {
				idc = append(idc, strings.ToLower(r.IDCard))
			}
		}
		if len(idc) > 0 {
			var taken []struct {
				IDCard string
				UserID int64
			}
			if err := m.db.Raw("SELECT lower(id_card_number) AS id_card, id AS user_id FROM users WHERE lower(id_card_number) IN ?", idc).Scan(&taken).Error; err != nil {
				return err
			}
			owner := map[string]int64{}
			for _, t := range taken {
				owner[t.IDCard] = t.UserID
			}
			for i := range rows {
				r := &rows[i]
				if uid, used := owner[strings.ToLower(r.IDCard)]; used && r.Err == "" && (r.existing == nil || r.existing.ID != uid) {
					r.Err = "CCCD " + r.IDCard + " đã được dùng"
				}
			}
		}
	}
	return nil
}

type rowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

func (m *Module) importStudents(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "Hãy chọn tệp Excel (.xlsx)."))
		return
	}
	if fh.Size > maxImportBytes || !strings.HasSuffix(strings.ToLower(fh.Filename), ".xlsx") {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "Chỉ nhận tệp .xlsx, tối đa 5 MB."))
		return
	}
	src, err := fh.Open()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	defer src.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(src); err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := readImportSheet(buf.Bytes())
	if err != nil {
		httpx.Fail(c, httpx.NewValidationError().Add("file", err.Error()))
		return
	}
	if len(rows) > maxImportRows {
		httpx.Fail(c, httpx.NewValidationError().Add("file", fmt.Sprintf("Tối đa %d sinh viên mỗi lần nhập.", maxImportRows)))
		return
	}
	mode := c.DefaultPostForm("mode", "create")
	if mode != "create" && mode != "update" {
		httpx.Fail(c, httpx.NewValidationError().Add("mode", "The mode must be create or update."))
		return
	}
	dryRun := c.PostForm("dry_run") == "1" || c.PostForm("dry_run") == "true"
	invite := c.PostForm("send_invite") == "1" || c.PostForm("send_invite") == "true"
	defaultPW := c.PostForm("default_password")
	if defaultPW != "" && (len(defaultPW) < 8 || len(defaultPW) > 72) {
		httpx.Fail(c, httpx.NewValidationError().Add("default_password", "The password must be between 8 and 72 characters."))
		return
	}
	if err := m.checkRows(rows, mode); err != nil {
		httpx.Fail(c, err)
		return
	}

	errs := []rowError{}
	create, update := 0, 0
	perClass := map[string]int{}
	for _, r := range rows {
		if r.Err != "" {
			errs = append(errs, rowError{Row: r.Row, Message: r.Err})
			continue
		}
		if r.existing != nil {
			update++
		} else {
			create++
		}
		label := "(chưa xếp lớp)"
		if r.class != nil {
			label = r.class.Code
		}
		perClass[label]++
	}
	summary := gin.H{"total": len(rows), "valid": len(rows) - len(errs), "create": create, "update": update, "row_errors": errs, "by_class": perClass}
	if dryRun {
		summary["dry_run"] = true
		c.JSON(http.StatusOK, summary)
		return
	}
	if len(errs) > 0 {
		summary["message"] = "Some rows are invalid."
		c.JSON(http.StatusUnprocessableEntity, summary)
		return
	}

	var sharedHash *string
	if defaultPW != "" {
		h, err := auth.HashPassword(defaultPW)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		sharedHash = &h
	}
	var newIDs []int64
	err = m.db.Transaction(func(tx *gorm.DB) error {
		for i := range rows {
			id, created, err := m.saveStudent(tx, &rows[i], sharedHash)
			if err != nil {
				return fmt.Errorf("dòng %d: %w", rows[i].Row, err)
			}
			if created {
				newIDs = append(newIDs, id)
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	summary["created"], summary["updated"] = len(newIDs), update
	if invite && m.resetter != nil && len(newIDs) > 0 {
		go func(ids []int64) { // e-mail a "set your password" link to every new account; failures do not undo the import
			for _, id := range ids {
				_ = m.resetter.SendPasswordReset(context.Background(), id)
			}
		}(newIDs)
		summary["invites"] = len(newIDs)
	}
	c.JSON(http.StatusOK, summary)
}

// saveStudent inserts a new student or updates the existing one; returns (id, created).
func (m *Module) saveStudent(tx *gorm.DB, r *importRow, sharedHash *string) (int64, bool, error) {
	var inst, unit, prog, major, cohort, class, advisor any
	if r.class != nil {
		inst, prog, class = r.class.InstitutionID, r.class.ProgramID, r.class.ID
		unit, major, cohort, advisor = ptr(r.class.UnitID), ptr(r.class.MajorID), ptr(r.class.CohortID), ptr(r.class.AdvisorID)
	}
	var dob any
	if r.DOB != nil {
		dob = r.DOB.Format("2006-01-02")
	}
	if r.existing != nil {
		// Only what the file provides is changed; a blank cell keeps the current value. Password and roles are untouched.
		sets := []string{"name = ?", "email = ?"}
		args := []any{r.Name, r.Email}
		add := func(col string, val any, present bool) {
			if present {
				sets, args = append(sets, col+" = ?"), append(args, val)
			}
		}
		add("phone", r.Phone, r.Phone != "")
		add("gender", r.Gender, r.Gender != "")
		add("date_of_birth", dob, r.DOB != nil)
		add("hometown", r.Home, r.Home != "")
		add("id_card_number", r.IDCard, r.IDCard != "")
		if r.class != nil {
			sets = append(sets, "institution_id = ?", "unit_id = ?", "program_id = ?", "major_id = ?", "cohort_id = ?", "administrative_class_id = ?")
			args = append(args, inst, unit, prog, major, cohort, class)
			if advisor != nil {
				sets, args = append(sets, "advisor_id = ?"), append(args, advisor)
			}
		}
		args = append(args, r.existing.ID)
		return r.existing.ID, false, tx.Exec("UPDATE users SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...).Error
	}

	var hash *string
	switch {
	case r.Password != "":
		h, err := auth.HashPassword(r.Password)
		if err != nil {
			return 0, false, err
		}
		hash = &h
	case sharedHash != nil:
		hash = sharedHash
	} // nil hash: password login stays disabled until the student sets one through "forgot password"
	var id int64
	err := tx.Raw(`INSERT INTO users (name, email, password_hash, user_type, student_code, phone, gender, date_of_birth, hometown, id_card_number,
		institution_id, unit_id, program_id, major_id, cohort_id, administrative_class_id, advisor_id, study_status, email_verified_at)
		VALUES (?, ?, ?, 'student', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'dang_hoc', now()) RETURNING id`,
		r.Name, r.Email, hash, r.Code, nullable(r.Phone), nullable(r.Gender), dob, nullable(r.Home), nullable(r.IDCard),
		inst, unit, prog, major, cohort, class, advisor).Scan(&id).Error
	if err != nil {
		return 0, false, err
	}
	return id, true, m.assignRoles(tx, id, []string{"student"})
}

func ptr(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// importTemplate serves an .xlsx with the expected columns, example rows and a short guide.
func (m *Module) importTemplate(c *gin.Context) {
	f := excelize.NewFile()
	defer f.Close()
	sheet := "Sinh viên"
	_ = f.SetSheetName("Sheet1", sheet)
	for i, h := range importHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	head, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Color: []string{"#E8EEF7"}, Pattern: 1}})
	_ = f.SetCellStyle(sheet, "A1", "J1", head)
	text, _ := f.NewStyle(&excelize.Style{NumFmt: 49}) // text: keeps leading zeros of codes and phone numbers
	_ = f.SetCellStyle(sheet, "A2", "A1000", text)
	_ = f.SetCellStyle(sheet, "D2", "D1000", text)
	_ = f.SetCellStyle(sheet, "H2", "H1000", text)
	for r, row := range [][]any{
		{"B21DCCN001", "Nguyễn Văn An", "an.nv@example.com", "0912345678", "15/03/2003", "Nam", "D21CQCN01", "001203000001", "Hà Nội", ""},
		{"B21DCCN002", "Trần Thị Bình", "binh.tt@example.com", "", "02/09/2003", "Nữ", "D21CQCN01", "", "", ""},
	} {
		for i, v := range row {
			cell, _ := excelize.CoordinatesToCellName(i+1, r+2)
			_ = f.SetCellValue(sheet, cell, v)
		}
	}
	for col, w := range map[string]float64{"A": 16, "B": 26, "C": 30, "D": 16, "E": 14, "F": 10, "G": 20, "H": 16, "I": 16, "J": 16} {
		_ = f.SetColWidth(sheet, col, col, w)
	}
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	guide := "Hướng dẫn"
	_, _ = f.NewSheet(guide)
	for i, line := range []string{
		"HƯỚNG DẪN NHẬP DANH SÁCH SINH VIÊN",
		"Dòng 1 của sheet \"Sinh viên\" là tiêu đề. Xóa 2 dòng ví dụ rồi nhập sinh viên từ dòng 2. Mỗi lần tối đa 2000 sinh viên.",
		"Bắt buộc: Mã sinh viên, Họ và tên, Email. Mã sinh viên và Email không được trùng với tài khoản đã có (kể cả tài khoản đã xóa).",
		"Mã lớp hành chính: nhập đúng mã lớp đã tạo ở Quản trị > Lớp hành chính. Khi có mã lớp, hệ thống tự điền chương trình, khóa, ngành và cố vấn theo lớp.",
		"Ngày sinh: dd/mm/yyyy. Giới tính: Nam, Nữ hoặc Khác. Các cột còn lại có thể để trống.",
		"Mật khẩu (không bắt buộc, từ 8 ký tự): nếu để trống, sinh viên đặt mật khẩu qua \"Quên mật khẩu\" hoặc email mời. Không nên dùng chung một mật khẩu cho cả lớp.",
		"Chế độ \"Thêm mới và cập nhật\": sinh viên đã có (cùng mã sinh viên) được cập nhật thông tin và chuyển lớp; ô để trống giữ nguyên giá trị cũ; mật khẩu và vai trò không đổi.",
		"Có lỗi ở bất kỳ dòng nào thì không có sinh viên nào được nhập. Hãy bấm \"Kiểm tra\" trước để xem lỗi và số sinh viên mỗi lớp.",
	} {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		_ = f.SetCellValue(guide, cell, line)
	}
	_ = f.SetColWidth(guide, "A", "A", 140)
	f.SetActiveSheet(0)
	buf, err := f.WriteToBuffer()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="mau-nhap-sinh-vien.xlsx"`)
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf.Bytes())
}
