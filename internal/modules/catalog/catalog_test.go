package catalog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/rbac"
)

func init() { gin.SetMode(gin.TestMode) }

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Lập trình Python cơ bản":   "lap-trinh-python-co-ban",
		"  Đại số tuyến tính  ":     "dai-so-tuyen-tinh",
		"C++ & Java":                "c-java",
		"node_js @ home":            "node-js-at-home",
		"a.b -- c":                  "ab-c",
		"Ứng dụng AI/ML 2025":       "ung-dung-aiml-2025",
		"日本語":                       "",
		"Crème brûlée façade Œuvre": "creme-brulee-facade-oeuvre",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPHPCasts(t *testing.T) {
	ints := map[any]bool{"5": true, " 7 ": true, "-3": true, "05": false, "1.0": false, "abc": false,
		json.Number("12"): true, json.Number("1.5"): false, true: true, false: false, nil: false}
	for v, want := range ints {
		if got := isInteger(v); got != want {
			t.Errorf("isInteger(%#v) = %v", v, got)
		}
	}
	nums := map[any]bool{"1.5": true, ".5": true, "1e3": true, "abc": false, "": false, json.Number("2"): true, true: false}
	for v, want := range nums {
		if got := isNumeric(v); got != want {
			t.Errorf("isNumeric(%#v) = %v", v, got)
		}
	}
	intval := map[any]int64{"12abc": 12, "abc": 0, "1.9": 1, "1e3": 1000, json.Number("7"): 7, nil: 0, true: 1}
	for v, want := range intval {
		if got := phpInt(v); got != want {
			t.Errorf("phpInt(%#v) = %d", v, got)
		}
	}
	for v, want := range map[any]bool{"yes": true, "on": true, "1": true, "true": true, "0": false, "no": false, json.Number("1"): true} {
		if got := phpBool(v); got != want {
			t.Errorf("phpBool(%#v) = %v", v, got)
		}
	}
}

func TestFormValuesBrackets(t *testing.T) {
	m := map[string]any{}
	setFormValue(m, "order[]", "3")
	setFormValue(m, "order[]", "1")
	setFormValue(m, "sections[0][id]", "9")
	setFormValue(m, "sections[0][position]", "2")
	setFormValue(m, "title", "x")
	b, _ := json.Marshal(m)
	if string(b) != `{"order":["3","1"],"sections":{"0":{"id":"9","position":"2"}},"title":"x"}` {
		t.Fatalf("got %s", b)
	}
}

func newInput(t *testing.T, body string) *input {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	in, err := readInput(c)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestValidatorMessages(t *testing.T) {
	in := newInput(t, `{"title":"  ","price":"abc","category_id":"x","is_featured":"maybe","level":"pro",
		"learning_outcomes":["ok",null],"thumbnail":"not a url","order":-1}`)
	v := newValidator(in)
	v.field("title").required().string().max(255)
	v.field("price").required().numericRule().min(0)
	v.field("category_id").nullableRule().integer().exists(func(int64) (bool, error) { return true, nil })
	v.field("is_featured").nullableRule().boolean()
	v.field("level").nullableRule().string().in("beginner", "intermediate", "advanced")
	v.field("learning_outcomes").nullableRule().array()
	keys, vals := arrayItems(in.get("learning_outcomes"))
	for i, k := range keys {
		v.value("learning_outcomes."+k, vals[i], true).string().max(500)
	}
	v.field("thumbnail").nullableRule().url()
	v.field("order").sometimes().integer().min(0)
	v.field("duration").sometimes().integer() // absent: skipped
	v.field("description").nullableRule().string()
	b, _ := json.Marshal(v.err())
	want := `{"message":"The title field is required. (and 8 more errors)","errors":{` +
		`"title":["The title field is required."],` +
		`"price":["The price field must be a number."],` +
		`"category_id":["The category id field must be an integer.","The selected category id is invalid."],` +
		`"is_featured":["The is featured field must be true or false."],` +
		`"level":["The selected level is invalid."],` +
		`"learning_outcomes.1":["The learning_outcomes.1 field must be a string."],` +
		`"thumbnail":["The thumbnail field must be a valid URL."],` +
		`"order":["The order field must be at least 0."]}}`
	if string(b) != want {
		t.Fatalf("got\n%s\nwant\n%s", b, want)
	}
}

func TestValidatorMaxString(t *testing.T) {
	in := newInput(t, `{"title":"`+strings.Repeat("ă", 256)+`","price":5}`)
	v := newValidator(in)
	v.field("title").required().string().max(255)
	v.field("price").required().numericRule().min(10)
	b, _ := json.Marshal(v.err())
	if !bytes.Contains(b, []byte(`"The title field must not be greater than 255 characters."`)) ||
		!bytes.Contains(b, []byte(`"The price field must be at least 10."`)) {
		t.Fatalf("got %s", b)
	}
}

func TestSharedHelpers(t *testing.T) {
	if got := learning.CourseNotFoundMessage; got != `No query results for model [App\Models\Course].` {
		t.Errorf("course: %s", got)
	}
	if got := learning.ModelNotFoundMessage("Lesson", "12"); got != `No query results for model [App\Models\Lesson] 12` {
		t.Errorf("lesson: %s", got)
	}
	for b, want := range map[int64]string{0: "0MB", 1048576: "1MB", 13107200: "12.5MB", 1234567: "1.18MB"} {
		if got := *learning.FormatSizeMB(&b); got != want {
			t.Errorf("FormatSizeMB(%d) = %s, want %s", b, got, want)
		}
	}
	if learning.FormatSizeMB(nil) != nil {
		t.Error("nil size")
	}
	for k, ok := range map[string]bool{"12": true, "0": false, "abc": false, "1.5": false, "": false} {
		if _, got := learning.RouteID(k); got != ok {
			t.Errorf("RouteID(%q) = %v", k, got)
		}
	}
}

func TestPriceAndJSON(t *testing.T) {
	if intPrice("199000.00") != 199000 || intPrice("19.99") != 19 || intPrice("") != 0 {
		t.Error("intPrice")
	}
	if string(jsonArray("")) != "[]" || string(jsonArray(`["a"]`)) != `["a"]` {
		t.Error("jsonArray")
	}
}

// TestRoutesRegister makes sure every route (and the sibling modules'
// wildcard names) mount without gin conflicts.
func TestRoutesRegister(t *testing.T) {
	a := &app.App{}
	m := New(a, rbac.New(nil, nil, nil), auth.NewTokenService(nil, auth.TokenOptions{}))
	r := gin.New()
	api := r.Group("/api")
	m.Register(api)
	// Routes other modules add under the same prefixes.
	api.GET("/courses/:course/lessons/:lesson/attachments", func(*gin.Context) {})
	api.GET("/courses/:course/reviews", func(*gin.Context) {})
	if n := len(r.Routes()); n < 26 {
		t.Fatalf("only %d routes", n)
	}
	// Spoofed method without override -> 405.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/courses/1", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /courses/1 = %d", w.Code)
	}
}
