package interaction

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"sort"
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
	"github.com/Chilly913/PTIT_ERIPT_API/migrations"
)

// Integration tests run against TEST_DATABASE_URL (migrated to the latest
// version first). Every row created here is deleted at the end.

// fakeStore is an in-memory ObjectStore.
type fakeStore struct {
	mu   sync.Mutex
	objs map[string]string // key -> content type
	data map[string][]byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{objs: map[string]string{}, data: map[string][]byte{}}
}

func (s *fakeStore) Put(_ context.Context, key string, r io.Reader, _ int64, ct string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs[key], s.data[key] = ct, b
	return nil
}

func (s *fakeStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objs, key)
	delete(s.data, key)
	return nil
}

func (s *fakeStore) DeletePrefix(_ context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.objs {
		if strings.HasPrefix(k, prefix) {
			delete(s.objs, k)
			delete(s.data, k)
		}
	}
	return nil
}

func (s *fakeStore) URL(key string) string { return "http://localhost:9000/lms-videos/" + key }
func (s *fakeStore) Bucket() string        { return "lms-videos" }

func (s *fakeStore) keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

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
	cfg, err := config.LoadFrom(map[string]string{
		"APP_ENV": "testing", "APP_NAME": "ERIPT", "APP_URL": "https://eript-lms.io.vn",
		"APP_KEY": "base64:MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: gdb, run: fmt.Sprint(time.Now().UnixNano()), store: newFakeStore()}
	e.roles = rbac.New(gdb, nil, quiet)
	e.tokens = auth.NewTokenService(gdb, auth.TokenOptions{})
	a := &app.App{Cfg: cfg, Log: quiet, DB: gdb}
	lim := DefaultScormLimits
	lim.MaxUploadBytes = 1 << 20 // 1024 KB, to test the max rule
	mod := New(a, e.roles, e.tokens, WithStore(e.store), WithScormLimits(lim))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.ContextWithFallback = true
	mod.Register(r.Group("/api"))
	e.router = r

	t.Cleanup(func() {
		like := "%." + e.run + "@p2interaction.test"
		gdb.Exec(`DELETE FROM courses WHERE instructor_id IN (SELECT id FROM users WHERE email LIKE ?)`, like)
		gdb.Exec(`DELETE FROM users WHERE email LIKE ?`, like)
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return e
}

// user creates a user with roles and returns (id, bearer token).
func (e *env) user(name string, roles ...string) (int64, string) {
	e.t.Helper()
	ctx := context.Background()
	var id int64
	if err := e.db.Raw(`INSERT INTO users (email, name, avatar_path) VALUES (?, ?, ?) RETURNING id`,
		name+"."+e.run+"@p2interaction.test", "User "+name, "users/avatars/"+name+".jpg").Scan(&id).Error; err != nil {
		e.t.Fatal(err)
	}
	for _, r := range roles {
		if err := e.roles.AssignRole(ctx, nil, id, r); err != nil {
			e.t.Fatal(err)
		}
	}
	tok, err := e.tokens.Issue(ctx, nil, id, "test")
	if err != nil {
		e.t.Fatal(err)
	}
	return id, tok.PlainText
}

func (e *env) course(owner int64, slug string) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.Raw(`INSERT INTO courses (instructor_id, title, slug, status) VALUES (?, ?, ?, 'published') RETURNING id`,
		owner, "Course "+slug, slug+"-"+e.run).Scan(&id).Error; err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) lesson(course int64, typ string) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.Raw(`INSERT INTO lessons (course_id, title, type) VALUES (?, 'Lesson', ?) RETURNING id`, course, typ).Scan(&id).Error; err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) enroll(user, course int64) {
	e.t.Helper()
	if err := e.db.Exec(`INSERT INTO enrollments (user_id, course_id) VALUES (?, ?)`, user, course).Error; err != nil {
		e.t.Fatal(err)
	}
}

type multipartBody struct {
	buf bytes.Buffer
	ct  string
}

func newMultipart(fields map[string]string, fileField, fileName string, file []byte) *multipartBody {
	b := &multipartBody{}
	w := multipart.NewWriter(&b.buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if fileField != "" {
		fw, _ := w.CreateFormFile(fileField, fileName)
		_, _ = fw.Write(file)
	}
	_ = w.Close()
	b.ct = w.FormDataContentType()
	return b
}

type resp struct {
	Code int
	Body map[string]any
	Raw  string
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

func TestReviewsFlow(t *testing.T) {
	e := setup(t)
	owner, _ := e.user("owner", "instructor")
	stu, stuTok := e.user("stu", "student")
	_, otherTok := e.user("other", "student")
	_, adminTok := e.user("admin", "admin")
	_, instrTok := e.user("instr", "instructor")
	cid := e.course(owner, "reviews")
	base := fmt.Sprintf("/api/courses/%d/reviews", cid)

	e.expect(e.do("GET", "/api/courses/999999999/reviews", "", nil), 404, `No query results for model [App\Models\Course].`)
	e.expect(e.do("POST", base, "", map[string]any{"rating": 5}), 401, "Unauthenticated.")
	e.expect(e.do("POST", base, instrTok, map[string]any{"rating": 5}), 403, "User does not have the right permissions.")
	e.expect(e.do("POST", base, stuTok, map[string]any{"rating": 5}), 403, "Bạn cần đăng ký khóa học trước khi đánh giá.")

	e.enroll(stu, cid)
	r := e.do("POST", base, stuTok, map[string]any{"rating": 9})
	e.expect(r, 422, "The rating field must not be greater than 5.")
	r = e.do("POST", base, stuTok, map[string]any{"rating": 4, "comment": " Hay "})
	e.expect(r, 201, "Đánh giá thành công!")
	rev := r.Body["review"].(map[string]any)
	user := rev["user"].(map[string]any)
	if rev["rating"] != 4.0 || rev["comment"] != "Hay" || user["name"] != "User stu" ||
		user["avatar"] != "/minio/lms-videos/users/avatars/stu.jpg" || len(user) != 3 {
		t.Fatalf("review %v", rev)
	}
	rid := int64(rev["id"].(float64))
	e.expect(e.do("POST", base, stuTok, map[string]any{"rating": 5}), 422, "Bạn đã đánh giá khóa học này rồi.")

	// Public list via slug.
	r = e.do("GET", "/api/courses/reviews-"+e.run+"/reviews", "", nil)
	e.expect(r, 200, "")
	if r.Body["total"] != 1.0 || r.Body["per_page"] != 10.0 || len(r.Body["data"].([]any)) != 1 {
		t.Fatalf("list %s", r.Raw)
	}

	rp := fmt.Sprintf("%s/%d", base, rid)
	e.expect(e.do("PUT", base+"/999999999", stuTok, map[string]any{}), 404, `No query results for model [App\Models\Review] 999999999`)
	e.expect(e.do("PUT", rp, otherTok, map[string]any{"rating": 1}), 403, "Forbidden")
	r = e.do("PUT", rp, stuTok, map[string]any{"comment": nil})
	e.expect(r, 200, "Cập nhật đánh giá thành công!")
	if rv := r.Body["review"].(map[string]any); rv["comment"] != nil || rv["rating"] != 4.0 {
		t.Fatalf("update %v", rv)
	}
	r = e.do("PUT", rp, stuTok, map[string]any{"rating": 2})
	if rv := r.Body["review"].(map[string]any); rv["rating"] != 2.0 {
		t.Fatalf("update rating %v", rv)
	}
	e.expect(e.do("DELETE", rp, otherTok, nil), 403, "Forbidden")
	e.expect(e.do("DELETE", rp, adminTok, nil), 200, "Đã xóa đánh giá.")
}

func TestQaFlow(t *testing.T) {
	e := setup(t)
	owner, ownerTok := e.user("qowner", "instructor")
	stu, stuTok := e.user("qstu", "student")
	_, outTok := e.user("qout", "student")
	cid := e.course(owner, "qa")
	lid := e.lesson(cid, "video")
	base := fmt.Sprintf("/api/courses/%d", cid)

	e.expect(e.do("GET", base+"/qas", "", nil), 401, "Unauthenticated.")
	e.expect(e.do("POST", base+"/qas", outTok, map[string]any{"subject": "s", "content": "c"}), 403, "Bạn cần đăng ký khóa học để đặt câu hỏi.")
	e.enroll(stu, cid)
	r := e.do("POST", base+"/qas", stuTok, map[string]any{"lesson_id": 999999999})
	e.expect(r, 422, "The selected lesson id is invalid. (and 2 more errors)")
	r = e.do("POST", base+"/qas", stuTok, map[string]any{"lesson_id": lid, "subject": "Why?", "content": "Because"})
	e.expect(r, 201, "")
	if r.Body["lesson_id"] != float64(lid) || r.Body["user"].(map[string]any)["id"] != float64(stu) {
		t.Fatalf("qa %s", r.Raw)
	}
	qid := int64(r.Body["id"].(float64))
	r = e.do("POST", base+"/qas", ownerTok, map[string]any{"subject": "General", "content": "x"})
	e.expect(r, 201, "")

	qp := fmt.Sprintf("%s/qas/%d", base, qid)
	e.expect(e.do("POST", qp+"/replies", outTok, map[string]any{"content": "x"}), 403, "Forbidden")
	r = e.do("POST", qp+"/replies", ownerTok, map[string]any{"content": "Answer"})
	e.expect(r, 201, "")
	if r.Body["like_count"] != 0.0 || r.Body["my_reaction"] != nil || r.Body["course_qa_id"] != float64(qid) {
		t.Fatalf("reply %s", r.Raw)
	}
	replyID := r.Body["id"].(float64)

	// Reactions: create, flip, toggle off; reply reactions.
	react := func(tok, typ string, id any, kind string) resp {
		return e.do("POST", base+"/qa-reactions", tok, map[string]any{"reactable_type": typ, "reactable_id": id, "kind": kind})
	}
	r = react(stuTok, "qa", qid, "like")
	if r.Raw != `{"like_count":1,"dislike_count":0,"my_reaction":"like"}` {
		t.Fatalf("react %s", r.Raw)
	}
	r = react(stuTok, "qa", qid, "dislike")
	if r.Raw != `{"like_count":0,"dislike_count":1,"my_reaction":"dislike"}` {
		t.Fatalf("flip %s", r.Raw)
	}
	r = react(outTok, "qa", qid, "like") // no enrollment check, like Laravel
	r = react(stuTok, "reply", replyID, "like")
	if r.Raw != `{"like_count":1,"dislike_count":0,"my_reaction":"like"}` {
		t.Fatalf("reply react %s", r.Raw)
	}
	e.expect(react(stuTok, "qa", 999999999, "like"), 404, "Not found")
	e.expect(react(stuTok, "post", qid, "love"), 422, "The selected reactable type is invalid. (and 1 more error)")

	// Listing with reactions and replies.
	r = e.do("GET", base+"/qas", stuTok, nil)
	e.expect(r, 200, "")
	data := r.Body["data"].([]any)
	if r.Body["total"] != 2.0 || r.Body["per_page"] != 20.0 || len(data) != 2 {
		t.Fatalf("list %s", r.Raw)
	}
	var item map[string]any
	for _, d := range data {
		if d.(map[string]any)["id"] == float64(qid) {
			item = d.(map[string]any)
		}
	}
	if item["like_count"] != 1.0 || item["dislike_count"] != 1.0 || item["my_reaction"] != "dislike" ||
		len(item["reactions"].([]any)) != 2 {
		t.Fatalf("item %v", item)
	}
	rx := item["reactions"].([]any)[0].(map[string]any)
	if rx["reactable_type"] != `App\Models\CourseQa` || rx["reactable_id"] != float64(qid) {
		t.Fatalf("reaction %v", rx)
	}
	rep := item["replies"].([]any)[0].(map[string]any)
	if rep["my_reaction"] != "like" || rep["user"].(map[string]any)["id"] != float64(owner) {
		t.Fatalf("reply item %v", rep)
	}
	r = e.do("GET", fmt.Sprintf("%s/qas?lesson_id=%d", base, lid), stuTok, nil)
	if r.Body["total"] != 1.0 {
		t.Fatalf("lesson filter %s", r.Raw)
	}
	r = e.do("GET", base+"/qas?lesson_id=", stuTok, nil)
	if r.Body["total"] != 1.0 || r.Body["data"].([]any)[0].(map[string]any)["subject"] != "General" {
		t.Fatalf("null lesson filter %s", r.Raw)
	}

	// Show: replies with users, no reaction state.
	r = e.do("GET", qp, outTok, nil)
	e.expect(r, 200, "")
	if _, ok := r.Body["like_count"]; ok || len(r.Body["replies"].([]any)) != 1 {
		t.Fatalf("show %s", r.Raw)
	}
	other := e.course(owner, "qa2")
	e.expect(e.do("GET", fmt.Sprintf("/api/courses/%d/qas/%d", other, qid), stuTok, nil), 404, "Not found")
	e.expect(e.do("GET", base+"/qas/abc", stuTok, nil), 404, `No query results for model [App\Models\CourseQa] abc`)
}

func TestVirtualClassFlow(t *testing.T) {
	e := setup(t)
	owner, ownerTok := e.user("vowner", "instructor")
	_, otherInstrTok := e.user("vother", "instructor")
	_, stuTok := e.user("vstu", "student")
	cid := e.course(owner, "vc")
	lid := e.lesson(cid, "video")
	p := fmt.Sprintf("/api/courses/%d/lessons/%d/virtual-class", cid, lid)

	e.expect(e.do("GET", p, stuTok, nil), 404, "No virtual class set for this lesson")
	body := map[string]any{"provider": "zoom", "join_url": "https://zoom.us/j/1", "meeting_password": "pw",
		"start_at": "2026-10-20T15:30:00+07:00", "duration": "45"}
	e.expect(e.do("POST", p, stuTok, body), 403, "User does not have the right permissions.")
	e.expect(e.do("POST", p, otherInstrTok, body), 403, "Forbidden")
	e.expect(e.do("POST", p, ownerTok, map[string]any{}), 422, "The provider field is required. (and 3 more errors)")
	r := e.do("POST", p, ownerTok, body)
	e.expect(r, 200, "Virtual class updated successfully")
	vc := r.Body["virtual_class"].(map[string]any)
	if vc["start_at"] != "2026-10-20T08:30:00.000000Z" || vc["duration"] != 45.0 || vc["meeting_password"] != "pw" || vc["meeting_id"] != nil {
		t.Fatalf("vc %v", vc)
	}
	var typ string
	e.db.Raw(`SELECT type FROM lessons WHERE id = ?`, lid).Scan(&typ)
	if typ != "virtual_class" {
		t.Fatalf("lesson type %s", typ)
	}
	// Update keeps absent optional fields.
	r = e.do("POST", p, ownerTok, map[string]any{"provider": "jitsi", "join_url": "https://meet.jit.si/x", "start_at": "2026-10-21 09:00", "duration": 30})
	vc = r.Body["virtual_class"].(map[string]any)
	if vc["provider"] != "jitsi" || vc["meeting_password"] != "pw" {
		t.Fatalf("vc update %v", vc)
	}
	r = e.do("GET", p, stuTok, nil)
	e.expect(r, 200, "")
	if r.Body["join_url"] != "https://meet.jit.si/x" {
		t.Fatalf("show %s", r.Raw)
	}
	other := e.course(owner, "vc2")
	e.expect(e.do("GET", fmt.Sprintf("/api/courses/%d/lessons/%d/virtual-class", other, lid), stuTok, nil), 404, "Not found")
	e.expect(e.do("DELETE", fmt.Sprintf("/api/courses/%d/lessons/%d/virtual-class", other, lid), ownerTok, nil), 404, "Not found")
	e.expect(e.do("DELETE", p, ownerTok, nil), 200, "Virtual class details removed")
	e.expect(e.do("GET", p, stuTok, nil), 404, "No virtual class set for this lesson")
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, _ := w.Create(n)
		_, _ = f.Write([]byte(files[n]))
	}
	_ = w.Close()
	return buf.Bytes()
}

func TestScormFlow(t *testing.T) {
	e := setup(t)
	owner, ownerTok := e.user("sowner", "instructor")
	stu, stuTok := e.user("sstu", "student")
	cid := e.course(owner, "scorm")
	lid := e.lesson(cid, "video")
	p := fmt.Sprintf("/api/courses/%d/lessons/%d/scorm-package", cid, lid)

	e.expect(e.do("GET", p, stuTok, nil), 404, "No package found")
	e.expect(e.do("POST", p, ownerTok, newMultipart(map[string]string{"type": "scorm"}, "", "", nil)), 422, "SCORM package file is required")
	e.expect(e.do("POST", p, ownerTok, map[string]any{"type": "h5p"}), 422, "H5P embed URL is required")
	r := e.do("POST", p, ownerTok, newMultipart(map[string]string{"type": "scorm"}, "scorm_file", "x.zip", []byte("not a zip")))
	e.expect(r, 422, "The scorm file field must be a file of type: zip.")
	big := append([]byte("PK\x03\x04"), make([]byte, 1100*1024)...)
	r = e.do("POST", p, ownerTok, newMultipart(map[string]string{"type": "scorm"}, "scorm_file", "x.zip", big))
	e.expect(r, 422, "The scorm file field must not be greater than 1024 kilobytes.")
	evil := zipBytes(t, map[string]string{"index.html": "x", "../evil.sh": "rm -rf /"})
	r = e.do("POST", p, ownerTok, newMultipart(map[string]string{"type": "scorm"}, "scorm_file", "x.zip", evil))
	e.expect(r, 422, "Invalid path in SCORM package: ../evil.sh")
	if len(e.store.keys("scorm/")) != 0 {
		t.Fatal("nothing must be uploaded for a rejected package")
	}

	pkg := zipBytes(t, map[string]string{"imsmanifest.xml": manifest12, "content/start.html": "<html/>", ".DS_Store": "x"})
	r = e.do("POST", p, ownerTok, newMultipart(map[string]string{"type": "scorm"}, "scorm_file", "pkg.zip", pkg))
	e.expect(r, 200, "SCORM/H5P package saved successfully")
	sp := r.Body["scorm_package"].(map[string]any)
	id := sp["uuid"].(string)
	if sp["entry_url"] != "/minio/lms-videos/scorm/"+id+"/content/start.html" || sp["version"] != "1.2" || sp["title"] != "Intro to Go" {
		t.Fatalf("package %v", sp)
	}
	keys := e.store.keys("scorm/" + id + "/")
	if strings.Join(keys, ",") != "scorm/"+id+"/content/start.html,scorm/"+id+"/imsmanifest.xml" {
		t.Fatalf("keys %v", keys)
	}
	if ct := e.store.objs["scorm/"+id+"/content/start.html"]; !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type %q", ct)
	}

	// Re-upload keeps the uuid and replaces the files.
	pkg2 := zipBytes(t, map[string]string{"index.html": "v2"})
	r = e.do("POST", p, ownerTok, newMultipart(map[string]string{"type": "scorm", "version": "2004 4th Edition", "title": "Mine"}, "scorm_file", "pkg.zip", pkg2))
	e.expect(r, 200, "")
	sp = r.Body["scorm_package"].(map[string]any)
	if sp["uuid"] != id || sp["version"] != "2004" || sp["title"] != "Mine" || len(e.store.keys("scorm/"+id+"/")) != 1 {
		t.Fatalf("re-upload %v %v", sp, e.store.keys("scorm/"))
	}
	r = e.do("GET", p, stuTok, nil)
	e.expect(r, 200, "")

	// SCORM tracking.
	tp := fmt.Sprintf("/api/courses/%d/lessons/%d/scorm/track", cid, lid)
	r = e.do("POST", tp, stuTok, map[string]any{"version": "2004", "completion_status": "completed", "score": 88.5, "suspend_data": "abc"})
	if r.Raw != `{"completed":true,"message":"Progress tracked"}` {
		t.Fatalf("track %s", r.Raw)
	}
	var st struct {
		Done  bool
		Score float64
		Data  string
	}
	e.db.Raw(`SELECT p.completed_at IS NOT NULL AS done, s.score, s.suspend_data AS data FROM lesson_progress p
JOIN scorm_runtime_states s USING (user_id, lesson_id) WHERE p.user_id = ? AND p.lesson_id = ?`, stu, lid).Scan(&st)
	if !st.Done || st.Score != 88.5 || st.Data != "abc" {
		t.Fatalf("state %+v", st)
	}
	r = e.do("POST", tp, stuTok, map[string]any{"status": "incomplete"})
	if r.Body["completed"] != false {
		t.Fatalf("track 2 %s", r.Raw)
	}
	e.expect(e.do("POST", tp, stuTok, map[string]any{"status": "done", "score": 101}), 422, "The selected status is invalid. (and 1 more error)")

	// H5P switch, then delete.
	r = e.do("POST", p, ownerTok, map[string]any{"type": "h5p", "entry_url": "https://h5p.org/h5p/embed/1"})
	e.expect(r, 200, "")
	if sp = r.Body["scorm_package"].(map[string]any); sp["entry_url"] != "https://h5p.org/h5p/embed/1" || sp["version"] != "h5p" || sp["title"] != "Lesson" {
		t.Fatalf("h5p %v", sp)
	}
	e.expect(e.do("DELETE", p, ownerTok, nil), 200, "SCORM/H5P package removed")
	if len(e.store.keys("scorm/"+id+"/")) != 0 {
		t.Fatal("objects not deleted")
	}
	e.expect(e.do("GET", p, stuTok, nil), 404, "No package found")
}
