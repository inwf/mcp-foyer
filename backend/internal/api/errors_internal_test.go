package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"mcp-foyer/internal/config"
)

// respond drives fail with err and returns what a client would receive.
//
// The mapping from a code to a status is the contract of the error
// envelope, so it is exercised through the function that writes the
// response rather than by reading the table directly.
func respond(t *testing.T, err error) (*httptest.ResponseRecorder, Envelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/thing", nil)
	c.Set(requestIDKey, "req-1")

	fail(c, err)

	var envelope Envelope
	if body := recorder.Body.Bytes(); len(body) > 0 {
		if decodeErr := json.Unmarshal(body, &envelope); decodeErr != nil {
			t.Fatalf("decode %s: %v", body, decodeErr)
		}
	}
	return recorder, envelope
}

func TestEveryCodeMapsToAStatus(t *testing.T) {
	cases := []struct {
		err    *Error
		code   Code
		status int
	}{
		{BadRequest("bad"), CodeBadRequest, http.StatusBadRequest},
		{Invalid("invalid"), CodeValidationFailed, http.StatusUnprocessableEntity},
		{NotFound("missing"), CodeNotFound, http.StatusNotFound},
		{Conflict("taken"), CodeConflict, http.StatusConflict},
		{Unavailable("not connected"), CodeUnavailable, http.StatusServiceUnavailable},
		{Internal("broke", errors.New("cause")), CodeInternal, http.StatusInternalServerError},
		{&Error{Code: CodeForbidden, Message: "no"}, CodeForbidden, http.StatusForbidden},
		{&Error{Code: CodeTooManyRequests, Message: "busy"}, CodeTooManyRequests, http.StatusTooManyRequests},
	}

	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			recorder, envelope := respond(t, tc.err)

			if recorder.Code != tc.status {
				t.Errorf("status = %d, want %d", recorder.Code, tc.status)
			}
			if envelope.Error.Code != tc.code {
				t.Errorf("code = %q, want %q", envelope.Error.Code, tc.code)
			}
			if envelope.Error.Message == "" {
				t.Error("no message was sent")
			}
		})
	}
}

// A code this package does not know is a bug in mcp-foyer, and reporting it
// as a server fault is both accurate and louder than a 200 would be.
func TestAnUnknownCodeIsAServerFault(t *testing.T) {
	recorder, _ := respond(t, &Error{Code: Code("invented"), Message: "?"})

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorder.Code)
	}
}

// A handler returning a bare error has not decided how it should be
// reported. Guessing a 400 would hide a server bug behind a status that
// blames the caller.
func TestABareErrorBecomesAServerFault(t *testing.T) {
	recorder, envelope := respond(t, errors.New("something went wrong deep inside"))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorder.Code)
	}
	if envelope.Error.Code != CodeInternal {
		t.Errorf("code = %q, want %q", envelope.Error.Code, CodeInternal)
	}
}

// The cause of an internal fault belongs in the operator's log, not in
// the response: it can name paths, hosts and query text.
func TestAnInternalCauseIsNotSentToTheClient(t *testing.T) {
	cause := errors.New("dial tcp 10.1.2.3:5432: connection refused")
	recorder, envelope := respond(t, Internal("the request could not be completed", cause))

	if body := recorder.Body.String(); contains(body, "10.1.2.3") {
		t.Errorf("the response leaks the cause: %s", body)
	}
	if envelope.Error.Message != "the request could not be completed" {
		t.Errorf("message = %q, want the safe summary", envelope.Error.Message)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// The request id ties a response a user reports to the line in the log,
// which is what makes it findable at all.
func TestTheEnvelopeCarriesTheRequestID(t *testing.T) {
	_, envelope := respond(t, NotFound("missing"))

	if envelope.Error.RequestID != "req-1" {
		t.Errorf("requestId = %q, want req-1", envelope.Error.RequestID)
	}
}

// ===== validation failures =====

// A form has to mark the inputs that were wrong, so every field error
// has to survive rather than being collapsed into one message.
func TestValidationFailuresKeepEveryField(t *testing.T) {
	invalid := &config.ValidationError{Errors: []config.FieldError{
		{Field: "listen.port", Message: "must be between 1 and 65535"},
		{Field: "mcpServers.files.command", Message: "is required for the stdio transport"},
	}}

	recorder, envelope := respond(t, FromValidation("the configuration is not valid", invalid))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", recorder.Code)
	}
	if len(envelope.Error.Fields) != 2 {
		t.Fatalf("sent %d field errors, want 2: %+v", len(envelope.Error.Fields), envelope.Error.Fields)
	}

	byField := map[string]string{}
	for _, field := range envelope.Error.Fields {
		byField[field.Field] = field.Message
	}
	for _, want := range []string{"listen.port", "mcpServers.files.command"} {
		if byField[want] == "" {
			t.Errorf("field %q is missing from %+v", want, envelope.Error.Fields)
		}
	}
}

// Anything that is not a validation failure was unusable for some other
// reason, and calling it a validation failure would send a client
// looking for field errors that do not exist.
func TestANonValidationErrorIsABadRequest(t *testing.T) {
	recorder, envelope := respond(t, FromValidation("could not read the body", errors.New("unexpected EOF")))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", recorder.Code)
	}
	if len(envelope.Error.Fields) != 0 {
		t.Errorf("sent field errors for a non-validation failure: %+v", envelope.Error.Fields)
	}
}

// The access log reports the outcome of each request, so the cause has
// to reach the request rather than being dropped at the response.
func TestTheCauseIsRecordedForTheAccessLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/thing", nil)

	cause := errors.New("the disk is full")
	fail(c, Internal("the request could not be completed", cause))

	if len(c.Errors) == 0 {
		t.Fatal("nothing was recorded on the request")
	}
	if !errors.Is(c.Errors.Last().Err, cause) {
		t.Errorf("recorded %v, want it to wrap the cause", c.Errors.Last().Err)
	}
}
