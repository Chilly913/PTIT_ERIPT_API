package httpx

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

// Recovery converts panics into a JSON 500 (Laravel "Server Error") and logs
// the stack trace. Broken client connections are aborted silently.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec) // let net/http handle deliberate aborts
			}
			ctx := c.Request.Context()
			log := logx.FromContext(ctx)
			if err, ok := rec.(error); ok && isBrokenPipe(err) {
				log.WarnContext(ctx, "client connection closed", "error", err.Error())
				c.Abort()
				return
			}
			log.ErrorContext(ctx, "panic recovered", "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			if c.Writer.Written() {
				c.Abort()
				return
			}
			err, ok := rec.(error)
			if !ok {
				err = fmt.Errorf("panic: %v", rec)
			}
			serverError(c, err)
		}()
		c.Next()
	}
}

func isBrokenPipe(err error) bool {
	var ne *net.OpError
	if errors.As(err, &ne) {
		var se *os.SyscallError
		if errors.As(ne, &se) {
			s := strings.ToLower(se.Error())
			return strings.Contains(s, "broken pipe") || strings.Contains(s, "connection reset by peer")
		}
	}
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}
