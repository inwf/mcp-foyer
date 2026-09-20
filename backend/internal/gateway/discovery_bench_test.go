package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcphub/internal/config"
	"mcphub/internal/gateway"
	"mcphub/internal/upstream"
)

// These are directory benchmarks, not upstream execution benchmarks.
// They include a Go SDK round trip over an in-memory transport. No real
// child processes are started, so they say nothing about their RSS or boot time.
func BenchmarkSearchDirectory(b *testing.B) {
	for _, size := range []struct{ servers, tools int }{{20, 50}, {100, 50}, {100, 100}} {
		b.Run(fmt.Sprintf("%dx%d", size.servers, size.tools), func(b *testing.B) {
			for _, mode := range []struct {
				name string
				args map[string]any
			}{
				{"summary_default", map[string]any{"query": "documents"}},
				{"summary_5", map[string]any{"query": "documents", "limit": 5}},
				{"schemas_2", map[string]any{"query": "documents", "includeSchema": true, "limit": 2}},
				{"browse_5", map[string]any{"server": "server_000", "limit": 5}},
			} {
				b.Run(mode.name, func(b *testing.B) {
					session := benchmarkDirectory(b, size.servers, size.tools)
					params := &mcp.CallToolParams{Name: gateway.ToolSearchTools, Arguments: mode.args}
					first, err := session.CallTool(b.Context(), params)
					if err != nil || first.IsError {
						b.Fatalf("search failed: %v, %+v", err, first)
					}
					encoded, err := json.Marshal(first)
					if err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					for b.Loop() {
						result, err := session.CallTool(b.Context(), params)
						if err != nil || result.IsError {
							b.Fatalf("search failed: %v, %+v", err, result)
						}
					}
					b.ReportMetric(float64(len(encoded)), "response-B")
				})
			}
		})
	}
}

func BenchmarkSystemToolList(b *testing.B) {
	session := benchmarkDirectory(b, 0, 0)
	listed, err := session.ListTools(b.Context(), nil)
	if err != nil {
		b.Fatal(err)
	}
	encoded, err := json.Marshal(listed)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := session.ListTools(b.Context(), nil); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(encoded)), "response-B")
}

func benchmarkDirectory(b *testing.B, serverCount, toolCount int) *mcp.ClientSession {
	b.Helper()
	ups := &fakeUpstreams{tools: make(map[string][]*mcp.Tool)}
	cfg := config.Default()
	cfg.MCPServers = make(map[string]config.MCPServer)
	schema := map[string]any{
		"type": "object", "description": strings.Repeat("argument documentation ", 64),
		"properties": map[string]any{"tags": map[string]any{
			"type": "array", "items": map[string]any{"type": "string"},
		}},
	}
	for i := range serverCount {
		name := fmt.Sprintf("server_%03d", i)
		ups.statuses = append(ups.statuses, upstream.Status{Name: name, State: upstream.StateConnected, ToolCount: toolCount})
		server := config.DefaultMCPServer()
		server.Command = "unused"
		cfg.MCPServers[name] = server
		for j := range toolCount {
			ups.tools[name] = append(ups.tools[name], &mcp.Tool{
				Name: fmt.Sprintf("tool_%03d", j), Description: "search and read documents", InputSchema: schema,
			})
		}
	}
	path := filepath.Join(b.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		b.Fatal(err)
	}
	configs, err := config.NewManager(path)
	if err != nil {
		b.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "benchmark", Version: "test"}, nil)
	gateway.RegisterSystemTools(server, ups, configs, nil, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(b.Context())
	b.Cleanup(cancel)
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "benchmark-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { session.Close() })
	return session
}
