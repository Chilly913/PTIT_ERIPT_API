package learning

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Reads the visible text of a .docx as one string per paragraph, including the labels Word generates itself
// for numbered lists ("A.", "Câu 1:", ...): those labels are not in the text runs, only in numbering.xml.

type numLevel struct {
	Fmt   string
	Text  string
	Start int
}

type numbering struct {
	abstract map[string]map[int]numLevel // abstractNumId -> ilvl -> level
	nums     map[string]string           // numId -> abstractNumId
	counters map[string][]int            // numId -> counter per level
}

func readZipFile(zr *zip.Reader, name string) []byte {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		b, _ := io.ReadAll(io.LimitReader(rc, 20<<20))
		return b
	}
	return nil
}

func attrVal(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func parseNumbering(data []byte) *numbering {
	n := &numbering{abstract: map[string]map[int]numLevel{}, nums: map[string]string{}, counters: map[string][]int{}}
	if len(data) == 0 {
		return n
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	var absID, numID string
	var lvl = -1
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "abstractNum":
				absID = attrVal(t, "abstractNumId")
				n.abstract[absID] = map[int]numLevel{}
			case "lvl":
				lvl, _ = strconv.Atoi(attrVal(t, "ilvl"))
				if absID != "" {
					n.abstract[absID][lvl] = numLevel{Start: 1}
				}
			case "start":
				if absID != "" && lvl >= 0 {
					l := n.abstract[absID][lvl]
					l.Start, _ = strconv.Atoi(attrVal(t, "val"))
					n.abstract[absID][lvl] = l
				}
			case "numFmt":
				if absID != "" && lvl >= 0 {
					l := n.abstract[absID][lvl]
					l.Fmt = attrVal(t, "val")
					n.abstract[absID][lvl] = l
				}
			case "lvlText":
				if absID != "" && lvl >= 0 {
					l := n.abstract[absID][lvl]
					l.Text = attrVal(t, "val")
					n.abstract[absID][lvl] = l
				}
			case "num":
				numID = attrVal(t, "numId")
			case "abstractNumId":
				if numID != "" {
					n.nums[numID] = attrVal(t, "val")
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "abstractNum":
				absID, lvl = "", -1
			case "num":
				numID = ""
			}
		}
	}
	return n
}

func formatCounter(format string, v int) string {
	switch format {
	case "upperLetter", "lowerLetter":
		if v < 1 {
			v = 1
		}
		s := ""
		for x := v; x > 0; x = (x - 1) / 26 {
			s = string(rune('a'+(x-1)%26)) + s
		}
		if format == "upperLetter" {
			return strings.ToUpper(s)
		}
		return s
	case "upperRoman", "lowerRoman":
		vals := []int{1000, 900, 500, 400, 100, 90, 50, 40, 10, 9, 5, 4, 1}
		syms := []string{"M", "CM", "D", "CD", "C", "XC", "L", "XL", "X", "IX", "V", "IV", "I"}
		s := ""
		for i, x := range vals {
			for v >= x {
				s += syms[i]
				v -= x
			}
		}
		if format == "lowerRoman" {
			return strings.ToLower(s)
		}
		return s
	}
	return strconv.Itoa(v)
}

// label returns the generated label of the next item of list numID at level ilvl ("" for bullets / no numbering).
func (n *numbering) label(numID string, ilvl int) string {
	abs := n.abstract[n.nums[numID]]
	lv, ok := abs[ilvl]
	if !ok || lv.Fmt == "bullet" || lv.Fmt == "none" || lv.Text == "" {
		return ""
	}
	c := n.counters[numID]
	for len(c) <= ilvl {
		c = append(c, 0)
	}
	if c[ilvl] == 0 {
		c[ilvl] = lv.Start - 1
	}
	c[ilvl]++
	for i := ilvl + 1; i < len(c); i++ {
		c[i] = 0 // a new item at this level restarts the deeper ones
	}
	n.counters[numID] = c
	out := lv.Text
	for i := 0; i <= ilvl && i < 9; i++ {
		v := 0
		if i < len(c) {
			v = c[i]
		}
		f := abs[i].Fmt
		if i == ilvl {
			f = lv.Fmt
		}
		out = strings.ReplaceAll(out, "%"+strconv.Itoa(i+1), formatCounter(f, v))
	}
	return strings.TrimSpace(out)
}

// styleNumbering maps style ids to the list they apply (python-docx "List Number" and Word list styles).
func styleNumbering(data []byte) map[string][2]string {
	out := map[string][2]string{}
	if len(data) == 0 {
		return out
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	var style, numID, ilvl string
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "style":
				style, numID, ilvl = attrVal(t, "styleId"), "", "0"
			case "numId":
				if style != "" {
					numID = attrVal(t, "val")
				}
			case "ilvl":
				if style != "" {
					ilvl = attrVal(t, "val")
				}
			}
		case xml.EndElement:
			if t.Name.Local == "style" {
				if style != "" && numID != "" && numID != "0" {
					out[style] = [2]string{numID, ilvl}
				}
				style = ""
			}
		}
	}
	return out
}

// docPara is one paragraph of the document split into its lines. tbl is the index of the outermost
// table containing it (-1 outside tables); row and col locate the cell.
type docPara struct {
	lines         []string
	tbl, row, col int
}

// docxLines returns every line of the document in reading order (table cells included).
func docxLines(data []byte) ([]string, error) {
	paras, err := docxParas(data)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, p := range paras {
		lines = append(lines, p.lines...)
	}
	return lines, nil
}

func docxParas(data []byte) ([]docPara, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("Không đọc được tệp Word. Hãy dùng tệp .docx.")
	}
	doc := readZipFile(zr, "word/document.xml")
	if doc == nil {
		return nil, fmt.Errorf("Tệp Word không hợp lệ.")
	}
	num := parseNumbering(readZipFile(zr, "word/numbering.xml"))
	styles := styleNumbering(readZipFile(zr, "word/styles.xml"))

	d := xml.NewDecoder(bytes.NewReader(doc))
	var paras []docPara
	var sb strings.Builder
	var inP, inT, inPPr bool
	depth, tbl, row, col := 0, -1, -1, -1
	var numID, ilvlStr, styleID string
	flush := func() {
		text := strings.TrimSpace(strings.ReplaceAll(sb.String(), " ", " "))
		sb.Reset()
		id, lv := numID, 0
		lv, _ = strconv.Atoi(ilvlStr)
		if id == "" && styleID != "" {
			if s, ok := styles[styleID]; ok {
				id = s[0]
				lv, _ = strconv.Atoi(s[1])
			}
		}
		if id != "" && id != "0" {
			if lab := num.label(id, lv); lab != "" && text != "" {
				text = lab + " " + text
			}
		}
		// A soft line break (Shift+Enter) inside a paragraph is a line of its own for the question parser.
		dp := docPara{tbl: -1, row: -1, col: -1}
		if depth > 0 {
			dp.tbl, dp.row, dp.col = tbl, row, col
		}
		for _, part := range strings.Split(text, "\n") {
			if part = strings.TrimSpace(part); part != "" {
				dp.lines = append(dp.lines, part)
			}
		}
		if len(dp.lines) > 0 {
			paras = append(paras, dp)
		}
		numID, ilvlStr, styleID = "", "", ""
	}
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tbl":
				if depth == 0 {
					tbl++
					row, col = -1, -1
				}
				depth++
			case "tr":
				if depth == 1 {
					row++
					col = -1
				}
			case "tc":
				if depth == 1 {
					col++
				}
			case "drawing", "pict":
				if inP {
					sb.WriteString(" [hình ảnh] ")
				}
			case "p":
				inP = true
				sb.Reset()
				numID, ilvlStr, styleID = "", "", ""
			case "pPr":
				inPPr = inP
			case "pStyle":
				if inPPr {
					styleID = attrVal(t, "val")
				}
			case "numId":
				if inPPr {
					numID = attrVal(t, "val")
				}
			case "ilvl":
				if inPPr {
					ilvlStr = attrVal(t, "val")
				}
			case "t":
				inT = inP
			case "tab":
				if inP && !inPPr {
					sb.WriteString(" ")
				}
			case "br", "cr":
				if inP {
					sb.WriteString("\n")
				}
			}
		case xml.CharData:
			if inT {
				sb.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "tbl":
				depth--
			case "t":
				inT = false
			case "pPr":
				inPPr = false
			case "p":
				if inP {
					flush()
				}
				inP = false
			}
		}
	}
	return paras, nil
}
