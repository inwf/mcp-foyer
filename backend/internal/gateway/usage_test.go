package gateway_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcphub/internal/gateway"
)

// recorder is a Usage that remembers what it was told.
type recorder struct {
	mu       sync.Mutex
	searched []string
	called   []string
	failed   []string
}

func (r *recorder) Searched(server, tool string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.searched = append(r.searched, server+"/"+tool)
}

func (r *recorder) Called(server, tool string, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = append(r.called, server+"/"+tool)
	if failed {
		r.failed = append(r.failed, server+"/"+tool)
	}
}

// The counts answer "what does a model reach for", and a model reaches
// for a tool by either of two routes: call_tool, or the published name
// of an exposed tool. Both have to land on the same count, under the
// upstream's name for the tool, or half of the use goes unrecorded.
func TestBothCallRoutesAreCounted(t *testing.T) {
	use := &recorder{}
	url, _ := gatewayOn(t, twoServers(), func(o *gateway.Options) { o.Usage = use })
	session := clientOn(t, url, nil)

	callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
		"server": "files", "tool": "read", "args": map[string]any{"path": "/a"},
	})
	callSystemTool(t, session, "files_read", map[string]any{"path": "/b"})

	if got, want := use.called, []string{"files/read", "files/read"}; !equalStrings(got, want) {
		t.Errorf("called = %v, want %v", got, want)
	}
	if len(use.failed) != 0 {
		t.Errorf("successful calls were counted as failed: %v", use.failed)
	}
}

func TestAFailedCallIsCountedAsSuchOnBothRoutes(t *testing.T) {
	ups := twoServers()
	ups.callErr = errors.New("the disk is on fire")
	use := &recorder{}
	url, _ := gatewayOn(t, ups, func(o *gateway.Options) { o.Usage = use })
	session := clientOn(t, url, nil)

	callSystemTool(t, session, gateway.ToolCallTool, map[string]any{"server": "files", "tool": "read"})
	callSystemTool(t, session, "files_read", nil)

	if got, want := use.failed, []string{"files/read", "files/read"}; !equalStrings(got, want) {
		t.Errorf("failed = %v, want %v", got, want)
	}
}

// A tool that reports its own failure in the result is a failure the
// model saw, which is the failure an operator wants counted.
func TestAToolReportingAnErrorCountsAsFailed(t *testing.T) {
	ups := twoServers()
	ups.result = &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no such file"}}}
	use := &recorder{}
	url, _ := gatewayOn(t, ups, func(o *gateway.Options) { o.Usage = use })
	session := clientOn(t, url, nil)

	callSystemTool(t, session, "files_read", map[string]any{"path": "/missing"})

	if got, want := use.failed, []string{"files/read"}; !equalStrings(got, want) {
		t.Errorf("failed = %v, want %v", got, want)
	}
}

// A call the gateway refused never reached the tool. Counting it would
// say the tool was used when what happened is that it was not.
func TestARefusedCallIsNotCounted(t *testing.T) {
	use := &recorder{}
	url, _ := gatewayOn(t, twoServers(), func(o *gateway.Options) { o.Usage = use })
	session := clientOn(t, url, nil)

	// Unknown server, and bad arguments to a known tool.
	callSystemTool(t, session, gateway.ToolCallTool, map[string]any{"server": "ghost", "tool": "read"})
	callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
		"server": "files", "tool": "read", "args": map[string]any{"path": 42},
	})

	if len(use.called) != 0 {
		t.Errorf("refused calls were counted: %v", use.called)
	}
}

// What the model saw is the page it was given, not every tool that
// matched. A count of matches would credit tools the model never had a
// chance to choose.
func TestOnlyTheReturnedPageCountsAsSearched(t *testing.T) {
	use := &recorder{}
	url, _ := gatewayOn(t, twoServers(), func(o *gateway.Options) { o.Usage = use })
	session := clientOn(t, url, nil)

	// Both files tools match "disk"; ask for one.
	callSystemTool(t, session, gateway.ToolSearchTools, map[string]any{"query": "disk", "limit": 1})

	if len(use.searched) != 1 {
		t.Errorf("searched = %v, want exactly the one hit that was returned", use.searched)
	}
}

// The gateway's own tools are in every client's list; a model finding
// them by search says nothing an operator can act on.
func TestBrowsingTheGatewaysOwnToolsIsNotCounted(t *testing.T) {
	use := &recorder{}
	url, _ := gatewayOn(t, twoServers(), func(o *gateway.Options) { o.Usage = use })
	session := clientOn(t, url, nil)

	callSystemTool(t, session, gateway.ToolSearchTools, map[string]any{"server": gateway.Name})

	if len(use.searched) != 0 {
		t.Errorf("the gateway's own tools were counted as searched: %v", use.searched)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
