package learning

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Behaviour of the question formats: validation by the instructor, what the learner sees, and grading.

func ip(n int) *int       { return &n }
func sp(s string) *string { return &s }

func opts(contents ...string) []qa {
	out := make([]qa, len(contents))
	for i, c := range contents {
		out[i] = qa{ID: int64(10 + i), Content: c, Position: i}
	}
	return out
}

func withCorrect(a []qa, idx ...int) []qa {
	for _, i := range idx {
		a[i].IsCorrect = true
	}
	return a
}

func fullMarks(r gradeResult) bool { return r.Fraction >= 1-1e-9 }

func TestGradeChoice(t *testing.T) {
	single := quizQuestion{ID: 1, Type: "single_choice", Answers: withCorrect(opts("a", "b", "c"), 1)}
	if r := gradeQuestion(single, float64(11)); !fullMarks(r) || r.Manual {
		t.Errorf("right single answer: %+v", r)
	}
	if r := gradeQuestion(single, float64(10)); r.Fraction != 0 {
		t.Errorf("wrong single answer: %+v", r)
	}
	if r := gradeQuestion(single, []any{float64(11)}); !fullMarks(r) {
		t.Errorf("single answer sent as array: %+v", r)
	}
	if r := gradeQuestion(single, nil); r.Fraction != 0 {
		t.Errorf("no answer: %+v", r)
	}

	multi := quizQuestion{ID: 2, Type: "multiple_choice", Answers: withCorrect(opts("a", "b", "c", "d"), 0, 2)}
	if r := gradeQuestion(multi, []any{float64(12), float64(10)}); !fullMarks(r) {
		t.Errorf("right set in any order: %+v", r)
	}
	if r := gradeQuestion(multi, []any{float64(10)}); r.Fraction != 0 {
		t.Errorf("incomplete set must score zero: %+v", r)
	}
	if r := gradeQuestion(multi, []any{float64(10), float64(11), float64(12)}); r.Fraction != 0 {
		t.Errorf("extra option must score zero: %+v", r)
	}

	tf := quizQuestion{ID: 3, Type: "true_false", Answers: withCorrect(opts("Đúng", "Sai"), 0)}
	if r := gradeQuestion(tf, float64(10)); !fullMarks(r) {
		t.Errorf("true/false: %+v", r)
	}
}

func TestGradeOrdering(t *testing.T) {
	a := opts("một", "hai", "ba")
	a[0].CorrectPos, a[1].CorrectPos, a[2].CorrectPos = ip(3), ip(1), ip(2)
	q := quizQuestion{ID: 4, Type: "ordering", Answers: a}
	if r := gradeQuestion(q, []any{float64(11), float64(12), float64(10)}); !fullMarks(r) {
		t.Errorf("right order: %+v", r)
	}
	if r := gradeQuestion(q, []any{float64(10), float64(11), float64(12)}); r.Fraction != 0 {
		t.Errorf("wrong order: %+v", r)
	}
	if got, _ := gradeQuestion(q, nil).Reveal.([]int64); len(got) != 3 || got[0] != 11 {
		t.Errorf("reveal must list ids in the correct order, got %v", got)
	}
}

func TestGradeMatching(t *testing.T) {
	a := opts("Hà Nội", "Huế")
	a[0].SubContent, a[1].SubContent = sp("Bắc"), sp("Trung")
	q := quizQuestion{ID: 5, Type: "matching", Answers: a}
	if r := gradeQuestion(q, map[string]any{"10": "Bắc", "11": "Trung"}); !fullMarks(r) {
		t.Errorf("right pairs: %+v", r)
	}
	if r := gradeQuestion(q, map[string]any{"10": "Trung", "11": "Bắc"}); r.Fraction != 0 {
		t.Errorf("swapped pairs: %+v", r)
	}
	if r := gradeQuestion(q, map[string]any{"10": "Bắc"}); r.Fraction != 0 {
		t.Errorf("missing pair: %+v", r)
	}
}

func TestGradeTextAndNumber(t *testing.T) {
	short := quizQuestion{ID: 6, Type: "short_answer", Answers: withCorrect(opts("Hà Nội", "Ha Noi"), 0, 1)}
	if r := gradeQuestion(short, "  hà nội "); !fullMarks(r) {
		t.Errorf("case/space insensitive: %+v", r)
	}
	if r := gradeQuestion(short, "Huế"); r.Fraction != 0 {
		t.Errorf("wrong text: %+v", r)
	}

	num := quizQuestion{ID: 7, Type: "numerical", Answers: withCorrect(opts("3.14"), 0), Metadata: []byte(`{"tolerance": 0.01}`)}
	for _, ans := range []any{"3,14", "3.15", float64(3.13)} {
		if r := gradeQuestion(num, ans); !fullMarks(r) {
			t.Errorf("%v should be inside tolerance: %+v", ans, r)
		}
	}
	for _, ans := range []any{"3.2", "abc", nil} {
		if r := gradeQuestion(num, ans); r.Fraction != 0 {
			t.Errorf("%v should be wrong: %+v", ans, r)
		}
	}

	essay := quizQuestion{ID: 8, Type: "essay"}
	if r := gradeQuestion(essay, "bài luận"); !r.Manual || r.Reveal != nil {
		t.Errorf("essay needs a human: %+v", r)
	}
}

func TestPresentHidesAnswers(t *testing.T) {
	a := opts("một", "hai", "ba")
	a[0].CorrectPos, a[1].CorrectPos, a[2].CorrectPos = ip(1), ip(2), ip(3)
	q := quizQuestion{ID: 9, Type: "ordering", Content: "Sắp xếp", Points: 2, Answers: a}
	out := presentQuestion(q, 77)
	list, _ := out["options"].([]gin.H)
	ids := []int{}
	for _, o := range list {
		if _, leaked := o["is_correct"]; leaked {
			t.Error("is_correct leaked to the learner")
		}
		if _, leaked := o["correct_position"]; leaked {
			t.Error("correct_position leaked to the learner")
		}
		ids = append(ids, int(o["id"].(int64)))
	}
	sort.Ints(ids)
	if len(ids) != 3 || ids[0] != 10 || ids[2] != 12 {
		t.Errorf("ordering options must contain every item once, got %v", ids)
	}
	again, _ := presentQuestion(q, 77)["options"].([]gin.H)
	for i, o := range list {
		if again[i]["id"] != o["id"] {
			t.Error("shuffle must be stable for the same seed")
		}
	}

	m := opts("x", "y")
	m[0].SubContent, m[1].SubContent = sp("T1"), sp("T2")
	mo := presentQuestion(quizQuestion{ID: 10, Type: "matching", Answers: m}, 1)
	if targets, _ := mo["targets"].([]string); len(targets) != 2 {
		t.Errorf("matching needs its target list, got %#v", mo["targets"])
	}
}

func TestCheckQuestionBody(t *testing.T) {
	ok := func(q questionBody) bool { return q.check() == "" }
	two := []answerBody{{Content: "a", IsCorrect: true}, {Content: "b"}}
	cases := []struct {
		name string
		q    questionBody
		want bool
	}{
		{"single ok", questionBody{Type: "single_choice", Content: "?", Answers: two}, true},
		{"single without correct", questionBody{Type: "single_choice", Content: "?", Answers: []answerBody{{Content: "a"}, {Content: "b"}}}, false},
		{"single with two correct", questionBody{Type: "single_choice", Content: "?", Answers: []answerBody{{Content: "a", IsCorrect: true}, {Content: "b", IsCorrect: true}}}, false},
		{"multiple needs one correct", questionBody{Type: "multiple_choice", Content: "?", Answers: []answerBody{{Content: "a"}, {Content: "b"}}}, false},
		{"essay needs no answers", questionBody{Type: "essay", Content: "?"}, true},
		{"empty content", questionBody{Type: "essay", Content: "  "}, false},
		{"unknown type", questionBody{Type: "nope", Content: "?"}, false},
		{"bad difficulty", questionBody{Type: "essay", Content: "?", Difficulty: 9}, false},
		{"numerical not a number", questionBody{Type: "numerical", Content: "?", Answers: []answerBody{{Content: "abc", IsCorrect: true}}}, false},
		{"numerical ok", questionBody{Type: "numerical", Content: "?", Answers: []answerBody{{Content: "1.5", IsCorrect: true}}}, true},
		{"matching without right side", questionBody{Type: "matching", Content: "?", Answers: []answerBody{{Content: "a"}, {Content: "b"}}}, false},
		{"short answer needs accepted text", questionBody{Type: "short_answer", Content: "?", Answers: []answerBody{{Content: "a"}}}, false},
		{"ordering needs two", questionBody{Type: "ordering", Content: "?", Answers: []answerBody{{Content: "a"}}}, false},
	}
	for _, c := range cases {
		if got := ok(c.q); got != c.want {
			t.Errorf("%s: valid=%v, want %v (%s)", c.name, got, c.want, c.q.check())
		}
	}
}

func blankRow(id, text string, correct bool) answerBody {
	return answerBody{Content: text, SubContent: sp(id), IsCorrect: correct}
}

func TestCheckCloze(t *testing.T) {
	good := questionBody{Type: "cloze", Content: "Thủ đô là {{1}}, sông {{2}}.", Answers: []answerBody{blankRow("1", "Hà Nội", true), blankRow("2", "Hồng", true)}}
	if msg := good.check(); msg != "" {
		t.Fatalf("valid cloze rejected: %s", msg)
	}
	cases := map[string]questionBody{
		"no blank in text":     {Type: "cloze", Content: "Không có chỗ trống", Answers: good.Answers},
		"blank without answer": {Type: "cloze", Content: "{{1}} và {{2}}", Answers: []answerBody{blankRow("1", "a", true)}},
		"only wrong options":   {Type: "cloze", Content: "{{1}}", Answers: []answerBody{blankRow("1", "a", false)}},
		"answer for unknown":   {Type: "cloze", Content: "{{1}}", Answers: []answerBody{blankRow("1", "a", true), blankRow("3", "b", true)}},
		"answer without blank": {Type: "cloze", Content: "{{1}}", Answers: []answerBody{{Content: "a", IsCorrect: true}}},
		"bad input mode":       {Type: "cloze", Content: "{{1}}", Answers: []answerBody{blankRow("1", "a", true)}, Metadata: map[string]any{"input": "x"}},
	}
	for name, q := range cases {
		if q.check() == "" {
			t.Errorf("%s: should be rejected", name)
		}
	}
	if got := blankIDs("{{1}} {{ 2 }} {{1}} {{01}}"); len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Errorf("blankIDs = %v", got)
	}
}

func clozeQuestion(meta string) quizQuestion {
	a := []qa{
		{ID: 1, Content: "Hà Nội", SubContent: sp("1"), IsCorrect: true},
		{ID: 2, Content: "Ha Noi", SubContent: sp("1"), IsCorrect: true},
		{ID: 3, Content: "Hồng", SubContent: sp("2"), IsCorrect: true},
		{ID: 4, Content: "Mê Kông", SubContent: sp("2")}, // distractor: blank 2 becomes a dropdown
	}
	return quizQuestion{ID: 20, Type: "cloze", Content: "Thủ đô {{1}} bên sông {{2}}.", Answers: a, Metadata: []byte(meta)}
}

func TestGradeCloze(t *testing.T) {
	q := clozeQuestion(``)
	if r := gradeQuestion(q, map[string]any{"1": " hà  nội ", "2": "hồng"}); !fullMarks(r) {
		t.Errorf("all blanks right (case and spaces ignored): %+v", r)
	}
	if r := gradeQuestion(q, map[string]any{"1": "Ha Noi"}); r.Fraction != 0.5 {
		t.Errorf("one of two blanks should earn half: %+v", r)
	}
	if r := gradeQuestion(q, map[string]any{"1": "Huế", "2": "Mê Kông"}); r.Fraction != 0 {
		t.Errorf("wrong blanks: %+v", r)
	}
	if r := gradeQuestion(q, nil); r.Fraction != 0 || r.Manual {
		t.Errorf("empty answer: %+v", r)
	}
	if r := gradeQuestion(clozeQuestion(`{"partial_credit": false}`), map[string]any{"1": "Hà Nội"}); r.Fraction != 0 {
		t.Errorf("all-or-nothing: %+v", r)
	}
	if r := gradeQuestion(clozeQuestion(`{"case_sensitive": true}`), map[string]any{"1": "hà nội", "2": "Hồng"}); r.Fraction != 0.5 {
		t.Errorf("case sensitive: %+v", r)
	}
	rev, _ := gradeQuestion(q, nil).Reveal.(map[string][]string)
	if len(rev["1"]) != 2 || len(rev["2"]) != 1 {
		t.Errorf("reveal lists every accepted text: %v", rev)
	}
}

func TestPresentCloze(t *testing.T) {
	out := presentQuestion(clozeQuestion(`{"input": "drag"}`), 5)
	blanks, _ := out["blanks"].([]gin.H)
	if len(blanks) != 2 || out["input"] != "drag" {
		t.Fatalf("blanks/input: %#v", out)
	}
	if _, has := blanks[0]["options"]; has {
		t.Error("blank 1 has no wrong option, so it is a text box")
	}
	if o, _ := blanks[1]["options"].([]string); len(o) != 2 {
		t.Errorf("blank 2 lists its options, got %#v", blanks[1]["options"])
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "is_correct") {
		t.Error("the learner payload must not carry correctness")
	}
}

func classQuestion() quizQuestion {
	a := opts("Cá voi", "Đại bàng", "Cá mập", "Chim cánh cụt")
	for i, c := range []string{"Dưới nước", "Biết bay", "Dưới nước", "Biết bay"} {
		a[i].SubContent = sp(c)
	}
	return quizQuestion{ID: 30, Type: "classification", Answers: a}
}

func TestClassification(t *testing.T) {
	good := questionBody{Type: "classification", Content: "Phân loại", Answers: []answerBody{
		{Content: "Cá voi", SubContent: sp("Nước")}, {Content: "Đại bàng", SubContent: sp("Trời")}}}
	if msg := good.check(); msg != "" {
		t.Fatalf("valid classification rejected: %s", msg)
	}
	oneCategory := questionBody{Type: "classification", Content: "?", Answers: []answerBody{
		{Content: "a", SubContent: sp("X")}, {Content: "b", SubContent: sp("X")}}}
	noCategory := questionBody{Type: "classification", Content: "?", Answers: []answerBody{{Content: "a", SubContent: sp("X")}, {Content: "b"}}}
	if oneCategory.check() == "" || noCategory.check() == "" {
		t.Error("a single category, or an item without category, must be rejected")
	}

	q := classQuestion()
	if r := gradeQuestion(q, map[string]any{"10": "Dưới nước", "11": "Biết bay", "12": "Dưới nước", "13": "Biết bay"}); !fullMarks(r) {
		t.Errorf("all right: %+v", r)
	}
	if r := gradeQuestion(q, map[string]any{"10": "Dưới nước", "11": "Dưới nước"}); r.Fraction != 0.25 {
		t.Errorf("one of four items right earns a quarter: %+v", r)
	}
	q.Metadata = []byte(`{"partial_credit": false}`)
	if r := gradeQuestion(q, map[string]any{"10": "Dưới nước"}); r.Fraction != 0 {
		t.Errorf("all-or-nothing: %+v", r)
	}
	out := presentQuestion(classQuestion(), 3)
	if cats, _ := out["categories"].([]string); len(cats) != 2 || cats[0] != "Dưới nước" {
		t.Errorf("categories: %#v", out["categories"])
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "sub_content") || strings.Contains(string(raw), "Biết bay\"}") {
		t.Errorf("the item list must not reveal each item's category: %s", raw)
	}
}

func TestFilesCoverNewFormats(t *testing.T) {
	// Excel: one cell per blank, "~" marks a wrong option.
	q, msg := buildQuestion("Điền khuyết", "Thủ đô {{1}}, sông {{2}}.", 1, nil,
		[]string{"1: Hà Nội | Ha Noi", "2: Hồng | ~Mê Kông"}, "", "")
	if msg != "" || q.Type != "cloze" || len(q.Answers) != 4 {
		t.Fatalf("cloze row: %q %+v", msg, q)
	}
	if !q.Answers[1].IsCorrect || q.Answers[3].IsCorrect || *q.Answers[3].SubContent != "2" {
		t.Errorf("cloze answers: %+v", q.Answers)
	}
	if q.check() != "" {
		t.Errorf("imported cloze must pass validation: %s", q.check())
	}
	cells := clozeCells(q.Answers)
	if len(cells) != 2 || cells[0] != "1: Hà Nội | Ha Noi" || cells[1] != "2: Hồng | ~Mê Kông" {
		t.Errorf("round trip: %v", cells)
	}
	if _, msg := buildQuestion("Điền khuyết", "{{1}}", 1, nil, []string{"không có số ô"}, "", ""); msg == "" {
		t.Error("a cloze option without a blank number must be rejected")
	}

	c, msg := buildQuestion("Phân loại", "Xếp nhóm", 1, nil, []string{"Cá | Nước", "Chim | Trời"}, "", "")
	if msg != "" || c.Type != "classification" || len(c.Answers) != 2 || c.check() != "" {
		t.Fatalf("classification row: %q %+v %s", msg, c, c.check())
	}
	if _, msg := buildQuestion("Đoạn văn", "Bài đọc", 1, nil, nil, "", ""); msg == "" {
		t.Error("passages cannot be imported from a flat file")
	}
}

func TestCheckPassage(t *testing.T) {
	child := questionBody{Type: "single_choice", Content: "Ý chính?", Answers: []answerBody{{Content: "a", IsCorrect: true}, {Content: "b"}}}
	ok := questionBody{Type: "passage", Content: "Đọc đoạn sau...", Children: []questionBody{child, child}}
	if msg := ok.check(); msg != "" {
		t.Fatalf("valid passage rejected: %s", msg)
	}
	bad := map[string]questionBody{
		"answers on passage": {Type: "passage", Content: "x", Answers: []answerBody{{Content: "a"}}},
		"nested passage":     {Type: "passage", Content: "x", Children: []questionBody{{Type: "passage", Content: "y"}}},
		"invalid child":      {Type: "passage", Content: "x", Children: []questionBody{{Type: "single_choice", Content: "y"}}},
		"passage as child":   {Type: "passage", Content: "x", ParentQuestionID: new(int64)},
	}
	for name, q := range bad {
		if q.check() == "" {
			t.Errorf("%s should be rejected", name)
		}
	}
	// A passage is a heading in the paper: never graded, never pending.
	if r := gradeQuestion(quizQuestion{Type: "passage"}, nil); r.Manual || r.Fraction != 0 {
		t.Errorf("passage grade: %+v", r)
	}
}

func TestScoreAttemptManualGrades(t *testing.T) {
	single := quizQuestion{ID: 1, Type: "single_choice", Points: 2, Answers: withCorrect(opts("a", "b"), 0)}
	essay := quizQuestion{ID: 2, Type: "essay", Points: 4}
	upload := quizQuestion{ID: 3, Type: "file_upload", Points: 4}
	qs := []quizQuestion{single, essay, upload}
	answers := map[string]any{"1": float64(10), "2": "bài luận", "3": map[string]any{"files": []any{}}}

	// Nothing graded by hand yet: the attempt is pending and the manual points are not counted.
	r := scoreAttempt(qs, answers, 1, nil)
	if !r.Pending || r.Earned != 2 || r.Max != 10 || r.Score != 20 {
		t.Fatalf("pending attempt: %+v", r)
	}
	if r.Review[1]["needs_manual"] != true || r.Review[2]["needs_manual"] != true || r.Review[1]["manual"] != true {
		t.Errorf("manual questions must be flagged: %v %v", r.Review[1], r.Review[2])
	}

	// Only the essay is graded: still pending because the upload has no grade.
	r = scoreAttempt(qs, answers, 1, map[string]manualGrade{"2": {Score: 3, Comment: "tốt"}})
	if !r.Pending || r.Earned != 5 {
		t.Fatalf("one manual grade: %+v", r)
	}
	if r.Review[1]["needs_manual"] != false || r.Review[1]["grader_comment"] != "tốt" || r.Review[1]["partial"] != true {
		t.Errorf("graded essay review: %v", r.Review[1])
	}

	// Everything graded: final score. A grade above the question's points is clamped, below zero is zero.
	r = scoreAttempt(qs, answers, 1, map[string]manualGrade{"2": {Score: 4}, "3": {Score: 99}})
	if r.Pending || r.Earned != 10 || r.Score != 100 {
		t.Fatalf("all graded: %+v", r)
	}
	r = scoreAttempt(qs, answers, 1, map[string]manualGrade{"2": {Score: -5}, "3": {Score: 1.234}})
	if r.Earned != 3.23 {
		t.Errorf("negative grade clamps to 0, grade rounds to 2 decimals: earned %v", r.Earned)
	}
	// A grade given to an auto-graded question is ignored.
	r = scoreAttempt([]quizQuestion{single}, map[string]any{"1": float64(11)}, 1, map[string]manualGrade{"1": {Score: 2}})
	if r.Earned != 0 || r.Pending {
		t.Errorf("auto-graded questions ignore manual grades: %+v", r)
	}
}

func TestCheckFileUpload(t *testing.T) {
	ok := questionBody{Type: "file_upload", Content: "Nộp báo cáo", Metadata: map[string]any{"max_files": float64(2), "max_size_mb": float64(5), "allowed_ext": []any{"pdf", ".DOCX"}}}
	if msg := ok.check(); msg != "" {
		t.Fatalf("valid upload question rejected: %s", msg)
	}
	if (&questionBody{Type: "file_upload", Content: "x"}).check() != "" {
		t.Error("metadata is optional")
	}
	bad := map[string]questionBody{
		"too many files": {Type: "file_upload", Content: "x", Metadata: map[string]any{"max_files": float64(9)}},
		"huge size":      {Type: "file_upload", Content: "x", Metadata: map[string]any{"max_size_mb": float64(500)}},
		"fraction":       {Type: "file_upload", Content: "x", Metadata: map[string]any{"max_files": 1.5}},
		"bad ext":        {Type: "file_upload", Content: "x", Metadata: map[string]any{"allowed_ext": []any{"pdf", "../x"}}},
		"has answers":    {Type: "file_upload", Content: "x", Answers: []answerBody{{Content: "a"}}},
	}
	for name, q := range bad {
		if q.check() == "" {
			t.Errorf("%s should be rejected", name)
		}
	}
	r := uploadRulesOf([]byte(`{"max_files": 99, "max_size_mb": 0, "allowed_ext": [" .PDF ", "bad ext"]}`))
	if r.MaxFiles != 5 || r.MaxSizeMB != 10 || len(r.Ext) != 1 || r.Ext[0] != "pdf" {
		t.Errorf("rules are clamped and cleaned: %+v", r)
	}
	if got := uploadRulesOf(nil); got.MaxFiles != 1 || len(got.Ext) == 0 {
		t.Errorf("defaults: %+v", got)
	}
	if r := gradeQuestion(quizQuestion{Type: "file_upload"}, nil); !r.Manual {
		t.Error("an uploaded file is graded by a human")
	}
}

func TestApplyExamReview(t *testing.T) {
	build := func() gin.H {
		return gin.H{"score": 80.0, "passed": true, "points_earned": 8.0, "points_max": 10.0, "review_available": true,
			"questions": []gin.H{{"id": 1, "answer": "x", "is_correct": true, "correct_answer": "y", "explanation": "vì", "feedback": "f"}}}
	}
	// Default: the score is shown, answers and corrections are not.
	out := build()
	applyExamReview(out, nil)
	if out["score"] == nil || out["questions"] != nil || out["review_available"] != false {
		t.Errorf("defaults: %v", out)
	}
	// Everything on.
	out = build()
	applyExamReview(out, []byte(`{"show_score": true, "show_answers": true, "show_correct_answers": true, "show_explanation": true}`))
	item := out["questions"].([]gin.H)[0]
	if item["correct_answer"] == nil || item["explanation"] == nil || out["review_available"] != true {
		t.Errorf("all on: %v", out)
	}
	// Answers shown, but not which are right nor why.
	out = build()
	applyExamReview(out, []byte(`{"show_answers": true}`))
	item = out["questions"].([]gin.H)[0]
	if item["answer"] == nil || item["correct_answer"] != nil || item["is_correct"] != nil || item["explanation"] != nil {
		t.Errorf("answers only: %v", item)
	}
	// Score hidden.
	out = build()
	applyExamReview(out, []byte(`{"show_score": false, "show_answers": true}`))
	if out["score"] != nil || out["passed"] != nil || out["points_max"] != nil {
		t.Errorf("score hidden: %v", out)
	}
}
