package gateway_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/gateway"
)

// toolSet builds the shape BuildNames takes: server name to tools.
func toolSet(spec map[string][]string) map[string][]*mcp.Tool {
	out := map[string][]*mcp.Tool{}
	for server, names := range spec {
		tools := make([]*mcp.Tool, 0, len(names))
		for _, name := range names {
			tools = append(tools, &mcp.Tool{Name: name})
		}
		out[server] = tools
	}
	return out
}

func TestNamesArePrefixedByServer(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{
		"files": {"read", "write"},
	}))

	for _, want := range []string{"files_read", "files_write"} {
		if _, ok := names.Route(want); !ok {
			t.Errorf("no route for %q; have %v", want, names.Names())
		}
	}
}

func TestRouteRecoversTheOrigin(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{
		"files": {"read"},
	}))

	route, ok := names.Route("files_read")
	if !ok {
		t.Fatalf("no route for files_read; have %v", names.Names())
	}
	if route.Server != "files" || route.Tool != "read" {
		t.Errorf("route = %+v, want files/read", route)
	}
}

func TestExposedIsTheInverseOfRoute(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{
		"files":  {"read", "write"},
		"search": {"read"},
	}))

	for _, exposed := range names.Names() {
		route, ok := names.Route(exposed)
		if !ok {
			t.Fatalf("Names listed %q but Route does not know it", exposed)
		}
		back, ok := names.Exposed(route.Server, route.Tool)
		if !ok {
			t.Errorf("Exposed(%q, %q) is unknown", route.Server, route.Tool)
			continue
		}
		if back != exposed {
			t.Errorf("Exposed(%q, %q) = %q, want %q", route.Server, route.Tool, back, exposed)
		}
	}
}

// The ordinary case: several servers offering a tool of the same name.
// Prefixing is what keeps them apart.
func TestTheSameToolOnSeveralServers(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{
		"alpha": {"search"},
		"bravo": {"search"},
		"delta": {"search"},
	}))

	if names.Len() != 3 {
		t.Fatalf("exposed %d tools, want 3: %v", names.Len(), names.Names())
	}
	for _, server := range []string{"alpha", "bravo", "delta"} {
		exposed, ok := names.Exposed(server, "search")
		if !ok {
			t.Errorf("%s/search was not exposed", server)
			continue
		}
		route, _ := names.Route(exposed)
		if route.Server != server {
			t.Errorf("%q routes to %q, want %q", exposed, route.Server, server)
		}
	}
}

// Where the separator falls is ambiguous: server "a_b" with tool "c" and
// server "a" with tool "b_c" both join to "a_b_c". Every tool still has
// to end up individually addressable.
func TestSeparatorAmbiguityStillYieldsDistinctNames(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{
		"a_b": {"c"},
		"a":   {"b_c"},
	}))

	if names.Len() != 2 {
		t.Fatalf("exposed %d tools, want 2: %v", names.Len(), names.Names())
	}

	first, ok := names.Exposed("a_b", "c")
	if !ok {
		t.Fatal("a_b/c was not exposed")
	}
	second, ok := names.Exposed("a", "b_c")
	if !ok {
		t.Fatal("a/b_c was not exposed")
	}
	if first == second {
		t.Fatalf("both tools were exposed as %q", first)
	}

	// Each name must route back to the tool it was assigned for.
	for _, tc := range []struct {
		exposed string
		want    gateway.Route
	}{
		{first, gateway.Route{Server: "a_b", Tool: "c"}},
		{second, gateway.Route{Server: "a", Tool: "b_c"}},
	} {
		got, ok := names.Route(tc.exposed)
		if !ok {
			t.Errorf("no route for %q", tc.exposed)
			continue
		}
		if got != tc.want {
			t.Errorf("%q routes to %+v, want %+v", tc.exposed, got, tc.want)
		}
	}
}

func TestThreeWayCollision(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{
		"a_b_c": {"d"},
		"a_b":   {"c_d"},
		"a":     {"b_c_d"},
	}))

	if names.Len() != 3 {
		t.Fatalf("exposed %d tools, want 3: %v", names.Len(), names.Names())
	}
	if got := len(unique(names.Names())); got != 3 {
		t.Errorf("names are not distinct: %v", names.Names())
	}
}

// A client that learned a tool name must still find it after a restart,
// so the assignment cannot depend on map iteration order.
func TestAssignmentIsDeterministic(t *testing.T) {
	spec := map[string][]string{
		"zulu":  {"c", "a", "b"},
		"alpha": {"b", "a"},
		"mike":  {"a"},
		"a_b":   {"c"},
		"a":     {"b_c"},
	}

	first := gateway.BuildNames(toolSet(spec))
	for i := 0; i < 50; i++ {
		again := gateway.BuildNames(toolSet(spec))
		if !slices.Equal(first.Names(), again.Names()) {
			t.Fatalf("run %d assigned %v, first run assigned %v",
				i, again.Names(), first.Names())
		}
	}
}

// The order a server lists its tools in is not guaranteed to be stable,
// so it must not influence the names.
func TestUpstreamOrderDoesNotAffectNames(t *testing.T) {
	forward := gateway.BuildNames(toolSet(map[string][]string{"srv": {"a", "b", "c"}}))
	reverse := gateway.BuildNames(toolSet(map[string][]string{"srv": {"c", "b", "a"}}))

	if !slices.Equal(forward.Names(), reverse.Names()) {
		t.Errorf("reordering the upstream list changed the names: %v vs %v",
			forward.Names(), reverse.Names())
	}
}

func TestCharactersClientsRejectAreReplaced(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{
		"srv": {"has space", "has.dot", "has/slash", "has:colon", "emoji-🚀"},
	}))

	for _, exposed := range names.Names() {
		for _, r := range exposed {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
				r >= '0' && r <= '9' || r == '_' || r == '-'
			if !ok {
				t.Errorf("exposed name %q contains %q, which clients reject", exposed, r)
			}
		}
	}
	if names.Len() != 5 {
		t.Errorf("exposed %d tools, want 5: %v", names.Len(), names.Names())
	}
}

// Sanitising can map two different names onto the same string, which is
// just another collision.
func TestSanitisingCollisionsAreResolved(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{
		"srv": {"a.b", "a/b", "a b"},
	}))

	if names.Len() != 3 {
		t.Fatalf("exposed %d tools, want 3: %v", names.Len(), names.Names())
	}
	if got := len(unique(names.Names())); got != 3 {
		t.Errorf("names are not distinct: %v", names.Names())
	}
}

func TestLongNamesAreTruncated(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{
		strings.Repeat("s", 100): {strings.Repeat("t", 100)},
	}))

	for _, exposed := range names.Names() {
		if len(exposed) > 128 {
			t.Errorf("exposed name is %d characters, want at most 128", len(exposed))
		}
	}
}

// Truncation can cut two long names down to the same prefix. They still
// have to be addressable separately.
func TestTruncationCollisionsAreResolved(t *testing.T) {
	long := strings.Repeat("t", 200)
	names := gateway.BuildNames(toolSet(map[string][]string{
		"srv": {long + "-one", long + "-two", long + "-three"},
	}))

	if names.Len() != 3 {
		t.Fatalf("exposed %d tools, want 3: %v", names.Len(), names.Names())
	}
	if got := len(unique(names.Names())); got != 3 {
		t.Errorf("names are not distinct: %v", names.Names())
	}
	for _, exposed := range names.Names() {
		if len(exposed) > 128 {
			t.Errorf("exposed name is %d characters, want at most 128", len(exposed))
		}
	}
}

func TestEmptyInput(t *testing.T) {
	for _, spec := range []map[string][]*mcp.Tool{
		nil,
		{},
		{"srv": nil},
		{"srv": {}},
	} {
		names := gateway.BuildNames(spec)
		if names.Len() != 0 {
			t.Errorf("BuildNames(%v) exposed %v, want nothing", spec, names.Names())
		}
	}
}

// A malformed upstream list must not produce a nameless tool.
func TestToolsWithoutANameAreSkipped(t *testing.T) {
	names := gateway.BuildNames(map[string][]*mcp.Tool{
		"srv": {nil, {Name: ""}, {Name: "real"}},
	})

	if names.Len() != 1 {
		t.Fatalf("exposed %v, want only the named tool", names.Names())
	}
	if _, ok := names.Exposed("srv", "real"); !ok {
		t.Error("the named tool was not exposed")
	}
}

// A server listing the same tool twice must not produce two entries.
func TestDuplicateUpstreamToolsCollapse(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{
		"srv": {"read", "read", "read"},
	}))

	if names.Len() != 1 {
		t.Errorf("exposed %v, want one entry", names.Names())
	}
}

func TestUnknownNamesHaveNoRoute(t *testing.T) {
	names := gateway.BuildNames(toolSet(map[string][]string{"srv": {"read"}}))

	if _, ok := names.Route("nothing_like_this"); ok {
		t.Error("Route accepted a name that was never assigned")
	}
	if _, ok := names.Exposed("srv", "absent"); ok {
		t.Error("Exposed accepted a tool that was never assigned")
	}
	if _, ok := names.Exposed("absent", "read"); ok {
		t.Error("Exposed accepted a server that was never assigned")
	}
}

func unique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
