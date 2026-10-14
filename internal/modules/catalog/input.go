package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// The catalog write endpoints validate with Laravel's loose rules ("5" is an
// integer, "1" a boolean, JSON-encoded lists inside multipart forms...), which
// struct binding cannot express. input holds the request data the way
// $request->input() sees it and validator ports the handful of rules used.

// input is $request->all(): body (JSON or form) merged over the query string,
// strings trimmed and "" converted to null (TrimStrings + ConvertEmptyStringsToNull).
type input struct {
	vals  map[string]any // string, json.Number, bool, nil, []any, map[string]any
	files map[string]*multipart.FileHeader
}

// maxMemory is kept in RAM while parsing multipart bodies; the rest spills to disk.
const maxMemory = 32 << 20

// readInput parses the request body. Malformed JSON is treated as an empty
// body (Laravel's json() returns [] and validation then reports the fields).
func readInput(c *gin.Context) (*input, error) {
	in := &input{vals: map[string]any{}, files: map[string]*multipart.FileHeader{}}
	for k, vs := range c.Request.URL.Query() {
		for _, v := range vs {
			setFormValue(in.vals, k, v)
		}
	}
	ct := c.ContentType()
	switch {
	case ct == "multipart/form-data":
		if err := c.Request.ParseMultipartForm(maxMemory); err != nil && err != http.ErrNotMultipart {
			return nil, httpx.NewError(http.StatusBadRequest, "The request contains invalid form values.")
		}
		if f := c.Request.MultipartForm; f != nil {
			for k, vs := range f.Value {
				for _, v := range vs {
					setFormValue(in.vals, k, v)
				}
			}
			for k, fs := range f.File {
				if len(fs) > 0 {
					in.files[k] = fs[len(fs)-1]
				}
			}
		}
	case ct == "application/x-www-form-urlencoded":
		if err := c.Request.ParseForm(); err != nil {
			return nil, httpx.NewError(http.StatusBadRequest, "The request contains invalid form values.")
		}
		for k, vs := range c.Request.PostForm {
			for _, v := range vs {
				setFormValue(in.vals, k, v)
			}
		}
	case c.Request.Body != nil && c.Request.Method != http.MethodGet:
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return nil, err
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		if len(bytes.TrimSpace(body)) > 0 {
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.UseNumber()
			var m map[string]any
			if dec.Decode(&m) == nil {
				for k, v := range m {
					in.vals[k] = v
				}
			}
		}
	}
	for k, v := range in.vals {
		in.vals[k] = normalize(k, v)
	}
	return in, nil
}

// setFormValue stores a form/query field, expanding PHP bracket syntax
// ("a[]", "a[0][id]") into nested values; the last duplicate wins.
func setFormValue(dst map[string]any, key, val string) {
	name, rest, nested := strings.Cut(key, "[")
	if !nested || name == "" {
		dst[key] = val
		return
	}
	var parts []string
	for rest != "" {
		p, r, ok := strings.Cut(rest, "]")
		if !ok {
			dst[key] = val
			return
		}
		parts = append(parts, p)
		rest = strings.TrimPrefix(r, "[")
	}
	dst[name] = setPath(dst[name], parts, val)
}

func setPath(cur any, parts []string, val string) any {
	if len(parts) == 0 {
		return val
	}
	if parts[0] == "" { // "a[]"
		list, _ := cur.([]any)
		return append(list, setPath(nil, parts[1:], val))
	}
	m, ok := cur.(map[string]any)
	if !ok {
		m = map[string]any{}
		if list, isList := cur.([]any); isList {
			for i, v := range list {
				m[strconv.Itoa(i)] = v
			}
		}
	}
	m[parts[0]] = setPath(m[parts[0]], parts[1:], val)
	return m
}

// normalize applies TrimStrings and ConvertEmptyStringsToNull recursively.
func normalize(key string, v any) any {
	switch t := v.(type) {
	case string:
		if key != "password" && key != "password_confirmation" && key != "current_password" {
			t = strings.TrimSpace(t)
		}
		if t == "" {
			return nil
		}
		return t
	case []any:
		for i := range t {
			t[i] = normalize("", t[i])
		}
	case map[string]any:
		for k := range t {
			t[k] = normalize(k, t[k])
		}
	}
	return v
}

// has is $request->has / exists: the key is present (even when null).
func (in *input) has(key string) bool {
	if _, ok := in.files[key]; ok {
		return true
	}
	_, ok := in.vals[key]
	return ok
}

// get returns the value (a *multipart.FileHeader for uploaded files).
func (in *input) get(key string) any {
	if f, ok := in.files[key]; ok {
		return f
	}
	return in.vals[key]
}

// filled is $request->filled: present and not an empty string/null.
func (in *input) filled(key string) bool {
	v := in.get(key)
	if v == nil {
		return false
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	return true
}

// str returns the value as PHP would cast it to string ("" for arrays/null).
func (in *input) str(key string) string { return phpString(in.get(key)) }

func phpString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "1"
		}
		return ""
	}
	return ""
}

// phpBool is $request->boolean(): FILTER_VALIDATE_BOOLEAN ("1", "true", "on", "yes").
func phpBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case json.Number:
		return t.String() == "1"
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "on", "yes":
			return true
		}
	}
	return false
}

// phpInt is intval(): leading integer part, 0 when none.
func phpInt(v any) int64 {
	switch t := v.(type) {
	case bool:
		if t {
			return 1
		}
		return 0
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
		if f, err := t.Float64(); err == nil {
			return int64(f)
		}
	case string:
		s := strings.TrimSpace(t)
		if isNumeric(s) {
			f, _ := strconv.ParseFloat(s, 64)
			return int64(f)
		}
		end := 0
		for end < len(s) && (s[end] >= '0' && s[end] <= '9' || (end == 0 && (s[end] == '-' || s[end] == '+'))) {
			end++
		}
		n, _ := strconv.ParseInt(s[:end], 10, 64)
		return n
	}
	return 0
}

// isInteger is FILTER_VALIDATE_INT (Laravel `integer`).
func isInteger(v any) bool {
	switch t := v.(type) {
	case bool:
		return t // filter_var(true) is 1
	case json.Number:
		_, err := strconv.ParseInt(t.String(), 10, 64)
		return err == nil
	case string:
		s := strings.TrimSpace(t)
		s = strings.TrimPrefix(strings.TrimPrefix(s, "+"), "-")
		if s == "" || strings.Trim(s, "0123456789") != "" || (len(s) > 1 && s[0] == '0') {
			return false
		}
		_, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return err == nil
	}
	return false
}

var numericRe = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

// isNumeric is PHP is_numeric (Laravel `numeric`).
func isNumeric(v any) bool {
	switch t := v.(type) {
	case json.Number:
		return true
	case string:
		return numericRe.MatchString(strings.TrimSpace(t))
	}
	return false
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	}
	return 0
}

// isBoolean is Laravel `boolean`: true, false, 0, 1, "0", "1".
func isBoolean(v any) bool {
	switch t := v.(type) {
	case bool:
		return true
	case json.Number:
		return t.String() == "0" || t.String() == "1"
	case string:
		return t == "0" || t == "1"
	}
	return false
}

func isArray(v any) bool {
	switch v.(type) {
	case []any, map[string]any:
		return true
	}
	return false
}

// arrayItems returns the elements of an array value with their keys, list
// order first, then map keys (numeric keys ascending, like PHP form arrays).
func arrayItems(v any) ([]string, []any) {
	switch t := v.(type) {
	case []any:
		keys := make([]string, len(t))
		for i := range t {
			keys[i] = strconv.Itoa(i)
		}
		return keys, t
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.SliceStable(keys, func(i, j int) bool {
			a, ea := strconv.Atoi(keys[i])
			b, eb := strconv.Atoi(keys[j])
			if ea == nil && eb == nil {
				return a < b
			}
			if (ea == nil) != (eb == nil) {
				return ea == nil
			}
			return keys[i] < keys[j]
		})
		vals := make([]any, len(keys))
		for i, k := range keys {
			vals[i] = t[k]
		}
		return keys, vals
	}
	return nil, nil
}

// isURL approximates Laravel `url` (scheme + host required).
func isURL(v any) bool {
	s, ok := v.(string)
	if !ok || strings.ContainsAny(s, " \t\n") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ftp", "ftps":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// validator

// validator collects Laravel-style messages in rule order.
type validator struct {
	in        *input
	bag       *httpx.ValidationError
	lookupErr error // unexpected error of an exists lookup
}

func newValidator(in *input) *validator {
	return &validator{in: in, bag: httpx.NewValidationError()}
}

// err returns the bag, or nil when everything passed.
func (v *validator) err() error {
	if v.lookupErr != nil {
		return v.lookupErr
	}
	if v.bag.HasErrors() {
		return v.bag
	}
	return nil
}

// field starts the rule chain of a top-level attribute.
func (v *validator) field(name string) *check {
	return &check{v: v, name: name, val: v.in.get(name), present: v.in.has(name)}
}

// value starts a chain for a nested attribute ("sections.0.id").
func (v *validator) value(name string, val any, present bool) *check {
	return &check{v: v, name: name, val: val, present: present}
}

// check is the rule chain of one attribute. Non-implicit rules are skipped
// when the attribute is absent, and every rule is skipped for null values of
// nullable attributes; a failed `required` stops the chain (Laravel semantics).
type check struct {
	v        *validator
	name     string
	val      any
	present  bool
	nullable bool
	numeric  bool // has a numeric/integer rule: size rules compare the number
	stop     bool
}

func (c *check) skip() bool {
	return c.stop || !c.present || (c.nullable && c.val == nil)
}

func (c *check) fail(rule string, repl map[string]string) *check {
	c.v.bag.AddRule(c.name, rule, repl)
	return c
}

// sometimes: only validate when present (already the default for non-required rules).
func (c *check) sometimes() *check {
	if !c.present {
		c.stop = true
	}
	return c
}

func (c *check) nullableRule() *check { c.nullable = true; return c }

func (c *check) required() *check {
	if c.stop {
		return c
	}
	empty := c.val == nil
	if s, ok := c.val.(string); ok && strings.TrimSpace(s) == "" {
		empty = true
	}
	if isArray(c.val) {
		keys, _ := arrayItems(c.val)
		empty = len(keys) == 0
	}
	if !c.present || empty {
		c.fail("required", nil)
		c.stop = true
	}
	return c
}

func (c *check) is(ok func(any) bool, rule string) *check {
	if !c.skip() && !ok(c.val) {
		c.fail(rule, nil)
	}
	return c
}

func (c *check) string() *check {
	return c.is(func(v any) bool { _, ok := v.(string); return ok }, "string")
}

func (c *check) integer() *check { c.numeric = true; return c.is(isInteger, "integer") }
func (c *check) numericRule() *check {
	c.numeric = true
	return c.is(isNumeric, "numeric")
}
func (c *check) boolean() *check { return c.is(isBoolean, "boolean") }
func (c *check) array() *check   { return c.is(isArray, "array") }
func (c *check) url() *check     { return c.is(isURL, "url") }

func (c *check) file() *check {
	return c.is(func(v any) bool { _, ok := v.(*multipart.FileHeader); return ok }, "file")
}

// image is Laravel 13 `image` (jpg, jpeg, png, gif, bmp, webp by content).
func (c *check) image() *check {
	return c.is(func(v any) bool {
		fh, ok := v.(*multipart.FileHeader)
		return ok && imageMimes[sniff(fh)]
	}, "image")
}

func (c *check) in(values ...string) *check {
	return c.is(func(v any) bool {
		if isArray(v) {
			return false
		}
		s := phpString(v)
		for _, w := range values {
			if s == w {
				return true
			}
		}
		return false
	}, "in")
}

// size returns the value's size as Laravel getSize() and the kind used for
// the message variant.
func (c *check) size() (float64, string) {
	switch t := c.val.(type) {
	case *multipart.FileHeader:
		return float64(t.Size) / 1024, "file"
	case []any, map[string]any:
		keys, _ := arrayItems(t)
		return float64(len(keys)), "array"
	}
	if c.numeric && isNumeric(c.val) {
		return toFloat(c.val), "numeric"
	}
	return float64(utf8.RuneCountInString(phpString(c.val))), "string"
}

func (c *check) max(n float64) *check {
	if c.skip() {
		return c
	}
	if sz, kind := c.size(); sz > n {
		c.fail("max."+kind, map[string]string{"max": strconv.FormatFloat(n, 'f', -1, 64)})
	}
	return c
}

func (c *check) min(n float64) *check {
	if c.skip() {
		return c
	}
	if sz, kind := c.size(); sz < n {
		c.fail("min."+kind, map[string]string{"min": strconv.FormatFloat(n, 'f', -1, 64)})
	}
	return c
}

// exists runs `exists:table,id` through lookup (false for non-integer ids,
// which MySQL compared as 0 and never matched).
func (c *check) exists(lookup func(id int64) (bool, error)) *check {
	if c.skip() {
		return c
	}
	ok := false
	if isInteger(c.val) && !isBool(c.val) {
		found, err := lookup(phpInt(c.val))
		if err != nil {
			c.v.lookupErr = err
			return c
		}
		ok = found
	}
	if !ok {
		c.fail("exists", nil)
	}
	return c
}

func isBool(v any) bool { _, ok := v.(bool); return ok }

// fmtErr wraps unexpected lookup errors.
func fmtErr(op string, err error) error { return fmt.Errorf("catalog: %s: %w", op, err) }
