package admin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
)

// Runs against TEST_DATABASE_URL inside a transaction that is always rolled back (needs a database with at least one
// administrative class, e.g. after `go run ./cmd/cli seed academic`).
func TestImportStudentsDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	gdb, err := db.Open(context.Background(), config.DBConfig{URL: dsn, MaxOpenConns: 2, MaxIdleConns: 1, LogLevel: "silent"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	m := &Module{db: tx}

	var classes []classInfo
	if err := tx.Raw(`SELECT id, code::text AS code, name, status, institution_id, unit_id, program_id, major_id, cohort_id, advisor_id
		FROM administrative_classes WHERE status = 'active' ORDER BY id LIMIT 2`).Scan(&classes).Error; err != nil || len(classes) == 0 {
		t.Skip("no administrative class in the database")
	}
	c1 := classes[0]
	n := time.Now().UnixNano()
	code := func(i int) string { return fmt.Sprintf("IMP%d-%d", n, i) }
	email := func(i int) string { return fmt.Sprintf("imp.%d.%d@import.test", n, i) }
	mk := func() []importRow {
		return []importRow{
			{Row: 2, Code: code(1), Name: "Nguyễn Văn An", Email: email(1), ClassCode: strings.ToLower(c1.Code), Gender: "male", Phone: "0912345678"},
			{Row: 3, Code: code(2), Name: "Trần Thị Bình", Email: email(2), ClassCode: c1.Code},
			{Row: 4, Code: code(3), Name: "Lê Văn Cường", Email: email(3)}, // no class
		}
	}

	// 1. A clean file: no errors, every class-bound row picks up the class (case-insensitive code).
	rows := mk()
	if err := m.checkRows(rows, "create"); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Err != "" {
			t.Fatalf("row %d: %s", r.Row, r.Err)
		}
	}
	if rows[0].class == nil || rows[0].class.ID != c1.ID || rows[2].class != nil {
		t.Fatalf("class resolution: %+v / %+v", rows[0].class, rows[2].class)
	}

	// 2. Save them; the student is placed in the class with program/cohort filled from it and gets the student role.
	var ids []int64
	for i := range rows {
		id, created, err := m.saveStudent(tx, &rows[i], nil)
		if err != nil || !created {
			t.Fatalf("save row %d: %v created=%v", rows[i].Row, err, created)
		}
		ids = append(ids, id)
	}
	var got struct {
		AdministrativeClassID *int64
		ProgramID             *int64
		CohortID              *int64
		StudyStatus           *string
		Gender                *string
		Verified              bool
		HasPassword           bool
		Roles                 string
	}
	if err := tx.Raw(`SELECT administrative_class_id, program_id, cohort_id, study_status, gender, email_verified_at IS NOT NULL AS verified,
		password_hash IS NOT NULL AS has_password,
		(SELECT string_agg(r.name, ',') FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = users.id) AS roles
		FROM users WHERE id = ?`, ids[0]).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.AdministrativeClassID == nil || *got.AdministrativeClassID != c1.ID || got.ProgramID == nil || *got.ProgramID != c1.ProgramID ||
		got.StudyStatus == nil || *got.StudyStatus != "dang_hoc" || got.Gender == nil || *got.Gender != "male" || !got.Verified || got.Roles != "student" {
		t.Errorf("created student: %+v", got)
	}
	if got.HasPassword {
		t.Error("without a password the account must not be able to log in with one until it is set")
	}

	// 3. Importing the same file again in create mode: every row is a duplicate.
	again := mk()
	if err := m.checkRows(again, "create"); err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if !strings.Contains(r.Err, "đã tồn tại") {
			t.Errorf("row %d should be a duplicate: %q", r.Row, r.Err)
		}
	}

	// 4. In update mode the same file updates the students instead, and a blank cell keeps the old value.
	upd := mk()
	upd[0].Name, upd[0].Phone = "Nguyễn Văn An (sửa)", ""
	if err := m.checkRows(upd, "update"); err != nil {
		t.Fatal(err)
	}
	for _, r := range upd {
		if r.Err != "" || r.existing == nil {
			t.Fatalf("row %d should update: %q", r.Row, r.Err)
		}
	}
	if _, created, err := m.saveStudent(tx, &upd[0], nil); err != nil || created {
		t.Fatalf("update: %v created=%v", err, created)
	}
	var after struct{ Name, Phone string }
	_ = tx.Raw("SELECT name, coalesce(phone, '') AS phone FROM users WHERE id = ?", ids[0]).Scan(&after).Error
	if after.Name != "Nguyễn Văn An (sửa)" || after.Phone != "0912345678" {
		t.Errorf("after update: %+v", after)
	}

	// 5. Bad rows are reported, with the reason.
	bad := []importRow{
		{Row: 2, Code: code(9), Name: "X", Email: email(9), ClassCode: "NO-SUCH-CLASS"},
		{Row: 3, Code: code(1), Name: "Trùng mã", Email: email(8)},
		{Row: 4, Code: code(7), Name: "Trùng email", Email: email(2)},
		{Row: 5, Code: code(6), Name: "Trùng trong tệp", Email: email(6)},
		{Row: 6, Code: code(6), Name: "Trùng trong tệp 2", Email: email(5)},
		{Row: 7, Code: "", Name: "", Email: "khong-hop-le"},
	}
	if err := m.checkRows(bad, "create"); err != nil {
		t.Fatal(err)
	}
	wants := []string{"Không có lớp", "đã tồn tại", "Email", "", "trùng với dòng 5", "Thiếu"}
	for i, w := range wants {
		if (w == "" && bad[i].Err != "") || (w != "" && !strings.Contains(bad[i].Err, w)) {
			t.Errorf("row %d: want %q, got %q", bad[i].Row, w, bad[i].Err)
		}
	}

	// 6. A deleted account still owns its code and email.
	tx.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", ids[2])
	dead := []importRow{{Row: 2, Code: code(3), Name: "Lê Văn Cường", Email: email(4)}}
	if err := m.checkRows(dead, "create"); err != nil || !strings.Contains(dead[0].Err, "đã tồn tại") {
		t.Errorf("deleted account code must stay reserved: %v %q", err, dead[0].Err)
	}
	dead = []importRow{{Row: 2, Code: code(3), Name: "Lê Văn Cường", Email: email(3)}}
	if err := m.checkRows(dead, "update"); err != nil || !strings.Contains(dead[0].Err, "đã bị xóa") {
		t.Errorf("a deleted account is not updated: %v %q", err, dead[0].Err)
	}
}
