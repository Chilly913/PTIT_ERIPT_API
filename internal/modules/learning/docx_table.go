package learning

import (
	"regexp"
	"strings"
)

// Word exams are often one question per table row: a header row (Câu hỏi | A | B | C | D | Đáp án ...)
// followed by one row per question. questionTable recognises such a table from its header row.

type tableCols struct {
	content, typ, correct, explain, diff, score, group int         // -1 when absent
	opts                                               map[int]int // option index (A=0) -> column
}

var (
	reOptHeader = regexp.MustCompile(`^(?:(?:đáp án|dap an|phương án|phuong an|lựa chọn|lua chon)\s*)?([a-f])$`)
	reNorm      = regexp.MustCompile(`\s+`)
)

func normHeader(s string) string {
	return strings.ToLower(strings.TrimSpace(reNorm.ReplaceAllString(s, " ")))
}

func questionTable(header []string) (tableCols, bool) {
	c := tableCols{content: -1, typ: -1, correct: -1, explain: -1, diff: -1, score: -1, group: -1, opts: map[int]int{}}
	for i, h := range header {
		h = normHeader(h)
		if m := reOptHeader.FindStringSubmatch(h); m != nil {
			c.opts[letterIndex(m[1])] = i
			continue
		}
		switch {
		case strings.Contains(h, "câu hỏi") || strings.Contains(h, "cau hoi") || h == "nội dung" || h == "noi dung" || strings.HasPrefix(h, "nội dung câu"):
			c.content = i
		case h == "đáp án đúng" || h == "đáp án" || h == "dap an" || h == "kết quả" || h == "đáp án đúng nhất":
			c.correct = i
		case h == "loại" || h == "loại câu hỏi" || h == "dạng":
			c.typ = i
		case strings.Contains(h, "giải thích") || strings.Contains(h, "giai thich"):
			c.explain = i
		case strings.Contains(h, "độ khó") || strings.Contains(h, "mức độ"):
			c.diff = i
		case h == "điểm" || h == "diem":
			c.score = i
		case strings.Contains(h, "nhóm") || strings.Contains(h, "chương") || strings.Contains(h, "chủ đề"):
			c.group = i
		}
	}
	return c, c.content >= 0 && (len(c.opts) >= 2 || c.correct >= 0)
}

// tableGrid holds the text of one table: grid[row][col] is the lines of that cell.
type tableGrid map[int]map[int][]string

func gridsOf(paras []docPara) map[int]tableGrid {
	out := map[int]tableGrid{}
	for _, p := range paras {
		if p.tbl < 0 {
			continue
		}
		g := out[p.tbl]
		if g == nil {
			g = tableGrid{}
			out[p.tbl] = g
		}
		if g[p.row] == nil {
			g[p.row] = map[int][]string{}
		}
		g[p.row][p.col] = append(g[p.row][p.col], p.lines...)
	}
	return out
}

func (g tableGrid) cell(r, c int) string {
	if c < 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(g[r][c], "\n"))
}

// questionsFromTable turns every data row of a recognised table into a question.
func questionsFromTable(g tableGrid, c tableCols) []parsedQuestion {
	last := 0
	for r := range g {
		if r > last {
			last = r
		}
	}
	var out []parsedQuestion
	for r := 1; r <= last; r++ {
		content := g.cell(r, c.content)
		if content == "" {
			continue
		}
		opts := make([]string, maxOptions)
		for idx, col := range c.opts {
			if idx < maxOptions {
				opts[idx] = g.cell(r, col)
			}
		}
		correct := g.cell(r, c.correct)
		// the key may repeat an option's text instead of its letter
		if _, ok := splitCorrect(correct); !ok && correct != "" {
			for i, o := range opts {
				if o != "" && strings.EqualFold(strings.TrimSpace(o), correct) {
					correct = string(rune('A' + i))
				}
			}
		}
		diff, derr := parseDifficulty(g.cell(r, c.diff))
		q, qerr := buildQuestion(g.cell(r, c.typ), content, diff, parseScore(g.cell(r, c.score)), opts, correct, g.cell(r, c.explain))
		if gr := g.cell(r, c.group); gr != "" {
			q.Group = &gr
		}
		pq := parsedQuestion{Row: r + 1, Q: q, Err: qerr}
		if derr != "" {
			pq.Err = derr
		}
		out = append(out, pq)
	}
	return out
}
