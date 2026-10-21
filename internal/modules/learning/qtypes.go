package learning

import (
	"encoding/json"
	"math"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Question formats. Everything that depends on the format lives behind questionType, so adding a format means
// writing one type here and registering it in questionTypes (plus its editor and runner in the web app).
//
//	check    validates what an instructor saves
//	present  adds the learner-facing fields to the question payload (it must never reveal the answers)
//	grade    scores a learner's answer
//
// Answer shapes sent by the learner: choice -> option id, multiple -> [ids], ordering -> [ids],
// matching/classification -> {optionId: target}, cloze -> {blankId: text}, short/numerical/essay -> string.

// gradeResult is the outcome of grading one answer.
type gradeResult struct {
	Fraction float64 // share of the question's points earned, 0..1 (formats without partial credit give 0 or 1)
	Manual   bool    // a human has to grade it; Fraction is meaningless then
	Reveal   any     // the correct answer, shown in the review (nil when Manual)
}

func (g gradeResult) full() bool { return g.Fraction >= 1-1e-9 }

type questionType interface {
	check(q *questionBody) string
	present(q quizQuestion, r *rand.Rand, out gin.H)
	grade(q quizQuestion, ans any) gradeResult
}

var questionTypes = map[string]questionType{
	"single_choice":   choiceType{single: true},
	"true_false":      choiceType{single: true},
	"multiple_choice": choiceType{},
	"essay":           essayType{},
	"matching":        matchingType{},
	"ordering":        orderingType{},
	"short_answer":    shortAnswerType{},
	"numerical":       numericalType{},
	"cloze":           clozeType{},
	"classification":  classificationType{},
	"passage":         passageType{},
	"file_upload":     fileUploadType{},
}

// gradeQuestion scores one answer; a format this build does not know is left to a human.
func gradeQuestion(q quizQuestion, ans any) gradeResult {
	t, ok := questionTypes[q.Type]
	if !ok {
		return gradeResult{Manual: true}
	}
	return t.grade(q, ans)
}

func bool01(ok bool) float64 {
	if ok {
		return 1
	}
	return 0
}

func correctCount(q *questionBody) int {
	n := 0
	for _, a := range q.Answers {
		if a.IsCorrect {
			n++
		}
	}
	return n
}

func optionsOf(answers []qa) []gin.H {
	opts := make([]gin.H, 0, len(answers))
	for _, a := range answers {
		opts = append(opts, gin.H{"id": a.ID, "content": a.Content})
	}
	return opts
}

// ---- single_choice, true_false, multiple_choice ------------------------------------

type choiceType struct{ single bool }

func (t choiceType) check(q *questionBody) string {
	if t.single {
		if len(q.Answers) < 2 || correctCount(q) != 1 {
			return "Provide at least two answers with exactly one correct."
		}
		return ""
	}
	if len(q.Answers) < 2 || correctCount(q) < 1 {
		return "Provide at least two answers with at least one correct."
	}
	return ""
}

func (choiceType) present(q quizQuestion, _ *rand.Rand, out gin.H) {
	out["options"] = optionsOf(q.Answers)
}

func (t choiceType) grade(q quizQuestion, ans any) gradeResult {
	got := idsOf(ans)
	if t.single {
		var right int64
		for _, a := range q.Answers {
			if a.IsCorrect {
				right = a.ID
			}
		}
		return gradeResult{Fraction: bool01(len(got) == 1 && got[0] == right), Reveal: right}
	}
	want := []int64{}
	for _, a := range q.Answers {
		if a.IsCorrect {
			want = append(want, a.ID)
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	eq := len(got) == len(want)
	for i := 0; eq && i < len(got); i++ {
		eq = got[i] == want[i]
	}
	return gradeResult{Fraction: bool01(eq), Reveal: want}
}

// ---- essay -------------------------------------------------------------------------

type essayType struct{}

func (essayType) check(*questionBody) string              { return "" }
func (essayType) present(quizQuestion, *rand.Rand, gin.H) {}
func (essayType) grade(quizQuestion, any) gradeResult     { return gradeResult{Manual: true} }

// ---- ordering ----------------------------------------------------------------------

type orderingType struct{}

func (orderingType) check(q *questionBody) string {
	if len(q.Answers) < 2 {
		return "Provide at least two items."
	}
	return ""
}

func (orderingType) present(q quizQuestion, r *rand.Rand, out gin.H) {
	opts := optionsOf(q.Answers)
	r.Shuffle(len(opts), func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })
	out["options"] = opts
}

func (orderingType) grade(q quizQuestion, ans any) gradeResult {
	sorted := append([]qa{}, q.Answers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := 0, 0
		if sorted[i].CorrectPos != nil {
			a = *sorted[i].CorrectPos
		}
		if sorted[j].CorrectPos != nil {
			b = *sorted[j].CorrectPos
		}
		return a < b
	})
	want := make([]int64, len(sorted))
	for i, a := range sorted {
		want[i] = a.ID
	}
	got := idsOf(ans)
	eq := len(got) == len(want)
	for i := 0; eq && i < len(got); i++ {
		eq = got[i] == want[i]
	}
	return gradeResult{Fraction: bool01(eq), Reveal: want}
}

// ---- matching ----------------------------------------------------------------------

type matchingType struct{}

func (matchingType) check(q *questionBody) string {
	if len(q.Answers) < 2 {
		return "Provide at least two pairs."
	}
	for _, a := range q.Answers {
		if a.SubContent == nil || strings.TrimSpace(*a.SubContent) == "" {
			return "Every pair needs both sides."
		}
	}
	return ""
}

func (matchingType) present(q quizQuestion, r *rand.Rand, out gin.H) {
	out["options"] = optionsOf(q.Answers)
	targets := []string{}
	for _, a := range q.Answers {
		if a.SubContent != nil {
			targets = append(targets, *a.SubContent)
		}
	}
	r.Shuffle(len(targets), func(i, j int) { targets[i], targets[j] = targets[j], targets[i] })
	out["targets"] = targets
}

func (matchingType) grade(q quizQuestion, ans any) gradeResult {
	want := map[string]string{}
	for _, a := range q.Answers {
		if a.SubContent != nil {
			want[strconv.FormatInt(a.ID, 10)] = *a.SubContent
		}
	}
	got, _ := ans.(map[string]any)
	eq := len(got) == len(want) && len(want) > 0
	for k, w := range want {
		if s, _ := got[k].(string); s != w {
			eq = false
		}
	}
	return gradeResult{Fraction: bool01(eq), Reveal: want}
}

// ---- short_answer ------------------------------------------------------------------

type shortAnswerType struct{}

func (shortAnswerType) check(q *questionBody) string {
	if correctCount(q) < 1 {
		return "Provide at least one accepted answer."
	}
	return ""
}

func (shortAnswerType) present(quizQuestion, *rand.Rand, gin.H) {}

func (shortAnswerType) grade(q quizQuestion, ans any) gradeResult {
	s, _ := ans.(string)
	accepted := []string{}
	hit := false
	for _, a := range q.Answers {
		if a.IsCorrect {
			accepted = append(accepted, a.Content)
			if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(a.Content)) {
				hit = true
			}
		}
	}
	return gradeResult{Fraction: bool01(hit), Reveal: accepted}
}

// ---- numerical ---------------------------------------------------------------------

type numericalType struct{}

func (numericalType) check(q *questionBody) string {
	if correctCount(q) != 1 {
		return "Provide exactly one correct numeric value."
	}
	for _, a := range q.Answers {
		if a.IsCorrect {
			if _, err := strconv.ParseFloat(strings.TrimSpace(a.Content), 64); err != nil {
				return "The correct value must be a number."
			}
		}
	}
	return ""
}

func (numericalType) present(quizQuestion, *rand.Rand, gin.H) {}

func (numericalType) grade(q quizQuestion, ans any) gradeResult {
	var want float64
	for _, a := range q.Answers {
		if a.IsCorrect {
			want, _ = strconv.ParseFloat(strings.TrimSpace(a.Content), 64)
		}
	}
	var meta struct {
		Tolerance float64 `json:"tolerance"`
	}
	_ = json.Unmarshal(q.Metadata, &meta)
	got, ok := asFloat(ans)
	return gradeResult{
		Fraction: bool01(ok && math.Abs(got-want) <= meta.Tolerance+1e-9),
		Reveal:   gin.H{"value": want, "tolerance": meta.Tolerance},
	}
}

// ---- partial credit ------------------------------------------------------------------

// partialCredit reports whether a multi-part question (cloze, classification) pays out per correct part.
// Default true; set metadata {"partial_credit": false} to require every part.
func partialCredit(raw []byte) bool {
	var meta struct {
		Partial *bool `json:"partial_credit"`
	}
	_ = json.Unmarshal(raw, &meta)
	return meta.Partial == nil || *meta.Partial
}

// share is right/total, or all-or-nothing when partial credit is off.
func share(right, total int, partial bool) float64 {
	switch {
	case total == 0:
		return 0
	case right == total:
		return 1
	case partial:
		return float64(right) / float64(total)
	}
	return 0
}

// ---- cloze (fill in the blanks) --------------------------------------------------------
//
// content: "Thủ đô của Việt Nam là {{1}}, nằm bên sông {{2}}."
// answers: one row per accepted text; sub_content is the blank id ("1"), content the text, is_correct = true.
// A wrong option (is_correct = false) for a blank makes that blank a dropdown (or a draggable word) instead of a text box.
// metadata: partial_credit (default true), case_sensitive (default false), input = "select" | "drag" (how option blanks look).

var blankRe = regexp.MustCompile(`\{\{\s*(\d{1,2})\s*\}\}`)

const maxBlanks = 20

// blankIDs lists the blank ids of a text in order of first appearance.
func blankIDs(content string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range blankRe.FindAllStringSubmatch(content, -1) {
		id := strings.TrimLeft(m[1], "0")
		if id == "" {
			id = "0"
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func blankOf(sub *string) string {
	if sub == nil {
		return ""
	}
	id := strings.TrimLeft(strings.TrimSpace(*sub), "0")
	if id == "" && strings.TrimSpace(*sub) != "" {
		return "0"
	}
	return id
}

func normText(s string, caseSensitive bool) string {
	s = strings.Join(strings.Fields(s), " ")
	if !caseSensitive {
		s = strings.ToLower(s)
	}
	return s
}

type clozeType struct{}

func (clozeType) check(q *questionBody) string {
	ids := blankIDs(q.Content)
	if len(ids) == 0 {
		return "Add at least one blank such as {{1}} to the text."
	}
	if len(ids) > maxBlanks {
		return "A cloze question can have at most 20 blanks."
	}
	known := map[string]bool{}
	for _, id := range ids {
		known[id] = true
	}
	hasCorrect := map[string]bool{}
	for _, a := range q.Answers {
		id := blankOf(a.SubContent)
		if id == "" {
			return "Every answer must say which blank it belongs to."
		}
		if !known[id] {
			return "Answer for blank {{" + id + "}} but the text has no such blank."
		}
		if a.IsCorrect {
			hasCorrect[id] = true
		}
	}
	for _, id := range ids {
		if !hasCorrect[id] {
			return "Blank {{" + id + "}} needs at least one correct answer."
		}
	}
	if in, _ := q.Metadata["input"].(string); in != "" && in != "select" && in != "drag" {
		return "The input must be select or drag."
	}
	return ""
}

func (clozeType) present(q quizQuestion, r *rand.Rand, out gin.H) {
	var meta struct {
		Input string `json:"input"`
	}
	_ = json.Unmarshal(q.Metadata, &meta)
	if meta.Input != "drag" {
		meta.Input = "select"
	}
	blanks := []gin.H{}
	for _, id := range blankIDs(q.Content) {
		var texts []string
		seen := map[string]bool{}
		distractor := false
		for _, a := range q.Answers {
			if blankOf(a.SubContent) != id {
				continue
			}
			if !a.IsCorrect {
				distractor = true
			}
			if !seen[a.Content] {
				seen[a.Content] = true
				texts = append(texts, a.Content)
			}
		}
		b := gin.H{"id": id}
		if distractor {
			r.Shuffle(len(texts), func(i, j int) { texts[i], texts[j] = texts[j], texts[i] })
			b["options"] = texts
		}
		blanks = append(blanks, b)
	}
	out["blanks"], out["input"] = blanks, meta.Input
}

func (clozeType) grade(q quizQuestion, ans any) gradeResult {
	var meta struct {
		CaseSensitive bool `json:"case_sensitive"`
	}
	_ = json.Unmarshal(q.Metadata, &meta)
	got, _ := ans.(map[string]any)
	ids := blankIDs(q.Content)
	reveal := map[string][]string{}
	right := 0
	for _, id := range ids {
		s, _ := got[id].(string)
		s = normText(s, meta.CaseSensitive)
		hit := false
		for _, a := range q.Answers {
			if a.IsCorrect && blankOf(a.SubContent) == id {
				reveal[id] = append(reveal[id], a.Content)
				if s != "" && s == normText(a.Content, meta.CaseSensitive) {
					hit = true
				}
			}
		}
		if hit {
			right++
		}
	}
	return gradeResult{Fraction: share(right, len(ids), partialCredit(q.Metadata)), Reveal: reveal}
}

// ---- classification (drag items into categories) ----------------------------------------
//
// answers: content = the item, sub_content = its category. Several items share a category.
// The learner answer is {itemId: category}; partial credit per item unless metadata.partial_credit is false.

type classificationType struct{}

func categoriesOf(answers []qa) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, a := range answers {
		if a.SubContent == nil {
			continue
		}
		c := strings.TrimSpace(*a.SubContent)
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func (classificationType) check(q *questionBody) string {
	if len(q.Answers) < 2 {
		return "Provide at least two items."
	}
	cats := map[string]bool{}
	for _, a := range q.Answers {
		if a.SubContent == nil || strings.TrimSpace(*a.SubContent) == "" {
			return "Every item needs a category."
		}
		cats[strings.TrimSpace(*a.SubContent)] = true
	}
	if len(cats) < 2 {
		return "Provide at least two categories."
	}
	return ""
}

func (classificationType) present(q quizQuestion, r *rand.Rand, out gin.H) {
	opts := optionsOf(q.Answers)
	r.Shuffle(len(opts), func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })
	out["options"], out["categories"] = opts, categoriesOf(q.Answers)
}

func (classificationType) grade(q quizQuestion, ans any) gradeResult {
	got, _ := ans.(map[string]any)
	want := map[string]string{}
	right := 0
	for _, a := range q.Answers {
		if a.SubContent == nil {
			continue
		}
		key := strconv.FormatInt(a.ID, 10)
		want[key] = strings.TrimSpace(*a.SubContent)
		if s, _ := got[key].(string); strings.TrimSpace(s) == want[key] {
			right++
		}
	}
	return gradeResult{Fraction: share(right, len(want), partialCredit(q.Metadata)), Reveal: want}
}

// ---- passage (a reading text with sub-questions) ------------------------------------------
//
// The passage itself is not answered or scored: questions.parent_question_id links its sub-questions, which are
// picked, drawn and graded as a group (see expandPassages). Points come from the sub-questions.

type passageType struct{}

func (passageType) check(q *questionBody) string {
	switch {
	case len(q.Answers) > 0:
		return "A passage has no answers of its own."
	case q.ParentQuestionID != nil:
		return "A passage cannot be a sub-question."
	}
	for i := range q.Children {
		c := &q.Children[i]
		if c.Type == "passage" {
			return "A passage cannot contain another passage."
		}
		if msg := c.check(); msg != "" {
			return "Sub-question " + strconv.Itoa(i+1) + ": " + msg
		}
	}
	return ""
}

func (passageType) present(quizQuestion, *rand.Rand, gin.H) {}
func (passageType) grade(quizQuestion, any) gradeResult     { return gradeResult{} }

// ---- file_upload (the learner attaches files, a human grades) ------------------------------------
//
// metadata: max_files (1-5, default 1), max_size_mb (1-25, default 10), allowed_ext (default pdf/office/zip/images/txt).
// Files are uploaded to POST /attempts/:id/files and stored in attempt_files; the answer is rebuilt from that table
// when the attempt is graded (the client cannot claim a file it did not upload). Needs manual grading.

type uploadRules struct {
	MaxFiles  int      `json:"max_files"`
	MaxSizeMB int      `json:"max_size_mb"`
	Ext       []string `json:"allowed_ext"`
}

var defaultUploadExt = []string{"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "zip", "rar", "txt", "png", "jpg", "jpeg"}

func uploadRulesOf(raw []byte) uploadRules {
	var r uploadRules
	_ = json.Unmarshal(raw, &r)
	if r.MaxFiles < 1 {
		r.MaxFiles = 1
	}
	if r.MaxFiles > 5 {
		r.MaxFiles = 5
	}
	if r.MaxSizeMB < 1 {
		r.MaxSizeMB = 10
	}
	if r.MaxSizeMB > 25 {
		r.MaxSizeMB = 25
	}
	clean := []string{}
	for _, e := range r.Ext {
		if e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), ".")); extRe.MatchString(e) {
			clean = append(clean, e)
		}
	}
	if len(clean) == 0 {
		clean = defaultUploadExt
	}
	r.Ext = clean
	return r
}

type fileUploadType struct{}

func (fileUploadType) check(q *questionBody) string {
	if len(q.Answers) > 0 {
		return "A file question has no answers."
	}
	num := func(key string, lo, hi float64) string {
		v, present := q.Metadata[key]
		if !present {
			return ""
		}
		f, ok := v.(float64)
		if !ok || f < lo || f > hi || f != math.Trunc(f) {
			return "The " + key + " must be a whole number between " + strconv.Itoa(int(lo)) + " and " + strconv.Itoa(int(hi)) + "."
		}
		return ""
	}
	if msg := num("max_files", 1, 5); msg != "" {
		return msg
	}
	if msg := num("max_size_mb", 1, 25); msg != "" {
		return msg
	}
	if raw, present := q.Metadata["allowed_ext"]; present {
		list, ok := raw.([]any)
		if !ok {
			return "The allowed_ext must be a list."
		}
		for _, e := range list {
			s, _ := e.(string)
			if !extRe.MatchString(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))) {
				return "The allowed_ext list has an invalid extension."
			}
		}
	}
	return ""
}

func (fileUploadType) present(q quizQuestion, _ *rand.Rand, out gin.H) {
	r := uploadRulesOf(q.Metadata)
	out["upload"] = gin.H{"max_files": r.MaxFiles, "max_size_mb": r.MaxSizeMB, "allowed_ext": r.Ext}
}

func (fileUploadType) grade(quizQuestion, any) gradeResult { return gradeResult{Manual: true} }
