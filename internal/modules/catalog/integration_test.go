package catalog

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
// version first). Every row created here is deleted at the end.

// fakeStore is an in-memory ObjectStore.
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
	roles  *rbac.Service
	tokens *auth.TokenService
	store  *fakeStore
	run    string
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	mg, err := db.NewMigrator(dsn, migrations.FS, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := mg.Up(); err != nil {
		t.Fatal(err)
	}
	_ = mg.Close()
	gdb, err := db.Open(ctx, config.DBConfig{URL: dsn, MaxOpenConns: 5, MaxIdleConns: 2, LogLevel: "silent"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: gdb, run: fmt.Sprint(time.Now().UnixNano()), store: &fakeStore{objects: map[string]int64{}}}
	e.roles = rbac.New(gdb, nil, quiet)
	e.tokens = auth.NewTokenService(gdb, auth.TokenOptions{})
	a := &app.App{Log: quiet, DB: gdb}
	a.Cfg.App.URL = "https://eript-lms.io.vn"
	mod := New(a, e.roles, e.tokens, WithStore(e.store))
	r := gin.New()
	r.ContextWithFallback = true
	mod.Register(r.Group("/api"))
	e.router = r

	t.Cleanup(func() {
		like := "%." + e.run + "@catalog.test"
		gdb.Exec(`DELETE FROM courses WHERE instructor_id IN (SELECT id FROM users WHERE email LIKE ?)`, like)
		gdb.Exec(`DELETE FROM categories WHERE slug LIKE ? AND parent_id IS NOT NULL`, "%-"+e.run)
		gdb.Exec(`DELETE FROM categories WHERE slug LIKE ?`, "%-"+e.run)
		gdb.Exec(`DELETE FROM users WHERE email LIKE ?`, like)
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return e
}

// user creates a user with roles and returns its id and bearer token.
func (e *env) user(name string, roles ...string) (int64, string) {
	e.t.Helper()
	ctx := context.Background()
	var id int64
	email := fmt.Sprintf("%s.%s@catalog.test", name, e.run)
	if err := e.db.Raw(`INSERT INTO users (email, name, avatar_path) VALUES (?, ?, 'users/avatars/a.jpg') RETURNING id`,
		email, "User "+name).Scan(&id).Error; err != nil {
		e.t.Fatal(err)
	}
	for _, r := range roles {
		if err := e.roles.AssignRole(ctx, nil, id, r); err != nil {
			e.t.Fatal(err)
		}
	}
	tok, err := e.tokens.Issue(ctx, nil, id, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return id, tok.PlainText
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
	if strings.HasPrefix(out.Raw, "[") {
		_ = json.Unmarshal(w.Body.Bytes(), &out.List)
	} else {
		_ = json.Unmarshal(w.Body.Bytes(), &out.Body)
	}
	return out
}

func (e *env) expect(r resp, code int, msg string) resp {
	e.t.Helper()
	if r.Code != code {
		e.t.Fatalf("status %d, want %d: %s", r.Code, code, r.Raw)
	}
	if msg != "" && r.Body["message"] != msg {
		e.t.Fatalf("message %q, want %q (%s)", r.Body["message"], msg, r.Raw)
	}
	return r
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func arr(v any) []any          { a, _ := v.([]any); return a }
func num(v any) int64          { f, _ := v.(float64); return int64(f) }

func (e *env) category(name string, parent *int64, sort int) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.Raw(`INSERT INTO categories (name, slug, parent_id, sort_order) VALUES (?, ?, ?, ?) RETURNING id`,
		name, name+"-"+e.run, parent, sort).Scan(&id).Error; err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) course(instructor int64, title, status string, category *int64) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.Raw(`INSERT INTO courses (instructor_id, title, slug, status, category_id, price) VALUES (?, ?, ?, ?, ?, 100000) RETURNING id`,
		instructor, title, slugify(title)+"-"+e.run+fmt.Sprint(time.Now().UnixNano()%100000), status, category).Scan(&id).Error; err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestCategories(t *testing.T) {
	e := setup(t)
	inst, _ := e.user("inst", rbac.RoleInstructor)
	root := e.category("root", nil, 0)
	child := e.category("child", &root, 0)
	grand := e.category("grand", &child, 0)
	e.course(inst, "Published grand", "published", &grand)
	e.course(inst, "Published root", "published", &root)
	e.course(inst, "Draft root", "draft", &root)

	r := e.do("GET", "/api/categories", "", nil)
	e.expect(r, 200, "")
	var found map[string]any
	for _, c := range r.List {
		if num(obj(c)["id"]) == root {
			found = obj(c)
		}
	}
	if found == nil || num(found["courses_count"]) != 2 {
		t.Fatalf("root: %v", found)
	}
	ch := obj(arr(found["children"])[0])
	gr := obj(arr(ch["children"])[0])
	if num(ch["courses_count"]) != 1 || num(gr["courses_count"]) != 1 {
		t.Fatalf("subtree counts: %v", ch)
	}
	if _, has := gr["children"]; has {
		t.Fatalf("third level must not carry children: %v", gr)
	}

	r = e.expect(e.do("GET", fmt.Sprintf("/api/categories/%d", child), "", nil), 200, "")
	if num(obj(r.Body["parent"])["id"]) != root || num(r.Body["courses_count"]) != 1 {
		t.Fatalf("show: %s", r.Raw)
	}
	e.expect(e.do("GET", "/api/categories/abc", "", nil), 404, `No query results for model [App\Models\Category] abc`)

	r = e.expect(e.do("GET", "/api/courses/categories", "", nil), 200, "")
	for _, c := range r.List {
		if num(obj(c)["id"]) == root {
			// Only extension courses of root + direct children count (grandchild excluded).
			if num(obj(c)["extension_courses_count"]) != 1 || len(arr(obj(c)["children"])) != 1 {
				t.Fatalf("program categories: %v", c)
			}
		}
	}

	// Catalog filter: parent category includes descendants.
	r = e.expect(e.do("GET", "/api/courses?category=root-"+e.run, "", nil), 200, "")
	if num(r.Body["total"]) != 2 {
		t.Fatalf("category filter: %s", r.Raw)
	}
	r = e.expect(e.do("GET", "/api/courses?category=missing-"+e.run, "", nil), 200, "")
	if num(r.Body["total"]) != 0 {
		t.Fatalf("unknown category must match nothing: %s", r.Raw)
	}
}

func TestCourseLifecycle(t *testing.T) {
	e := setup(t)
	instID, inst := e.user("owner", rbac.RoleInstructor)
	_, other := e.user("other", rbac.RoleInstructor)
	studentID, student := e.user("student", rbac.RoleStudent)
	_, admin := e.user("admin", rbac.RoleAdmin)
	cat := e.category("cat", nil, 0)

	// --- create
	e.expect(e.do("POST", "/api/courses", "", map[string]any{}), 401, "Unauthenticated.")
	e.expect(e.do("POST", "/api/courses", student, map[string]any{}), 403, "User does not have the right permissions.")
	r := e.expect(e.do("POST", "/api/courses", inst, map[string]any{"price": -1, "category_id": 999999999}), 422,
		"The title field is required. (and 2 more errors)")
	r = e.expect(e.do("POST", "/api/courses", inst, map[string]any{
		"title": "Lập trình Gô " + e.run, "price": "199000", "category_id": cat, "is_featured": "1",
		"thumbnail": "http://localhost:9000/lms-videos/courses/thumbnails/x.png",
	}), 201, "Course created")
	course := obj(r.Body["course"])
	slug := course["slug"].(string)
	if slug != "lap-trinh-go-"+e.run || num(course["user_id"]) != instID || num(course["price"]) != 199000 ||
		course["status"] != "draft" || course["is_featured"] != true ||
		course["thumbnail"] != "/minio/lms-videos/courses/thumbnails/x.png" || obj(course["instructor"])["avatar"] == nil {
		t.Fatalf("created: %s", r.Raw)
	}
	cid := num(course["id"])
	var stored string
	e.db.Raw(`SELECT thumbnail_path FROM courses WHERE id = ?`, cid).Scan(&stored)
	if stored != "courses/thumbnails/x.png" {
		t.Fatalf("thumbnail key: %s", stored)
	}
	// Same title -> "-1" suffix.
	r = e.expect(e.do("POST", "/api/courses", inst, map[string]any{"title": "Lập trình Gô " + e.run, "price": 0}), 201, "")
	if obj(r.Body["course"])["slug"] != slug+"-1" {
		t.Fatalf("slug suffix: %v", obj(r.Body["course"])["slug"])
	}
	base := fmt.Sprintf("/api/courses/%d", cid)

	// --- update (JSON and spoofed multipart)
	e.expect(e.do("PUT", base, other, map[string]any{"title": "x"}), 403, "Forbidden")
	e.expect(e.do("PUT", "/api/courses/no-such-"+e.run, inst, map[string]any{}), 404, `No query results for model [App\Models\Course].`)
	r = e.expect(e.do("PUT", base, inst, map[string]any{"level": "pro", "learning_outcomes": []any{"ok", 5}}), 422, "")
	if r.Body["errors"].(map[string]any)["learning_outcomes.1"] == nil {
		t.Fatalf("nested errors: %s", r.Raw)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	r = e.expect(e.do("POST", base, inst, newMultipart(map[string]string{
		"_method": "PUT", "learning_outcomes": `["A","B"]`, "level": "", "description": "Mô tả", "is_featured": "0",
	}, "thumbnail_file", "t.png", png)), 200, "Course updated")
	course = obj(r.Body["course"])
	if fmt.Sprint(course["learning_outcomes"]) != "[A B]" || course["is_featured"] != false || course["description"] != "Mô tả" ||
		!strings.HasPrefix(course["thumbnail"].(string), "/minio/lms-videos/courses/thumbnails/") || obj(course["category"]) == nil {
		t.Fatalf("updated: %s", r.Raw)
	}

	// --- sections
	r = e.expect(e.do("POST", base+"/sections", inst, map[string]any{"title": "Chương 1"}), 201, "Section created successfully")
	sec1 := num(obj(r.Body["data"])["id"])
	if num(obj(r.Body["data"])["position"]) != 1 {
		t.Fatalf("section position: %s", r.Raw)
	}
	r = e.expect(e.do("POST", base+"/sections", inst, map[string]any{"title": "Chương 2"}), 201, "")
	sec2 := num(obj(r.Body["data"])["id"])
	e.expect(e.do("POST", base+"/sections", other, map[string]any{"title": "x"}), 403, "Unauthorized")
	e.expect(e.do("POST", base+"/sections/reorder", inst, map[string]any{"sections": []any{
		map[string]any{"id": sec1, "position": 2}, map[string]any{"id": sec2, "position": 1}}}), 200, "Sections reordered successfully")
	r = e.expect(e.do("POST", base+"/sections/reorder", inst, map[string]any{"sections": []any{map[string]any{"id": 0}}}), 422, "")
	if r.Body["errors"].(map[string]any)["sections.0.position"] == nil {
		t.Fatalf("reorder errors: %s", r.Raw)
	}

	// --- publish needs a lesson
	e.expect(e.do("POST", base+"/publish", inst, nil), 422, "Course must have at least 1 lesson before publishing")

	// --- lessons
	otherCourse := e.course(instID, "Other "+e.run, "draft", nil)
	var foreignSection int64
	e.db.Raw(`INSERT INTO sections (course_id, title) VALUES (?, 'x') RETURNING id`, otherCourse).Scan(&foreignSection)
	r = e.expect(e.do("POST", base+"/lessons", inst, map[string]any{"title": "L", "section_id": foreignSection}), 422,
		"The selected section id is invalid.")
	r = e.expect(e.do("POST", base+"/lessons", inst, map[string]any{"title": "Preview", "section_id": sec1, "is_preview": true, "duration": 60}), 201, "Lesson created")
	preview := obj(r.Body["lesson"])
	if num(preview["order"]) != 1 || preview["type"] != "video" || num(preview["duration"]) != 60 || preview["video_url"] != nil {
		t.Fatalf("lesson: %s", r.Raw)
	}
	r = e.expect(e.do("POST", base+"/lessons", inst, map[string]any{"title": "Locked", "section_id": sec1,
		"video_url": "https://youtu.be/abc"}), 201, "")
	locked := num(obj(r.Body["lesson"])["id"])
	previewID := num(preview["id"])
	r = e.expect(e.do("PUT", fmt.Sprintf("%s/lessons/%d", base, locked), inst, map[string]any{"order": 0, "type": "page"}), 200, "Lesson updated")
	if num(obj(r.Body["lesson"])["order"]) != 0 || obj(r.Body["lesson"])["type"] != "page" {
		t.Fatalf("lesson update: %s", r.Raw)
	}
	e.expect(e.do("POST", base+"/lessons/reorder", inst, map[string]any{"order": []any{locked, previewID}}), 200, "Lessons reordered")
	e.expect(e.do("GET", fmt.Sprintf("/api/courses/%d/lessons/%d", otherCourse, locked), "", nil), 404, "Not found")
	e.expect(e.do("GET", base+"/lessons/999999999", "", nil), 404, `No query results for model [App\Models\Lesson] 999999999`)

	// --- visibility before publishing
	e.expect(e.do("GET", base+"/sections", "", nil), 404, "Course not found")
	r = e.expect(e.do("GET", base+"/sections", inst, nil), 200, "Sections retrieved successfully")
	data := arr(r.Body["data"])
	if len(data) != 2 || num(obj(data[0])["id"]) != sec2 || len(arr(obj(data[1])["lessons"])) != 2 {
		t.Fatalf("builder sections: %s", r.Raw)
	}
	if _, ok := obj(arr(obj(data[1])["lessons"])[0])["virtual_class"]; !ok {
		t.Fatalf("builder lesson relations: %s", r.Raw)
	}

	// --- publish
	r = e.expect(e.do("POST", base+"/publish", inst, nil), 200, "Course submitted for review")
	if obj(r.Body["course"])["status"] != "pending_review" {
		t.Fatalf("publish: %s", r.Raw)
	}
	e.expect(e.do("POST", base+"/publish", inst, nil), 422, "Course has already been submitted or published")
	e.db.Exec(`UPDATE courses SET status = 'published' WHERE id = ?`, cid)

	// --- public catalog search (accent-insensitive)
	r = e.expect(e.do("GET", "/api/courses?search=lap+trinh+go+"+e.run+"&per_page=5", "", nil), 200, "")
	items := arr(r.Body["data"])
	if num(r.Body["total"]) != 1 || num(r.Body["per_page"]) != 5 || num(obj(items[0])["lessons_count"]) != 2 ||
		obj(items[0])["reviews_avg_rating"] != nil {
		t.Fatalf("search: %s", r.Raw)
	}
	r = e.expect(e.do("GET", "/api/my-courses?sort=title", inst, nil), 200, "")
	if num(r.Body["total"]) != 3 {
		t.Fatalf("my courses: %s", r.Raw)
	}

	// --- lessons index / show / video-url
	r = e.expect(e.do("GET", base+"/lessons", "", nil), 200, "")
	if len(r.List) != 2 || obj(r.List[0])["locked"] != true || obj(r.List[1])["locked"] != false {
		t.Fatalf("guest lessons: %s", r.Raw)
	}
	if _, ok := obj(r.List[0])["video_url"]; ok {
		t.Fatal("guest must not see video_url")
	}
	r = e.expect(e.do("GET", base+"/lessons?include_attachments=1", inst, nil), 200, "")
	if obj(r.List[0])["video_url"] != "https://youtu.be/abc" || obj(r.List[0])["attachments"] == nil {
		t.Fatalf("owner lessons: %s", r.Raw)
	}
	lockedURL := fmt.Sprintf("%s/lessons/%d", base, locked)
	e.expect(e.do("GET", lockedURL, student, nil), 403, "Enrollment required")
	e.expect(e.do("GET", fmt.Sprintf("%s/lessons/%d", base, previewID), "", nil), 200, "")
	e.db.Exec(`INSERT INTO enrollments (user_id, course_id) VALUES (?, ?)`, studentID, cid)
	for _, q := range []string{
		`INSERT INTO virtual_classes (lesson_id, provider, join_url, start_at, duration_minutes) VALUES (?, 'zoom', 'https://zoom.us/j/1', now(), 45)`,
		`INSERT INTO scorm_packages (lesson_id, uuid, entry_path) VALUES (?, gen_random_uuid(), 'scorm/abc/index.html')`,
		`INSERT INTO lesson_assignments (lesson_id, instructions) VALUES (?, 'Do it')`,
		`INSERT INTO offline_sessions (lesson_id, location, start_at, duration_minutes, latitude, longitude, qr_token) VALUES (?, 'A1', now(), 90, 21.0285, 105.8542, 'secret')`,
		`INSERT INTO quizzes (lesson_id, title, settings) VALUES (?, 'Q', '{"shuffle":true}')`,
		`INSERT INTO lesson_attachments (lesson_id, original_name, file_path, size_bytes) VALUES (?, 'a.pdf', 'lessons/attachments/a.pdf', 1887437)`,
	} {
		if err := e.db.Exec(q, locked).Error; err != nil {
			t.Fatal(err)
		}
	}
	r = e.expect(e.do("GET", lockedURL, student, nil), 200, "")
	vc, sp, as, off, qz := obj(r.Body["virtual_class"]), obj(r.Body["scorm_package"]), obj(r.Body["assignment"]),
		obj(r.Body["offline_session"]), obj(r.Body["quiz"])
	att := obj(arr(r.Body["attachments"])[0])
	if num(vc["duration"]) != 45 || sp["entry_url"] != "/minio/lms-videos/scorm/abc/index.html" ||
		as["allowed_extensions"] != "pdf,doc,docx,zip" || num(as["max_file_size"]) != 10240 ||
		off["qr_token"] != nil || off["latitude"] != 21.0285 || num(off["duration"]) != 90 ||
		obj(qz["settings"])["shuffle"] != true || att["file_size"] != "1.8MB" ||
		att["url"] != "/minio/lms-videos/lessons/attachments/a.pdf" {
		t.Fatalf("show lesson relations: %s", r.Raw)
	}
	r = e.expect(e.do("GET", lockedURL, inst, nil), 200, "")
	if obj(r.Body["offline_session"])["qr_token"] != "secret" {
		t.Fatalf("owner sees qr token: %s", r.Raw)
	}
	r = e.expect(e.do("GET", lockedURL+"/video-url", student, nil), 200, "")
	if r.Body["url"] != "https://youtu.be/abc" || num(r.Body["expires_in"]) != 3600 {
		t.Fatalf("video url: %s", r.Raw)
	}
	e.expect(e.do("GET", fmt.Sprintf("%s/lessons/%d/video-url", base, previewID), "", nil), 404, "No video uploaded yet")

	// --- upload video
	e.expect(e.do("POST", fmt.Sprintf("%s/lessons/%d/upload-video", base, previewID), inst,
		newMultipart(nil, "video", "a.exe", []byte("x"))), 422, "Định dạng .exe không được hỗ trợ. Chỉ chấp nhận: mp4, mov, avi, webm, mkv, m4v.")
	e.expect(e.do("POST", fmt.Sprintf("%s/lessons/%d/upload-video", base, previewID), inst,
		newMultipart(nil, "", "", nil)), 422, "The video field is required.")
	r = e.expect(e.do("POST", fmt.Sprintf("%s/lessons/%d/upload-video", base, previewID), inst,
		newMultipart(nil, "video", "clip.mp4", make([]byte, 2*1048576))), 200, "Video uploaded successfully")
	lesson := obj(r.Body["lesson"])
	if lesson["video_size"] != "2MB" || lesson["video_status"] != "ready" ||
		!strings.HasPrefix(lesson["video_url"].(string), "/minio/lms-videos/lessons/videos/") {
		t.Fatalf("upload: %s", r.Raw)
	}

	// --- course detail (with a career path suggesting the other published course)
	extra := e.course(instID, "Extra "+e.run, "published", nil)
	var pathID int64
	e.db.Raw(`INSERT INTO career_paths (title, slug, price, status, published_at) VALUES ('Path', ?, 500000, 'published', now()) RETURNING id`,
		"path-"+e.run).Scan(&pathID)
	t.Cleanup(func() { e.db.Exec(`DELETE FROM career_paths WHERE id = ?`, pathID) })
	e.db.Exec(`INSERT INTO career_path_courses (career_path_id, course_id, sort_order) VALUES (?, ?, 0), (?, ?, 1), (?, ?, 2)`,
		pathID, cid, pathID, extra, pathID, otherCourse)
	r = e.expect(e.do("GET", "/api/courses/"+slug, student, nil), 200, "")
	if r.Body["is_enrolled"] != true || r.Body["has_reviewed"] != false || len(arr(r.Body["lessons"])) != 2 ||
		len(arr(r.Body["sections"])) != 2 || num(r.Body["enrollments_count"]) != 1 {
		t.Fatalf("detail: %s", r.Raw)
	}
	ps := arr(r.Body["path_suggestions"])
	if len(ps) != 1 || num(obj(ps[0])["total_count"]) != 3 || num(obj(ps[0])["owned_count"]) != 1 ||
		num(obj(ps[0])["remaining_total_price"]) != 100000 || num(obj(arr(obj(ps[0])["remaining_courses"])[0])["id"]) != extra {
		t.Fatalf("path suggestions: %s", r.Raw)
	}
	r = e.expect(e.do("GET", base+"/sections", "", nil), 200, "")
	if lessons := arr(obj(arr(r.Body["data"])[1])["lessons"]); len(lessons) != 2 || obj(lessons[0])["video_url"] != nil {
		t.Fatalf("public outline: %s", r.Raw)
	}

	// --- deletes
	e.expect(e.do("DELETE", fmt.Sprintf("/api/sections/%d", sec1), inst, nil), 422, "Cannot delete section with lessons. Please delete lessons first.")
	e.expect(e.do("DELETE", fmt.Sprintf("/api/sections/%d", sec2), other, nil), 403, "Unauthorized")
	r = e.expect(e.do("GET", fmt.Sprintf("/api/sections/%d", sec1), other, nil), 200, "Section retrieved successfully")
	e.expect(e.do("DELETE", fmt.Sprintf("/api/sections/%d", sec2), inst, nil), 200, "Section deleted successfully")
	e.expect(e.do("DELETE", fmt.Sprintf("%s/lessons/%d", base, previewID), inst, nil), 200, "Lesson deleted")
	if len(e.store.deleted) == 0 || !strings.HasPrefix(e.store.deleted[len(e.store.deleted)-1], "lessons/videos/") {
		t.Fatalf("video object not deleted: %v", e.store.deleted)
	}
	e.expect(e.do("DELETE", base, other, nil), 403, "Forbidden")
	e.expect(e.do("DELETE", base, admin, nil), 200, "Course deleted")
	e.expect(e.do("GET", base, "", nil), 404, `No query results for model [App\Models\Course].`)
}
