package api_test

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/logging"
)

// serveMCP builds an API whose MCP endpoint is the given handler, which
// is how these tests reach the middleware chain with a handler that
// panics, streams or blocks.
func serveMCP(t *testing.T, handler http.Handler) *harness {
	t.Helper()
	return start(t, func(o *api.Options) { o.MCP = handler })
}

// ===== request id =====

func TestEveryResponseCarriesARequestID(t *testing.T) {
	h := start(t, nil)

	resp := h.get(t, "/api/health")
	if resp.Header.Get(api.RequestIDHeader) == "" {
		t.Error("no request id was returned")
	}
}

func TestRequestIDsDiffer(t *testing.T) {
	h := start(t, nil)

	first := h.get(t, "/api/health").Header.Get(api.RequestIDHeader)
	second := h.get(t, "/api/health").Header.Get(api.RequestIDHeader)
	if first == second {
		t.Errorf("two requests share the id %q", first)
	}
}

// A trace started by a reverse proxy or by the web UI should stay joined
// up across the hop.
func TestASuppliedRequestIDIsHonoured(t *testing.T) {
	h := start(t, nil)

	req, _ := http.NewRequest(http.MethodGet, h.URL+"/api/health", nil)
	req.Header.Set(api.RequestIDHeader, "trace-from-the-proxy")
	resp, err := h.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get(api.RequestIDHeader); got != "trace-from-the-proxy" {
		t.Errorf("request id = %q, want the supplied one", got)
	}
	if text := logText(h.Logs); !contains(text, "trace-from-the-proxy") {
		t.Errorf("the supplied id did not reach the log:\n%s", text)
	}
}

// The id reaches the log, so a client that could choose it freely could
// forge log lines.
//
// Go's own client and server both refuse a header carrying CR or LF, so
// the injection that matters here is the one they permit: quotes, spaces
// and equals signs are all legal in a header value and are exactly what
// separates one field from the next in a structured log line.
//
// A malformed value is replaced rather than cleaned up. Stripping it
// would leave an identifier that matches no upstream trace, and that two
// different malformed values could collide on.
func TestASuppliedRequestIDCannotForgeALogLine(t *testing.T) {
	handler, store := router(t, nil)

	forgery := `abc" level=ERROR msg="the disk is full`
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "127.0.0.1:4000"
	req.Header.Set(api.RequestIDHeader, forgery)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	got := recorder.Header().Get(api.RequestIDHeader)
	if got == "" {
		t.Fatal("no request id was assigned")
	}
	if strings.ContainsAny(got, "\"= ") {
		t.Errorf("request id %q keeps characters that separate log fields", got)
	}
	// Sanitising is all or nothing, so the property to assert is that the
	// forgery was dropped and a fresh identifier generated in its place.
	//
	// Asking whether the result still contains some fragment of the
	// supplied value would not say that: a generated identifier is sixteen
	// random hex characters, and one of those in a few hundred contains any
	// given three-character sequence by chance. A test that fails once in
	// three hundred runs is worse than no test.
	if got == forgery {
		t.Errorf("request id %q is the supplied value", got)
	}
	if !generatedRequestID(got) {
		t.Errorf("request id %q is not a freshly generated one", got)
	}
	if text := logText(store); contains(text, "the disk is full") {
		t.Errorf("a forged field reached the log:\n%s", text)
	}
}

// generatedRequestID reports whether id has the shape newRequestID
// produces: sixteen lowercase hex characters.
func generatedRequestID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

// Two clients sending different malformed identifiers must still be
// told apart, which is the whole purpose of the identifier.
func TestMalformedRequestIDsDoNotCollide(t *testing.T) {
	handler, _ := router(t, nil)

	idFor := func(supplied string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.RemoteAddr = "127.0.0.1:4000"
		req.Header.Set(api.RequestIDHeader, supplied)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder.Header().Get(api.RequestIDHeader)
	}

	first := idFor(`abc" one`)
	second := idFor(`abc" two`)
	if first == second {
		t.Errorf("two different malformed ids both became %q", first)
	}
}

// Whatever the transport happens to permit, an identifier that reaches
// the log must not carry control characters.
func TestARequestIDWithControlCharactersIsReplaced(t *testing.T) {
	handler, _ := router(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "127.0.0.1:4000"
	req.Header[api.RequestIDHeader] = []string{"abc\r\nlevel=ERROR"}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	got := recorder.Header().Get(api.RequestIDHeader)
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("request id %q carries control characters", got)
	}
	if got == "" {
		t.Error("no request id was assigned")
	}
}

// An over-long id would pad every log line and every response header
// with whatever a caller chose to send.
func TestAnOverlongRequestIDIsTruncated(t *testing.T) {
	h := start(t, nil)

	req, _ := http.NewRequest(http.MethodGet, h.URL+"/api/health", nil)
	req.Header.Set(api.RequestIDHeader, strings.Repeat("a", 500))
	resp, err := h.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if got := len(resp.Header.Get(api.RequestIDHeader)); got > 64 {
		t.Errorf("request id is %d characters long, want it bounded", got)
	}
}

// An id made only of characters that are dropped leaves nothing usable,
// and a response with no id cannot be traced.
func TestAnEntirelyUnusableRequestIDIsReplaced(t *testing.T) {
	handler, _ := router(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "127.0.0.1:4000"
	req.Header.Set(api.RequestIDHeader, "!@#$%^&*()")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Header().Get(api.RequestIDHeader) == "" {
		t.Error("no request id was assigned")
	}
}

// ===== recovery =====

// One malformed request must not take the process down and with it every
// upstream connection and every other client's session.
func TestAPanicBecomesAServerFault(t *testing.T) {
	h := serveMCP(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("the handler exploded")
	}))

	resp := h.get(t, "/mcp")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}

	envelope := envelopeOf(t, resp)
	if envelope.Error.Code != api.CodeInternal {
		t.Errorf("code = %q, want %q", envelope.Error.Code, api.CodeInternal)
	}
	// A panic message can carry internal detail, so it stays in the log.
	if contains(envelope.Error.Message, "exploded") {
		t.Errorf("the response repeats the panic: %q", envelope.Error.Message)
	}
}

func TestAPanicIsLoggedWithItsStack(t *testing.T) {
	h := serveMCP(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("the handler exploded")
	}))
	h.get(t, "/mcp")

	text := logText(h.Logs)
	if !contains(text, "exploded") {
		t.Errorf("the panic value is missing from the log:\n%s", text)
	}
	if !contains(text, "runtime/debug.Stack") && !contains(text, "panic") {
		t.Errorf("no stack reached the log:\n%s", text)
	}
}

// The process has to survive, which is the whole point.
func TestTheServerKeepsWorkingAfterAPanic(t *testing.T) {
	h := serveMCP(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("the handler exploded")
	}))

	h.get(t, "/mcp")
	if resp := h.get(t, "/api/health"); resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d after a panic, want the server still serving", resp.StatusCode)
	}
}

// A panic after the response has started cannot be turned into a 500 —
// the status is already on the wire — but it must still not crash the
// process.
func TestAPanicAfterTheResponseStartedIsContained(t *testing.T) {
	h := serveMCP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("partial"))
		panic("too late")
	}))

	// The connection may be closed mid-body, which is a transport error
	// rather than a test failure; what matters is the next line.
	if resp, err := h.Client().Get(h.URL + "/mcp"); err == nil {
		resp.Body.Close()
	}

	if resp := h.get(t, "/api/health"); resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want the server still serving", resp.StatusCode)
	}
}

// ===== access log =====

func TestTheAccessLogRecordsTheRequest(t *testing.T) {
	h := start(t, nil)
	resp := h.get(t, "/api/health")

	text := logText(h.Logs)
	for _, want := range []string{"/api/health", "200", resp.Header.Get(api.RequestIDHeader)} {
		if !contains(text, want) {
			t.Errorf("%q is missing from the access log:\n%s", want, text)
		}
	}
}

// An operator grepping for problems has to find them, so the level
// follows the outcome rather than being uniform.
func TestTheAccessLogLevelFollowsTheOutcome(t *testing.T) {
	h := serveMCP(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	h.get(t, "/api/health")  // 200
	h.get(t, "/api/nothing") // 404
	h.get(t, "/mcp")         // 500

	levels := map[slog.Level][]string{}
	for _, entry := range h.Logs.Query(logging.Query{}) {
		levels[entry.Level] = append(levels[entry.Level], entry.Message)
	}

	if len(levels[slog.LevelInfo]) == 0 {
		t.Error("a successful request was not logged at info")
	}
	if len(levels[slog.LevelWarn]) == 0 {
		t.Error("a rejected request was not logged at warn")
	}
	if len(levels[slog.LevelError]) == 0 {
		t.Error("a failed request was not logged at error")
	}
}

// The internal cause is kept out of the response, so the log is the only
// place it appears. If it were dropped here too there would be nothing
// to diagnose from.
func TestTheAccessLogCarriesTheCause(t *testing.T) {
	h := serveMCP(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("dial tcp 10.1.2.3:5432: connection refused")
	}))
	h.get(t, "/mcp")

	if text := logText(h.Logs); !contains(text, "10.1.2.3") {
		t.Errorf("the cause is missing from the log:\n%s", text)
	}
}

// The address in the log is what an operator uses to find who did
// something. A caller that could choose it could frame someone else.
func TestTheAccessLogRecordsTheRealPeer(t *testing.T) {
	handler, store := router(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "203.0.113.9:4000"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	text := logText(store)
	if !contains(text, "203.0.113.9") {
		t.Errorf("the real peer is missing from the log:\n%s", text)
	}
	if contains(text, "clientIp=127.0.0.1") {
		t.Errorf("the log believed a forwarding header:\n%s", text)
	}
}

// ===== streaming =====

// This is the property the whole gateway rests on: gin must not buffer a
// streamed response. If it did, an MCP client would receive its
// notifications only when the stream closed, which is to say never.
func TestAStreamedResponseIsNotBuffered(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan time.Duration, 1)

	h := serveMCP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		control := http.NewResponseController(w)

		if _, err := w.Write([]byte(": ok\n\n")); err != nil {
			t.Errorf("write: %v", err)
		}
		if err := control.Flush(); err != nil {
			t.Errorf("flush through the router: %v", err)
		}
		<-release // hold the stream open, as a real one would be
	}))
	defer close(release)

	started := time.Now()
	resp, err := h.Client().Get(h.URL + "/mcp")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		if line != "" {
			arrived <- time.Since(started)
		}
	}()

	select {
	case took := <-arrived:
		t.Logf("the first line arrived after %v, while the stream was still open", took)
	case <-time.After(10 * time.Second):
		t.Fatal("nothing arrived while the stream was open; the router buffered it")
	}
}

// A stream has to keep receiving after the first flush, or a session
// would get its first notification and then go deaf.
func TestAStreamKeepsDelivering(t *testing.T) {
	release := make(chan struct{})

	h := serveMCP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		control := http.NewResponseController(w)
		for i := 0; i < 3; i++ {
			w.Write([]byte("data: tick\n\n"))
			if err := control.Flush(); err != nil {
				t.Errorf("flush %d: %v", i, err)
			}
		}
		<-release
	}))
	defer close(release)

	resp, err := h.Client().Get(h.URL + "/mcp")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	seen := 0
	deadline := time.Now().Add(10 * time.Second)
	for seen < 3 && time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "data: tick") {
			seen++
		}
	}
	if seen != 3 {
		t.Errorf("received %d of 3 events while the stream was open", seen)
	}
}

// Both limits default to something, and a stream must not be turned away
// by the request limit; see the note on the concurrency middleware.
func TestAStreamIsNotSubjectToTheRequestLimit(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	h := start(t, func(o *api.Options) {
		o.Security = config.Default().Security
		o.Security.MaxConcurrentRequests = 1
		o.MCP = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			http.NewResponseController(w).Flush()
			<-release
		})
	})

	// Occupy the MCP endpoint with more streams than the request limit.
	for i := 0; i < 3; i++ {
		go func() {
			if resp, err := h.Client().Get(h.URL + "/mcp"); err == nil {
				defer resp.Body.Close()
				io.Copy(io.Discard, resp.Body)
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)

	// The management API is still reachable.
	if resp := h.get(t, "/api/health"); resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d while streams were open; streams consumed the request limit",
			resp.StatusCode)
	}
}
