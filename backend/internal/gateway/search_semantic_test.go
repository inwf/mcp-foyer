package gateway_test

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/upstream"
)

// stubSemantics answers with what the test tells it to, and records that
// it was asked — so that a wiring mistake shows up as a search that never
// consulted it.
type stubSemantics struct {
	similarity float32
	queried    []string
}

func (s *stubSemantics) Similarities(_ context.Context, query string, candidates []gateway.Searchable) []float32 {
	s.queried = append(s.queried, query)
	out := make([]float32, len(candidates))
	for i := range out {
		out[i] = s.similarity
	}
	return out
}

// The whole point of the plumbing: a search_tools call reaches the
// semantic side, and what it says changes the page.
func TestASearchReachesTheSemanticRanking(t *testing.T) {
	ups := &fakeUpstreams{
		statuses: []upstream.Status{{Name: "tracker", State: upstream.StateConnected, ToolCount: 1}},
		tools: map[string][]*mcp.Tool{
			"tracker": {{Name: "list_tickets", Description: "list the tickets of a project"}},
		},
	}
	cfgs := configFixture(t, map[string]config.MCPServer{
		"tracker": {Transport: config.TransportStdio, Command: "echo", Timeout: time.Minute},
	})
	sem := &stubSemantics{similarity: 0.8}

	session := gatewayFixtureWith(t, ups, cfgs, sem)
	result := callSystemTool(t, session, gateway.ToolSearchTools, map[string]any{"query": "bug tracking"})

	var out struct {
		Hits []struct {
			Server  string `json:"server"`
			Tool    string `json:"tool"`
			Matched int    `json:"matched"`
		} `json:"hits"`
	}
	structured(t, result, &out)

	if len(out.Hits) != 1 || out.Hits[0].Tool != "list_tickets" {
		t.Fatalf("hits = %+v, want the tool the meaning found", out.Hits)
	}
	if out.Hits[0].Matched != 0 {
		t.Errorf("matched = %d, want 0: the words matched nothing", out.Hits[0].Matched)
	}
	if len(sem.queried) != 1 || sem.queried[0] != "bug tracking" {
		t.Errorf("queried = %v, want the search's own words", sem.queried)
	}
}

// Browsing a server is not a query to embed, and asking the service for
// one would be paying for nothing.
func TestBrowsingDoesNotAskTheSemanticRanking(t *testing.T) {
	ups := &fakeUpstreams{
		statuses: []upstream.Status{{Name: "tracker", State: upstream.StateConnected, ToolCount: 1}},
		tools: map[string][]*mcp.Tool{
			"tracker": {{Name: "list_tickets", Description: "list the tickets of a project"}},
		},
	}
	cfgs := configFixture(t, map[string]config.MCPServer{
		"tracker": {Transport: config.TransportStdio, Command: "echo", Timeout: time.Minute},
	})
	sem := &stubSemantics{similarity: 0.9}

	session := gatewayFixtureWith(t, ups, cfgs, sem)
	callSystemTool(t, session, gateway.ToolSearchTools, map[string]any{"server": "tracker"})

	if len(sem.queried) != 0 {
		t.Errorf("queried = %v, want nothing asked while browsing", sem.queried)
	}
}
