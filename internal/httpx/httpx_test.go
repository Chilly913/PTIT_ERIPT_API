package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func serve(h gin.HandlerFunc) *httptest.ResponseRecorder {
	r := gin.New()
	r.Use(Recovery())
	r.GET("/x", h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	return w
}

func TestErrorResponses(t *testing.T) {
	cases := []struct {
		name   string
		h      gin.HandlerFunc
		status int
		body   string
	}{
		{"401", Unauthenticated, 401, `{"message":"Unauthenticated."}`},
		{"403", func(c *gin.Context) { Forbidden(c, "") }, 403, `{"message":"This action is unauthorized."}`},
		{"404 gorm", func(c *gin.Context) { Fail(c, fmt.Errorf("find: %w", gorm.ErrRecordNotFound)) }, 404, `{"message":"Not Found"}`},
		{"custom", func(c *gin.Context) { Fail(c, NewError(409, "Already enrolled.")) }, 409, `{"message":"Already enrolled."}`},
		{"429", func(c *gin.Context) { TooManyRequests(c, 60) }, 429, `{"message":"Too Many Attempts."}`},
		{"500", func(c *gin.Context) { Fail(c, errors.New("pq: secret detail")) }, 500, `{"message":"Server Error"}`},
		{"panic", func(c *gin.Context) { panic("boom") }, 500, `{"message":"Server Error"}`},
		{"422", func(c *gin.Context) { Fail(c, NewValidationError().Add("code", "Invalid code.")) }, 422,
			`{"message":"Invalid code.","errors":{"code":["Invalid code."]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := serve(tc.h)
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), tc.status, tc.body)
			}
		})
	}
}

func TestDebugExposesMessage(t *testing.T) {
	SetDebug(true)
	defer SetDebug(false)
	w := serve(func(c *gin.Context) { Fail(c, errors.New("detail")) })
	var b map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	if b["message"] != "detail" || b["exception"] == "" {
		t.Fatalf("got %s", w.Body.String())
	}
}

func TestParsePage(t *testing.T) {
	cases := map[string]PageParams{
		"":                         {1, 15},
		"?page=3&per_page=20":      {3, 20},
		"?page=-1&per_page=0":      {1, 15},
		"?page=abc&per_page=10000": {1, 100},
	}
	for q, want := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/courses"+q, nil)
		if got := ParsePage(c, 15, 100); got != want {
			t.Errorf("%q: got %+v want %+v", q, got, want)
		}
	}
}

func TestPaginatorShape(t *testing.T) {
	p := NewPage("http://localhost/api/courses", []int{4, 5, 6}, 7, PageParams{Page: 2, PerPage: 3})
	got, _ := json.Marshal(p)
	want := `{"current_page":2,"data":[4,5,6],"first_page_url":"http://localhost/api/courses?page=1","from":4,"last_page":3,` +
		`"last_page_url":"http://localhost/api/courses?page=3","links":[` +
		`{"url":"http://localhost/api/courses?page=1","label":"&laquo; Previous","page":1,"active":false},` +
		`{"url":"http://localhost/api/courses?page=1","label":"1","page":1,"active":false},` +
		`{"url":"http://localhost/api/courses?page=2","label":"2","page":2,"active":true},` +
		`{"url":"http://localhost/api/courses?page=3","label":"3","page":3,"active":false},` +
		`{"url":"http://localhost/api/courses?page=3","label":"Next &raquo;","page":3,"active":false}],` +
		`"next_page_url":"http://localhost/api/courses?page=3","path":"http://localhost/api/courses","per_page":3,` +
		`"prev_page_url":"http://localhost/api/courses?page=1","to":6,"total":7}`
	if unescapeAmp(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestPaginatorEmpty(t *testing.T) {
	p := NewPage[string]("http://h/api/x", nil, 0, PageParams{Page: 1, PerPage: 10})
	got, _ := json.Marshal(p)
	want := `{"current_page":1,"data":[],"first_page_url":"http://h/api/x?page=1","from":null,"last_page":1,` +
		`"last_page_url":"http://h/api/x?page=1","links":[{"url":null,"label":"&laquo; Previous","page":null,"active":false},` +
		`{"url":"http://h/api/x?page=1","label":"1","page":1,"active":true},` +
		`{"url":null,"label":"Next &raquo;","page":null,"active":false}],"next_page_url":null,"path":"http://h/api/x",` +
		`"per_page":10,"prev_page_url":null,"to":null,"total":0}`
	if unescapeAmp(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestPaginatorSliderWindows(t *testing.T) {
	labels := func(page, last int) []string {
		p := NewPage[int]("u", []int{1}, int64(last), PageParams{Page: page, PerPage: 1})
		var out []string
		for _, l := range p.Links[1 : len(p.Links)-1] {
			out = append(out, l.Label)
		}
		return out
	}
	check := func(page, last int, want string) {
		t.Helper()
		got := fmt.Sprint(labels(page, last))
		if got != want {
			t.Errorf("page %d/%d: got %s want %s", page, last, got, want)
		}
	}
	check(1, 20, "[1 2 3 4 5 6 7 8 9 10 ... 19 20]")
	check(10, 20, "[1 2 ... 7 8 9 10 11 12 13 ... 19 20]")
	check(13, 20, "[1 2 ... 10 11 12 13 14 15 16 ... 19 20]")
	check(18, 20, "[1 2 ... 11 12 13 14 15 16 17 18 19 20]")
	check(5, 13, "[1 2 3 4 5 6 7 8 9 10 11 12 13]")
	check(2, 5, "[1 2 3 4 5]")
}

func TestTimeJSON(t *testing.T) {
	loc := time.FixedZone("ICT", 7*3600)
	tm := NewTime(time.Date(2025, 1, 15, 15, 30, 0, 123456789, loc))
	b, _ := json.Marshal(struct {
		A Time  `json:"a"`
		B Time  `json:"b"`
		C *Time `json:"c"`
		D Date  `json:"d"`
	}{A: tm, D: NewDate(tm.Time)})
	want := `{"a":"2025-01-15T08:30:00.123456Z","b":null,"c":null,"d":"2025-01-15"}`
	if string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	var back struct {
		A Time `json:"a"`
		D Date `json:"d"`
	}
	if err := json.Unmarshal([]byte(`{"a":"2025-01-15 08:30:00","d":"2025-01-15T00:00:00.000000Z"}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.A.String() != "2025-01-15T08:30:00.000000Z" || back.D.String() != "2025-01-15" {
		t.Fatalf("got %s %s", back.A, back.D)
	}
}

// unescapeAmp undoes encoding/json's HTML escaping of "&" (Laravel emits a raw
// "&"; both decode to the same string).
func unescapeAmp(b []byte) string {
	return strings.ReplaceAll(string(b), "\\"+"u0026", "&")
}
