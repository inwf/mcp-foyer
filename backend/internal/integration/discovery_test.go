package integration

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/testmcp"
)

func discoveryStack(t *testing.T) *stack {
	t.Helper()
	stdio, err := testmcp.ServerConfig(testmcp.ModeDiscovery)
	if err != nil {
		t.Fatal(err)
	}
	stdio.ExposedTools = []string{"echo"}
	httpServer := httptest.NewServer(testmcp.Handler(testmcp.ModeDiscovery))
	t.Cleanup(httpServer.Close)
	remote := config.DefaultMCPServer()
	remote.Transport = config.TransportStreamableHTTP
	remote.URL = httpServer.URL
	remote.ExposedTools = []string{"echo"}
	return startConfigured(t, map[string]config.MCPServer{"stdio": stdio, "http": remote})
}

func TestDiscoveryAndCallsPreserveBusinessTagsOverBothUpstreamTransports(t *testing.T) {
	stack := discoveryStack(t)
	session := stack.connect(t)
	names := toolNames(t, session)
	for _, exposed := range []string{"stdio_echo", "http_echo"} {
		if !slices.Contains(names, exposed) {
			t.Errorf("explicitly exposed tool %s disappeared from tools/list", exposed)
		}
	}
	for _, server := range []string{"stdio", "http"} {
		t.Run(server, func(t *testing.T) {
			var cursor string
			var found []string
			for range 2 {
				var page struct {
					Hits []struct {
						Server      string               `json:"server"`
						Tool        string               `json:"tool"`
						Exposed     string               `json:"exposed"`
						InputSchema map[string]any       `json:"inputSchema"`
						Annotations *mcp.ToolAnnotations `json:"annotations"`
					} `json:"hits"`
					NextCursor string `json:"nextCursor"`
				}
				callDiscovery(t, session, gateway.ToolSearchTools, map[string]any{
					"server": server, "query": "metadata", "includeSchema": true, "limit": 1, "cursor": cursor,
				}, &page)
				if len(page.Hits) != 1 {
					t.Fatalf("got %d hits, want a page of one", len(page.Hits))
				}
				hit := page.Hits[0]
				found = append(found, hit.Tool)
				if hit.Server != server || hit.Exposed != "" {
					t.Fatalf("hidden tool has the wrong origin or exposure: %+v", hit)
				}
				properties, _ := hit.InputSchema["properties"].(map[string]any)
				if properties["tags"] == nil || hit.Annotations == nil || !hit.Annotations.ReadOnlyHint {
					t.Fatalf("discovery lost the business tags schema or annotations: %+v", hit)
				}
				var details map[string]any
				callDiscovery(t, session, gateway.ToolGetToolDetails, map[string]any{
					"server": server, "tool": hit.Tool,
				}, &details)
				if details["tool"] != hit.Tool || !reflect.DeepEqual(details["inputSchema"], hit.InputSchema) {
					t.Error("search and details disagree about the same tool")
				}
				var result map[string]any
				callDiscovery(t, session, gateway.ToolCallTool, map[string]any{
					"server": server, "tool": hit.Tool, "args": map[string]any{"tags": []string{"客户", "project"}},
				}, &result)
				if !reflect.DeepEqual(result["tags"], []any{"客户", "project"}) {
					t.Errorf("business tags did not survive forwarding: %v", result)
				}
				cursor = page.NextCursor
			}
			if !slices.Equal(found, []string{"call_tool", "search_tools"}) || cursor != "" {
				t.Errorf("paged tools = %v, remaining cursor %q", found, cursor)
			}
		})
	}
}

func callDiscovery(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any, target any) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s failed: %s", tool, resultText(result))
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
}
