// Package integration assembles the whole stack — configuration,
// connection manager, real child processes and the MCP gateway — and
// drives it through the protocol the way a client would.
//
// Everything below the HTTP endpoint is the real implementation. Where
// the unit tests substitute a fake to reach an error path precisely,
// these confirm the pieces fit together at all.
package integration

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/logging"
	"mcp-foyer/internal/testmcp"
	"mcp-foyer/internal/upstream"
)

func TestMain(m *testing.M) {
	if served, code := testmcp.ServeIfRequested(); served {
		os.Exit(code)
	}
	// gin's debug mode writes route tables to stdout, burying the output.
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// stack is a running gateway with its upstream servers connected.
type stack struct {
	// URL is the aggregated MCP endpoint, served through the real
	// router: every request passes the same middleware a deployed
	// instance applies.
	URL string

	// BaseURL is the listener's root, for reaching the management API.
	BaseURL string

	Gateway   *gateway.Gateway
	Upstreams *upstream.Manager
	Configs   *config.Manager
	Bus       *events.Bus
	Logs      *logging.Store
}

// start brings up the whole stack with one stdio upstream per entry.
func start(t *testing.T, servers map[string]string) *stack {
	t.Helper()
	configured := make(map[string]config.MCPServer, len(servers))
	for name, mode := range servers {
		server, err := testmcp.ServerConfig(mode)
		if err != nil {
			t.Fatalf("build the configuration for %s: %v", name, err)
		}
		configured[name] = server
	}
	return startConfigured(t, configured)
}

// startConfigured also accepts HTTP upstreams, using the same stack.
func startConfigured(t *testing.T, servers map[string]config.MCPServer) *stack {
	t.Helper()

	store := logging.NewStore(500)
	log, err := logging.New(logging.Options{Level: slog.LevelDebug, Store: store})
	if err != nil {
		t.Fatalf("build the logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })

	cfg := config.Default()
	// No stagger and no retries: the tests should not wait on either.
	cfg.Startup.ConnectDelay = 0
	cfg.Startup.MaxRetries = 0
	cfg.Startup.RetryBackoff = time.Millisecond
	// Publish changes immediately rather than after a quiet window.
	cfg.Gateway.NotifyDebounce = 0
	cfg.MCPServers = servers

	path := t.TempDir() + "/config.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save the configuration: %v", err)
	}
	configs, err := config.NewManager(path)
	if err != nil {
		t.Fatalf("open the configuration: %v", err)
	}

	bus := events.NewBus()
	t.Cleanup(bus.Close)

	ups := upstream.NewManager("test", log, bus)
	ups.Apply(configs.Get())
	t.Cleanup(ups.CloseAll)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	ups.ConnectAll(ctx, configs.Get().Startup)

	g := gateway.New(gateway.Options{
		Version:   "test",
		Upstreams: ups,
		Configs:   configs,
		Logger:    log.For(logging.ModuleGateway),
		Gateway:   configs.Get().Gateway,
	})
	g.Sync()
	g.Watch(ctx, bus)

	// The gateway is reached through the real router rather than
	// directly, so these tests cover the middleware chain and the mount
	// as well as the protocol.
	served, err := api.New(api.Options{
		Version:   "test",
		Logger:    log.For(logging.ModuleAPI),
		Security:  cfg.Security,
		MCP:       g.Handler(),
		Configs:   configs,
		Upstreams: ups,
		Gateway:   g,
		Logs:      store,
	})
	if err != nil {
		t.Fatalf("build the api: %v", err)
	}

	httpServer := httptest.NewUnstartedServer(served.Handler())
	httpServer.Listener = served.Listen(httpServer.Listener)
	httpServer.Start()
	t.Cleanup(httpServer.Close)

	return &stack{
		URL: httpServer.URL + api.MCPPath, BaseURL: httpServer.URL,
		Gateway: g, Upstreams: ups,
		Configs: configs, Bus: bus, Logs: store,
	}
}

// connect attaches an MCP client to the gateway.
func (s *stack) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	client := mcp.NewClient(&mcp.Implementation{Name: "contract-test", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: s.URL}, nil)
	if err != nil {
		t.Fatalf("connect to the gateway: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// eventually retries until condition holds or the deadline passes.
func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func resultText(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func logText(store *logging.Store) string {
	var b strings.Builder
	for _, entry := range store.Query(logging.Query{}) {
		b.WriteString(entry.Message)
		b.WriteByte('\n')
	}
	return b.String()
}
