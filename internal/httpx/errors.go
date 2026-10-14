// Package httpx implements the HTTP contract the legacy Laravel API exposed to
// the frontend and mobile apps: error JSON, 422 validation errors, pagination
// and Carbon-style timestamps.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

var debugMode atomic.Bool

// SetDebug toggles verbose error bodies (like Laravel APP_DEBUG). Never enable
// in production: messages of unexpected errors may leak internals.
func SetDebug(on bool) { debugMode.Store(on) }

// Default messages, as rendered by Laravel's exception handler for JSON requests.
const (
	MsgUnauthenticated  = "Unauthenticated."
	MsgForbidden        = "This action is unauthorized."
	MsgNotFound         = "Not Found"
	MsgMethodNotAllowed = "Method Not Allowed"
	MsgTooManyRequests  = "Too Many Attempts."
	MsgServerError      = "Server Error"
	MsgInvalidData      = "The given data was invalid."
)

// Error is an HTTP error with a client-safe message.
type Error struct {
	Status  int
	Message string
	Headers map[string]string
	Err     error // internal cause, logged but never rendered outside debug
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%d %s: %v", e.Status, e.Message, e.Err)
	}
	return fmt.Sprintf("%d %s", e.Status, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// NewError builds an *Error; an empty message falls back to the status default.
func NewError(status int, message string) *Error {
	if message == "" {
		message = defaultMessage(status)
	}
	return &Error{Status: status, Message: message}
}

// Wrap attaches an internal cause.
func (e *Error) Wrap(err error) *Error { cp := *e; cp.Err = err; return &cp }

// Common errors.
var (
	ErrUnauthenticated = NewError(http.StatusUnauthorized, MsgUnauthenticated)
	ErrForbidden       = NewError(http.StatusForbidden, MsgForbidden)
	ErrNotFound        = NewError(http.StatusNotFound, MsgNotFound)
)

func defaultMessage(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return MsgUnauthenticated
	case http.StatusForbidden:
		return MsgForbidden
	case http.StatusNotFound:
		return MsgNotFound
	case http.StatusMethodNotAllowed:
		return MsgMethodNotAllowed
	case http.StatusTooManyRequests:
		return MsgTooManyRequests
	case http.StatusUnprocessableEntity:
		return MsgInvalidData
	case http.StatusInternalServerError:
		return MsgServerError
	}
	if t := http.StatusText(status); t != "" {
		return t
	}
	return MsgServerError
}

// Abort renders {"message": msg} with status and aborts the chain.
func Abort(c *gin.Context, status int, message string) {
	if message == "" {
		message = defaultMessage(status)
	}
	c.AbortWithStatusJSON(status, gin.H{"message": message})
}

// Unauthenticated renders 401 {"message":"Unauthenticated."}.
func Unauthenticated(c *gin.Context) { Abort(c, http.StatusUnauthorized, MsgUnauthenticated) }

// Forbidden renders 403; empty message uses Laravel's default.
func Forbidden(c *gin.Context, message string) { Abort(c, http.StatusForbidden, message) }

// NotFound renders 404; empty message uses the default.
func NotFound(c *gin.Context, message string) { Abort(c, http.StatusNotFound, message) }

// TooManyRequests renders 429 with a Retry-After header (seconds).
func TooManyRequests(c *gin.Context, retryAfterSeconds int) {
	if retryAfterSeconds > 0 {
		c.Header("Retry-After", fmt.Sprint(retryAfterSeconds))
	}
	Abort(c, http.StatusTooManyRequests, MsgTooManyRequests)
}

// Fail renders any error the way Laravel's handler would and aborts:
//   - *ValidationError -> 422 {"message","errors"}
//   - *Error           -> its status and message
//   - gorm.ErrRecordNotFound -> 404
//   - context.Canceled (client went away) -> nothing is written
//   - anything else    -> 500 {"message":"Server Error"} (details only in debug)
func Fail(c *gin.Context, err error) {
	if err == nil {
		return
	}
	var ve *ValidationError
	var he *Error
	switch {
	case errors.As(err, &ve):
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, ve)
	case errors.As(err, &he):
		for k, v := range he.Headers {
			c.Header(k, v)
		}
		if he.Status >= 500 {
			logx.FromContext(c.Request.Context()).ErrorContext(c.Request.Context(), "request failed", "status", he.Status, "error", err.Error())
		}
		Abort(c, he.Status, he.Message)
	case errors.Is(err, gorm.ErrRecordNotFound):
		Abort(c, http.StatusNotFound, MsgNotFound)
	case errors.Is(err, context.Canceled):
		c.Abort()
	default:
		ctx := c.Request.Context()
		logx.FromContext(ctx).ErrorContext(ctx, "unhandled error", "error", err.Error(), "type", fmt.Sprintf("%T", err))
		serverError(c, err)
	}
}

func serverError(c *gin.Context, err error) {
	_ = c.Error(err)
	if debugMode.Load() {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"message":   err.Error(),
			"exception": reflect.TypeOf(err).String(),
		})
		return
	}
	Abort(c, http.StatusInternalServerError, MsgServerError)
}
