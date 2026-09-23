package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"mcp-foyer/internal/config"
)

// Code is a stable, machine-readable identifier for a class of failure.
//
// The HTTP status alone is too coarse for a client to act on — several
// distinct failures share 400 — so every error response carries one of
// these as well. They are part of the API contract: renaming one is a
// breaking change, whereas rewording a message is not.
type Code string

const (
	// CodeBadRequest is a malformed request: unparseable body, wrong
	// type, missing parameter.
	CodeBadRequest Code = "bad_request"

	// CodeValidationFailed is a well-formed request whose contents were
	// rejected. It is the only code that carries per-field details.
	CodeValidationFailed Code = "validation_failed"

	// CodeNotFound is a request naming something that does not exist.
	CodeNotFound Code = "not_found"

	// CodeConflict is a request that contradicts the current state, such
	// as creating a server under a name already taken.
	CodeConflict Code = "conflict"

	// CodeForbidden is a caller not permitted to reach this listener.
	CodeForbidden Code = "forbidden"

	// CodeTooManyRequests is a caller turned away by a concurrency or
	// connection limit. Retrying later may succeed.
	CodeTooManyRequests Code = "too_many_requests"

	// CodeUnavailable is a request that cannot be served in the current
	// state but is otherwise valid, such as calling a tool on a server
	// that is not connected.
	CodeUnavailable Code = "unavailable"

	// CodeInternal is a fault in mcp-foyer itself.
	CodeInternal Code = "internal"
)

// statusOf maps a code to the HTTP status it is reported with.
//
// The mapping lives in one place so that a handler chooses only what
// went wrong, never which number to send. An unknown code is a
// programming mistake, and reporting it as 500 is both accurate and
// louder than silently sending 200.
func statusOf(code Code) int {
	switch code {
	case CodeBadRequest:
		return http.StatusBadRequest
	case CodeValidationFailed:
		return http.StatusUnprocessableEntity
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeForbidden:
		return http.StatusForbidden
	case CodeTooManyRequests:
		return http.StatusTooManyRequests
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Envelope is the body of every failed response.
//
// Errors are nested under a single key so a client can tell success from
// failure by shape alone, without having to consult the status code
// first. That is what lets the frontend funnel every response through
// one normalising step.
type Envelope struct {
	Error Failure `json:"error"`
}

// Failure describes what went wrong.
type Failure struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`

	// RequestID ties the response to the access log entry, which is what
	// makes a user-reported failure findable.
	RequestID string `json:"requestId,omitempty"`

	// Fields carries one entry per rejected input. It is populated for
	// CodeValidationFailed so a form can mark the offending inputs
	// rather than showing one combined message.
	Fields []FieldError `json:"fields,omitempty"`
}

// FieldError names one rejected input.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error is a failure a handler can return, carrying the code that
// decides the status.
type Error struct {
	Code    Code
	Message string
	Fields  []FieldError

	// cause is kept for the log, not for the response: internal details
	// belong in the operator's log rather than in a client's hands.
	cause error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.Message + ": " + e.cause.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// Errorf-style constructors. Each names the failure it makes, so a
// handler reads as a statement about what went wrong.

func BadRequest(message string) *Error {
	return &Error{Code: CodeBadRequest, Message: message}
}

func NotFound(message string) *Error {
	return &Error{Code: CodeNotFound, Message: message}
}

func Conflict(message string) *Error {
	return &Error{Code: CodeConflict, Message: message}
}

func Unavailable(message string) *Error {
	return &Error{Code: CodeUnavailable, Message: message}
}

// Internal wraps a fault in mcp-foyer. The message reaches the client; the
// cause reaches the log only.
func Internal(message string, cause error) *Error {
	return &Error{Code: CodeInternal, Message: message, cause: cause}
}

// Invalid reports rejected input. Field errors are what a form needs to
// mark the inputs that were wrong.
func Invalid(message string, fields ...FieldError) *Error {
	return &Error{Code: CodeValidationFailed, Message: message, Fields: fields}
}

// FromValidation converts a configuration validation failure, preserving
// every field error rather than collapsing them into one message.
//
// If err is not a validation failure it becomes a bad request, since the
// input was unusable for some other reason.
func FromValidation(message string, err error) *Error {
	var invalid *config.ValidationError
	if !errors.As(err, &invalid) {
		return &Error{Code: CodeBadRequest, Message: message, cause: err}
	}

	fields := make([]FieldError, 0, len(invalid.Errors))
	for _, fieldErr := range invalid.Errors {
		fields = append(fields, FieldError{Field: fieldErr.Field, Message: fieldErr.Message})
	}
	return &Error{Code: CodeValidationFailed, Message: message, Fields: fields, cause: err}
}

// asError recovers the structured error from an arbitrary one. Anything
// unrecognised is an internal fault: a handler that returns a bare error
// has not decided how it should be reported, and guessing a 400 would
// hide a bug behind a client-looking status.
func asError(err error) *Error {
	var structured *Error
	if errors.As(err, &structured) {
		return structured
	}
	return Internal("an unexpected error occurred", err)
}

// fail writes err as the response and records it for the access log.
func fail(c *gin.Context, err error) {
	structured := asError(err)

	// The access log reports the outcome, so the cause is attached to the
	// request rather than logged here — one line per request, not two.
	_ = c.Error(err)

	c.AbortWithStatusJSON(statusOf(structured.Code), Envelope{
		Error: Failure{
			Code:      structured.Code,
			Message:   structured.Message,
			RequestID: RequestID(c),
			Fields:    structured.Fields,
		},
	})
}
