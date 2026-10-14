package interaction

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

func ptr[T any](v T) *T { return &v }

func TestComputeReactionState(t *testing.T) {
	rs := []reactionRow{
		{ID: 1, UserID: 7, Kind: "like"},
		{ID: 2, UserID: 8, Kind: "dislike"},
		{ID: 3, UserID: 9, Kind: "like"},
	}
	st := computeReactionState(rs, 8)
	if st.LikeCount != 2 || st.DislikeCount != 1 || st.MyReaction == nil || *st.MyReaction != "dislike" {
		t.Fatalf("state %+v", st)
	}
	if st := computeReactionState(rs, 0); st.MyReaction != nil {
		t.Fatalf("guest my_reaction %v", *st.MyReaction)
	}
	if st := computeReactionState(nil, 7); st.LikeCount != 0 || st.DislikeCount != 0 || st.MyReaction != nil {
		t.Fatalf("empty state %+v", st)
	}
	b, _ := json.Marshal(computeReactionState(rs, 42))
	if string(b) != `{"like_count":2,"dislike_count":1,"my_reaction":null}` {
		t.Fatalf("json %s", b)
	}
}

func TestReactionJSONRebuildsMorph(t *testing.T) {
	r := reactionRow{ID: 1, UserID: 2, QaID: ptr(int64(5)), Kind: "like"}.toJSON()
	if r.ReactableType != `App\Models\CourseQa` || r.ReactableID != 5 {
		t.Fatalf("qa morph %+v", r)
	}
	r = reactionRow{ID: 1, UserID: 2, ReplyID: ptr(int64(6)), Kind: "like"}.toJSON()
	if r.ReactableType != `App\Models\CourseQaReply` || r.ReactableID != 6 {
		t.Fatalf("reply morph %+v", r)
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"reactable_type":"App\\Models\\CourseQaReply"`) {
		t.Fatalf("json %s", b)
	}
}

func TestScormCompleted(t *testing.T) {
	cases := []struct {
		version                     string
		status, completion, success *string
		want                        bool
	}{
		{"1.2", ptr("completed"), nil, nil, true},
		{"1.2", ptr("passed"), nil, nil, true},
		{"1.2", ptr("failed"), ptr("completed"), nil, false},
		{"1.2", nil, nil, ptr("passed"), false},
		{"2004", nil, ptr("completed"), ptr("failed"), true},
		{"2004", nil, ptr("incomplete"), ptr("passed"), true},
		{"2004", ptr("completed"), ptr("incomplete"), nil, false},
	}
	for i, c := range cases {
		if got := scormCompleted(c.version, c.status, c.completion, c.success); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}

func jsonInput(t *testing.T, body string) *input {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/x", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	in, err := readInput(c)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func validationJSON(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	ve, ok := err.(*httpx.ValidationError)
	if !ok {
		t.Fatalf("not a validation error: %v", err)
	}
	b, _ := json.Marshal(ve)
	return string(b)
}

func TestValidateReviewRules(t *testing.T) {
	specs := []fieldRules{rules("rating", "required|integer|min:1|max:5"), rules("comment", "nullable|string|max:2000")}
	cases := map[string]string{
		`{"rating":5,"comment":"  ok  "}`: ``,
		`{"rating":"4"}`:                  ``,
		`{"rating":5,"comment":null}`:     ``,
		`{"rating":5,"comment":""}`:       ``,
		`{}`:                              `{"message":"The rating field is required.","errors":{"rating":["The rating field is required."]}}`,
		`{"rating":"  "}`:                 `{"message":"The rating field is required.","errors":{"rating":["The rating field is required."]}}`,
		`{"rating":9}`:                    `{"message":"The rating field must not be greater than 5.","errors":{"rating":["The rating field must not be greater than 5."]}}`,
		`{"rating":0}`:                    `{"message":"The rating field must be at least 1.","errors":{"rating":["The rating field must be at least 1."]}}`,
		`{"rating":2.5,"comment":12}`:     `{"message":"The rating field must be an integer. (and 1 more error)","errors":{"rating":["The rating field must be an integer."],"comment":["The comment field must be a string."]}}`,
		`{"rating":3,"comment":"` + strings.Repeat("é", 2001) + `"}`: `{"message":"The comment field must not be greater than 2000 characters.","errors":{"comment":["The comment field must not be greater than 2000 characters."]}}`,
	}
	for body, want := range cases {
		if got := validationJSON(t, validate(jsonInput(t, body), nil, specs...)); got != want {
			t.Errorf("%.40s:\n got %s\nwant %s", body, got, want)
		}
	}
	in := jsonInput(t, `{"rating":5,"comment":"  ok  "}`)
	if c := in.str("comment"); c == nil || *c != "ok" {
		t.Fatalf("trimmed comment %v", c)
	}
	if in := jsonInput(t, `{"comment":""}`); !in.has("comment") || in.str("comment") != nil {
		t.Fatal("empty string must become a present null")
	}
}

func TestValidateSometimesAndNullable(t *testing.T) {
	specs := []fieldRules{rules("rating", "sometimes|integer|min:1|max:5")}
	if err := validate(jsonInput(t, `{}`), nil, specs...); err != nil {
		t.Fatalf("absent sometimes field: %v", err)
	}
	// Present null without "nullable" still fails the type rule (Laravel).
	got := validationJSON(t, validate(jsonInput(t, `{"rating":null}`), nil, specs...))
	if !strings.Contains(got, "The rating field must be an integer.") {
		t.Fatalf("null rating: %s", got)
	}
}

func TestValidateVirtualClassRules(t *testing.T) {
	specs := []fieldRules{
		rules("provider", "required|string|in:zoom,google_meet,jitsi,other"),
		rules("join_url", "required|url"),
		rules("start_url", "nullable|url"),
		rules("start_at", "required|date"),
		rules("duration", "required|integer|min:1"),
	}
	ok := `{"provider":"zoom","join_url":"https://zoom.us/j/1","start_url":null,"start_at":"2026-10-20T08:30","duration":"60"}`
	if err := validate(jsonInput(t, ok), nil, specs...); err != nil {
		t.Fatalf("valid: %v", err)
	}
	bad := `{"provider":"teams","join_url":"zoom.us/j/1","start_url":"nope","start_at":"tomorrow-ish","duration":0}`
	got := validationJSON(t, validate(jsonInput(t, bad), nil, specs...))
	want := `{"message":"The selected provider is invalid. (and 4 more errors)","errors":{"provider":["The selected provider is invalid."],"join_url":["The join url field must be a valid URL."],"start_url":["The start url field must be a valid URL."],"start_at":["The start at field must be a valid date."],"duration":["The duration field must be at least 1."]}}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestValidateTrackRules(t *testing.T) {
	specs := []fieldRules{
		rules("version", "nullable|string|in:1.2,2004"),
		rules("score", "nullable|numeric|min:0|max:100"),
	}
	if err := validate(jsonInput(t, `{"version":"2004","score":"99.5"}`), nil, specs...); err != nil {
		t.Fatal(err)
	}
	got := validationJSON(t, validate(jsonInput(t, `{"version":1.2,"score":"abc"}`), nil, specs...))
	want := `{"message":"The version field must be a string. (and 1 more error)","errors":{"version":["The version field must be a string."],"score":["The score field must be a number."]}}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	got = validationJSON(t, validate(jsonInput(t, `{"score":101}`), nil, specs...))
	if !strings.Contains(got, "The score field must not be greater than 100.") {
		t.Fatalf("score max: %s", got)
	}
}

func TestValidateExists(t *testing.T) {
	calls := 0
	exists := func(table, column string, v any) (bool, error) {
		calls++
		return table == "lessons" && column == "id" && scalarString(v) == "3", nil
	}
	spec := rules("lesson_id", "nullable|exists:lessons,id")
	if err := validate(jsonInput(t, `{"lesson_id":3}`), exists, spec); err != nil {
		t.Fatal(err)
	}
	if err := validate(jsonInput(t, `{"lesson_id":null}`), exists, spec); err != nil || calls != 1 {
		t.Fatalf("null lesson_id: %v (calls %d)", err, calls)
	}
	got := validationJSON(t, validate(jsonInput(t, `{"lesson_id":4}`), exists, spec))
	if got != `{"message":"The selected lesson id is invalid.","errors":{"lesson_id":["The selected lesson id is invalid."]}}` {
		t.Fatalf("got %s", got)
	}
}

func TestIsIntegerAndNumeric(t *testing.T) {
	for _, v := range []any{json.Number("5"), json.Number("5.0"), "12", "+3", "-0", true} {
		if !isInteger(v) {
			t.Errorf("isInteger(%#v) = false", v)
		}
	}
	for _, v := range []any{json.Number("5.5"), "05", "1e3", "abc", "", false, nil} {
		if isInteger(v) {
			t.Errorf("isInteger(%#v) = true", v)
		}
	}
	for _, v := range []any{"1e3", " 2.5", ".5", "-1", json.Number("3")} {
		if _, ok := toNumber(v); !ok {
			t.Errorf("toNumber(%#v) failed", v)
		}
	}
	for _, v := range []any{"0x1A", "Inf", "NaN", "1,5", true} {
		if _, ok := toNumber(v); ok {
			t.Errorf("toNumber(%#v) accepted", v)
		}
	}
}

func TestParseDateUTC(t *testing.T) {
	cases := map[string]string{
		"2026-10-20T08:30":          "2026-10-20T08:30:00Z",
		"2026-10-20 08:30:15":       "2026-10-20T08:30:15Z",
		"2026-10-20T15:30:00+07:00": "2026-10-20T08:30:00Z",
		"2026-10-20T08:30:00.000Z":  "2026-10-20T08:30:00Z",
		"2026-10-20":                "2026-10-20T00:00:00Z",
	}
	for in, want := range cases {
		got, err := parseDate(in)
		if err != nil || got.Format("2006-01-02T15:04:05Z07:00") != want {
			t.Errorf("parseDate(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
}
