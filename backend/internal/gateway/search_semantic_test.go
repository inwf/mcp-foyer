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

// Searchables is the list search_tools ranks, built once and shared with
// the preview: the handshake name and the configured description
// included, parameter prose flattened in.
func TestSearchablesCarriesWhatSearchMatches(t *testing.T) {
	ups := &fakeUpstreams{
		statuses: []upstream.Status{
			{Name: "files", State: upstream.StateConnected, ServerName: "Filesystem", ToolCount: 2},
		},
		tools: map[string][]*mcp.Tool{"files": {
			{Name: "read", Description: "read a file", InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "the file to read"},
				},
			}},
			{Name: "write", Description: "write a file"},
		}},
	}
	cfgs := configFixture(t, map[string]config.MCPServer{
		"files": {
			Transport: config.TransportStdio, Command: "echo", Timeout: time.Minute,
			Description: "the team's files",
		},
	})

	got := gateway.Searchables(ups, cfgs, "")

	if len(got) != 2 {
		t.Fatalf("candidates = %d, want one per usable tool", len(got))
	}
	read := got[0]
	if read.Server != "files" || read.Tool != "read" {
		t.Fatalf("first candidate = %s/%s, want files/read", read.Server, read.Tool)
	}
	if read.ServerTitle != "Filesystem" || read.ServerDescription != "the team's files" {
		t.Errorf("candidate carries %q/%q, want the handshake name and the configured description",
			read.ServerTitle, read.ServerDescription)
	}
	if read.Arguments == "" {
		t.Error("candidate carries no parameter text")
	}
	// Nothing is exposed in this fixture, which is the default of the
	// whole design: a model reaches these tools by searching.
	if read.Exposed != "" {
		t.Errorf("exposed = %q, want empty", read.Exposed)
	}
}

// A disabled tool is not there to be found, by a search or by a preview.
func TestSearchablesLeavesDisabledToolsOut(t *testing.T) {
	ups := &fakeUpstreams{
		statuses: []upstream.Status{{Name: "files", State: upstream.StateConnected, ToolCount: 2}},
		tools: map[string][]*mcp.Tool{"files": {
			{Name: "read", Description: "read a file"},
			{Name: "write", Description: "write a file"},
		}},
	}
	cfgs := configFixture(t, map[string]config.MCPServer{
		"files": {
			Transport: config.TransportStdio, Command: "echo", Timeout: time.Minute,
			DisabledTools: []string{"write"},
		},
	})

	got := gateway.Searchables(ups, cfgs, "")
	if len(got) != 1 || got[0].Tool != "read" {
		t.Errorf("candidates = %+v, want only the usable tool", got)
	}
}
