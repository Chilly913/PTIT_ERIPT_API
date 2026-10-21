package learning

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// buildDocx wraps raw <w:body> XML in a minimal .docx package.
func buildDocx(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"word/document.xml":   `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"><w:body>` + body + `</w:body></w:document>`,
	}
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func para(text string) string {
	return `<w:p><w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
}

func row(cells ...string) string {
	var sb strings.Builder
	sb.WriteString("<w:tr>")
	for _, c := range cells {
		sb.WriteString("<w:tc>" + para(c) + "</w:tc>")
	}
	sb.WriteString("</w:tr>")
	return sb.String()
}

func table(rows ...string) string { return "<w:tbl>" + strings.Join(rows, "") + "</w:tbl>" }

func TestParseDOCXTypedQuestions(t *testing.T) {
	data := buildDocx(t, para("Đề kiểm tra")+
		para("Câu 1: Thủ đô của Việt Nam?")+para("A. Hà Nội")+para("B. Huế")+para("C. Đà Nẵng")+para("Đáp án: A")+para("Nhóm: Địa lý")+
		para("Câu 2: Trình bày khái niệm marketing."))
	qs, err := parseDOCX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 {
		t.Fatalf("want 2 questions, got %d", len(qs))
	}
	if qs[0].Err != "" || qs[0].Q.Type != "single_choice" || qs[0].Q.Group == nil || *qs[0].Q.Group != "Địa lý" {
		t.Fatalf("q1 wrong: %+v", qs[0])
	}
	if qs[1].Err != "" || qs[1].Q.Type != "essay" {
		t.Fatalf("q2 should be an essay without answer key: %+v", qs[1])
	}
}

func TestParseDOCXQuestionPerRowTable(t *testing.T) {
	data := buildDocx(t, para("Đề thi giữa kỳ")+table(
		row("STT", "Câu hỏi", "A", "B", "C", "D", "Đáp án"),
		row("1", "2 + 2 bằng mấy?", "3", "4", "5", "6", "B"),
		row("2", "Số nguyên tố nhỏ nhất?", "1", "2", "3", "4", "2"), // key repeats the option text
		row("3", "Chọn các số chẵn", "2", "3", "4", "5", "A, C"),
	)+table(row("Tiêu chí", "Giá trị"), row("Câu 9: không phải câu hỏi", "x")))
	qs, err := parseDOCX(data)
	if err != nil {
		t.Fatal(err)
	}
	// a plain table is not a question table, so its "Câu 9:" line goes through the line parser (first in the result)
	if len(qs) != 4 || qs[0].Row != 9 {
		t.Fatalf("want 1 line question + 3 table questions, got %d: %+v", len(qs), qs)
	}
	qs = qs[1:]
	for i, want := range []string{"single_choice", "single_choice", "multiple_choice"} {
		if qs[i].Err != "" || qs[i].Q.Type != want {
			t.Fatalf("q%d: %+v", i+1, qs[i])
		}
	}
	correct := func(q questionBody) string {
		var s string
		for _, a := range q.Answers {
			if a.IsCorrect {
				s += a.Content + ","
			}
		}
		return s
	}
	if correct(qs[0].Q) != "4," || correct(qs[1].Q) != "2," || correct(qs[2].Q) != "2,4," {
		t.Fatalf("wrong keys: %q %q %q", correct(qs[0].Q), correct(qs[1].Q), correct(qs[2].Q))
	}
	if qs[0].Row != 2 {
		t.Fatalf("table row numbers should match the Word row (header = 1): %d", qs[0].Row)
	}
}

func TestDocxImageMarker(t *testing.T) {
	data := buildDocx(t, `<w:p><w:r><w:t>Câu 1: Xem hình</w:t></w:r><w:r><w:drawing/></w:r></w:p>`+para("A. x")+para("B. y")+para("Đáp án: A"))
	qs, err := parseDOCX(data)
	if err != nil || len(qs) != 1 {
		t.Fatalf("%v %v", err, qs)
	}
	if !strings.Contains(qs[0].Q.Content, "[hình ảnh]") {
		t.Fatalf("image placeholder missing: %q", qs[0].Q.Content)
	}
}

func TestDocxFormulaText(t *testing.T) {
	data := buildDocx(t, `<w:p><w:r><w:t xml:space="preserve">Câu 1: Tính </w:t></w:r><m:oMath><m:r><m:t>x+1</m:t></m:r></m:oMath></w:p>`+para("A. 1")+para("B. 2")+para("Đáp án: B"))
	qs, err := parseDOCX(data)
	if err != nil || len(qs) != 1 || !strings.Contains(qs[0].Q.Content, "x+1") {
		t.Fatalf("%v %+v", err, qs)
	}
}

func TestSampleRoundTrip(t *testing.T) {
	want := sampleQuestions()
	xl, err := writeXLSX(want, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseXLSX(xl)
	if err != nil || len(got) != len(want) {
		t.Fatalf("xlsx: %v, %d vs %d", err, len(got), len(want))
	}
	for i, g := range got {
		if g.Err != "" {
			t.Fatalf("xlsx row %d: %s", g.Row, g.Err)
		}
		if g.Q.Type != want[i].Type || g.Q.Group == nil || want[i].Group == nil || *g.Q.Group != *want[i].Group {
			t.Fatalf("xlsx q%d: type/group differ: %+v", i+1, g.Q)
		}
	}
	doc, err := writeDOCX(docxParagraphs(want))
	if err != nil {
		t.Fatal(err)
	}
	gotW, err := parseDOCX(doc)
	if err != nil || len(gotW) != len(want) {
		t.Fatalf("docx: %v, %d vs %d", err, len(gotW), len(want))
	}
	for _, g := range gotW {
		if g.Err != "" {
			t.Fatalf("docx q%d: %s", g.Row, g.Err)
		}
	}
}
