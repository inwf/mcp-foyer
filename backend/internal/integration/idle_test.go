package integration

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/testmcp"
	"mcp-foyer/internal/upstream"
)

func TestSleepingServersRemainDiscoverableAndWakeForToolsAndResources(t *testing.T) {
	stdio, err := testmcp.ServerConfig(testmcp.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(testmcp.Handler(testmcp.ModeFull))
	t.Cleanup(httpServer.Close)
	remote := config.DefaultMCPServer()
	remote.Transport, remote.URL = config.TransportStreamableHTTP, httpServer.URL
	for name, server := range map[string]config.MCPServer{"stdio": stdio, "http": remote} {
		t.Run(name, func(t *testing.T) {
			server.IdleTimeout = 150 * time.Millisecond
			s := startConfigured(t, map[string]config.MCPServer{name: server})
			session := s.connect(t)
			conn, _ := s.Upstreams.Get(name)
			sleeping := func() bool { return conn.Status().State == upstream.StateSleeping }
			eventually(t, "idle session to close", sleeping)
			s.Upstreams.KeepConnected(t.Context(), upstream.RetryPolicy{Backoff: time.Millisecond})
			var page struct{ Hits []struct{ Tool string } }
			callDiscovery(t, session, gateway.ToolSearchTools, map[string]any{"server": name, "query": "echo"}, &page)
			if len(page.Hits) == 0 || page.Hits[0].Tool != "echo" || !sleeping() || conn.Status().PID != 0 {
				t.Fatalf("search result %v, status %+v", page, conn.Status())
			}
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: gateway.ToolCallTool,
				Arguments: map[string]any{"server": name, "tool": "sleep", "args": map[string]any{"seconds": 1}}})
			if err != nil || result.IsError {
				t.Fatalf("call longer than idleTimeout: %v, %v", result, err)
			}
			eventually(t, "idle after tool call", sleeping)
			resource, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{
				URI: gateway.ForwardedResourceURI(name, testmcp.GreetingURI),
			})
			if err != nil || len(resource.Contents) != 1 || resource.Contents[0].Text != testmcp.GreetingText {
				t.Fatalf("read after wake: %v, %v", resource, err)
			}
			eventually(t, "idle after reading", sleeping)
			if err := s.Upstreams.Disconnect(name); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ReadResource(t.Context(), testmcp.GreetingURI); err == nil || len(conn.Tools()) != 0 {
				t.Fatal("manual disconnect must withdraw the catalog and prevent wakeup")
			}
		})
	}
}
