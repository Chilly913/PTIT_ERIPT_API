package interaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// input is Laravel's $request->all(): query string, then the JSON or form body
// on top, then uploaded files; strings trimmed (TrimStrings) and "" turned into
// null (ConvertEmptyStringsToNull). JSON numbers are kept as json.Number.
type input struct {
	vals map[string]any
}

var errBadBody = errors.New("interaction: malformed request body")

// readInput parses the request. Multipart bodies must already be size-limited
// by the caller when large uploads are expected.
func readInput(c *gin.Context) (*input, error) {
	in := &input{vals: map[string]any{}}
	for k, v := range c.Request.URL.Query() {
		if len(v) > 0 {
			in.set(k, v[len(v)-1])
		}
	}
	switch c.ContentType() {
	case binding.MIMEMultipartPOSTForm:
		if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
			return nil, err
		}
		for k, v := range c.Request.MultipartForm.Value {
			if len(v) > 0 {
				in.set(k, v[len(v)-1])
			}
		}
		for k, v := range c.Request.MultipartForm.File {
			if len(v) > 0 {
				in.vals[k] = v[len(v)-1]
			}
		}
	case binding.MIMEPOSTForm:
		if err := c.Request.ParseForm(); err != nil {
			return nil, errBadBody
		}
		for k, v := range c.Request.PostForm {
			if len(v) > 0 {
				in.set(k, v[len(v)-1])
			}
		}
	default:
		if c.Request.Body == nil {
			return in, nil
		}
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return in, nil
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			// Laravel ignores a body that is not a JSON object.
			return in, nil //nolint:nilerr // matches Laravel's lenient JSON parsing
		}
		for k, v := range obj {
			in.set(k, v)
		}
	}
	return in, nil
}

func (in *input) set(k string, v any) {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			in.vals[k] = nil
			return
		}
		v = s
	}
	in.vals[k] = v
}

// has is Arr::has on the input (present, even when null).
func (in *input) has(k string) bool { _, ok := in.vals[k]; return ok }

// str returns the value of a validated string field (nil when absent/null).
func (in *input) str(k string) *string {
	if s, ok := in.vals[k].(string); ok {
		return &s
	}
	return nil
}

// int returns the value of a validated integer field.
func (in *input) int(k string) (int64, bool) {
	f, ok := toNumber(in.vals[k])
	if !ok || f != math.Trunc(f) {
		return 0, false
	}
	if n, ok := in.vals[k].(json.Number); ok {
		if v, err := n.Int64(); err == nil {
			return v, true
		}
	}
	if s, ok := in.vals[k].(string); ok {
		if v, err := strconv.ParseInt(strings.TrimPrefix(s, "+"), 10, 64); err == nil {
			return v, true
		}
	}
	return int64(f), true
}

// float returns the value of a validated numeric field.
func (in *input) float(k string) (float64, bool) { return toNumber(in.vals[k]) }

// file returns the uploaded file of a field.
func (in *input) file(k string) *multipart.FileHeader {
	fh, _ := in.vals[k].(*multipart.FileHeader)
	return fh
}

// ---------------------------------------------------------------------------
// Validation: the subset of Laravel rules the ported controllers use.

// rule is one Laravel rule ("max", "255").
type rule struct{ name, param string }

// fieldRules is one entry of a $request->validate([...]) array.
type fieldRules struct {
	field string
	rules []rule
}

// rules parses "required|string|max:255".
func rules(field, spec string) fieldRules {
	fr := fieldRules{field: field}
	for _, r := range strings.Split(spec, "|") {
		name, param, _ := strings.Cut(r, ":")
		fr.rules = append(fr.rules, rule{name: name, param: param})
	}
	return fr
}

func (fr fieldRules) has(name string) bool {
	for _, r := range fr.rules {
		if r.name == name {
			return true
		}
	}
	return false
}

// existsFunc answers an exists:table,column rule.
type existsFunc func(table, column string, value any) (bool, error)

// validate runs the rules like Illuminate\Validation\Validator and returns a
// *httpx.ValidationError (or nil). A failing implicit rule (required) stops
// the attribute; other failures accumulate.
func validate(in *input, exists existsFunc, specs ...fieldRules) error {
	bag := httpx.NewValidationError()
	for _, fr := range specs {
		val, present := in.vals[fr.field]
		if fr.has("sometimes") && !present {
			continue
		}
		for _, r := range fr.rules {
			if r.name == "sometimes" || r.name == "nullable" {
				continue
			}
			if r.name == "required" {
				if !present || isEmpty(val) {
					bag.AddRule(fr.field, "required", nil)
					break
				}
				continue
			}
			// Non-implicit rules only run on present, non-null values
			// (null without "nullable" still runs and fails).
			if !present || (val == nil && fr.has("nullable")) {
				continue
			}
			ok, msgKey, repl, err := checkRule(fr, r, val, exists)
			if err != nil {
				return err
			}
			if !ok {
				bag.AddRule(fr.field, msgKey, repl)
			}
		}
	}
	if bag.HasErrors() {
		return bag
	}
	return nil
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	case *multipart.FileHeader:
		return x == nil
	}
	return false
}

// sizeKind is Laravel's getAttributeType for size messages.
func sizeKind(fr fieldRules, val any) string {
	switch {
	case fr.has("numeric") || fr.has("integer"):
		return "numeric"
	case fr.has("file"):
		return "file"
	}
	if _, ok := val.([]any); ok {
		return "array"
	}
	if _, ok := val.(*multipart.FileHeader); ok {
		return "file"
	}
	return "string"
}

// size is Laravel's getSize.
func size(fr fieldRules, val any) float64 {
	if fr.has("numeric") || fr.has("integer") {
		if f, ok := toNumber(val); ok {
			return f
		}
	}
	switch x := val.(type) {
	case []any:
		return float64(len(x))
	case *multipart.FileHeader:
		return float64(x.Size) / 1024
	}
	return float64(utf8.RuneCountInString(scalarString(val)))
}

func checkRule(fr fieldRules, r rule, val any, exists existsFunc) (bool, string, map[string]string, error) {
	switch r.name {
	case "string":
		_, ok := val.(string)
		return ok, "string", nil, nil
	case "integer":
		return isInteger(val), "integer", nil, nil
	case "numeric":
		_, ok := toNumber(val)
		return ok, "numeric", nil, nil
	case "min", "max":
		limit, _ := strconv.ParseFloat(r.param, 64)
		s := size(fr, val)
		ok := s >= limit
		if r.name == "max" {
			ok = s <= limit
		}
		return ok, r.name + "." + sizeKind(fr, val), map[string]string{r.name: r.param}, nil
	case "in":
		s, scalar := inString(val)
		if scalar {
			for _, p := range strings.Split(r.param, ",") {
				if p == s {
					return true, "", nil, nil
				}
			}
		}
		return false, "in", nil, nil
	case "url":
		s, ok := val.(string)
		return ok && isURL(s), "url", nil, nil
	case "date":
		s, ok := val.(string)
		if !ok {
			return false, "date", nil, nil
		}
		_, err := parseDate(s)
		return err == nil, "date", nil, nil
	case "file":
		_, ok := val.(*multipart.FileHeader)
		return ok, "file", nil, nil
	case "mimes":
		fh, ok := val.(*multipart.FileHeader)
		return ok && hasMime(fh, r.param), "mimes", map[string]string{"values": strings.ReplaceAll(r.param, ",", ", ")}, nil
	case "exists":
		table, column, _ := strings.Cut(r.param, ",")
		if exists == nil {
			return false, "exists", nil, nil
		}
		ok, err := exists(table, column, val)
		return ok, "exists", nil, err
	}
	return true, "", nil, nil
}

// numericRe is PHP is_numeric for strings (leading whitespace allowed,
// trailing whitespace allowed since PHP 8).
var numericRe = regexp.MustCompile(`^\s*[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?\s*$`)

// toNumber is is_numeric + cast.
func toNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		if !numericRe.MatchString(x) {
			return 0, false
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

var intRe = regexp.MustCompile(`^[+-]?(0|[1-9]\d*)$`)

// isInteger is filter_var($v, FILTER_VALIDATE_INT) !== false.
func isInteger(v any) bool {
	switch x := v.(type) {
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return true
		}
		// json_decode turns 5.0 into a float; filter_var accepts integral floats.
		f, err := x.Float64()
		return err == nil && f == math.Trunc(f) && math.Abs(f) < 1<<63
	case string:
		if !intRe.MatchString(x) {
			return false
		}
		_, err := strconv.ParseInt(strings.TrimPrefix(x, "+"), 10, 64)
		return err == nil
	case bool:
		return x // filter_var(true) is 1
	}
	return false
}

func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "1"
		}
		return ""
	}
	return ""
}

// inString is the (string) cast used by the "in" rule; false for arrays/files.
func inString(v any) (string, bool) {
	switch v.(type) {
	case string, json.Number, bool:
		return scalarString(v), true
	}
	return "", false
}

// isURL approximates Laravel's url rule: an absolute URL with a scheme and host.
func isURL(s string) bool {
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	for _, r := range u.Scheme {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

var dateLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05 Z07:00",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02 15:04",
	"2006/01/02",
}

// parseDate accepts the date formats the frontends send for the "date" rule.
// Values without an offset are UTC (Laravel app.timezone is UTC).
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("interaction: invalid date")
}

// hasMime checks an upload's content against a mimes: list. Only "zip" is
// used here: Laravel guesses the extension from the content (finfo), so the
// file must start with a ZIP signature whatever its name.
func hasMime(fh *multipart.FileHeader, list string) bool {
	for _, want := range strings.Split(list, ",") {
		if want != "zip" {
			continue
		}
		f, err := fh.Open()
		if err != nil {
			return false
		}
		head := make([]byte, 4)
		n, _ := io.ReadFull(f, head)
		_ = f.Close()
		if n == 4 && (bytes.Equal(head, []byte("PK\x03\x04")) || bytes.Equal(head, []byte("PK\x05\x06"))) {
			return true
		}
	}
	return false
}

// bindInput reads the input; on a malformed body it renders 400 and returns false.
func bindInput(c *gin.Context) (*input, bool) {
	in, err := readInput(c)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) || strings.Contains(err.Error(), "request body too large") {
			httpx.Abort(c, http.StatusRequestEntityTooLarge, "")
			return nil, false
		}
		httpx.Abort(c, http.StatusBadRequest, "The request body could not be parsed.")
		return nil, false
	}
	return in, true
}
