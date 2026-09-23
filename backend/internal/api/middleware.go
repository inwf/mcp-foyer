package api

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestIDHeader carries the request identifier in both directions. A
// client or reverse proxy may supply one, and the response always
// repeats it.
const RequestIDHeader = "X-Request-Id"

// requestIDKey is where the identifier is kept on the request context.
const requestIDKey = "mcp-foyer.requestID"

// maxSuppliedRequestIDLen bounds an identifier accepted from a client.
const maxSuppliedRequestIDLen = 64

// RequestID returns the identifier assigned to this request, or "" if
// the request did not pass through [requestIDMiddleware].
func RequestID(c *gin.Context) string {
	if value, ok := c.Get(requestIDKey); ok {
		if id, ok := value.(string); ok {
			return id
		}
	}
	return ""
}

// requestIDMiddleware gives every request an identifier, echoes it back
// and makes it available to handlers and to the access log.
//
// A client-supplied identifier is honoured so that a trace started by a
// reverse proxy or by the web UI stays joined up across the hop. It is
// sanitised first: the value reaches the log, and a client that could
// put control characters there could forge log lines.
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := sanitizeRequestID(c.GetHeader(RequestIDHeader))
		if id == "" {
			id = newRequestID()
		}

		c.Set(requestIDKey, id)
		// Set on the way in, not on the way out: a hijacked connection or
		// a streamed response has already sent its headers by the time
		// the handler returns.
		c.Writer.Header().Set(RequestIDHeader, id)

		c.Next()
	}
}

// sanitizeRequestID returns supplied if it is safe to put in a log line
// and in a response header, and "" otherwise.
//
// A value with anything unexpected in it is rejected outright rather
// than stripped down to its usable characters. A stripped value is worse
// than a fresh one on both counts that matter: it no longer matches the
// trace it was supposed to join, and two different malformed values can
// strip to the same string — which would defeat the one thing an
// identifier has to do.
func sanitizeRequestID(supplied string) string {
	if supplied == "" || len(supplied) > maxSuppliedRequestIDLen {
		return ""
	}

	for i := 0; i < len(supplied); i++ {
		ch := supplied[i]
		switch {
		case ch >= 'a' && ch <= 'z',
			ch >= 'A' && ch <= 'Z',
			ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.':
		default:
			return ""
		}
	}
	return supplied
}

// newRequestID returns a fresh identifier.
//
// This needs to be unique, not unguessable, so a failure of the random
// source is not worth refusing the request over — but falling back to a
// constant would make the log unusable precisely when something is
// already wrong, so the timestamp keeps it distinguishing.
func newRequestID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "t" + hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	return hex.EncodeToString(raw[:])
}

// recoveryMiddleware turns a panic in a handler into a 500 response.
//
// One malformed request must not take the process down and with it every
// upstream connection and every other client's session. The panic is
// logged with its stack, and the client is told only that something went
// wrong: a panic message can carry internal detail.
func recoveryMiddleware(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			panicked := recover()
			if panicked == nil {
				return
			}

			log.Error("a request handler panicked",
				"requestId", RequestID(c),
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"panic", panicked,
				"stack", string(debug.Stack()))

			// A panic after the response started cannot be turned into a
			// 500 — the status is already on the wire. Closing the
			// connection at least tells the client the body is truncated
			// rather than leaving it to parse a half-written one.
			if c.Writer.Written() {
				c.Abort()
				c.Writer.Header().Set("Connection", "close")
				return
			}

			fail(c, &Error{
				Code:    CodeInternal,
				Message: "the request could not be completed",
			})
		}()

		c.Next()
	}
}

// accessLogMiddleware records one line per request.
//
// The level follows the outcome so that an operator grepping for
// problems finds them: a server fault is an error, a rejected request is
// a warning, and everything else is informational.
func accessLogMiddleware(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()

		c.Next()

		status := c.Writer.Status()
		attrs := []any{
			"requestId", RequestID(c),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"took", time.Since(started),
			"clientIp", c.ClientIP(),
		}

		// Whatever the handler recorded is the explanation for the status,
		// and it is the only place an internal cause appears.
		if errs := c.Errors; len(errs) > 0 {
			attrs = append(attrs, "error", errs.Last().Err)
		}

		switch {
		case status >= http.StatusInternalServerError:
			log.Error("request failed", attrs...)
		case status >= http.StatusBadRequest:
			log.Warn("request rejected", attrs...)
		default:
			log.Info("request", attrs...)
		}
	}
}
