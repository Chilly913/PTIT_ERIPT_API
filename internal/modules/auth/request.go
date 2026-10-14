package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// bindWith decodes the body (JSON or form), runs `binding` validation and lets
// extra add database-backed rule errors (e.g. unique) so that every failure
// is reported in one 422, in field order, like a Laravel FormRequest.
// It returns false when a response was written.
func bindWith(c *gin.Context, dst any, order []string, extra func(bag *httpx.ValidationError) error) bool {
	pre := httpx.NewValidationError()
	switch c.ContentType() {
	case binding.MIMEPOSTForm, binding.MIMEMultipartPOSTForm:
		b := binding.Form
		if c.ContentType() == binding.MIMEMultipartPOSTForm {
			b = binding.FormMultipart
		}
		if err := b.Bind(c.Request, dst); err != nil && !isValidationErr(err) {
			httpx.Abort(c, http.StatusBadRequest, "The request contains invalid form values.")
			return false
		}
	default:
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			httpx.Fail(c, err)
			return false
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, dst); err != nil {
				var ute *json.UnmarshalTypeError
				if !errors.As(err, &ute) || ute.Field == "" {
					httpx.Abort(c, http.StatusBadRequest, "The request body is not valid JSON.")
					return false
				}
				pre.AddRule(ute.Field, "string", nil)
			}
		}
	}

	collected := map[string][]string{}
	add := func(bag *httpx.ValidationError) {
		if bag == nil {
			return
		}
		for _, f := range bag.Fields() {
			collected[f] = append(collected[f], bag.Errors()[f]...)
		}
	}
	add(pre)
	if err := httpx.Validate(dst); err != nil {
		var ve *httpx.ValidationError
		if !errors.As(err, &ve) {
			httpx.Fail(c, err)
			return false
		}
		for _, f := range ve.Fields() {
			if len(pre.Errors()[f]) == 0 {
				collected[f] = append(collected[f], ve.Errors()[f]...)
			}
		}
	}
	if extra != nil {
		bag := httpx.NewValidationError()
		if err := extra(bag); err != nil {
			httpx.Fail(c, err)
			return false
		}
		add(bag)
	}
	if len(collected) == 0 {
		return true
	}
	out := httpx.NewValidationError()
	seen := map[string]bool{}
	for _, f := range order {
		for _, msg := range collected[f] {
			out.Add(f, msg)
		}
		seen[f] = true
	}
	for f, msgs := range collected {
		if !seen[f] {
			for _, msg := range msgs {
				out.Add(f, msg)
			}
		}
	}
	httpx.Fail(c, out)
	return false
}

func isValidationErr(err error) bool {
	var ve validator.ValidationErrors
	return errors.As(err, &ve)
}

// currentUser loads the authenticated user; writes 401 when it vanished.
func (m *Module) currentUser(c *gin.Context) (*auth.User, bool) {
	u, err := auth.FindUserByID(c.Request.Context(), m.db, auth.UserID(c))
	if errors.Is(err, auth.ErrUserNotFound) {
		httpx.Unauthenticated(c)
		return nil, false
	}
	if err != nil {
		httpx.Fail(c, err)
		return nil, false
	}
	return u, true
}
