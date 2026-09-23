package gateway_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/gateway"
)

// The gateway is two things at once: an MCP endpoint that clients call,
// and a subject the management API reports on. Those are separate code
// paths, and nothing was making them agree.
//
// They drifted. The management view reported only the forwarded tools, so
// the gateway's own seven were invisible in the web interface and in the
// CLI while being served perfectly well over MCP — one number said 16 and
// the other said 23, and no test compared them.

// This is that comparison. It is not a tautology: one side is assembled
// by the management API from the gateway's own bookkeeping, the other is
// whatever the SDK's server decides to answer a real client with.
func TestTheManagementViewMatchesWhatAClientSees(t *testing.T) {
	url, g := gatewayOn(t, twoServers(), nil)
	session := clientOn(t, url, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var overMCP []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		overMCP = append(overMCP, tool.Name)
	}

	var reported []string
	for _, tool := range g.PublishedTools() {
		reported = append(reported, tool.Name)
	}

	slices.Sort(overMCP)
	slices.Sort(reported)

	if !slices.Equal(overMCP, reported) {
		t.Errorf("the management API and an MCP client disagree about what is on offer\n"+
			"  over MCP  (%d): %v\n"+
			"  reported  (%d): %v\n"+
			"missing from the report: %v\n"+
			"reported but not served: %v",
			len(overMCP), overMCP, len(reported), reported,
			missing(overMCP, reported), missing(reported, overMCP))
	}
}

// The gateway's own tools have to be in the management view, with the
// schemas a caller needs. This is what the web interface groups under
// "system tools" and what the CLI needs to type a call's arguments.
func TestTheManagementViewIncludesTheGatewaysOwnTools(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	byName := map[string]bool{}
	for _, tool := range g.PublishedTools() {
		byName[tool.Name] = true
	}

	for _, name := range gateway.SystemToolNames {
		if !byName[name] {
			t.Errorf("the management view omits the gateway tool %q", name)
		}
	}
}

// The schemas have to survive, because they are read back from the server
// rather than kept from registration — and a read-back that returned
// names only would look fine until someone tried to call one.
func TestTheGatewaysOwnToolsCarryTheirSchemas(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	tools := g.SystemTools()
	if len(tools) != len(gateway.SystemToolNames) {
		t.Fatalf("got %d gateway tools, want %d", len(tools), len(gateway.SystemToolNames))
	}

	for _, tool := range tools {
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("%s has no input schema, so a caller cannot build a call", tool.Name)
		}
	}
}

// Reading the tools back opens a session to the gateway's own server. It
// must not be left behind: the API reports connected sessions, and an
// introspection session masquerading as a client would be a lie in the
// one place an operator looks to see who is connected.
//
// No real client is involved, because the count has to be exactly zero on
// both sides of the read — comparing against a client's sessions would
// only say "it did not add more than a client does".
func TestReadingBackTheGatewaysToolsLeavesNoSession(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	if before := len(g.Sessions()); before != 0 {
		t.Fatalf("%d sessions before anything connected", before)
	}

	tools := g.SystemTools()
	if len(tools) == 0 {
		t.Fatal("no tools were read back, so nothing was opened and this proves nothing")
	}

	if after := len(g.Sessions()); after != 0 {
		t.Errorf("%d sessions after reading the tools back; introspection left one behind", after)
	}
}

// The tools are read back once, so repeated calls must agree rather than
// each opening a session and returning something slightly different.
func TestTheGatewaysOwnToolsAreStable(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	first, second := names(g.SystemTools()), names(g.SystemTools())
	if !slices.Equal(first, second) {
		t.Errorf("two reads disagreed:\n  %v\n  %v", first, second)
	}
}

// The forwarded tools change as upstreams come and go, and the gateway's
// own must survive that.
func TestTheGatewaysOwnToolsSurviveAResync(t *testing.T) {
	_, g := gatewayOn(t, twoServers(), nil)

	before := len(g.PublishedTools())
	g.Sync()

	if after := len(g.PublishedTools()); after != before {
		t.Errorf("%d tools after a resync, want %d", after, before)
	}
	for _, name := range gateway.SystemToolNames {
		if !slices.Contains(names(g.PublishedTools()), name) {
			t.Errorf("the gateway tool %q was lost in a resync", name)
		}
	}
}

func names(tools []*mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	slices.Sort(out)
	return out
}

// missing returns the entries of want that are absent from have.
func missing(want, have []string) []string {
	var absent []string
	for _, name := range want {
		if !slices.Contains(have, name) {
			absent = append(absent, name)
		}
	}
	return absent
}
