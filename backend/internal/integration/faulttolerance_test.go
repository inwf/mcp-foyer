package integration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/testmcp"
	"mcp-foyer/internal/upstream"
)

// statusOf finds one server's status by name.
func statusOf(t *testing.T, ups *upstream.Manager, name string) upstream.Status {
	t.Helper()
	for _, status := range ups.Statuses() {
		if status.Name == name {
			return status
		}
	}
	t.Fatalf("no server named %q; have %v", name, ups.Names())
	return upstream.Status{}
}

// kill terminates a server's child process the way a crash would.
func kill(t *testing.T, ups *upstream.Manager, name string) {
	t.Helper()

	status := statusOf(t, ups, name)
	if status.PID == 0 {
		t.Fatalf("server %q has no child process to kill", name)
	}
	process, err := os.FindProcess(status.PID)
	if err != nil {
		t.Fatalf("find process %d: %v", status.PID, err)
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("kill process %d: %v", status.PID, err)
	}
}

// This is the property that makes a gateway worth having: one upstream
// server dying must not take the others with it.
func TestOneServerDyingLeavesTheOthersWorking(t *testing.T) {
	stack := start(t, map[string]string{"doomed": "full", "survivor": "full"})
	session := stack.connect(t)

	// Both work to begin with.
	for _, server := range []string{"doomed", "survivor"} {
		if !callEcho(t, session, server, "before") {
			t.Fatalf("%s did not work before the crash", server)
		}
	}

	kill(t, stack.Upstreams, "doomed")

	eventually(t, "the dead server to be marked failed", func() bool {
		return statusOf(t, stack.Upstreams, "doomed").State == upstream.StateFailed
	})

	// The survivor is untouched.
	if got := statusOf(t, stack.Upstreams, "survivor").State; got != upstream.StateConnected {
		t.Errorf("the surviving server is %q, want connected", got)
	}
	if !callEcho(t, session, "survivor", "after") {
		t.Error("the surviving server stopped working after the other one died")
	}
}

// callEcho calls one server's echo tool and reports whether it worked.
func callEcho(t *testing.T, session *mcp.ClientSession, server, message string) bool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      server + "_echo",
		Arguments: map[string]any{"message": message},
	})
	if err != nil {
		t.Logf("calling %s_echo: %v", server, err)
		return false
	}
	if result.IsError {
		t.Logf("calling %s_echo reported: %s", server, resultText(result))
		return false
	}
	return resultText(result) == message
}

// A tool that cannot possibly work should stop being advertised, so a
// model does not keep choosing it.
func TestToolsOfADeadServerAreWithdrawn(t *testing.T) {
	stack := start(t, map[string]string{"doomed": "full", "survivor": "full"})
	session := stack.connect(t)

	if names := toolNames(t, session); !contains(names, "doomed_echo") {
		t.Fatalf("doomed_echo was not listed to begin with: %v", names)
	}

	kill(t, stack.Upstreams, "doomed")

	eventually(t, "the dead server's tools to be withdrawn", func() bool {
		return !contains(toolNames(t, session), "doomed_echo")
	})

	// The survivor's tools and the gateway's own are untouched.
	names := toolNames(t, session)
	if !contains(names, "survivor_echo") {
		t.Errorf("the surviving server's tools went too: %v", names)
	}
	for _, want := range gateway.SystemToolNames {
		if !contains(names, want) {
			t.Errorf("system tool %q was withdrawn: %v", want, names)
		}
	}
}

// The client is told the list changed rather than having to poll for it.
func TestClientsAreToldTheToolListChanged(t *testing.T) {
	stack := start(t, map[string]string{"doomed": "full", "survivor": "full"})

	changed := make(chan struct{}, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "watcher", Version: "1.0"},
		&mcp.ClientOptions{
			ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
				select {
				case changed <- struct{}{}:
				default:
				}
			},
		})
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: stack.URL}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	// Drain anything left over from establishing the session.
	drain(changed)

	kill(t, stack.Upstreams, "doomed")

	select {
	case <-changed:
	case <-time.After(30 * time.Second):
		t.Fatal("no tool list change notification arrived after a server died")
	}
}

func drain(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// list_servers has to show what actually happened, since that is how a
// model finds out why a tool disappeared.
func TestListServersShowsTheFailure(t *testing.T) {
	stack := start(t, map[string]string{"doomed": "full", "survivor": "full"})
	session := stack.connect(t)

	kill(t, stack.Upstreams, "doomed")
	eventually(t, "the dead server to be marked failed", func() bool {
		return statusOf(t, stack.Upstreams, "doomed").State == upstream.StateFailed
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolListServers})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	var out struct {
		Servers []gateway.ServerSummary `json:"servers"`
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("decode %s: %v", encoded, err)
	}

	byName := map[string]gateway.ServerSummary{}
	for _, server := range out.Servers {
		byName[server.Name] = server
	}

	// Both are still listed: a failed server that vanished from the list
	// would look like it was never configured.
	if len(byName) != 2 {
		t.Fatalf("listed %+v, want both servers", out.Servers)
	}
	if got := byName["doomed"].State; got != string(upstream.StateFailed) {
		t.Errorf("doomed is %q, want failed", got)
	}
	if byName["doomed"].Error == "" {
		t.Error("the failed server carries no explanation")
	}
	if got := byName["survivor"].State; got != string(upstream.StateConnected) {
		t.Errorf("survivor is %q, want connected", got)
	}
}

// A server that never starts must not stop the ones that can.
func TestAServerThatNeverStarts(t *testing.T) {
	stack := start(t, map[string]string{"working": "full"})

	// Add a server whose command does not exist and connect everything
	// again.
	if _, err := stack.Configs.Update(func(c *config.Config) error {
		broken, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			return err
		}
		broken.Command = "/nonexistent/definitely-not-a-program"
		c.MCPServers["broken"] = broken
		return nil
	}); err != nil {
		t.Fatalf("update the configuration: %v", err)
	}

	stack.Upstreams.Apply(stack.Configs.Get())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stack.Upstreams.ConnectAll(ctx, stack.Configs.Get().Startup)
	stack.Gateway.Sync()

	session := stack.connect(t)

	if got := statusOf(t, stack.Upstreams, "broken").State; got != upstream.StateFailed {
		t.Errorf("the broken server is %q, want failed", got)
	}
	if !callEcho(t, session, "working", "still fine") {
		t.Error("a server that could not start stopped the one that could")
	}
}

// The reason a server died has to be recorded, or there is nothing to
// diagnose from.
func TestTheFailureIsLogged(t *testing.T) {
	stack := start(t, map[string]string{"doomed": "full"})

	kill(t, stack.Upstreams, "doomed")
	eventually(t, "the dead server to be marked failed", func() bool {
		return statusOf(t, stack.Upstreams, "doomed").State == upstream.StateFailed
	})

	if text := logText(stack.Logs); !strings.Contains(text, "connection ended unexpectedly") {
		t.Errorf("the log does not record the lost connection:\n%s", text)
	}
}

// Losing a server has to reach the event bus, since that is what the
// WebSocket layer will forward to browsers.
func TestAFailureIsPublishedOnTheBus(t *testing.T) {
	stack := start(t, map[string]string{"doomed": "full"})

	failures, cancel := stack.Bus.Subscribe(events.ServerFailed)
	defer cancel()

	kill(t, stack.Upstreams, "doomed")

	select {
	case event := <-failures:
		if event.Server != "doomed" {
			t.Errorf("the event names %q, want doomed", event.Server)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no failure event was published")
	}
}
