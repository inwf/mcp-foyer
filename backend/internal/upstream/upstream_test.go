package upstream_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/logging"
	"mcp-foyer/internal/testmcp"
	"mcp-foyer/internal/upstream"
)

// serverConfig points a stdio server at this test binary, running in the
// requested mode.
func serverConfig(t *testing.T, mode string) config.MCPServer {
	t.Helper()
	cfg, err := testmcp.ServerConfig(mode)
	if err != nil {
		t.Fatalf("build the test server configuration: %v", err)
	}
	return cfg
}

// recorder collects the change notifications a connection emits.
type recorder struct {
	mu      sync.Mutex
	changes []upstream.ChangeKind
}

func (r *recorder) onChange(_ string, kind upstream.ChangeKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, kind)
}

func (r *recorder) saw(kind upstream.ChangeKind) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.changes {
		if k == kind {
			return true
		}
	}
	return false
}

// connect brings up a connection and closes it when the test ends.
func connect(t *testing.T, mode string) (*upstream.Conn, *logging.Store, *recorder) {
	t.Helper()
	conn, store, rec := newConn(t, mode, func(c *config.MCPServer) {})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return conn, store, rec
}

func newConn(t *testing.T, mode string, adjust func(*config.MCPServer)) (*upstream.Conn, *logging.Store, *recorder) {
	t.Helper()

	store := logging.NewStore(200)
	log, err := logging.New(logging.Options{Level: slog.LevelDebug, Store: store})
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })

	cfg := serverConfig(t, mode)
	adjust(&cfg)

	rec := &recorder{}
	conn := upstream.New("probe", cfg, "test", upstream.Deps{
		Logger:   log.ForServer(logging.ModuleUpstream, "probe"),
		OnChange: rec.onChange,
	})
	t.Cleanup(func() { conn.Close() })

	return conn, store, rec
}

func logText(store *logging.Store) string {
	var b strings.Builder
	for _, e := range store.Query(logging.Query{}) {
		b.WriteString(e.Message)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestConnectCompletesTheHandshake(t *testing.T) {
	conn, _, _ := connect(t, modeFull)
	status := conn.Status()

	if !status.Connected() {
		t.Fatalf("state = %q, want connected (error: %s)", status.State, status.Error)
	}
	if status.ServerName != "test-server" {
		t.Errorf("serverName = %q, want test-server", status.ServerName)
	}
	if status.ServerVersion != "9.9.9" {
		t.Errorf("serverVersion = %q, want 9.9.9", status.ServerVersion)
	}
	if status.ProtocolVersion == "" {
		t.Error("protocolVersion is empty")
	}
	if status.Error != "" {
		t.Errorf("error = %q, want empty", status.Error)
	}
}

// A stdio server runs as a child process, and knowing which process it
// is is what makes it possible to investigate a hung one.
func TestConnectRecordsTheChildProcess(t *testing.T) {
	conn, _, _ := connect(t, modeFull)
	status := conn.Status()

	if status.PID == 0 {
		t.Error("pid = 0, want the child process id")
	}
	if status.StartedAt.IsZero() {
		t.Error("startedAt is zero")
	}
}

func TestConnectCachesTools(t *testing.T) {
	conn, _, _ := connect(t, modeFull)

	tools := conn.Tools()
	if len(tools) == 0 {
		t.Fatal("no tools were cached")
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"echo", "sleep", "grow"} {
		if !names[want] {
			t.Errorf("tool %q missing from %v", want, names)
		}
	}
	if got := conn.Status().ToolCount; got != len(tools) {
		t.Errorf("toolCount = %d, want %d", got, len(tools))
	}
}

// The input schema is forwarded to gateway clients verbatim, so it has
// to survive being cached.
func TestCachedToolsKeepTheirInputSchema(t *testing.T) {
	conn, _, _ := connect(t, modeFull)

	for _, tool := range conn.Tools() {
		if tool.Name == "echo" {
			if tool.InputSchema == nil {
				t.Error("echo has no input schema")
			}
			return
		}
	}
	t.Fatal("echo was not found")
}

func TestConnectCachesResources(t *testing.T) {
	conn, _, _ := connect(t, modeFull)

	resources := conn.Resources()
	if len(resources) != 1 {
		t.Fatalf("cached %d resources, want 1", len(resources))
	}
	if resources[0].URI != "test://greeting" {
		t.Errorf("uri = %q, want test://greeting", resources[0].URI)
	}
	if got := conn.Status().ResourceCount; got != 1 {
		t.Errorf("resourceCount = %d, want 1", got)
	}
}

// A server that never declared resources must not be asked for them:
// the request would come back as "method not found" and look like a
// fault in the logs.
func TestServerWithoutResourcesIsNotAskedForThem(t *testing.T) {
	conn, store, _ := connect(t, modeToolsOnly)
	status := conn.Status()

	if !status.HasTools {
		t.Error("hasTools = false, want true")
	}
	if status.HasResources {
		t.Error("hasResources = true for a server that declared none")
	}
	if got := len(conn.Resources()); got != 0 {
		t.Errorf("cached %d resources, want none", got)
	}

	text := logText(store)
	for _, complaint := range []string{"could not list resources", "method not found", "Method not found"} {
		if strings.Contains(text, complaint) {
			t.Errorf("a resource request was attempted anyway; log contains %q:\n%s", complaint, text)
		}
	}
}

func TestCallTool(t *testing.T) {
	conn, _, _ := connect(t, modeFull)

	result, err := conn.CallTool(context.Background(), "echo",
		map[string]any{"message": "round trip"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool reported an error: %+v", result.Content)
	}
	if got := renderContent(result); got != "round trip" {
		t.Errorf("result = %q, want %q", got, "round trip")
	}
}

// renderContent concatenates the text parts of a tool result.
func renderContent(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func TestCallUnknownToolFails(t *testing.T) {
	conn, _, _ := connect(t, modeFull)

	_, err := conn.CallTool(context.Background(), "no-such-tool", map[string]any{})
	if err == nil {
		t.Fatal("CallTool succeeded for a tool that does not exist")
	}
	if !strings.Contains(err.Error(), "no-such-tool") {
		t.Errorf("error %q does not name the tool", err)
	}
}

// A tool that never returns must not hold a request open forever.
func TestCallToolHonoursTheConfiguredTimeout(t *testing.T) {
	conn, _, rec := newConn(t, modeFull, func(c *config.MCPServer) {
		c.Timeout = 300 * time.Millisecond
	})
	_ = rec

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	start := time.Now()
	_, err := conn.CallTool(context.Background(), "sleep", map[string]any{"seconds": 30})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("CallTool returned no error for a call that should have timed out")
	}
	if elapsed > 10*time.Second {
		t.Errorf("CallTool took %v, want it to give up near the 300ms timeout", elapsed)
	}
}

// The caller's own deadline has to be respected too, not just the
// configured one.
func TestCallToolHonoursTheCallersContext(t *testing.T) {
	conn, _, _ := connect(t, modeFull)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := conn.CallTool(ctx, "sleep", map[string]any{"seconds": 30}); err == nil {
		t.Fatal("CallTool ignored the caller's deadline")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("CallTool took %v after a 200ms deadline", elapsed)
	}
}

func TestOperationsBeforeConnectingFail(t *testing.T) {
	conn, _, _ := newConn(t, modeFull, func(*config.MCPServer) {})

	if _, err := conn.CallTool(context.Background(), "echo", nil); !errors.Is(err, upstream.ErrNotConnected) {
		t.Errorf("CallTool error = %v, want ErrNotConnected", err)
	}
	if _, err := conn.ReadResource(context.Background(), "test://greeting"); !errors.Is(err, upstream.ErrNotConnected) {
		t.Errorf("ReadResource error = %v, want ErrNotConnected", err)
	}
	if err := conn.RefreshTools(context.Background()); !errors.Is(err, upstream.ErrNotConnected) {
		t.Errorf("RefreshTools error = %v, want ErrNotConnected", err)
	}
	if got := conn.Status().State; got != upstream.StateDisconnected {
		t.Errorf("state = %q, want disconnected", got)
	}
}

func TestReadResource(t *testing.T) {
	conn, _, _ := connect(t, modeFull)

	result, err := conn.ReadResource(context.Background(), "test://greeting")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(result.Contents) == 0 || result.Contents[0].Text != "hello" {
		t.Errorf("contents = %+v, want the text \"hello\"", result.Contents)
	}
}

func TestCloseClearsEverything(t *testing.T) {
	conn, _, _ := connect(t, modeFull)

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	status := conn.Status()
	if status.State != upstream.StateDisconnected {
		t.Errorf("state = %q, want disconnected", status.State)
	}
	if status.ToolCount != 0 || status.ResourceCount != 0 {
		t.Errorf("counts = %d tools / %d resources, want zero",
			status.ToolCount, status.ResourceCount)
	}
	if len(conn.Tools()) != 0 || len(conn.Resources()) != 0 {
		t.Error("caches were not cleared")
	}
}

func TestCloseIsRepeatableAndSafeBeforeConnecting(t *testing.T) {
	conn, _, _ := newConn(t, modeFull, func(*config.MCPServer) {})

	for i := 0; i < 3; i++ {
		if err := conn.Close(); err != nil {
			t.Errorf("Close before connecting, call %d: %v", i+1, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := conn.Close(); err != nil {
			t.Errorf("Close after connecting, call %d: %v", i+1, err)
		}
	}
}

func TestConnectingTwiceIsANoOp(t *testing.T) {
	conn, _, _ := connect(t, modeFull)
	first := conn.Status().PID

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.Connect(ctx); err != nil {
		t.Fatalf("second Connect: %v", err)
	}

	if got := conn.Status().PID; got != first {
		t.Errorf("pid changed from %d to %d; a second process was started", first, got)
	}
}

// Whatever the child says on standard error is the most useful
// explanation available, so it has to reach the log.
func TestChildStandardErrorIsCaptured(t *testing.T) {
	_, store, _ := connect(t, modeNoisyStderr)

	// The child writes before serving, but capture is asynchronous.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logText(store), "listening on stdio") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	text := logText(store)
	for _, want := range []string{"warming up", "listening on stdio"} {
		if !strings.Contains(text, want) {
			t.Errorf("stderr line %q missing from the log:\n%s", want, text)
		}
	}
}

func TestFailureToStartIsReported(t *testing.T) {
	conn, _, rec := newConn(t, modeFull, func(c *config.MCPServer) {
		c.Command = "/nonexistent/definitely-not-a-program"
	})

	err := conn.Connect(context.Background())
	if err == nil {
		t.Fatal("Connect succeeded with a command that does not exist")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-program") {
		t.Errorf("error %q does not name the command", err)
	}

	status := conn.Status()
	if status.State != upstream.StateFailed {
		t.Errorf("state = %q, want failed", status.State)
	}
	if status.Error == "" {
		t.Error("status carries no error message")
	}
	if !rec.saw(upstream.ChangeStatus) {
		t.Error("no status change was reported")
	}
}

// A process that starts, complains and exits is the common shape of a
// misconfigured server. The complaint is the whole diagnosis.
func TestFailureAfterStartingSurfacesTheChildsComplaint(t *testing.T) {
	conn, store, _ := newConn(t, modeCrash, func(*config.MCPServer) {})

	if err := conn.Connect(context.Background()); err == nil {
		t.Fatal("Connect succeeded against a server that exits immediately")
	}

	if got := conn.Status().State; got != upstream.StateFailed {
		t.Errorf("state = %q, want failed", got)
	}
	if text := logText(store); !strings.Contains(text, "flux capacitor") {
		t.Errorf("the child's explanation is missing from the log:\n%s", text)
	}
}

func TestChangesAreReported(t *testing.T) {
	_, _, rec := connect(t, modeFull)

	for _, kind := range []upstream.ChangeKind{
		upstream.ChangeStatus, upstream.ChangeTools, upstream.ChangeResources,
	} {
		if !rec.saw(kind) {
			t.Errorf("no %q change was reported", kind)
		}
	}
}

// When the server says its tool list changed, the cache must follow
// without anyone polling for it.
func TestToolListChangeRefreshesTheCache(t *testing.T) {
	conn, _, _ := connect(t, modeFull)

	before := len(conn.Tools())
	if _, err := conn.CallTool(context.Background(), "grow", map[string]any{}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(conn.Tools()) > before {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	names := map[string]bool{}
	for _, tool := range conn.Tools() {
		names[tool.Name] = true
	}
	if !names["grown"] {
		t.Errorf("the new tool did not reach the cache; have %v", names)
	}
	if got := conn.Status().ToolCount; got != len(conn.Tools()) {
		t.Errorf("toolCount = %d, want %d", got, len(conn.Tools()))
	}
}

// Status and the caches are read by API handlers while refreshes are in
// flight.
func TestConcurrentReadsAndRefreshes(t *testing.T) {
	conn, _, _ := connect(t, modeFull)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				_ = conn.Status()
				_ = conn.Tools()
				_ = conn.Resources()
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 5; n++ {
				if err := conn.RefreshTools(context.Background()); err != nil {
					t.Errorf("RefreshTools: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
