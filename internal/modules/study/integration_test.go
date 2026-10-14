package study

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/rbac"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/storage"
	"github.com/Chilly913/PTIT_ERIPT_API/migrations"
)

// Integration tests run against TEST_DATABASE_URL (migrated to the latest
// version first). Rows created here are deleted at the end.

type fakeStore struct {
	mu      sync.Mutex
	objects map[string]int64
	deleted []string
}

func (f *fakeStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	n, err := io.Copy(io.Discard, r)
	f.mu.Lock()
	f.objects[key] = n
	f.mu.Unlock()
	return err
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) URL(key string) string {
	return storage.JoinURL("http://localhost:9000/lms-videos", key)
}
func (f *fakeStore) Bucket() string { return "lms-videos" }

type env struct {
	t      *testing.T
	db     *gorm.DB
	router *gin.Engine
	store  *fakeStore
	tokens *auth.TokenService
	roles  *rbac.Service
	run    string
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := db.NewMigrator(dsn, migrations.FS, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	gdb, err := db.Open(context.Background(), config.DBConfig{URL: dsn, MaxOpenConns: 5, MaxIdleConns: 2, LogLevel: "silent"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: gdb, run: fmt.Sprint(time.Now().UnixNano()), store: &fakeStore{objects: map[string]int64{}}}
	e.tokens = auth.NewTokenService(gdb, auth.TokenOptions{})
	e.roles = rbac.New(gdb, nil, quiet)
	a := &app.App{Cfg: config.Config{App: config.AppConfig{URL: "https://eript-lms.io.vn"}}, Log: quiet, DB: gdb}
	mod := New(a, e.roles, e.tokens, WithStore(e.store))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.ContextWithFallback = true
	mod.Register(r.Group("/api"))
	e.router = r

	t.Cleanup(func() {
		like := "%." + e.run + "@study.test"
		gdb.Exec(`DELETE FROM user_certificates WHERE user_id IN (SELECT id FROM users WHERE email LIKE ?)`, like)
		gdb.Exec(`DELETE FROM career_paths WHERE slug LIKE ?`, "%-"+e.run)
		gdb.Exec(`DELETE FROM courses WHERE instructor_id IN (SELECT id FROM users WHERE email LIKE ?)`, like)
		gdb.Exec(`DELETE FROM users WHERE email LIKE ?`, like)
		gdb.Exec(`DELETE FROM certificate_templates WHERE name = ?`, "tpl-"+e.run)
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if err := e.db.Exec(q, args...).Error; err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) id(q string, args ...any) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.Raw(q, args...).Scan(&id).Error; err != nil || id == 0 {
		e.t.Fatalf("%s: id=%d err=%v", q, id, err)
	}
	return id
}

// user creates a user with role and returns (id, bearer token).
func (e *env) user(name, role string) (int64, string) {
	e.t.Helper()
	uid := e.id(`INSERT INTO users (email, name) VALUES (?, ?) RETURNING id`, name+"."+e.run+"@study.test", "User "+name)
	if err := e.roles.AssignRole(context.Background(), nil, uid, role); err != nil {
		e.t.Fatal(err)
	}
	tok, err := e.tokens.Issue(context.Background(), nil, uid, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return uid, tok.PlainText
}

type resp struct {
	Code int
	Body map[string]any
	List []any
	Raw  string
}

type multipartBody struct {
	buf bytes.Buffer
	ct  string
}

func newMultipart(fields map[string]string, fileField, fileName string, content []byte) *multipartBody {
	mb := &multipartBody{}
	w := multipart.NewWriter(&mb.buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if fileField != "" {
		fw, _ := w.CreateFormFile(fileField, fileName)
		_, _ = fw.Write(content)
	}
	_ = w.Close()
	mb.ct = w.FormDataContentType()
	return mb
}

func (e *env) do(method, path, token string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case *multipartBody:
		rd, ct = &b.buf, b.ct
	default:
		js, _ := json.Marshal(b)
		rd, ct = bytes.NewReader(js), "application/json"
	}
	req := httptest.NewRequest(method, path, rd)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	out := resp{Code: w.Code, Raw: w.Body.String()}
	_ = json.Unmarshal(w.Body.Bytes(), &out.Body)
	_ = json.Unmarshal(w.Body.Bytes(), &out.List)
	return out
}

func (e *env) expect(r resp, code int, msg string) {
	e.t.Helper()
	if r.Code != code {
		e.t.Fatalf("status %d, want %d: %s", r.Code, code, r.Raw)
	}
	if msg != "" && r.Body["message"] != msg {
		e.t.Fatalf("message %q, want %q (%s)", r.Body["message"], msg, r.Raw)
	}
}

type fixture struct {
	instructor, student, outsider, other int64
	tInstr, tStudent, tOutsider, tOther  string
	course, otherCourse                  int64
	slug                                 string
	video, page, foreign                 int64
	template                             int64
}

func (e *env) fixture() fixture {
	var f fixture
	f.instructor, f.tInstr = e.user("instr", rbac.RoleInstructor)
	f.other, f.tOther = e.user("instr2", rbac.RoleInstructor)
	f.student, f.tStudent = e.user("stud", rbac.RoleStudent)
	f.outsider, f.tOutsider = e.user("out", rbac.RoleStudent)
	f.template = e.id(`INSERT INTO certificate_templates (name) VALUES (?) RETURNING id`, "tpl-"+e.run)
	f.slug = "study-course-" + e.run
	f.course = e.id(`INSERT INTO courses (instructor_id, title, slug, status, price, certificate_template_id)
		VALUES (?, 'Study course', ?, 'published', 1500000.75, ?) RETURNING id`, f.instructor, f.slug, f.template)
	f.otherCourse = e.id(`INSERT INTO courses (instructor_id, title, slug) VALUES (?, 'Other', ?) RETURNING id`,
		f.other, "study-other-"+e.run)
	f.video = e.id(`INSERT INTO lessons (course_id, title, type, position, duration_seconds) VALUES (?, 'Video', 'video', 1, 100) RETURNING id`, f.course)
	f.page = e.id(`INSERT INTO lessons (course_id, title, type, position) VALUES (?, 'Page', 'page', 2) RETURNING id`, f.course)
	f.foreign = e.id(`INSERT INTO lessons (course_id, title) VALUES (?, 'Foreign') RETURNING id`, f.otherCourse)
	e.exec(`INSERT INTO enrollments (user_id, course_id, enrollment_source) VALUES (?, ?, 'manual')`, f.student, f.course)
	return f
}

func lessonURL(course any, lesson int64, rest string) string {
	return fmt.Sprintf("/api/courses/%v/lessons/%d%s", course, lesson, rest)
}

func TestProgressFlow(t *testing.T) {
	e := setup(t)
	f := e.fixture()

	e.expect(e.do("GET", lessonURL(f.course, f.video, "/progress"), "", nil), 401, "Unauthenticated.")
	e.expect(e.do("GET", lessonURL(f.course, f.video, "/progress"), f.tOutsider, nil), 403, "Enrollment required")
	e.expect(e.do("GET", lessonURL(f.course, f.foreign, "/progress"), f.tStudent, nil), 404, "Not found")
	e.expect(e.do("GET", lessonURL("no-such-slug", f.video, "/progress"), f.tStudent, nil), 404,
		`No query results for model [App\Models\Course].`)
	e.expect(e.do("GET", lessonURL(f.course, 999999999, "/progress"), f.tStudent, nil), 404,
		`No query results for model [App\Models\Lesson] 999999999`)

	// show: firstOrCreate with zero defaults; slug binding works.
	r := e.do("GET", lessonURL(f.slug, f.video, "/progress"), f.tStudent, nil)
	e.expect(r, 200, "")
	if r.Body["completed"] != false || r.Body["progress_percent"] != float64(0) || r.Body["lesson_id"] != float64(f.video) {
		t.Fatalf("show: %s", r.Raw)
	}

	// upsert validation.
	r = e.do("PUT", lessonURL(f.course, f.video, "/progress"), f.tStudent, map[string]any{
		"progress_percent": 150, "last_position": "x", "completed": "yes"})
	e.expect(r, 422, "The progress percent field must not be greater than 100. (and 2 more errors)")

	r = e.do("PUT", lessonURL(f.course, f.video, "/progress"), f.tStudent, map[string]any{"progress_percent": "100", "last_position": 42})
	e.expect(r, 200, "Lesson progress saved successfully.")
	p := r.Body["progress"].(map[string]any)
	if p["completed"] != true || p["completed_at"] == nil || p["last_position"] != float64(42) {
		t.Fatalf("upsert: %s", r.Raw)
	}
	r = e.do("PUT", lessonURL(f.course, f.video, "/progress"), f.tStudent, map[string]any{"completed": false})
	if p := r.Body["progress"].(map[string]any); p["completed"] != false || p["completed_at"] != nil || p["progress_percent"] != float64(100) {
		t.Fatalf("uncomplete: %s", r.Raw)
	}

	// Career path following the course (required) with a template.
	path := e.id(`INSERT INTO career_paths (title, slug, certificate_template_id) VALUES ('Path', ?, ?) RETURNING id`,
		"path-"+e.run, f.template)
	e.exec(`INSERT INTO career_path_courses (career_path_id, course_id) VALUES (?, ?)`, path, f.course)
	e.exec(`INSERT INTO user_career_paths (user_id, career_path_id, status) VALUES (?, ?, 'following')`, f.student, path)

	// POST progress (UserManagement): validation, auto-completion at 90%.
	e.expect(e.do("POST", lessonURL(f.course, f.video, "/progress"), f.tStudent, map[string]any{"completed": nil}), 422,
		"The watched seconds field is required. (and 1 more error)")
	r = e.do("POST", lessonURL(f.course, f.video, "/progress"), f.tStudent, map[string]any{"watched_seconds": 95})
	e.expect(r, 200, "Progress saved")
	if r.Body["progress"].(map[string]any)["completed"] != true {
		t.Fatalf("auto complete: %s", r.Raw)
	}
	var certs int64
	e.db.Raw(`SELECT count(*) FROM user_certificates WHERE user_id = ?`, f.student).Scan(&certs)
	if certs != 0 {
		t.Fatalf("certificate issued too early")
	}
	r = e.do("POST", lessonURL(f.course, f.page, "/progress"), f.tStudent, map[string]any{"watched_seconds": 0, "completed": true})
	e.expect(r, 200, "Progress saved")

	var credentials []string
	e.db.Raw(`SELECT credential_id::text FROM user_certificates WHERE user_id = ? ORDER BY id`, f.student).Scan(&credentials)
	if len(credentials) != 2 || !strings.HasPrefix(credentials[0], "CERT-") || !strings.HasPrefix(credentials[1], "PATH-") {
		t.Fatalf("certificates: %v", credentials)
	}
	var ucp struct {
		Status  string
		Percent int `gorm:"column:progress_percent"`
	}
	e.db.Raw(`SELECT status, progress_percent FROM user_career_paths WHERE user_id = ?`, f.student).Scan(&ucp)
	if ucp.Status != "completed" || ucp.Percent != 100 {
		t.Fatalf("career path: %+v", ucp)
	}
	// Completing again does not duplicate certificates.
	e.expect(e.do("POST", lessonURL(f.course, f.page, "/progress"), f.tStudent, map[string]any{"watched_seconds": 1, "completed": "1"}), 200, "")
	e.db.Raw(`SELECT count(*) FROM user_certificates WHERE user_id = ?`, f.student).Scan(&certs)
	if certs != 2 {
		t.Fatalf("certificates after repeat: %d", certs)
	}

	// Course progress.
	e.expect(e.do("GET", fmt.Sprintf("/api/courses/%d/progress", f.course), f.tOutsider, nil), 403, "Enrollment required")
	r = e.do("GET", fmt.Sprintf("/api/courses/%d/progress", f.course), f.tStudent, nil)
	e.expect(r, 200, "")
	if !strings.Contains(r.Raw, `"percent":100.0`) || r.Body["completed_lessons"] != float64(2) {
		t.Fatalf("course progress: %s", r.Raw)
	}
	lessons := r.Body["lessons"].([]any)
	if first := lessons[0].(map[string]any); first["id"] != float64(f.video) || first["order"] != float64(1) || first["duration"] != float64(100) {
		t.Fatalf("lessons: %s", r.Raw)
	}
	r = e.do("GET", fmt.Sprintf("/api/courses/%d/progress", f.otherCourse), f.tOther, nil)
	e.expect(r, 200, "")
	if r.Raw == "" || r.Body["total_lessons"] != float64(1) || !strings.Contains(r.Raw, `"percent":0.0`) {
		t.Fatalf("owner progress: %s", r.Raw)
	}

	// Enrollments.
	r = e.do("GET", "/api/enrollments", f.tStudent, nil)
	e.expect(r, 200, "")
	if len(r.List) != 1 {
		t.Fatalf("enrollments: %s", r.Raw)
	}
	en := r.List[0].(map[string]any)
	course := en["course"].(map[string]any)
	if en["window_status"] != "current" || en["term"] != nil || en["is_planned"] != false ||
		en["lessons_count"] != float64(2) || course["price"] != float64(1500000) || course["user_id"] != float64(f.instructor) ||
		course["instructor"].(map[string]any)["name"] != "User instr" || !strings.Contains(r.Raw, `"progress":100.0`) {
		t.Fatalf("enrollment item: %s", r.Raw)
	}
	r = e.do("GET", "/api/enrollments", f.tOutsider, nil)
	if r.Code != 200 || r.Raw != "[]" {
		t.Fatalf("empty enrollments: %s", r.Raw)
	}
}

func TestNotes(t *testing.T) {
	e := setup(t)
	f := e.fixture()
	base := lessonURL(f.course, f.video, "/notes")

	e.expect(e.do("POST", base, f.tStudent, map[string]any{"content": "  "}), 422, "The content field is required.")
	r := e.do("POST", base, f.tStudent, map[string]any{"content": "first", "time_seconds": 12})
	e.expect(r, 201, "Lesson note created successfully.")
	note := r.Body["note"].(map[string]any)
	if note["position_seconds"] != float64(12) {
		t.Fatalf("note: %s", r.Raw)
	}
	nid := int64(note["id"].(float64))
	e.expect(e.do("POST", base, f.tStudent, map[string]any{"content": "second", "position_seconds": 5}), 201, "")

	r = e.do("GET", base, f.tStudent, nil)
	if r.Code != 200 || len(r.List) != 2 || r.List[0].(map[string]any)["content"] != "second" {
		t.Fatalf("list: %s", r.Raw)
	}
	// The owner sees only their own notes.
	if r = e.do("GET", base, f.tInstr, nil); r.Code != 200 || r.Raw != "[]" {
		t.Fatalf("owner list: %s", r.Raw)
	}

	noteURL := fmt.Sprintf("%s/%d", base, nid)
	e.expect(e.do("PUT", noteURL, f.tInstr, map[string]any{"content": "x"}), 403, "Forbidden")
	e.expect(e.do("PUT", noteURL, f.tStudent, map[string]any{"content": ""}), 422, "The content field must be a string.")
	r = e.do("PUT", noteURL, f.tStudent, map[string]any{"position_seconds": 99})
	e.expect(r, 200, "Lesson note updated successfully.")
	if n := r.Body["note"].(map[string]any); n["content"] != "first" || n["position_seconds"] != float64(99) {
		t.Fatalf("update: %s", r.Raw)
	}
	e.expect(e.do("DELETE", fmt.Sprintf("%s/%d", lessonURL(f.course, f.page, "/notes"), nid), f.tStudent, nil), 403, "Forbidden")
	e.expect(e.do("DELETE", noteURL, f.tStudent, nil), 200, "Lesson note deleted successfully.")
	e.expect(e.do("DELETE", noteURL, f.tStudent, nil), 404, fmt.Sprintf(`No query results for model [App\Models\LessonNote] %d`, nid))
}

func TestAttachments(t *testing.T) {
	e := setup(t)
	f := e.fixture()
	base := lessonURL(f.course, f.video, "/attachments")
	pdf := []byte("%PDF-1.7\n" + strings.Repeat("x", 2*1024*1024))

	e.expect(e.do("POST", base, f.tStudent, newMultipart(nil, "file", "a.pdf", pdf)), 403, "User does not have the right permissions.")
	e.expect(e.do("POST", base, f.tOther, newMultipart(nil, "file", "a.pdf", pdf)), 403, "Forbidden")
	e.expect(e.do("POST", base, f.tInstr, newMultipart(map[string]string{"x": "1"}, "", "", nil)), 422, "The file field is required.")

	r := e.do("POST", base, f.tInstr, newMultipart(nil, "file", "Slides.pdf", pdf))
	e.expect(r, 201, "Attachment uploaded successfully")
	att := r.Body["attachment"].(map[string]any)
	key := att["file_path"].(string)
	prefix := fmt.Sprintf("courses/%d/lessons/%d/attachments/", f.course, f.video)
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".pdf") || att["original_name"] != "Slides.pdf" ||
		att["mime_type"] != "application/pdf" || att["file_size"] != "2MB" || att["url"] != "/minio/lms-videos/"+key {
		t.Fatalf("attachment: %s", r.Raw)
	}
	aid := int64(att["id"].(float64))

	r = e.do("GET", base, f.tOutsider, nil) // legacy: no enrollment check
	if r.Code != 200 || len(r.Body["attachments"].([]any)) != 1 {
		t.Fatalf("list: %s", r.Raw)
	}
	r = e.do("GET", fmt.Sprintf("%s/%d/download", base, aid), f.tStudent, nil)
	if r.Code != 200 || r.Body["url"] != "/minio/lms-videos/"+key {
		t.Fatalf("download: %s", r.Raw)
	}
	e.expect(e.do("GET", fmt.Sprintf("%s/%d/download", lessonURL(f.course, f.page, "/attachments"), aid), f.tStudent, nil), 404, "Not found")

	e.expect(e.do("DELETE", fmt.Sprintf("%s/%d", lessonURL(f.course, f.page, "/attachments"), aid), f.tInstr, nil), 404, "Not found")
	e.expect(e.do("DELETE", fmt.Sprintf("%s/%d", base, aid), f.tInstr, nil), 200, "Attachment deleted")
	if len(e.store.deleted) != 1 || e.store.deleted[0] != key {
		t.Fatalf("storage delete: %v", e.store.deleted)
	}
}

func TestAssignments(t *testing.T) {
	e := setup(t)
	f := e.fixture()
	base := lessonURL(f.course, f.page, "/assignment")

	e.expect(e.do("GET", base, f.tStudent, nil), 404, "No assignment found")
	e.expect(e.do("POST", base+"/submit", f.tStudent, newMultipart(nil, "file", "a.zip", []byte("PK"))), 404, "No assignment for this lesson")
	e.expect(e.do("POST", base, f.tOther, map[string]any{"instructions": "x"}), 403, "Forbidden")
	e.expect(e.do("POST", base, f.tInstr, map[string]any{
		"instructions": "Do it", "submission_open_at": "2026-10-10 00:00:00", "due_at": "2026-10-01",
	}), 422, "The due at field must be a date after or equal to submission open at.")

	r := e.do("POST", base, f.tInstr, map[string]any{
		"instructions": "Do it", "allowed_extensions": "PDF, zip", "due_at": "2026-11-01T10:00:00Z",
	})
	e.expect(r, 200, "Assignment saved successfully")
	a := r.Body["assignment"].(map[string]any)
	if a["max_file_size"] != float64(10240) || a["allowed_extensions"] != "pdf,zip" || a["due_at"] != "2026-11-01T10:00:00.000000Z" {
		t.Fatalf("assignment: %s", r.Raw)
	}
	var typ string
	e.db.Raw(`SELECT type FROM lessons WHERE id = ?`, f.page).Scan(&typ)
	if typ != "assignment" {
		t.Fatalf("lesson type %s", typ)
	}
	e.expect(e.do("GET", base, f.tStudent, nil), 200, "")

	r = e.do("POST", base+"/submit", f.tStudent, newMultipart(map[string]string{"student_note": "hi"}, "file", "work.zip", []byte("PK\x03\x04data")))
	e.expect(r, 201, "Assignment submitted successfully")
	sub := r.Body["submission"].(map[string]any)
	url := sub["file_url"].(string)
	if !strings.HasPrefix(url, fmt.Sprintf("/minio/lms-videos/courses/%d/lessons/%d/assignments/submissions/%d/", f.course, f.page, f.student)) ||
		sub["student_note"] != "hi" || sub["grade"] != nil {
		t.Fatalf("submission: %s", r.Raw)
	}
	sid := int64(sub["id"].(float64))

	r = e.do("GET", base+"/submissions", f.tInstr, nil)
	if r.Code != 200 || len(r.List) != 1 || r.List[0].(map[string]any)["user"].(map[string]any)["name"] != "User stud" {
		t.Fatalf("submissions: %s", r.Raw)
	}
	r = e.do("GET", fmt.Sprintf("/api/courses/%d/assignments/submissions", f.course), f.tInstr, nil)
	e.expect(r, 200, "")
	if s := r.Body["summary"].(map[string]any); s["assignments"] != float64(1) || s["submissions"] != float64(1) || s["ungraded"] != float64(1) {
		t.Fatalf("inbox: %s", r.Raw)
	}

	gradeURL := fmt.Sprintf("/api/courses/%d/assignments/submissions/%d", f.course, sid)
	e.expect(e.do("PUT", gradeURL, f.tInstr, map[string]any{"grade": 11}), 422, "The grade field must not be greater than 10.")
	e.expect(e.do("PUT", fmt.Sprintf("/api/courses/%d/assignments/submissions/%d", f.otherCourse, sid), f.tOther, map[string]any{"grade": 5}), 404, "")
	r = e.do("PUT", gradeURL, f.tInstr, map[string]any{"grade": "8.5", "feedback": "good"})
	e.expect(r, 200, "Đã chấm điểm bài tập.")
	if g := r.Body["submission"].(map[string]any); g["grade"] != "8.50" || g["feedback"] != "good" || g["user"] == nil {
		t.Fatalf("grade: %s", r.Raw)
	}
	// Feedback omitted keeps the previous one.
	r = e.do("PUT", gradeURL, f.tInstr, map[string]any{"grade": 9})
	if g := r.Body["submission"].(map[string]any); g["grade"] != "9.00" || g["feedback"] != "good" {
		t.Fatalf("regrade: %s", r.Raw)
	}
	// Resubmission keeps the grade (updateOrCreate).
	r = e.do("POST", base+"/submit", f.tStudent, newMultipart(nil, "file", "v2.zip", []byte("PK")))
	if r.Code != 201 || r.Body["submission"].(map[string]any)["grade"] != "9.00" || r.Body["submission"].(map[string]any)["student_note"] != nil {
		t.Fatalf("resubmit: %s", r.Raw)
	}
}

func TestPlannedEnrollments(t *testing.T) {
	e := setup(t)
	f := e.fixture()
	start := time.Now().UTC().AddDate(0, 1, 0).Format("2006-01-02")
	end := time.Now().UTC().AddDate(0, 5, 0).Format("2006-01-02")
	past := time.Now().UTC().AddDate(-1, 0, 0).Format("2006-01-02")
	pastEnd := time.Now().UTC().AddDate(0, -6, 0).Format("2006-01-02")

	inst := e.id(`INSERT INTO institutions (code, name) VALUES (?, 'Inst') RETURNING id`, "I"+e.run)
	pt := e.id(`INSERT INTO program_types (name, code) VALUES ('PT', ?) RETURNING id`, "PT"+e.run)
	prog := e.id(`INSERT INTO programs (institution_id, program_type_id, name, code) VALUES (?, ?, 'Prog', ?) RETURNING id`, inst, pt, "P"+e.run)
	cur := e.id(`INSERT INTO curricula (program_id, name, code) VALUES (?, 'Cur', ?) RETURNING id`, prog, "C"+e.run)
	year := e.id(`INSERT INTO academic_years (institution_id, name, start_date, end_date) VALUES (?, '2026-2027', ?, ?) RETURNING id`,
		inst, past, end)
	upcoming := e.id(`INSERT INTO terms (academic_year_id, name, code, start_date, end_date) VALUES (?, 'Học kỳ 2 (2026-2027)', 'HK2', ?, ?) RETURNING id`,
		year, start, end)
	expired := e.id(`INSERT INTO terms (academic_year_id, name, code, start_date, end_date) VALUES (?, 'Học kỳ 1', 'HK1', ?, ?) RETURNING id`,
		year, past, pastEnd)
	cls := e.id(`INSERT INTO administrative_classes (institution_id, program_id, curriculum_id, code, name) VALUES (?, ?, ?, ?, 'Class') RETURNING id`,
		inst, prog, cur, "K"+e.run)
	e.exec(`INSERT INTO administrative_class_terms (administrative_class_id, term_number, term_id) VALUES (?, 1, ?), (?, 2, ?)`, cls, expired, cls, upcoming)
	planned := e.id(`INSERT INTO courses (instructor_id, title, slug) VALUES (?, 'Planned', ?) RETURNING id`, f.instructor, "study-planned-"+e.run)
	e.exec(`INSERT INTO curriculum_courses (curriculum_id, course_id, term_number) VALUES (?, ?, 2), (?, ?, 2), (?, ?, 1)`,
		cur, f.course, cur, planned, cur, f.otherCourse)
	e.exec(`UPDATE users SET administrative_class_id = ? WHERE id = ?`, cls, f.student)
	t.Cleanup(func() {
		e.db.Exec(`UPDATE users SET administrative_class_id = NULL WHERE id = ?`, f.student)
		e.db.Exec(`DELETE FROM administrative_classes WHERE id = ?`, cls)
		e.db.Exec(`DELETE FROM curricula WHERE id = ?`, cur)
		e.db.Exec(`DELETE FROM academic_years WHERE id = ?`, year)
		e.db.Exec(`DELETE FROM programs WHERE id = ?`, prog)
		e.db.Exec(`DELETE FROM program_types WHERE id = ?`, pt)
		e.db.Exec(`DELETE FROM institutions WHERE id = ?`, inst)
	})

	r := e.do("GET", "/api/enrollments", f.tStudent, nil)
	e.expect(r, 200, "")
	// enrolled course + the upcoming planned one; the enrolled curriculum course
	// and the expired-term course are skipped.
	if len(r.List) != 2 {
		t.Fatalf("enrollments: %s", r.Raw)
	}
	p := r.List[1].(map[string]any)
	term := p["term"].(map[string]any)
	if p["id"] != float64(-planned) || p["is_planned"] != true || p["window_status"] != "upcoming" ||
		p["enrollment_source"] != "academic" || p["starts_at"] != start || p["progress"] != float64(0) ||
		term["display_name"] != "Học kỳ 2 - Năm học 2026-2027" || term["academic_year"].(map[string]any)["name"] != "2026-2027" {
		t.Fatalf("planned: %s", r.Raw)
	}
}
