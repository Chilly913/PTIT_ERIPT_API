package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// ValidationError is Laravel's ValidationException rendered as
// 422 {"message": "...", "errors": {"field": ["msg", ...]}}.
// Field order is insertion order (struct field order), like Laravel's rule order.
type ValidationError struct {
	fields []string
	errors map[string][]string
}

// NewValidationError returns an empty error bag.
func NewValidationError() *ValidationError {
	return &ValidationError{errors: map[string][]string{}}
}

// Add appends a message for field and returns the bag (chainable).
func (v *ValidationError) Add(field, message string) *ValidationError {
	if _, ok := v.errors[field]; !ok {
		v.fields = append(v.fields, field)
	}
	v.errors[field] = append(v.errors[field], message)
	return v
}

// AddRule appends the Laravel message for a rule, e.g. AddRule("email", "unique").
// Supported rule names are the Laravel ones (see laravelMessages).
func (v *ValidationError) AddRule(field, rule string, replace map[string]string) *ValidationError {
	tpl, ok := laravelMessages[rule]
	if !ok {
		tpl = laravelMessages["invalid"]
	}
	return v.Add(field, render(tpl, field, replace))
}

// HasErrors reports whether any message was added.
func (v *ValidationError) HasErrors() bool { return len(v.fields) > 0 }

// Errors returns field -> messages.
func (v *ValidationError) Errors() map[string][]string { return v.errors }

// Fields returns the field names in insertion order.
func (v *ValidationError) Fields() []string { return v.fields }

// Message is Laravel's ValidationException::summarize().
func (v *ValidationError) Message() string {
	total := 0
	first := ""
	for _, f := range v.fields {
		for _, m := range v.errors[f] {
			if total == 0 {
				first = m
			}
			total++
		}
	}
	if total == 0 {
		return MsgInvalidData
	}
	if more := total - 1; more > 0 {
		noun := "errors"
		if more == 1 {
			noun = "error"
		}
		return fmt.Sprintf("%s (and %d more %s)", first, more, noun)
	}
	return first
}

func (v *ValidationError) Error() string { return v.Message() }

// MarshalJSON keeps the errors object in insertion order.
func (v *ValidationError) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	msg, _ := json.Marshal(v.Message())
	buf.WriteString(`{"message":`)
	buf.Write(msg)
	buf.WriteString(`,"errors":{`)
	for i, f := range v.fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(f)
		vals, err := json.Marshal(v.errors[f])
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(vals)
	}
	buf.WriteString(`}}`)
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------------------
// Binding

// validate is gin's validator engine, configured once to report json/form
// tag names instead of Go field names.
var validate = func() *validator.Validate {
	v, ok := binding.Validator.Engine().(*validator.Validate)
	if !ok {
		panic("httpx: unexpected gin validator engine")
	}
	v.RegisterTagNameFunc(tagName)
	return v
}()

// tagName returns the json name, else the form name, else "" (Go name).
func tagName(f reflect.StructField) string {
	j := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
	if j != "" && j != "-" {
		return j
	}
	if fm := strings.SplitN(f.Tag.Get("form"), ",", 2)[0]; fm != "" && fm != "-" {
		return fm
	}
	return j // "" or "-"
}

// BindJSON decodes the JSON body into dst (an empty body is treated as {}),
// trims strings like Laravel's TrimStrings middleware, validates `binding`
// tags and renders 422 on failure. It returns false when a response was written.
//
//	var req LoginRequest
//	if !httpx.BindJSON(c, &req) { return }
func BindJSON(c *gin.Context, dst any) bool {
	return finishBind(c, dst, decodeJSON(c.Request, dst))
}

// BindQuery binds the query string (`form` tags) and validates.
func BindQuery(c *gin.Context, dst any) bool {
	if err := binding.MapFormWithTag(dst, c.Request.URL.Query(), "form"); err != nil {
		Abort(c, http.StatusBadRequest, "The query string contains invalid values.")
		return false
	}
	return finishBind(c, dst, nil)
}

// Bind picks the decoder from Content-Type (JSON, urlencoded or multipart
// form; `form` tags for forms, *multipart.FileHeader for files) and validates.
func Bind(c *gin.Context, dst any) bool {
	var b binding.Binding
	switch {
	case c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead:
		return BindQuery(c, dst)
	case c.ContentType() == binding.MIMEMultipartPOSTForm:
		b = binding.FormMultipart
	case c.ContentType() == binding.MIMEPOSTForm:
		b = binding.Form
	default:
		return BindJSON(c, dst)
	}
	// gin also validates here; validation errors are recomputed after trimming.
	if err := c.ShouldBindWith(dst, b); err != nil {
		var verrs validator.ValidationErrors
		if !errors.As(err, &verrs) {
			Abort(c, http.StatusBadRequest, "The request contains invalid form values.")
			return false
		}
	}
	return finishBind(c, dst, nil)
}

// Validate runs `binding` tag validation on an already-populated struct and
// returns a *ValidationError (or nil).
func Validate(dst any) error {
	trimStrings(reflect.ValueOf(dst), "")
	err := validate.Struct(dst)
	if err == nil {
		return nil
	}
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return err
	}
	bag := NewValidationError()
	addFieldErrors(bag, reflect.TypeOf(dst), verrs, nil)
	return bag
}

type typeErrors struct{ bag *ValidationError }

func finishBind(c *gin.Context, dst any, decodeErr error) bool {
	pre := NewValidationError()
	if decodeErr != nil {
		var te *typeErrors
		if !errors.As(decodeErr, &te) {
			Abort(c, http.StatusBadRequest, "The request body is not valid JSON.")
			return false
		}
		pre = te.bag
	}
	trimStrings(reflect.ValueOf(dst), "")
	err := validate.Struct(dst)
	var verrs validator.ValidationErrors
	if err != nil && !errors.As(err, &verrs) {
		Fail(c, err)
		return false
	}
	if len(verrs) == 0 && !pre.HasErrors() {
		return true
	}
	bag := NewValidationError()
	addFieldErrors(bag, reflect.TypeOf(dst), verrs, pre)
	c.AbortWithStatusJSON(http.StatusUnprocessableEntity, bag)
	return false
}

func (t *typeErrors) Error() string { return t.bag.Message() }

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, dst); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) && ute.Field != "" {
			// encoding/json keeps decoding the remaining fields; report the
			// wrong type as a field error and still validate the rest.
			bag := NewValidationError()
			bag.Add(ute.Field, typeMessage(ute.Field, ute.Type))
			return &typeErrors{bag: bag}
		}
		return err
	}
	return nil
}

func typeMessage(field string, t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	rule := "string"
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		rule = "integer"
	case reflect.Float32, reflect.Float64:
		rule = "numeric"
	case reflect.Bool:
		rule = "boolean"
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		rule = "array"
	}
	return render(laravelMessages[rule], field, nil)
}

// passwordFields are skipped by Laravel's TrimStrings middleware.
var passwordFields = map[string]bool{"password": true, "password_confirmation": true, "current_password": true}

// trimStrings trims whitespace in every exported string field reachable from v.
func trimStrings(v reflect.Value, name string) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			trimStrings(v.Elem(), name)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			trimStrings(v.Field(i), tagName(f))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			trimStrings(v.Index(i), name)
		}
	case reflect.String:
		if v.CanSet() && !passwordFields[name] {
			v.SetString(strings.TrimSpace(v.String()))
		}
	}
}

// ---------------------------------------------------------------------------
// Message translation (Laravel lang/en/validation.php)

var laravelMessages = map[string]string{
	"accepted":             "The :attribute field must be accepted.",
	"alpha":                "The :attribute field must only contain letters.",
	"alpha_num":            "The :attribute field must only contain letters and numbers.",
	"array":                "The :attribute field must be an array.",
	"boolean":              "The :attribute field must be true or false.",
	"confirmed":            "The :attribute field confirmation does not match.",
	"date":                 "The :attribute field must be a valid date.",
	"date_format":          "The :attribute field must match the format :format.",
	"different":            "The :attribute field and :other must be different.",
	"digits":               "The :attribute field must be :digits digits.",
	"distinct":             "The :attribute field has a duplicate value.",
	"email":                "The :attribute field must be a valid email address.",
	"ends_with":            "The :attribute field must end with one of the following: :values.",
	"exists":               "The selected :attribute is invalid.",
	"file":                 "The :attribute field must be a file.",
	"image":                "The :attribute field must be an image.",
	"in":                   "The selected :attribute is invalid.",
	"integer":              "The :attribute field must be an integer.",
	"ip":                   "The :attribute field must be a valid IP address.",
	"ipv4":                 "The :attribute field must be a valid IPv4 address.",
	"ipv6":                 "The :attribute field must be a valid IPv6 address.",
	"json":                 "The :attribute field must be a valid JSON string.",
	"lowercase":            "The :attribute field must be lowercase.",
	"mimes":                "The :attribute field must be a file of type: :values.",
	"not_in":               "The selected :attribute is invalid.",
	"numeric":              "The :attribute field must be a number.",
	"prohibited":           "The :attribute field is prohibited.",
	"regex":                "The :attribute field format is invalid.",
	"required":             "The :attribute field is required.",
	"required_if":          "The :attribute field is required when :other is :value.",
	"required_unless":      "The :attribute field is required unless :other is in :values.",
	"required_with":        "The :attribute field is required when :values is present.",
	"required_with_all":    "The :attribute field is required when :values are present.",
	"required_without":     "The :attribute field is required when :values is not present.",
	"required_without_all": "The :attribute field is required when none of :values are present.",
	"same":                 "The :attribute field must match :other.",
	"starts_with":          "The :attribute field must start with one of the following: :values.",
	"string":               "The :attribute field must be a string.",
	"unique":               "The :attribute has already been taken.",
	"uppercase":            "The :attribute field must be uppercase.",
	"url":                  "The :attribute field must be a valid URL.",
	"uuid":                 "The :attribute field must be a valid UUID.",
	"invalid":              "The :attribute field is invalid.",

	"min.numeric":  "The :attribute field must be at least :min.",
	"min.string":   "The :attribute field must be at least :min characters.",
	"min.array":    "The :attribute field must have at least :min items.",
	"min.file":     "The :attribute field must be at least :min kilobytes.",
	"max.numeric":  "The :attribute field must not be greater than :max.",
	"max.string":   "The :attribute field must not be greater than :max characters.",
	"max.array":    "The :attribute field must not have more than :max items.",
	"max.file":     "The :attribute field must not be greater than :max kilobytes.",
	"size.numeric": "The :attribute field must be :size.",
	"size.string":  "The :attribute field must be :size characters.",
	"size.array":   "The :attribute field must contain :size items.",
	"size.file":    "The :attribute field must be :size kilobytes.",
	"gt.numeric":   "The :attribute field must be greater than :value.",
	"gt.string":    "The :attribute field must be greater than :value characters.",
	"gt.array":     "The :attribute field must have more than :value items.",
	"gte.numeric":  "The :attribute field must be greater than or equal to :value.",
	"gte.string":   "The :attribute field must be greater than or equal to :value characters.",
	"gte.array":    "The :attribute field must have :value items or more.",
	"lt.numeric":   "The :attribute field must be less than :value.",
	"lt.string":    "The :attribute field must be less than :value characters.",
	"lt.array":     "The :attribute field must have less than :value items.",
	"lte.numeric":  "The :attribute field must be less than or equal to :value.",
	"lte.string":   "The :attribute field must be less than or equal to :value characters.",
	"lte.array":    "The :attribute field must not have more than :value items.",
}

// DisplayAttribute mirrors Laravel's getDisplayableAttribute for attributes
// without custom names: nested keys stay raw ("items.0.name"), otherwise
// underscores become spaces ("first_name" -> "first name").
func DisplayAttribute(field string) string {
	if strings.Contains(field, ".") {
		return field
	}
	return strings.ReplaceAll(field, "_", " ")
}

func render(tpl, field string, replace map[string]string) string {
	out := strings.ReplaceAll(tpl, ":attribute", DisplayAttribute(field))
	for k, v := range replace {
		out = strings.ReplaceAll(out, ":"+k, v)
	}
	return out
}

func addFieldErrors(bag *ValidationError, root reflect.Type, verrs validator.ValidationErrors, pre *ValidationError) {
	if pre != nil {
		for _, f := range pre.fields {
			for _, m := range pre.errors[f] {
				bag.Add(f, m)
			}
		}
	}
	for _, fe := range verrs {
		field := fieldPath(fe.Namespace())
		if pre != nil && len(pre.errors[field]) > 0 {
			continue // already reported a type error for this field
		}
		target, msg := translate(root, fe, field)
		bag.Add(target, msg)
	}
}

// fieldPath turns "Req.items[0].name" into Laravel's "items.0.name".
func fieldPath(ns string) string {
	if i := strings.IndexByte(ns, '.'); i >= 0 {
		ns = ns[i+1:]
	}
	ns = strings.ReplaceAll(ns, "[", ".")
	return strings.ReplaceAll(ns, "]", "")
}

// sizeKind classifies a field like Laravel's getSize(): numeric, file, array or string.
func sizeKind(fe validator.FieldError) string {
	t := fe.Type()
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(multipart.FileHeader{}) {
		return "file"
	}
	switch fe.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "numeric"
	case reflect.Slice, reflect.Array, reflect.Map:
		return "array"
	}
	return "string"
}

// translate returns the (possibly re-targeted) field and the Laravel message.
func translate(root reflect.Type, fe validator.FieldError, field string) (string, string) {
	param := fe.Param()
	sibling := func(goName string) string {
		prefix := ""
		if i := strings.LastIndexByte(field, '.'); i >= 0 {
			prefix = field[:i+1]
		}
		return prefix + siblingName(root, fe.StructNamespace(), goName)
	}
	siblings := func(goNames []string) string {
		out := make([]string, len(goNames))
		for i, n := range goNames {
			out[i] = DisplayAttribute(sibling(n))
		}
		return strings.Join(out, " / ")
	}

	switch tag := fe.Tag(); tag {
	case "required", "email", "numeric", "boolean", "uuid", "uuid4", "uuid_rfc4122", "uuid4_rfc4122",
		"url", "http_url", "alpha", "alphanum", "lowercase", "uppercase", "ip", "ipv4", "ipv6", "json", "unique", "image":
		rule := map[string]string{
			"uuid4": "uuid", "uuid_rfc4122": "uuid", "uuid4_rfc4122": "uuid", "http_url": "url",
			"alphanum": "alpha_num", "unique": "distinct",
		}[tag]
		if rule == "" {
			rule = tag
		}
		return field, render(laravelMessages[rule], field, nil)
	case "number":
		return field, render(laravelMessages["numeric"], field, nil)
	case "min", "max", "len", "gt", "gte", "lt", "lte":
		rule := tag
		key := map[string]string{"min": "min", "max": "max", "len": "size", "gt": "value", "gte": "value", "lt": "value", "lte": "value"}[tag]
		if tag == "len" {
			rule = "size"
		}
		return field, render(laravelMessages[rule+"."+sizeKind(fe)], field, map[string]string{key: param})
	case "oneof":
		return field, render(laravelMessages["in"], field, nil)
	case "datetime":
		return field, render(laravelMessages["date_format"], field, map[string]string{"format": param})
	case "startswith":
		return field, render(laravelMessages["starts_with"], field, map[string]string{"values": param})
	case "endswith":
		return field, render(laravelMessages["ends_with"], field, map[string]string{"values": param})
	case "eqfield":
		other := sibling(param)
		// Laravel "confirmed": password_confirmation must equal password; the
		// error is reported on the confirmed field ("password").
		if field == other+"_confirmation" {
			return other, render(laravelMessages["confirmed"], other, nil)
		}
		return field, render(laravelMessages["same"], field, map[string]string{"other": DisplayAttribute(other)})
	case "nefield":
		return field, render(laravelMessages["different"], field, map[string]string{"other": DisplayAttribute(sibling(param))})
	case "required_if":
		parts := strings.Fields(param)
		other, value := "", ""
		if len(parts) >= 2 {
			other, value = DisplayAttribute(sibling(parts[0])), parts[1]
		}
		return field, render(laravelMessages["required_if"], field, map[string]string{"other": other, "value": value})
	case "required_unless":
		parts := strings.Fields(param)
		other, values := "", ""
		if len(parts) >= 2 {
			other, values = DisplayAttribute(sibling(parts[0])), strings.Join(parts[1:], ", ")
		}
		return field, render(laravelMessages["required_unless"], field, map[string]string{"other": other, "values": values})
	case "required_with", "required_with_all", "required_without", "required_without_all":
		return field, render(laravelMessages[tag], field, map[string]string{"values": siblings(strings.Fields(param))})
	case "excluded_with", "excluded_without", "excluded_if", "excluded_unless":
		return field, render(laravelMessages["prohibited"], field, nil)
	}
	return field, render(laravelMessages["invalid"], field, nil)
}

// siblingName resolves the json/form name of Go field goName that lives in the
// same struct as the field at structNS ("Req.Items[0].Name").
func siblingName(root reflect.Type, structNS, goName string) string {
	parts := strings.Split(structNS, ".")
	t := root
	deref := func() {
		for t != nil && (t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map) {
			t = t.Elem()
		}
	}
	deref()
	// parts[0] is the root type name, the last part is the field itself.
	for _, p := range parts[1 : len(parts)-1] {
		if i := strings.IndexByte(p, '['); i >= 0 {
			p = p[:i]
		}
		if t == nil || t.Kind() != reflect.Struct {
			break
		}
		f, ok := t.FieldByName(p)
		if !ok {
			t = nil
			break
		}
		t = f.Type
		deref()
	}
	if t != nil && t.Kind() == reflect.Struct {
		if f, ok := t.FieldByName(goName); ok {
			if n := tagName(f); n != "" && n != "-" {
				return n
			}
		}
	}
	return snake(goName)
}

func snake(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if r >= 'A' && r <= 'Z' {
			if i > 0 && (rs[i-1] < 'A' || rs[i-1] > 'Z' || (i+1 < len(rs) && rs[i+1] >= 'a' && rs[i+1] <= 'z')) {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}
