package admin

import (
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func xlsx(t *testing.T, rows [][]any) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	for r, row := range rows {
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
			_ = f.SetCellValue("Sheet1", cell, v)
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadImportSheet(t *testing.T) {
	data := xlsx(t, [][]any{
		{"MSSV", "Họ tên", "E-mail", "Ngày sinh", "Giới tính", "Lớp", "Cột lạ"},
		{"B21001", "Nguyễn Văn An", "an@example.com", "15/03/2003", "Nam", "D21CQCN01", "bỏ qua"},
		{"", "", "", "", "", "", ""}, // blank rows are skipped
		{"B21002", "Trần Thị Bình", "binh@example.com", "2003-09-02", "Nữ", "", ""},
		{"B21003", "Lê Văn C", "c@example.com", "31/02/2003", "", "", ""},
	})
	rows, err := readImportSheet(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Code != "B21001" || rows[0].ClassCode != "D21CQCN01" || rows[0].DOB == nil || rows[0].DOB.Format("2006-01-02") != "2003-03-15" || rows[0].Row != 2 {
		t.Errorf("row 1: %+v", rows[0])
	}
	if rows[1].Row != 4 || rows[1].DOB == nil || rows[1].DOB.Format("2006-01-02") != "2003-09-02" {
		t.Errorf("row 2 (after a blank line): %+v", rows[1])
	}
	if !strings.Contains(rows[2].Err, "Ngày sinh") {
		t.Errorf("an impossible date must be reported: %+v", rows[2])
	}

	if _, err := readImportSheet(xlsx(t, [][]any{{"Họ tên", "Email"}, {"A", "a@x.vn"}})); err == nil || !strings.Contains(err.Error(), "Mã sinh viên") {
		t.Errorf("missing column must be named: %v", err)
	}
	if _, err := readImportSheet(xlsx(t, [][]any{{"Mã sinh viên", "Họ tên", "Email"}})); err == nil {
		t.Error("a sheet without data rows is rejected")
	}
	if _, err := readImportSheet([]byte("not an excel file")); err == nil {
		t.Error("garbage is rejected")
	}
}

func TestImportHelpers(t *testing.T) {
	for in, want := range map[string]string{"": "", "Nam": "male", "nữ": "female", "NU": "female", "Khác": "other", "M": "male"} {
		if got, ok := parseGender(in); !ok || got != want {
			t.Errorf("parseGender(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := parseGender("robot"); ok {
		t.Error("unknown gender accepted")
	}
	for _, e := range []string{"a@b.vn", "an.nv+x@sub.example.com"} {
		if !validEmail(e) {
			t.Errorf("%q should be valid", e)
		}
	}
	for _, e := range []string{"", "abc", "a@b", "Tên <a@b.vn>", "a@@b.vn", "a b@c.vn"} {
		if validEmail(e) {
			t.Errorf("%q should be invalid", e)
		}
	}
	if d, ok := parseDOB("37695"); !ok || d.Format("2006-01-02") != "2003-03-15" { // Excel serial number of 15/03/2003
		t.Errorf("Excel serial date: %v %v", d, ok)
	}
	if _, ok := parseDOB("hôm qua"); ok {
		t.Error("text date accepted")
	}
	if headerKey("  Mã Sinh Viên ") != "ma-sinh-vien" {
		t.Errorf("headerKey = %q", headerKey("  Mã Sinh Viên "))
	}
}
