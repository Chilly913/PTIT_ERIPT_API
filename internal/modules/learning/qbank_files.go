package learning

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Excel / Word import and export of question banks, for instructors who do not work with JSON.
//
// Excel: one question per row (see xlsxHeader). Word: "Câu N: ..." blocks with "A. ..." options and "Đáp án: ...".

var typeLabels = map[string]string{
	"single_choice": "Một đáp án", "multiple_choice": "Nhiều đáp án", "true_false": "Đúng/Sai", "essay": "Tự luận",
	"short_answer": "Trả lời ngắn", "numerical": "Số học", "ordering": "Sắp xếp", "matching": "Ghép đôi",
	"cloze": "Điền khuyết", "classification": "Phân loại", "passage": "Đoạn văn", "file_upload": "Nộp tệp",
}

// notInFiles are formats a flat Excel/Word row cannot express; they are created in the web app.
var notInFiles = map[string]bool{"passage": true}

var labelTypes = func() map[string]string {
	m := map[string]string{}
	for k, v := range typeLabels {
		m[strings.ToLower(v)] = k
		m[k] = k
	}
	m["đúng sai"] = "true_false"
	return m
}()

const maxOptions = 6

var xlsxHeader = []string{"Loại câu hỏi", "Nội dung câu hỏi", "Độ khó (1-5)", "Điểm", "Đáp án A", "Đáp án B", "Đáp án C", "Đáp án D", "Đáp án E", "Đáp án F", "Đáp án đúng", "Giải thích", "Nhóm câu hỏi"}

// parsedQuestion is one imported item; Err is set when the row could not even be read.
type parsedQuestion struct {
	Row int
	Q   questionBody
	Err string
}

func letterIndex(s string) int {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 1 || s[0] < 'A' || s[0] > 'Z' {
		return -1
	}
	return int(s[0] - 'A')
}

// splitCorrect turns "A, C" / "A;C" / "AC" into letter indexes.
func splitCorrect(s string) ([]int, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	var out []int
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			out = append(out, int(r-'A'))
		case r == ',' || r == ';' || r == ' ' || r == '/' || r == '+':
		default:
			return nil, false
		}
	}
	return out, len(out) > 0
}

// buildQuestion assembles a question from loosely typed parts shared by the Excel and Word readers.
func buildQuestion(typ, content string, diff int, score *float64, options []string, correct, explanation string) (questionBody, string) {
	q := questionBody{Content: strings.TrimSpace(content), Difficulty: diff, DefaultScore: score}
	if e := strings.TrimSpace(explanation); e != "" {
		q.Explanation = &e
	}
	var opts []string
	for _, o := range options {
		if strings.TrimSpace(o) != "" {
			opts = append(opts, strings.TrimSpace(o))
		}
	}
	correct = strings.TrimSpace(correct)
	if typ != "" {
		t, ok := labelTypes[strings.ToLower(strings.TrimSpace(typ))]
		if !ok {
			return q, "Loại câu hỏi không hợp lệ: " + typ
		}
		q.Type = t
	} else { // infer
		switch {
		case len(opts) == 0 && correct == "":
			q.Type = "essay"
		case len(opts) == 0:
			q.Type = "short_answer"
		case len(opts) == 2 && isTrueFalse(opts):
			q.Type = "true_false"
		default:
			if idx, ok := splitCorrect(correct); ok && len(idx) > 1 {
				q.Type = "multiple_choice"
			} else {
				q.Type = "single_choice"
			}
		}
	}
	if notInFiles[q.Type] {
		return q, "Loại \"" + typeLabels[q.Type] + "\" chưa hỗ trợ nhập bằng tệp, hãy tạo trực tiếp trên hệ thống"
	}
	switch q.Type {
	case "essay", "file_upload":
	case "cloze":
		for _, o := range opts {
			id, rest, found := strings.Cut(o, ":")
			id = strings.TrimSpace(id)
			if !found || id == "" {
				return q, "Câu điền khuyết cần dạng \"số ô: đáp án | đáp án khác\" ở mỗi cột đáp án (thêm ~ trước phương án sai)"
			}
			for _, t := range strings.Split(rest, "|") {
				t = strings.TrimSpace(t)
				right := !strings.HasPrefix(t, "~")
				t = strings.TrimSpace(strings.TrimPrefix(t, "~"))
				if t != "" {
					sub := id
					q.Answers = append(q.Answers, answerBody{Content: t, SubContent: &sub, IsCorrect: right})
				}
			}
		}
	case "short_answer":
		for _, a := range strings.FieldsFunc(correct, func(r rune) bool { return r == ';' || r == '|' }) {
			if a = strings.TrimSpace(a); a != "" {
				q.Answers = append(q.Answers, answerBody{Content: a, IsCorrect: true})
			}
		}
	case "numerical":
		if correct != "" {
			q.Answers = []answerBody{{Content: strings.ReplaceAll(correct, ",", "."), IsCorrect: true}}
		}
	case "ordering":
		for i, o := range opts {
			pos := i + 1
			q.Answers = append(q.Answers, answerBody{Content: o, CorrectPosition: &pos})
		}
	case "matching", "classification":
		for _, o := range opts {
			l, r, found := strings.Cut(o, "|")
			if !found {
				return q, "Câu ghép đôi / phân loại cần dạng \"vế trái | vế phải\" (mục | nhóm) ở mỗi đáp án"
			}
			rr := strings.TrimSpace(r)
			q.Answers = append(q.Answers, answerBody{Content: strings.TrimSpace(l), SubContent: &rr})
		}
	default: // single_choice, multiple_choice, true_false
		if q.Type == "true_false" && len(opts) == 0 {
			opts = []string{"Đúng", "Sai"}
		}
		idx, ok := splitCorrect(correct)
		if !ok {
			// true/false written as words
			for i, o := range opts {
				if strings.EqualFold(o, correct) {
					idx, ok = []int{i}, true
				}
			}
		}
		if !ok {
			return q, "Thiếu hoặc sai cột \"Đáp án đúng\" (ví dụ: B hoặc A,C)"
		}
		set := map[int]bool{}
		for _, i := range idx {
			if i >= len(opts) {
				return q, "Đáp án đúng " + string(rune('A'+i)) + " không có trong danh sách đáp án"
			}
			set[i] = true
		}
		for i, o := range opts {
			q.Answers = append(q.Answers, answerBody{Content: o, IsCorrect: set[i]})
		}
	}
	return q, ""
}

func isTrueFalse(opts []string) bool {
	a, b := strings.ToLower(opts[0]), strings.ToLower(opts[1])
	return (a == "đúng" && b == "sai") || (a == "true" && b == "false")
}

func parseDifficulty(s string) (int, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 1, ""
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 5 {
		return 1, "Độ khó phải là số từ 1 đến 5"
	}
	return n, ""
}

func parseScore(s string) *float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return &f
	}
	return nil
}

// ---- reading -------------------------------------------------------------------

func parseXLSX(data []byte) ([]parsedQuestion, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("Không đọc được tệp Excel. Hãy dùng tệp .xlsx (tải file mẫu để xem định dạng).")
	}
	defer f.Close()
	rows, err := f.GetRows(f.GetSheetName(0))
	if err != nil || len(rows) < 2 {
		return nil, fmt.Errorf("Tệp Excel không có dòng dữ liệu nào.")
	}
	col := func(r []string, i int) string {
		if i < len(r) {
			return r[i]
		}
		return ""
	}
	var out []parsedQuestion
	for i, r := range rows[1:] {
		if strings.TrimSpace(strings.Join(r, "")) == "" {
			continue
		}
		pq := parsedQuestion{Row: i + 2}
		diff, derr := parseDifficulty(col(r, 2))
		opts := make([]string, maxOptions)
		for k := 0; k < maxOptions; k++ {
			opts[k] = col(r, 4+k)
		}
		q, qerr := buildQuestion(col(r, 0), col(r, 1), diff, parseScore(col(r, 3)), opts, col(r, 10), col(r, 11))
		if g := strings.TrimSpace(col(r, 12)); g != "" {
			q.Group = &g
		}
		pq.Q = q
		if derr != "" {
			pq.Err = derr
		} else {
			pq.Err = qerr
		}
		out = append(out, pq)
	}
	return out, nil
}

var (
	reQuestion = regexp.MustCompile(`(?i)^c[âa]u\s*(\d+)\s*[:.)\-]\s*(.*)$`)
	reOption   = regexp.MustCompile(`^([A-Fa-f])\s*[.)]\s*(.+)$`)
	reField    = regexp.MustCompile(`(?i)^(đáp án|dap an|giải thích|giai thich|độ khó|do kho|điểm|diem|loại|loai|nhóm|nhom)\s*:\s*(.*)$`)
)

func parseDOCX(data []byte) ([]parsedQuestion, error) {
	paras, err := docxParas(data)
	if err != nil {
		return nil, err
	}
	// tables that look like "one question per row" are read as such; every other paragraph goes to the line parser
	grids := gridsOf(paras)
	var tableQs []parsedQuestion
	isQTable := map[int]bool{}
	for idx := 0; idx < len(grids); idx++ {
		g := grids[idx]
		var header []string
		for c := 0; c < 40; c++ {
			header = append(header, g.cell(0, c))
		}
		if cols, ok := questionTable(header); ok {
			isQTable[idx] = true
			tableQs = append(tableQs, questionsFromTable(g, cols)...)
		}
	}
	var lines []string
	for _, p := range paras {
		if p.tbl >= 0 && isQTable[p.tbl] {
			continue
		}
		lines = append(lines, p.lines...)
	}
	type block struct {
		row                                                int
		content, typ, correct, explain, diff, score, group string
		opts                                               []string
	}
	var blocks []*block
	var cur *block
	for _, ln := range lines {
		if m := reQuestion.FindStringSubmatch(ln); m != nil {
			n, _ := strconv.Atoi(m[1])
			cur = &block{row: n, content: m[2]}
			blocks = append(blocks, cur)
			continue
		}
		if cur == nil {
			continue // title or instructions before the first question
		}
		if m := reField.FindStringSubmatch(ln); m != nil {
			v := strings.TrimSpace(m[2])
			switch strings.ToLower(m[1]) {
			case "đáp án", "dap an":
				cur.correct = v
			case "giải thích", "giai thich":
				cur.explain = v
			case "độ khó", "do kho":
				cur.diff = v
			case "điểm", "diem":
				cur.score = v
			case "nhóm", "nhom":
				cur.group = v
			default:
				cur.typ = v
			}
			continue
		}
		if m := reOption.FindStringSubmatch(ln); m != nil {
			idx := letterIndex(m[1])
			for len(cur.opts) <= idx {
				cur.opts = append(cur.opts, "")
			}
			cur.opts[idx] = m[2]
			continue
		}
		cur.content += "\n" + ln // continuation of the question text
	}
	if len(blocks) == 0 && len(tableQs) == 0 {
		return nil, fmt.Errorf("Không tìm thấy câu hỏi nào. Mỗi câu bắt đầu bằng \"Câu 1:\" (tải file mẫu để xem định dạng).")
	}
	out := make([]parsedQuestion, 0, len(blocks))
	for _, b := range blocks {
		diff, derr := parseDifficulty(b.diff)
		q, qerr := buildQuestion(b.typ, b.content, diff, parseScore(b.score), b.opts, b.correct, b.explain)
		if b.group != "" {
			g := b.group
			q.Group = &g
		}
		pq := parsedQuestion{Row: b.row, Q: q, Err: qerr}
		if derr != "" {
			pq.Err = derr
		}
		out = append(out, pq)
	}
	out = append(out, tableQs...)
	return out, nil
}

// ---- writing -------------------------------------------------------------------

// clozeCells renders the answers of a cloze question as one cell per blank: "1: Hà Nội | Ha Noi | ~Huế".
func clozeCells(answers []answerBody) []string {
	order := []string{}
	byBlank := map[string][]string{}
	for _, a := range answers {
		id := blankOf(a.SubContent)
		if _, seen := byBlank[id]; !seen {
			order = append(order, id)
		}
		t := a.Content
		if !a.IsCorrect {
			t = "~" + t
		}
		byBlank[id] = append(byBlank[id], t)
	}
	cells := make([]string, 0, len(order))
	for _, id := range order {
		cells = append(cells, id+": "+strings.Join(byBlank[id], " | "))
	}
	return cells
}

func correctLetters(q questionBody) string {
	var sb []string
	for i, a := range q.Answers {
		if a.IsCorrect {
			sb = append(sb, string(rune('A'+i)))
		}
	}
	return strings.Join(sb, ",")
}

// excelRow renders one question to the row layout of xlsxHeader.
func excelRow(q questionBody) []any {
	row := make([]any, len(xlsxHeader))
	row[0], row[1], row[2] = typeLabels[q.Type], q.Content, q.Difficulty
	if q.DefaultScore != nil {
		row[3] = *q.DefaultScore
	}
	correct := correctLetters(q)
	switch q.Type {
	case "short_answer":
		var t []string
		for _, a := range q.Answers {
			if a.IsCorrect {
				t = append(t, a.Content)
			}
		}
		correct = strings.Join(t, "; ")
	case "numerical":
		if len(q.Answers) > 0 {
			correct = q.Answers[0].Content
		}
	case "ordering", "matching", "classification", "cloze":
		correct = ""
	}
	if q.Type == "cloze" {
		for i, cell := range clozeCells(q.Answers) {
			if i >= maxOptions {
				break
			}
			row[4+i] = cell
		}
	} else if q.Type != "short_answer" && q.Type != "numerical" {
		for i, a := range q.Answers {
			if i >= maxOptions {
				break
			}
			v := a.Content
			if (q.Type == "matching" || q.Type == "classification") && a.SubContent != nil {
				v += " | " + *a.SubContent
			}
			row[4+i] = v
		}
	}
	row[10] = correct
	if q.Group != nil {
		row[12] = *q.Group
	}
	if q.Explanation != nil {
		row[11] = *q.Explanation
	}
	return row
}

func writeXLSX(qs []questionBody, withGuide bool) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	sheet := "Câu hỏi"
	_ = f.SetSheetName("Sheet1", sheet)
	for i, h := range xlsxHeader {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	head, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Color: []string{"#E8EEF7"}, Pattern: 1}, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true}})
	_ = f.SetCellStyle(sheet, "A1", "M1", head)
	wrap, _ := f.NewStyle(&excelize.Style{Alignment: &excelize.Alignment{Vertical: "top", WrapText: true}})
	for i, q := range qs {
		for j, v := range excelRow(q) {
			cell, _ := excelize.CoordinatesToCellName(j+1, i+2)
			_ = f.SetCellValue(sheet, cell, v)
		}
	}
	last := len(qs) + 1
	if last < 2 {
		last = 2
	}
	_ = f.SetCellStyle(sheet, "A2", fmt.Sprintf("M%d", last), wrap)
	for col, w := range map[string]float64{"A": 16, "B": 50, "C": 12, "D": 8, "E": 24, "F": 24, "G": 24, "H": 24, "I": 20, "J": 20, "K": 16, "L": 36, "M": 22} {
		_ = f.SetColWidth(sheet, col, col, w)
	}
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	// Drop-down for the question type column.
	dv := excelize.NewDataValidation(true)
	dv.Sqref = "A2:A1000"
	_ = dv.SetDropList([]string{"Một đáp án", "Nhiều đáp án", "Đúng/Sai", "Tự luận", "Trả lời ngắn", "Số học", "Sắp xếp", "Ghép đôi", "Điền khuyết", "Phân loại", "Nộp tệp"})
	_ = f.AddDataValidation(sheet, dv)
	if withGuide {
		g := "Hướng dẫn"
		_, _ = f.NewSheet(g)
		for i, line := range []string{
			"HƯỚNG DẪN NHẬP CÂU HỎI BẰNG EXCEL",
			"Mỗi dòng ở sheet \"Câu hỏi\" là một câu hỏi. Xóa các dòng ví dụ trước khi nhập câu của bạn.",
			"Loại câu hỏi: chọn trong danh sách (Một đáp án, Nhiều đáp án, Đúng/Sai, Tự luận, Trả lời ngắn, Số học, Sắp xếp, Ghép đôi, Điền khuyết, Phân loại). Câu hỏi theo đoạn văn tạo trực tiếp trên hệ thống.",
			"Một đáp án / Nhiều đáp án / Đúng/Sai: điền các cột Đáp án A, B, C... rồi ghi chữ cái đúng ở cột \"Đáp án đúng\" (ví dụ: B hoặc A,C).",
			"Trả lời ngắn: để trống các cột Đáp án A-F, ghi các đáp án được chấp nhận ở cột \"Đáp án đúng\", cách nhau bằng dấu chấm phẩy (;).",
			"Số học: ghi giá trị số ở cột \"Đáp án đúng\" (ví dụ: 3.14).",
			"Sắp xếp: điền các mục theo đúng thứ tự cần xếp ở cột Đáp án A, B, C...",
			"Ghép đôi: mỗi cột Đáp án ghi dạng \"vế trái | vế phải\".",
			"Phân loại (kéo thả vào nhóm): mỗi cột Đáp án ghi dạng \"mục | tên nhóm\"; nhiều mục có thể cùng một nhóm, cần ít nhất 2 nhóm.",
			"Điền khuyết: trong nội dung đánh dấu chỗ trống bằng {{1}}, {{2}}...; mỗi cột Đáp án ghi dạng \"1: đáp án | đáp án khác | ~phương án sai\" (phương án sai có dấu ~ trước; nếu có phương án sai, ô trống hiện dạng danh sách chọn).",
			"Nộp tệp, Đoạn văn: tạo trực tiếp trên hệ thống (Nộp tệp chỉ cần điền nội dung câu hỏi).",
			"Tự luận: chỉ cần điền nội dung câu hỏi.",
			"Nhóm câu hỏi (cột M): ghi tên nhóm/chủ đề/chương, ví dụ \"Chương 1\". Hệ thống tự tạo nhóm nếu chưa có và xếp câu hỏi vào đó; để trống nếu không phân nhóm.",
			"Độ khó từ 1 (dễ) đến 5 (khó), Điểm có thể để trống (mặc định 1).",
		} {
			cell, _ := excelize.CoordinatesToCellName(1, i+1)
			_ = f.SetCellValue(g, cell, line)
		}
		_ = f.SetColWidth(g, "A", "A", 130)
		f.SetActiveSheet(0)
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// writeDOCX builds a minimal .docx (no external library) from paragraphs.
func writeDOCX(paras []string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, content string) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(content))
		return err
	}
	var body strings.Builder
	for _, p := range paras {
		body.WriteString(`<w:p><w:r><w:t xml:space="preserve">` + esc(p) + `</w:t></w:r></w:p>`)
	}
	if err := add("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`); err != nil {
		return nil, err
	}
	if err := add("_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`); err != nil {
		return nil, err
	}
	if err := add("word/document.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`+body.String()+`</w:body></w:document>`); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func docxParagraphs(qs []questionBody) []string {
	var out []string
	for i, q := range qs {
		out = append(out, fmt.Sprintf("Câu %d: %s", i+1, q.Content))
		out = append(out, "Loại: "+typeLabels[q.Type])
		if q.Group != nil && strings.TrimSpace(*q.Group) != "" {
			out = append(out, "Nhóm: "+*q.Group)
		}
		switch q.Type {
		case "essay", "file_upload":
		case "cloze":
			for j, cell := range clozeCells(q.Answers) {
				out = append(out, fmt.Sprintf("%c. %s", 'A'+j, cell))
			}
		case "short_answer", "numerical":
			var t []string
			for _, a := range q.Answers {
				if a.IsCorrect {
					t = append(t, a.Content)
				}
			}
			out = append(out, "Đáp án: "+strings.Join(t, "; "))
		default:
			for j, a := range q.Answers {
				v := a.Content
				if (q.Type == "matching" || q.Type == "classification") && a.SubContent != nil {
					v += " | " + *a.SubContent
				}
				out = append(out, fmt.Sprintf("%c. %s", 'A'+j, v))
			}
			if c := correctLetters(q); c != "" {
				out = append(out, "Đáp án: "+c)
			}
		}
		out = append(out, fmt.Sprintf("Độ khó: %d", q.Difficulty))
		if q.Explanation != nil {
			out = append(out, "Giải thích: "+*q.Explanation)
		}
		out = append(out, "")
	}
	return out
}

const (
	mimeXLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	mimeDOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
)

func sampleQuestions() []questionBody {
	one, two := 1.0, 2.0
	ch1, ch2 := strPtr("Chương 1 - Kinh tế số"), strPtr("Chương 2 - Thương mại điện tử")
	e1 := "Kinh tế số dựa trên công nghệ số."
	tol := map[string]any{"tolerance": 0.01}
	return []questionBody{
		{Type: "single_choice", Group: ch1, Content: "Kinh tế số là gì?", Difficulty: 1, DefaultScore: &one, Explanation: &e1, Answers: []answerBody{{Content: "Nền kinh tế dựa trên công nghệ số", IsCorrect: true}, {Content: "Nền kinh tế nông nghiệp"}, {Content: "Nền kinh tế tự cung tự cấp"}}},
		{Type: "true_false", Group: ch1, Content: "Dữ liệu là một yếu tố sản xuất trong nền kinh tế số.", Difficulty: 1, Answers: []answerBody{{Content: "Đúng", IsCorrect: true}, {Content: "Sai"}}},
		{Type: "multiple_choice", Group: ch2, Content: "Những mô hình nào thuộc thương mại điện tử?", Difficulty: 2, DefaultScore: &two, Answers: []answerBody{{Content: "B2B", IsCorrect: true}, {Content: "B2C", IsCorrect: true}, {Content: "A2Z"}}},
		{Type: "short_answer", Group: ch2, Content: "Viết tắt tiếng Việt của \"thương mại điện tử\"?", Difficulty: 1, Answers: []answerBody{{Content: "TMĐT", IsCorrect: true}, {Content: "tmdt", IsCorrect: true}}},
		{Type: "numerical", Group: ch2, Content: "Doanh thu 200 triệu, chi phí 150 triệu. Lợi nhuận là bao nhiêu triệu?", Difficulty: 2, Metadata: tol, Answers: []answerBody{{Content: "50", IsCorrect: true}}},
		{Type: "ordering", Group: ch1, Content: "Sắp xếp các bước chuyển đổi số theo thứ tự hợp lý.", Difficulty: 3, Answers: []answerBody{{Content: "Đánh giá hiện trạng"}, {Content: "Lập chiến lược"}, {Content: "Triển khai thí điểm"}, {Content: "Nhân rộng"}}},
		{Type: "matching", Group: ch2, Content: "Ghép thuật ngữ với ý nghĩa.", Difficulty: 2, Answers: []answerBody{{Content: "B2B", SubContent: strPtr("Doanh nghiệp bán cho doanh nghiệp")}, {Content: "B2C", SubContent: strPtr("Doanh nghiệp bán cho người tiêu dùng")}, {Content: "C2C", SubContent: strPtr("Cá nhân bán cho cá nhân")}}},
		{Type: "cloze", Group: ch2, Content: "Thương mại điện tử viết tắt là {{1}}, mô hình bán hàng giữa doanh nghiệp với người tiêu dùng là {{2}}.", Difficulty: 2, Answers: []answerBody{
			{Content: "TMĐT", SubContent: strPtr("1"), IsCorrect: true}, {Content: "B2C", SubContent: strPtr("2"), IsCorrect: true}, {Content: "B2B", SubContent: strPtr("2")}}},
		{Type: "classification", Group: ch2, Content: "Xếp các mô hình vào đúng nhóm.", Difficulty: 2, Answers: []answerBody{
			{Content: "B2B", SubContent: strPtr("Doanh nghiệp - doanh nghiệp")}, {Content: "B2C", SubContent: strPtr("Doanh nghiệp - người tiêu dùng")},
			{Content: "Sàn thương mại giữa các công ty", SubContent: strPtr("Doanh nghiệp - doanh nghiệp")}}},
		{Type: "essay", Group: ch1, Content: "Trình bày khái niệm kinh tế số và nêu hai ví dụ.", Difficulty: 3, DefaultScore: &two},
	}
}

// ---- handlers ------------------------------------------------------------------

func sendFile(c *gin.Context, name, mime string, data []byte) {
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	c.Data(http.StatusOK, mime, data)
}

func (m *Module) bankQuestions(bankID int64) ([]questionBody, error) {
	rows, err := m.jsonRows("SELECT "+questionJSON+" FROM questions q WHERE q.question_bank_id = ? AND q.parent_question_id IS NULL AND q.type <> 'passage' ORDER BY q.id", bankID)
	if err != nil {
		return nil, err
	}
	out := make([]questionBody, 0, len(rows))
	for _, r := range rows {
		var q questionBody
		if err := json.Unmarshal(r, &q); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, nil
}

// exportBankFile serves the bank as Excel (default), Word or JSON.
func (m *Module) exportBankFile(c *gin.Context) {
	_, bid, ok := m.bankOf(c)
	if !ok {
		return
	}
	qs, err := m.bankQuestions(bid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	switch c.DefaultQuery("format", "xlsx") {
	case "docx":
		data, err := writeDOCX(docxParagraphs(qs))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		sendFile(c, fmt.Sprintf("ngan-hang-cau-hoi-%d.docx", bid), mimeDOCX, data)
	case "json":
		m.exportQuestions(c)
	default:
		data, err := writeXLSX(qs, false)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		sendFile(c, fmt.Sprintf("ngan-hang-cau-hoi-%d.xlsx", bid), mimeXLSX, data)
	}
}

// bankTemplate serves a ready-to-fill sample file (Excel or Word).
func (m *Module) bankTemplate(c *gin.Context) {
	if _, _, ok := m.manageCourse(c); !ok {
		return
	}
	qs := sampleQuestions()
	if c.DefaultQuery("format", "xlsx") == "docx" {
		paras := append([]string{
			"MẪU NHẬP CÂU HỎI",
			"Mỗi câu bắt đầu bằng \"Câu 1:\". Đáp án ghi A., B., C. (có thể dùng danh sách đánh số tự động của Word). Dòng \"Đáp án:\" ghi chữ cái đúng (nhiều đáp án: A,C), hoặc nội dung đáp án với câu trả lời ngắn / số học.",
			"Các dòng tùy chọn dưới mỗi câu: \"Loại:\" (Một đáp án, Nhiều đáp án, Đúng/Sai, Tự luận, Trả lời ngắn, Số học, Sắp xếp, Ghép đôi), \"Nhóm:\" (tên chương/chủ đề, hệ thống tự tạo nhóm), \"Độ khó:\" (1-5), \"Điểm:\", \"Giải thích:\". Sắp xếp: liệt kê các mục theo đúng thứ tự. Ghép đôi: mỗi mục dạng \"vế trái | vế phải\". Xóa các câu ví dụ trước khi nhập câu của bạn.",
			"Cách khác: dùng một bảng, mỗi hàng một câu. Hàng đầu là tiêu đề: Câu hỏi | A | B | C | D | Đáp án (có thể thêm cột Loại, Nhóm, Độ khó, Điểm, Giải thích). Hình ảnh trong đề được đánh dấu [hình ảnh] để bạn chèn lại sau khi nhập.",
			"",
		}, docxParagraphs(qs)...)
		data, err := writeDOCX(paras)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		sendFile(c, "mau-nhap-cau-hoi.docx", mimeDOCX, data)
		return
	}
	data, err := writeXLSX(qs, true)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	sendFile(c, "mau-nhap-cau-hoi.xlsx", mimeXLSX, data)
}

// readUpload returns the parsed questions of a multipart "file" (.xlsx or .docx).
func readUpload(c *gin.Context) ([]parsedQuestion, bool, error) {
	fh, err := c.FormFile("file")
	if err != nil {
		return nil, false, nil
	}
	if fh.Size > 10<<20 {
		return nil, true, fmt.Errorf("Tệp quá lớn (tối đa 10 MB).")
	}
	f, err := fh.Open()
	if err != nil {
		return nil, true, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, true, err
	}
	name := strings.ToLower(fh.Filename)
	switch {
	case strings.HasSuffix(name, ".xlsx"):
		pq, err := parseXLSX(data)
		return pq, true, err
	case strings.HasSuffix(name, ".docx"):
		pq, err := parseDOCX(data)
		return pq, true, err
	}
	return nil, true, fmt.Errorf("Chỉ hỗ trợ tệp Excel (.xlsx) hoặc Word (.docx).")
}

func strPtr(s string) *string { return &s }
