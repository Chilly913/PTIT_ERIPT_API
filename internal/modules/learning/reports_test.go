package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
)

func TestPct(t *testing.T) {
	for _, c := range []struct{ n, d, want int }{{0, 0, 0}, {1, 3, 33}, {2, 3, 67}, {7, 7, 100}, {5, -1, 0}} {
		if got := pct(c.n, c.d); got != c.want {
			t.Errorf("pct(%d,%d) = %d, want %d", c.n, c.d, got, c.want)
		}
	}
}

func TestAtRisk(t *testing.T) {
	now := time.Now()
	ago := func(d int) *time.Time { v := now.Add(-time.Duration(d) * 24 * time.Hour); return &v }
	cases := []struct {
		name string
		r    reportRow
		want bool
	}{
		{"finished is never at risk", reportRow{TotalLessons: 5, Percent: 100, LastActivity: ago(60)}, false},
		{"course without lessons", reportRow{TotalLessons: 0, EnrolledAt: *ago(60)}, false},
		{"active last week", reportRow{TotalLessons: 5, Percent: 40, EnrolledAt: *ago(60), LastActivity: ago(3)}, false},
		{"silent for 3 weeks", reportRow{TotalLessons: 5, Percent: 40, EnrolledAt: *ago(60), LastActivity: ago(21)}, true},
		{"never started, enrolled 10 days ago", reportRow{TotalLessons: 5, EnrolledAt: *ago(10)}, true},
		{"never started, enrolled yesterday", reportRow{TotalLessons: 5, EnrolledAt: *ago(1)}, false},
	}
	for _, c := range cases {
		if got := atRisk(c.r, now); got != c.want {
			t.Errorf("%s: atRisk = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSummarize(t *testing.T) {
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	q1, q2 := 80.0, 60.0
	rows := []reportRow{
		{TotalLessons: 4, CompletedLessons: 4, TotalAssignments: 2, SubmittedAssignments: 2, TotalSessions: 2, AttendedSessions: 2, QuizAvg: &q1, HasCertificate: true, EnrolledAt: old, LastActivity: &now},
		{TotalLessons: 4, CompletedLessons: 1, TotalAssignments: 2, SubmittedAssignments: 0, TotalSessions: 2, AttendedSessions: 1, QuizAvg: &q2, EnrolledAt: old, LastActivity: &old},
	}
	s := summarize(rows, now)
	if s.Students != 2 || s.Completed != 1 || s.Certificates != 1 || s.AtRisk != 1 {
		t.Fatalf("counts: %+v", s)
	}
	if s.AvgPercent != 63 { // (100 + 25) / 2 = 62.5 -> 63
		t.Errorf("avg percent %d", s.AvgPercent)
	}
	if s.AssignmentRate != 50 || s.AttendanceRate != 75 {
		t.Errorf("rates: %d %d", s.AssignmentRate, s.AttendanceRate)
	}
	if s.AvgQuizScore == nil || *s.AvgQuizScore != 70 {
		t.Errorf("avg quiz: %v", s.AvgQuizScore)
	}
	if empty := summarize(nil, now); empty.Students != 0 || empty.AvgPercent != 0 || empty.AvgQuizScore != nil {
		t.Errorf("empty class: %+v", empty)
	}
}

func TestCredentialID(t *testing.T) {
	re := regexp.MustCompile(`^ERIPT-[0-9A-F]{12}$`)
	a, b := newCredentialID(), newCredentialID()
	if !re.MatchString(a) || a == b {
		t.Fatalf("credential ids: %q %q", a, b)
	}
}

func TestValidateSchedule(t *testing.T) {
	keys := map[string]json.RawMessage{"opens_at": nil, "closes_at": nil, "learning_mode": nil}
	s := func(v string) *string { return &v }
	if v := validateSchedule(keys, s("2026-10-01T08:00:00Z"), s("2026-10-02T08:00:00Z"), s("sequential")); v.HasErrors() {
		t.Errorf("valid window rejected: %+v", v)
	}
	if v := validateSchedule(keys, s("2026-10-02T08:00:00Z"), s("2026-10-01T08:00:00Z"), s("free")); !v.HasErrors() {
		t.Error("closing before opening must be rejected")
	}
	if v := validateSchedule(keys, nil, nil, s("random")); !v.HasErrors() {
		t.Error("unknown learning mode must be rejected")
	}
	if v := validateSchedule(keys, s("not a date"), nil, nil); !v.HasErrors() {
		t.Error("bad date must be rejected")
	}
}

// Runs against TEST_DATABASE_URL (skipped when unset) inside a transaction that is always rolled back.
func TestGateReportAndCertificateDB(t *testing.T) {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	n := time.Now().UnixNano()
	teacher := auth.User{Email: fmt.Sprintf("rt.%d@test.local", n), Name: "Teacher", UserType: "instructor"}
	student := auth.User{Email: fmt.Sprintf("rs.%d@test.local", n), Name: "Student", UserType: "student"}
	must(tx.Create(&teacher).Error)
	must(tx.Create(&student).Error)
	var courseID, sec1, sec2 int64
	must(tx.Raw("INSERT INTO courses (instructor_id, title, slug, learning_mode) VALUES (?, 'Gate', ?, 'sequential') RETURNING id", teacher.ID, fmt.Sprintf("gate-%d", n)).Scan(&courseID).Error)
	must(tx.Exec("INSERT INTO enrollments (user_id, course_id) VALUES (?, ?)", student.ID, courseID).Error)
	must(tx.Raw("INSERT INTO sections (course_id, title, position) VALUES (?, 'S1', 0) RETURNING id", courseID).Scan(&sec1).Error)
	must(tx.Raw("INSERT INTO sections (course_id, title, position, opens_at) VALUES (?, 'S2', 1, now() + interval '2 days') RETURNING id", courseID).Scan(&sec2).Error)
	var l1, l2, l3 int64
	lesson := func(sec int64, title string, pos int, dst *int64) {
		must(tx.Raw("INSERT INTO lessons (course_id, section_id, title, position) VALUES (?, ?, ?, ?) RETURNING id", courseID, sec, title, pos).Scan(dst).Error)
	}
	lesson(sec1, "L1", 0, &l1)
	lesson(sec1, "L2", 1, &l2)
	lesson(sec2, "L3", 0, &l3)

	// Sequential: only the first lesson is open, the next needs the previous one; the future section stays closed.
	reasons, order, err := m.lockReasons(courseID, student.ID)
	must(err)
	if len(order) != 3 || reasons[l1] != "" || reasons[l2] == "" || reasons[l3] == "" {
		t.Fatalf("initial locks: %v", reasons)
	}
	must(tx.Exec("INSERT INTO lesson_progress (user_id, lesson_id, progress_percent, completed_at) VALUES (?, ?, 100, now())", student.ID, l1).Error)
	reasons, _, err = m.lockReasons(courseID, student.ID)
	must(err)
	if reasons[l2] != "" {
		t.Fatalf("lesson 2 should open after lesson 1 is done: %q", reasons[l2])
	}
	if reasons[l3] == "" {
		t.Fatal("lesson 3 is in a section that opens in two days")
	}
	must(tx.Exec("UPDATE courses SET learning_mode = 'free' WHERE id = ?", courseID).Error)
	reasons, _, _ = m.lockReasons(courseID, student.ID)
	if reasons[l2] != "" || reasons[l3] == "" {
		t.Fatalf("free mode still honours section schedule only: %v", reasons)
	}

	// Report: one learner with one of three lessons done.
	rows, sum, err := m.courseReport(courseID, reportFilter{})
	must(err)
	if len(rows) != 1 || rows[0].CompletedLessons != 1 || rows[0].TotalLessons != 3 || rows[0].Percent != 33 || sum.Students != 1 || sum.Completed != 0 {
		t.Fatalf("report: %+v %+v", rows, sum)
	}

	// Certificate: default template is created once and a learner gets one certificate per course.
	tpl, err := m.defaultCertificateTemplate()
	must(err)
	again, err := m.defaultCertificateTemplate()
	must(err)
	if tpl == 0 || tpl != again {
		t.Fatalf("default template ids: %d %d", tpl, again)
	}
	for i := 0; i < 2; i++ {
		must(tx.Exec(`INSERT INTO user_certificates (user_id, course_id, certificate_template_id, credential_id)
			VALUES (?, ?, ?, ?) ON CONFLICT (user_id, course_id) WHERE course_id IS NOT NULL DO NOTHING`, student.ID, courseID, tpl, newCredentialID()).Error)
	}
	var certs int64
	must(tx.Raw("SELECT count(*) FROM user_certificates WHERE user_id = ? AND course_id = ?", student.ID, courseID).Scan(&certs).Error)
	if certs != 1 {
		t.Fatalf("expected a single certificate, got %d", certs)
	}
	rows, sum, err = m.courseReport(courseID, reportFilter{})
	must(err)
	if !rows[0].HasCertificate || sum.Certificates != 1 {
		t.Fatalf("report should show the certificate: %+v", rows[0])
	}
	// Filter and group by class: the learner joins a class, so a class filter keeps or drops the row.
	var classID int64
	must(tx.Raw(`INSERT INTO administrative_classes (institution_id, program_id, code, name)
		SELECT institution_id, id, ?, 'Lop thu' FROM programs ORDER BY id LIMIT 1 RETURNING id`, fmt.Sprintf("T%d", n%1000000000)).Scan(&classID).Error)
	must(tx.Exec("UPDATE users SET administrative_class_id = ? WHERE id = ?", classID, student.ID).Error)
	rows, _, err = m.courseReport(courseID, reportFilter{Classes: []string{fmt.Sprint(classID)}})
	must(err)
	if len(rows) != 1 || rows[0].ClassID == nil || *rows[0].ClassID != classID {
		t.Fatalf("class filter should keep the learner: %+v", rows)
	}
	rows, _, err = m.courseReport(courseID, reportFilter{Classes: []string{"999999999"}})
	must(err)
	if len(rows) != 0 {
		t.Fatalf("other class filter should drop the learner: %+v", rows)
	}
	rows, _, err = m.courseReport(courseID, reportFilter{Classes: []string{"none"}})
	must(err)
	if len(rows) != 0 {
		t.Fatalf("\"no class\" filter should drop a learner who has one: %+v", rows)
	}
	all, _, _ := m.courseReport(courseID, reportFilter{})
	groups := byClass(all, time.Now())
	if len(groups) != 1 || groups[0].Summary.Students != 1 || groups[0].Name != "Lop thu" {
		t.Fatalf("by class: %+v", groups)
	}
}
