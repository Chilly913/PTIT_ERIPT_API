package study

import (
	"encoding/json"
	"testing"
	"time"
)

func TestApplyUpsert(t *testing.T) {
	now := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)
	p := func(v int64) *int64 { return &v }
	b := func(v bool) *bool { return &v }

	// New row: zero defaults, not completed.
	got := applyUpsert(nil, upsertInput{WatchedSeconds: p(30)}, 1, 2, now)
	if got.ProgressPercent != 0 || got.WatchedSeconds != 30 || got.CompletedAt != nil || got.LastWatchedAt == nil {
		t.Fatalf("new row: %+v", got)
	}
	// progress_percent >= 100 completes when `completed` is absent.
	got = applyUpsert(nil, upsertInput{ProgressPercent: p(100)}, 1, 2, now)
	if got.CompletedAt == nil || !got.CompletedAt.Equal(now) {
		t.Fatalf("auto complete: %+v", got)
	}
	// Existing values are kept, completed_at is not moved.
	prev := &progressRow{ProgressPercent: 100, LastPosition: 50, WatchedSeconds: 60, CompletedAt: &earlier}
	got = applyUpsert(prev, upsertInput{LastPosition: p(70)}, 1, 2, now)
	if got.ProgressPercent != 100 || got.LastPosition != 70 || got.WatchedSeconds != 60 || !got.CompletedAt.Equal(earlier) {
		t.Fatalf("keep: %+v", got)
	}
	// Explicit completed=false wins over progress_percent and clears completed_at.
	got = applyUpsert(prev, upsertInput{Completed: b(false)}, 1, 2, now)
	if got.CompletedAt != nil {
		t.Fatalf("uncomplete: %+v", got)
	}
	// Explicit completed=true with a low percent.
	got = applyUpsert(nil, upsertInput{ProgressPercent: p(10), Completed: b(true)}, 1, 2, now)
	if got.CompletedAt == nil {
		t.Fatalf("explicit complete: %+v", got)
	}
}

func TestAutoCompleted(t *testing.T) {
	cases := []struct {
		dur     int
		watched int64
		want    bool
	}{
		{0, 1000, false}, // no duration: never auto-completed
		{100, 89, false},
		{100, 90, true},
		{101, 90, false}, // 90.9 needed
		{101, 91, true},
	}
	for _, c := range cases {
		if got := autoCompleted(c.dur, c.watched); got != c.want {
			t.Errorf("autoCompleted(%d, %d) = %v", c.dur, c.watched, got)
		}
	}
}

func TestPercentAndPHPFloat(t *testing.T) {
	cases := []struct {
		done, total int64
		want        string
	}{
		{0, 3, "0.0"},
		{1, 3, "33.3"},
		{2, 3, "66.7"},
		{3, 3, "100.0"},
		{1, 8, "12.5"},
	}
	for _, c := range cases {
		js, _ := json.Marshal(phpFloat(roundPercent(c.done, c.total)))
		if string(js) != c.want {
			t.Errorf("percent(%d/%d) = %s, want %s", c.done, c.total, js, c.want)
		}
	}
	if js, _ := json.Marshal(enrollmentProgress(0, 0)); string(js) != "0" {
		t.Errorf("no lessons progress = %s", js)
	}
	if pathPercent(1, 3) != 33 || pathPercent(2, 3) != 67 || pathPercent(0, 0) != 0 || pathPercent(1, 2) != 50 {
		t.Error("pathPercent")
	}
}

func TestFormatFileSize(t *testing.T) {
	v := func(n int64) *int64 { return &n }
	cases := map[int64]string{
		0:                "0MB",
		1887437:          "1.8MB",
		2 * 1024 * 1024:  "2MB",
		1024 * 1024 / 3:  "0.33MB",
		50 * 1024 * 1024: "50MB",
	}
	for in, want := range cases {
		if got := formatFileSize(v(in)); got == nil || *got != want {
			t.Errorf("formatFileSize(%d) = %v, want %s", in, got, want)
		}
	}
	if formatFileSize(nil) != nil {
		t.Error("nil size")
	}
}

func TestParseExtensions(t *testing.T) {
	got := parseExtensions(" PDF, docx,,zip ")
	if len(got) != 3 || got[0] != "pdf" || got[1] != "docx" || got[2] != "zip" {
		t.Fatalf("got %v", got)
	}
	if pgTextArray(got) != `{"pdf","docx","zip"}` {
		t.Fatalf("array literal %s", pgTextArray(got))
	}
	if len(parseExtensions(" , ")) != 0 {
		t.Fatal("empty list")
	}
}

func TestLooseTypes(t *testing.T) {
	ints := map[any]bool{
		json.Number("5"): true, json.Number("5.0"): true, json.Number("5.5"): false,
		"12": true, "abc": false, true: false, "-3": true,
	}
	for in, ok := range ints {
		if _, got := toInt(in); got != ok {
			t.Errorf("toInt(%#v) ok=%v", in, got)
		}
	}
	bools := map[any]bool{true: true, false: true, json.Number("1"): true, json.Number("0"): true,
		"1": true, "0": true, "true": false, json.Number("2"): false}
	for in, ok := range bools {
		if _, got := toBool(in); got != ok {
			t.Errorf("toBool(%#v) ok=%v", in, got)
		}
	}
}

func TestRulesMessages(t *testing.T) {
	in := input{vals: map[string]any{
		"progress_percent": json.Number("150"),
		"last_position":    "x",
		"completed":        nil,
		"content":          json.Number("3"),
	}}
	r := newRules(in)
	r.intField("progress_percent", false, i64(0), i64(100))
	r.intField("last_position", false, i64(0), nil)
	r.boolField("completed", false) // sometimes|boolean: null fails
	r.stringField("content", true, false, 5000)
	r.intField("watched_seconds", true, i64(0), nil)
	r.afterOrEqual("due_at", "submission_open_at")
	errs := r.bag.Errors()
	want := map[string]string{
		"progress_percent": "The progress percent field must not be greater than 100.",
		"last_position":    "The last position field must be an integer.",
		"completed":        "The completed field must be true or false.",
		"content":          "The content field must be a string.",
		"watched_seconds":  "The watched seconds field is required.",
		"due_at":           "The due at field must be a date after or equal to submission open at.",
	}
	for f, msg := range want {
		if len(errs[f]) != 1 || errs[f][0] != msg {
			t.Errorf("%s: %v, want %q", f, errs[f], msg)
		}
	}
}

func TestTermHelpers(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	now := time.Date(2026, 10, 14, 23, 59, 0, 0, time.UTC)
	if windowStatus(d("2026-09-01"), d("2027-01-15"), now) != "current" ||
		windowStatus(d("2026-10-15"), d("2027-01-15"), now) != "upcoming" ||
		windowStatus(d("2026-01-01"), d("2026-10-13"), now) != "expired" ||
		windowStatus(d("2026-01-01"), d("2026-10-14"), now) != "current" {
		t.Error("windowStatus")
	}
	year := "2025-2026"
	cases := map[string]string{
		"Học kỳ 1 (2025-2026)": "Học kỳ 1 - Năm học 2025-2026",
		"học kỳ 2 (2025-2026)": "học kỳ 2 - Năm học 2025-2026",
		"Học kỳ hè":            "Học kỳ hè - Năm học 2025-2026",
		"HK1 2025-2026":        "HK1 2025-2026",
	}
	for in, want := range cases {
		if got := termDisplayName(in, &year); got != want {
			t.Errorf("termDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
	if termDisplayName(" X ", nil) != "X" {
		t.Error("no year")
	}
}

func TestDetectMime(t *testing.T) {
	if got := detectMime([]byte("%PDF-1.7\n"), "pdf"); got != "application/pdf" {
		t.Errorf("pdf: %s", got)
	}
	if got := detectMime([]byte("plain words"), ""); got != "text/plain" {
		t.Errorf("text: %s", got)
	}
}
