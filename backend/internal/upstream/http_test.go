package upstream_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"sync"
	"testing"
	"time"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/logging"
	"mcp-foyer/internal/testmcp"
	"mcp-foyer/internal/upstream"
)

// The streamable HTTP transport reaches a server someone else is already
// running. These tests run one in-process, so they exercise real HTTP
// over a real socket without depending on anything outside the test
// binary.

// httpServer starts the test MCP server behind an HTTP endpoint and
// returns its URL. The optional wrapper sees every request first, which
// is how a test asserts on what was actually sent.
func httpServer(t *testing.T, mode string, wrap func(http.Handler) http.Handler) string {
	t.Helper()

	var handler http.Handler = testmcp.Handler(mode)
	if wrap != nil {
		handler = wrap(handler)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

// connectHTTP brings up a connection to an HTTP server and closes it when
// the test ends.
func connectHTTP(t *testing.T, cfg config.MCPServer) (*upstream.Conn, *logging.Store) {
	t.Helper()

	store := logging.NewStore(200)
	log, err := logging.New(logging.Options{Level: slog.LevelDebug, Store: store})
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })

	conn := upstream.New("remote", cfg, "test", upstream.Deps{
		Logger: log.ForServer(logging.ModuleUpstream, "remote"),
	})
	t.Cleanup(func() { conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return conn, store
}

// httpConfig is a streamable-http server configuration pointing at url.
func httpConfig(url string) config.MCPServer {
	return config.MCPServer{
		Transport: config.TransportStreamableHTTP,
		URL:       url,
		Enabled:   true,
		Timeout:   20 * time.Second,
	}
}

func TestStreamableHTTPConnects(t *testing.T) {
	conn, _ := connectHTTP(t, httpConfig(httpServer(t, modeFull, nil)))

	status := conn.Status()
	if !status.Connected() {
		t.Fatalf("state = %q, error = %q", status.State, status.Error)
	}
	if status.ServerName != testmcp.ServerName {
		t.Errorf("serverName = %q, want %q", status.ServerName, testmcp.ServerName)
	}
	if status.ServerVersion != testmcp.ServerVersion {
		t.Errorf("serverVersion = %q, want %q", status.ServerVersion, testmcp.ServerVersion)
	}

	names := map[string]bool{}
	for _, tool := range conn.Tools() {
		names[tool.Name] = true
	}
	for _, want := range []string{"echo", "sleep", "grow"} {
		if !names[want] {
			t.Errorf("tool %q is missing; have %v", want, names)
		}
	}
}

// Nothing was started, so there is no process to report. Reporting one
// anyway — a PID of 0, or a start time of the year 1 — would be a fact
// about mcp-foyer's data structures rather than about the server.
func TestStreamableHTTPReportsNoProcess(t *testing.T) {
	conn, _ := connectHTTP(t, httpConfig(httpServer(t, modeFull, nil)))

	status := conn.Status()
	if status.PID != 0 {
		t.Errorf("pid = %d, want 0: no process was started", status.PID)
	}
	if !status.StartedAt.IsZero() {
		t.Errorf("startedAt = %v, want the zero time: no process was started", status.StartedAt)
	}
}

func TestStreamableHTTPCallsToolsAndReadsResources(t *testing.T) {
	conn, _ := connectHTTP(t, httpConfig(httpServer(t, modeFull, nil)))

	result, err := conn.CallTool(context.Background(), "echo",
		testmcp.EchoInput{Message: "over http"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := renderContent(result); got != "over http" {
		t.Errorf("echo returned %q, want %q", got, "over http")
	}

	read, err := conn.ReadResource(context.Background(), testmcp.GreetingURI)
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(read.Contents) == 0 || read.Contents[0].Text != testmcp.GreetingText {
		t.Errorf("reading %s returned %+v, want %q",
			testmcp.GreetingURI, read.Contents, testmcp.GreetingText)
	}
}

// The server pushes list-changed notifications down the standalone SSE
// stream, which only exists because the dialler leaves it enabled. If it
// were switched off this would hang until the deadline.
func TestStreamableHTTPReceivesListChangedNotifications(t *testing.T) {
	conn, _ := connectHTTP(t, httpConfig(httpServer(t, modeFull, nil)))

	before := len(conn.Tools())
	if _, err := conn.CallTool(context.Background(), "grow", map[string]any{}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(conn.Tools()) <= before {
		time.Sleep(20 * time.Millisecond)
	}

	names := map[string]bool{}
	for _, tool := range conn.Tools() {
		names[tool.Name] = true
	}
	if !names["grown"] {
		t.Errorf("the new tool did not reach the cache; have %v", names)
	}
}

// A configured header has to reach the server on every request, not just
// the first: an upstream behind an API key rejects the ones that arrive
// without it, and the requests after the handshake are the ones that
// carry the actual work.
func TestStreamableHTTPSendsConfiguredHeaders(t *testing.T) {
	var seen headerLog
	url := httpServer(t, modeFull, seen.record)

	cfg := httpConfig(url)
	cfg.Headers = map[string]string{
		"Authorization": "Bearer swordfish",
		"X-Tenant":      "acme",
	}
	conn, _ := connectHTTP(t, cfg)

	if _, err := conn.CallTool(context.Background(), "echo",
		testmcp.EchoInput{Message: "hi"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	requests := seen.all()
	if len(requests) < 2 {
		t.Fatalf("saw %d requests, want the handshake and the tool call at least", len(requests))
	}
	for i, header := range requests {
		if got := header.Get("Authorization"); got != "Bearer swordfish" {
			t.Errorf("request %d: Authorization = %q, want %q", i, got, "Bearer swordfish")
		}
		if got := header.Get("X-Tenant"); got != "acme" {
			t.Errorf("request %d: X-Tenant = %q, want %q", i, got, "acme")
		}
	}
}

// Without headers configured, nothing extra is invented and the SDK's own
// headers survive untouched.
func TestStreamableHTTPWithoutHeadersLeavesTheRequestAlone(t *testing.T) {
	var seen headerLog
	url := httpServer(t, modeFull, seen.record)

	connectHTTP(t, httpConfig(url))

	requests := seen.all()
	if len(requests) == 0 {
		t.Fatal("no requests reached the server")
	}
	// Accept is the header the streamable transport depends on; a
	// round-tripper that rebuilt the request would be visible here.
	if got := requests[0].Get("Accept"); !strings.Contains(got, "text/event-stream") {
		t.Errorf("Accept = %q, want it to include text/event-stream", got)
	}
}

// A proxy is how an upstream on the other side of a corporate egress
// gateway is reached, so what matters is that the traffic actually goes
// through it rather than around it.
func TestStreamableHTTPUsesTheConfiguredProxy(t *testing.T) {
	target := httpServer(t, modeFull, nil)

	var hits counter
	proxy := httptest.NewServer(forwardingProxy(&hits))
	t.Cleanup(proxy.Close)

	cfg := httpConfig(target)
	cfg.Proxy = proxy.URL
	conn, _ := connectHTTP(t, cfg)

	if _, err := conn.CallTool(context.Background(), "echo",
		testmcp.EchoInput{Message: "through the proxy"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if hits.get() == 0 {
		t.Error("the proxy saw no requests, so the connection bypassed it")
	}
}

// The proxy is unreachable, so the connection must fail rather than
// quietly falling back to a direct one — falling back would send traffic
// out of a route the operator deliberately closed.
func TestStreamableHTTPFailsWhenTheProxyIsUnreachable(t *testing.T) {
	target := httpServer(t, modeFull, nil)

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	cfg := httpConfig(target)
	cfg.Proxy = deadURL

	conn := upstream.New("remote", cfg, "test", upstream.Deps{})
	t.Cleanup(func() { conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := conn.Connect(ctx)
	if err == nil {
		t.Fatal("Connect succeeded, but the proxy was not listening")
	}
	if !strings.Contains(err.Error(), "remote") {
		t.Errorf("error %q does not name the server", err)
	}
	if state := conn.Status().State; state != upstream.StateFailed {
		t.Errorf("state = %q, want %q", state, upstream.StateFailed)
	}
}

// A proxy that cannot be parsed is a configuration mistake, and the
// message has to say which setting is wrong. Validation catches this
// before it is saved; this is the path taken by a file edited by hand.
func TestStreamableHTTPRejectsAnUnparseableProxy(t *testing.T) {
	cfg := httpConfig("http://127.0.0.1:1/mcp")
	cfg.Proxy = "://not a url"

	conn := upstream.New("remote", cfg, "test", upstream.Deps{})
	t.Cleanup(func() { conn.Close() })

	err := conn.Connect(context.Background())
	if err == nil {
		t.Fatal("Connect succeeded with an unparseable proxy")
	}
	if !strings.Contains(err.Error(), "proxy") {
		t.Errorf("error %q does not mention the proxy", err)
	}
}

// A server that is not there must fail with a message naming both the
// server and the address tried, because "connection refused" on its own
// does not say which of several upstreams is down.
func TestStreamableHTTPFailsWhenNothingIsListening(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	conn := upstream.New("remote", httpConfig(url), "test", upstream.Deps{})
	t.Cleanup(func() { conn.Close() })

	err := conn.Connect(context.Background())
	if err == nil {
		t.Fatal("Connect succeeded, but nothing was listening")
	}
	for _, want := range []string{"remote", url} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// ===== helpers =====

// headerLog records the headers of every request that reaches the server.
type headerLog struct {
	mu       sync.Mutex
	requests []http.Header
}

func (h *headerLog) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.requests = append(h.requests, r.Header.Clone())
		h.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (h *headerLog) all() []http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]http.Header(nil), h.requests...)
}

type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) add() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *counter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// forwardingProxy is a minimal HTTP proxy: a client configured to use it
// sends the absolute URL in the request line, so forwarding means little
// more than passing that URL on. ReverseProxy is used for the forwarding
// itself because it flushes an event stream as it arrives rather than
// buffering it, which the MCP session depends on.
func forwardingProxy(hits *counter) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			hits.add()
			r.Out.URL = r.In.URL
		},
	}
}
