package study

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// input is the request payload after Laravel's TrimStrings and
// ConvertEmptyStringsToNull middleware: present keys map to a value, empty
// strings to nil. JSON numbers are json.Number.
//
// The legacy validators accept loosely typed values ("5" for an integer,
// 0/1 for a boolean, form fields), so the endpoints validate this map instead
// of binding into typed structs.
type input struct {
	vals  map[string]any
	files map[string]*multipart.FileHeader
}

// readInput decodes JSON, urlencoded or multipart bodies. It writes a 400 and
// returns false on malformed bodies.
func readInput(c *gin.Context) (input, bool) {
	in := input{vals: map[string]any{}, files: map[string]*multipart.FileHeader{}}
	switch c.ContentType() {
	case binding.MIMEMultipartPOSTForm:
		if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
			httpx.Abort(c, http.StatusBadRequest, "The request contains invalid form values.")
			return in, false
		}
		if mf := c.Request.MultipartForm; mf != nil {
			for k, v := range mf.Value {
				if len(v) > 0 {
					in.vals[k] = normalize(v[0])
				}
			}
			for k, v := range mf.File {
				if len(v) > 0 {
					in.files[k] = v[0]
				}
			}
		}
	case binding.MIMEPOSTForm:
		if err := c.Request.ParseForm(); err != nil {
			httpx.Abort(c, http.StatusBadRequest, "The request contains invalid form values.")
			return in, false
		}
		for k, v := range c.Request.PostForm {
			if len(v) > 0 {
				in.vals[k] = normalize(v[0])
			}
		}
	default:
		if c.Request.Body == nil {
			return in, true
		}
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			httpx.Fail(c, err)
			return in, false
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return in, true
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		var raw any
		if err := dec.Decode(&raw); err != nil {
			httpx.Abort(c, http.StatusBadRequest, "The request body is not valid JSON.")
			return in, false
		}
		if obj, ok := raw.(map[string]any); ok {
			for k, v := range obj {
				in.vals[k] = normalize(v)
			}
		}
	}
	return in, true
}

// normalize applies TrimStrings + ConvertEmptyStringsToNull.
func normalize(v any) any {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		return s
	}
	return v
}

// has reports whether key was sent (even as null).
func (in input) has(key string) bool { _, ok := in.vals[key]; return ok }

// get returns the value (nil when absent or null).
func (in input) get(key string) any { return in.vals[key] }

// toInt is Laravel's `integer` rule (FILTER_VALIDATE_INT, which also accepts
// numeric strings and integral floats).
func toInt(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return int64(f), true
		}
	case string:
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return n, true
		}
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x), true
		}
	case int64:
		return x, true
	case int:
		return int64(x), true
	}
	return 0, false
}

// toBool is Laravel's `boolean` rule: true, false, 0, 1, "0", "1".
func toBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case json.Number:
		switch x.String() {
		case "0":
			return false, true
		case "1":
			return true, true
		}
	case string:
		switch x {
		case "0":
			return false, true
		case "1":
			return true, true
		}
	}
	return false, false
}

// toNumber is Laravel's `numeric` rule.
func toNumber(v any) (float64, bool) {
	var s string
	switch x := v.(type) {
	case json.Number:
		s = x.String()
	case string:
		s = x
	case float64:
		return x, true
	default:
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

// rules collects field errors in Laravel's order.
type rules struct {
	in  input
	bag *httpx.ValidationError
}

func newRules(in input) *rules { return &rules{in: in, bag: httpx.NewValidationError()} }

func (r *rules) fail(field, rule string, rep map[string]string) { r.bag.AddRule(field, rule, rep) }

// intField validates `[required|nullable|sometimes], integer, min, max`.
// Returns nil when the value is absent/null or invalid.
func (r *rules) intField(field string, required bool, minV, maxV *int64) *int64 {
	v := r.in.get(field)
	if v == nil {
		if required {
			r.fail(field, "required", nil)
		}
		return nil
	}
	n, ok := toInt(v)
	if !ok {
		r.fail(field, "integer", nil)
		return nil
	}
	bad := false
	if minV != nil && n < *minV {
		r.fail(field, "min.numeric", map[string]string{"min": strconv.FormatInt(*minV, 10)})
		bad = true
	}
	if maxV != nil && n > *maxV {
		r.fail(field, "max.numeric", map[string]string{"max": strconv.FormatInt(*maxV, 10)})
		bad = true
	}
	if bad {
		return nil
	}
	return &n
}

// boolField validates `boolean`; nullable=false reports a present null
// (the `sometimes|boolean` case).
func (r *rules) boolField(field string, nullable bool) *bool {
	if !r.in.has(field) {
		return nil
	}
	v := r.in.get(field)
	if v == nil && nullable {
		return nil
	}
	b, ok := toBool(v)
	if !ok {
		r.fail(field, "boolean", nil)
		return nil
	}
	return &b
}

// stringField validates `[required|nullable|sometimes], string, max`.
// sometimes=true reports a present null as "must be a string".
func (r *rules) stringField(field string, required, sometimes bool, maxLen int) *string {
	v := r.in.get(field)
	if v == nil {
		switch {
		case required:
			r.fail(field, "required", nil)
		case sometimes && r.in.has(field):
			r.fail(field, "string", nil)
		}
		return nil
	}
	s, ok := v.(string)
	if !ok {
		r.fail(field, "string", nil)
		return nil
	}
	if maxLen > 0 && utf8.RuneCountInString(s) > maxLen {
		r.fail(field, "max.string", map[string]string{"max": strconv.Itoa(maxLen)})
		return nil
	}
	return &s
}

// numberField validates `[required|nullable], numeric, min, max`.
func (r *rules) numberField(field string, required bool, minV, maxV float64) *float64 {
	v := r.in.get(field)
	if v == nil {
		if required {
			r.fail(field, "required", nil)
		}
		return nil
	}
	f, ok := toNumber(v)
	if !ok {
		r.fail(field, "numeric", nil)
		return nil
	}
	bad := false
	if f < minV {
		r.fail(field, "min.numeric", map[string]string{"min": strconv.FormatFloat(minV, 'f', -1, 64)})
		bad = true
	}
	if f > maxV {
		r.fail(field, "max.numeric", map[string]string{"max": strconv.FormatFloat(maxV, 'f', -1, 64)})
		bad = true
	}
	if bad {
		return nil
	}
	return &f
}

// afterOrEqual adds the `after_or_equal:<other>` message (the other field is
// shown by its display name, as Laravel does for non-date parameters).
func (r *rules) afterOrEqual(field, other string) {
	r.bag.Add(field, "The "+httpx.DisplayAttribute(field)+" field must be a date after or equal to "+
		httpx.DisplayAttribute(other)+".")
}

// dateField validates a nullable `date`.
func (r *rules) dateField(field string) (*time.Time, bool) {
	v := r.in.get(field)
	if v == nil {
		return nil, true
	}
	s, ok := v.(string)
	if !ok {
		r.fail(field, "date", nil)
		return nil, false
	}
	t, err := parseDate(s)
	if err != nil {
		r.fail(field, "date", nil)
		return nil, false
	}
	return &t, true
}

// parseDate accepts the layouts httpx.Time accepts (ISO-8601, "Y-m-d H:i[:s]", "Y-m-d").
func parseDate(s string) (time.Time, error) {
	var t httpx.Time
	js, _ := json.Marshal(s)
	if err := t.UnmarshalJSON(js); err != nil || t.IsZero() {
		return time.Time{}, errors.New("invalid date")
	}
	return t.Time, nil
}

// fileField validates `required|file|max:<kb>`.
func (r *rules) fileField(field string, maxKB int64) *multipart.FileHeader {
	fh := r.in.files[field]
	if fh == nil {
		if r.in.get(field) != nil {
			r.fail(field, "file", nil)
		} else {
			r.fail(field, "required", nil)
		}
		return nil
	}
	if float64(fh.Size)/1024 > float64(maxKB) {
		r.fail(field, "max.file", map[string]string{"max": fmt.Sprint(maxKB)})
		return nil
	}
	return fh
}

// ok renders the 422 when errors were collected.
func (r *rules) ok(c *gin.Context) bool {
	if r.bag.HasErrors() {
		httpx.Fail(c, r.bag)
		return false
	}
	return true
}

func i64(v int64) *int64 { return &v }
