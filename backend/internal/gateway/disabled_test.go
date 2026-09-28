package gateway_test

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/gateway"
)

// withWriteDisabled is twoServersConfig with the files server's write
// tool switched off. Read stays exposed, so every test sees a disabled
// tool beside a usable one on the same server, and can tell "this one was
// refused" apart from "everything was".
func withWriteDisabled(t *testing.T) *config.Manager {
	t.Helper()
	return configFixture(t, map[string]config.MCPServer{
		"files": {
			Transport: config.TransportStdio, Command: "npx", Enabled: true,
			Timeout: time.Minute, Description: "local filesystem",
			ExposedTools: []string{"read"}, DisabledTools: []string{"write"},
		},
		"broken": {
			Transport: config.TransportStdio, Command: "nope", Enabled: true,
			Timeout: time.Minute,
		},
	})
}

// Disabled means not there to be found: not by a query naming the tool,
// not by one it would match among others, and not by browsing its server.
// The tool beside it is still found each time, or this would pass on a
// search that finds nothing at all.
func TestSearchDoesNotFindADisabledTool(t *testing.T) {
	session := gatewayFixture(t, twoServers(), withWriteDisabled(t))

	for _, probe := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"query": "write"}, nil},
		// Both tools mention the disk.
		{map[string]any{"query": "disk"}, []string{"read"}},
		{map[string]any{"server": "files"}, []string{"read"}},
	} {
		var out struct {
			Hits []gateway.SearchHit `json:"hits"`
		}
		structured(t, callSystemTool(t, session, gateway.ToolSearchTools, probe.args), &out)

		var found []string
		for _, hit := range out.Hits {
			found = append(found, hit.Tool)
		}
		if !slices.Equal(found, probe.want) {
			t.Errorf("search_tools(%v) found %v, want %v", probe.args, found, probe.want)
		}
	}
}

// A model that names a disabled tool is told why it cannot have it. "No
// such tool" would send it looking on other servers, and the person it
// reports to should hear that the operator switched the tool off.
func TestADisabledToolHasNoDetails(t *testing.T) {
	session := gatewayFixture(t, twoServers(), withWriteDisabled(t))

	result := callSystemTool(t, session, gateway.ToolGetToolDetails,
		map[string]any{"server": "files", "tool": "write"})

	if !result.IsError {
		t.Fatal("get_tool_details described a disabled tool")
	}
	if text := resultText(result); !strings.Contains(text, "disabled") || !strings.Contains(text, "write") {
		t.Errorf("error %q does not say the tool is disabled", text)
	}
}

// The refusal comes before anything reaches the server. A disabled tool
// is one the operator does not want run, so it must not run once on the
// way to an error.
func TestCallToolRefusesADisabledTool(t *testing.T) {
	ups := twoServers()
	session := gatewayFixture(t, ups, withWriteDisabled(t))

	result := callSystemTool(t, session, gateway.ToolCallTool,
		map[string]any{"server": "files", "tool": "write"})

	if !result.IsError {
		t.Fatal("call_tool ran a disabled tool")
	}
	if text := resultText(result); !strings.Contains(text, "disabled") {
		t.Errorf("error %q does not say the tool is disabled", text)
	}
	if len(ups.calls) != 0 {
		t.Errorf("forwarded %v upstream, want nothing", ups.calls)
	}
}

// A refused call never reached the tool, so it is not a use of it; see
// TestARefusedCallIsNotCounted.
func TestACallToADisabledToolIsNotCounted(t *testing.T) {
	use := &recorder{}
	url, _ := gatewayOn(t, twoServers(), func(o *gateway.Options) {
		o.Configs = withWriteDisabled(t)
		o.Usage = use
	})
	session := clientOn(t, url, nil)

	callSystemTool(t, session, gateway.ToolCallTool, map[string]any{"server": "files", "tool": "write"})

	if len(use.called) != 0 {
		t.Errorf("a refused call was counted: %v", use.called)
	}
}

// The counts a model reads are of the tools it can use. The server's own
// count would promise a tool that search then cannot find.
func TestServerToolCountsLeaveOutDisabledTools(t *testing.T) {
	url, _ := gatewayOn(t, twoServers(), func(o *gateway.Options) {
		o.Configs = withWriteDisabled(t)
	})
	session := clientOn(t, url, nil)

	var out struct {
		Servers []gateway.ServerSummary `json:"servers"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolListServers, nil), &out)
	counted := -1
	for _, server := range out.Servers {
		if server.Name == "files" {
			counted = server.ToolCount
		}
	}
	if counted != 1 {
		t.Errorf("list_servers counts %d tools on files, want 1", counted)
	}

	described := readServerResource(t, session, "files")
	if described.ToolCount != 1 {
		t.Errorf("the server resource counts %d tools, want 1", described.ToolCount)
	}
	if want := map[string]string{"read": "read a file from disk"}; !maps.Equal(described.Tools, want) {
		t.Errorf("the server resource lists %v, want %v", described.Tools, want)
	}
}

// The configuration refuses a tool that is both exposed and disabled, but
// what a client is offered must not depend on every writer having asked
// it first. Disabled wins.
func TestADisabledToolIsNeverOffered(t *testing.T) {
	all := map[string][]*mcp.Tool{"files": {{Name: "read"}, {Name: "write"}}}
	cfg := config.Config{MCPServers: map[string]config.MCPServer{
		"files": {ExposedTools: []string{"read", "write"}, DisabledTools: []string{"write"}},
	}}

	if got := names(gateway.ExposedByServer(all, cfg)["files"]); !slices.Equal(got, []string{"read"}) {
		t.Errorf("offered %v, want only read", got)
	}
}

// A published name is routed by a table that Sync rebuilds, and Sync runs
// after the configuration has changed rather than with it. Disabling a
// tool means "stop now", so a call by that name is refused in between.
func TestAPublishedNameIsRefusedOnceItsToolIsDisabled(t *testing.T) {
	ups := twoServers()
	cfgs := exposingEverything(t, ups)
	url, g := gatewayOn(t, ups, func(o *gateway.Options) { o.Configs = cfgs })
	session := clientOn(t, url, nil)

	// What the web interface writes: the tool loses its exposure and is
	// disabled in the same save.
	if _, err := cfgs.Update(func(c *config.Config) error {
		files := c.MCPServers["files"]
		files.ExposedTools = []string{"read"}
		files.DisabledTools = []string{"write"}
		c.MCPServers["files"] = files
		return nil
	}); err != nil {
		t.Fatalf("disable the tool: %v", err)
	}
	if names := listedToolNames(t, session); !slices.Contains(names, "files_write") {
		t.Fatalf("files_write was withdrawn before the sync, so this tests nothing: %v", names)
	}

	result := callSystemTool(t, session, "files_write", nil)
	if !result.IsError {
		t.Fatal("a disabled tool ran under its published name")
	}
	if text := resultText(result); !strings.Contains(text, "disabled") {
		t.Errorf("error %q does not say the tool is disabled", text)
	}
	if len(ups.calls) != 0 {
		t.Errorf("forwarded %v upstream, want nothing", ups.calls)
	}

	g.Sync()
	if names := listedToolNames(t, session); slices.Contains(names, "files_write") {
		t.Errorf("files_write is still published after the sync: %v", names)
	}
}
