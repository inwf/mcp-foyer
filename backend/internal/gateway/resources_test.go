package gateway_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/upstream"
)

// Upstream URIs contain slashes, colons and query strings of their own.
// Round-tripping every shape is the property the whole scheme rests on:
// a resource read arrives as a gateway URI and has to be turned back
// into the exact upstream URI.
func TestResourceURIRoundTrip(t *testing.T) {
	cases := []struct{ server, upstreamURI string }{
		{"files", "file:///tmp/notes.txt"},
		{"files", "test://greeting"},
		{"web", "https://example.com/a/b/c?q=1&r=2"},
		{"db", "postgres://user@host:5432/db"},
		{"srv", "weird uri with spaces"},
		{"srv", "has#fragment"},
		{"srv", "percent%20encoded%2Falready"},
		{"srv", "unicode-路径-🚀"},
		{"srv", "a//b///c"},
		{"srv", "trailing/"},
		{"my-server_2", "file:///x"},
	}

	for _, tc := range cases {
		uri := gateway.ForwardedResourceURI(tc.server, tc.upstreamURI)

		server, upstreamURI, ok := gateway.ParseResourceURI(uri)
		if !ok {
			t.Errorf("ParseResourceURI(%q) failed", uri)
			continue
		}
		if server != tc.server {
			t.Errorf("%q gave server %q, want %q", uri, server, tc.server)
		}
		if upstreamURI != tc.upstreamURI {
			t.Errorf("%q gave upstream %q, want %q", uri, upstreamURI, tc.upstreamURI)
		}
	}
}

func TestServerResourceURIRoundTrip(t *testing.T) {
	uri := gateway.ServerResourceURI("files")

	server, upstreamURI, ok := gateway.ParseResourceURI(uri)
	if !ok {
		t.Fatalf("ParseResourceURI(%q) failed", uri)
	}
	if server != "files" {
		t.Errorf("server = %q, want files", server)
	}
	// No upstream URI means "this is the server's own resource".
	if upstreamURI != "" {
		t.Errorf("upstream = %q, want empty for a server resource", upstreamURI)
	}
}

func TestParseRejectsForeignURIs(t *testing.T) {
	for _, uri := range []string{
		"",
		"file:///tmp/x",
		"foyer://",
		"foyer://servers/",
		"foyer://other/files",
		"https://example.com/foyer://servers/files",
		"servers/files",
	} {
		if _, _, ok := gateway.ParseResourceURI(uri); ok {
			t.Errorf("ParseResourceURI(%q) succeeded, want rejected", uri)
		}
	}
}

func TestBuildResourcesDescribesEveryServer(t *testing.T) {
	statuses := []upstream.Status{
		{Name: "files", State: upstream.StateConnected},
		{Name: "web", State: upstream.StateFailed},
	}

	got := besidesTheGuide(t, gateway.BuildResources(statuses, nil))

	if len(got) != 2 {
		t.Fatalf("built %d resources, want one per server", len(got))
	}
	byURI := map[string]*mcp.Resource{}
	for _, r := range got {
		byURI[r.URI] = r
	}
	for _, name := range []string{"files", "web"} {
		uri := gateway.ServerResourceURI(name)
		resource, ok := byURI[uri]
		if !ok {
			t.Errorf("no resource for server %q", name)
			continue
		}
		if resource.Name != name {
			t.Errorf("resource name = %q, want %q", resource.Name, name)
		}
	}

	// A failed server is still described: knowing it exists and is down
	// is the point.
	if desc := byURI[gateway.ServerResourceURI("web")].Description; !strings.Contains(desc, "failed") {
		t.Errorf("description = %q, want it to mention the state", desc)
	}
}

func TestBuildResourcesForwardsUpstreamResources(t *testing.T) {
	statuses := []upstream.Status{{Name: "files", State: upstream.StateConnected}}
	resources := map[string][]*mcp.Resource{
		"files": {
			{URI: "file:///tmp/a.txt", Name: "a", MIMEType: "text/plain"},
			{URI: "file:///tmp/b.txt", Name: "b", MIMEType: "text/plain"},
		},
	}

	got := besidesTheGuide(t, gateway.BuildResources(statuses, resources))

	if len(got) != 3 {
		t.Fatalf("built %d resources, want 1 server plus 2 forwarded", len(got))
	}
	for _, upstreamURI := range []string{"file:///tmp/a.txt", "file:///tmp/b.txt"} {
		want := gateway.ForwardedResourceURI("files", upstreamURI)
		if !slices.ContainsFunc(got, func(r *mcp.Resource) bool { return r.URI == want }) {
			t.Errorf("no resource with uri %q", want)
		}
	}
}

// The upstream connection owns the resources it caches.
func TestBuildResourcesDoesNotMutateUpstream(t *testing.T) {
	original := &mcp.Resource{URI: "file:///tmp/a.txt", Name: "a", Description: "a file"}
	resources := map[string][]*mcp.Resource{"files": {original}}

	gateway.BuildResources([]upstream.Status{{Name: "files"}}, resources)

	if original.URI != "file:///tmp/a.txt" {
		t.Errorf("upstream uri became %q", original.URI)
	}
	if original.Description != "a file" {
		t.Errorf("upstream description became %q", original.Description)
	}
}

func TestBuildResourcesNamesTheSourceServer(t *testing.T) {
	resources := map[string][]*mcp.Resource{
		"files": {{URI: "file:///a", Name: "a", Description: "the letter a"}},
		"blank": {{URI: "file:///b", Name: "b"}},
	}

	got := besidesTheGuide(t, gateway.BuildResources(nil, resources))

	for _, resource := range got {
		server, _, ok := gateway.ParseResourceURI(resource.URI)
		if !ok {
			t.Fatalf("built an unparseable uri %q", resource.URI)
		}
		if !strings.Contains(resource.Description, server) {
			t.Errorf("description %q does not name the server %q", resource.Description, server)
		}
	}
}

func TestBuildResourcesOrderIsStable(t *testing.T) {
	statuses := []upstream.Status{{Name: "zulu"}, {Name: "alpha"}}
	resources := map[string][]*mcp.Resource{
		"zulu":  {{URI: "file:///z"}},
		"alpha": {{URI: "file:///a"}},
		"mike":  {{URI: "file:///m"}},
	}

	first := uris(gateway.BuildResources(statuses, resources))
	for i := 0; i < 25; i++ {
		if again := uris(gateway.BuildResources(statuses, resources)); !slices.Equal(again, first) {
			t.Fatalf("run %d built %v, first run built %v", i, again, first)
		}
	}
	if !slices.IsSorted(first) {
		t.Errorf("resources are not ordered by uri: %v", first)
	}
}

func TestBuildResourcesSkipsMalformedEntries(t *testing.T) {
	resources := map[string][]*mcp.Resource{
		"files": {nil, {URI: ""}, {URI: "file:///real"}},
	}

	got := besidesTheGuide(t, gateway.BuildResources(nil, resources))

	if len(got) != 1 {
		t.Fatalf("built %d resources, want only the valid one", len(got))
	}
}

// With no servers and no upstream resources there is still the guide: it
// describes the gateway rather than anything connected to it, so an empty
// installation is exactly when a client most needs to be able to read it.
func TestBuildResourcesOfNothing(t *testing.T) {
	got := gateway.BuildResources(nil, nil)

	if len(got) != 1 || got[0].URI != gateway.GuideResourceURI {
		t.Errorf("built %v, want just the guide", uris(got))
	}
}

// The guide is served under the gateway's own scheme but is not about a
// server, so it does not parse as one. Every test that walks the built set
// as though each entry named a server goes through here.
func besidesTheGuide(t *testing.T, resources []*mcp.Resource) []*mcp.Resource {
	t.Helper()

	kept := make([]*mcp.Resource, 0, len(resources))
	found := false
	for _, resource := range resources {
		if resource.URI == gateway.GuideResourceURI {
			found = true
			continue
		}
		kept = append(kept, resource)
	}
	if !found {
		t.Fatalf("the guide is missing from %v", uris(resources))
	}
	return kept
}

func uris(resources []*mcp.Resource) []string {
	out := make([]string, len(resources))
	for i, r := range resources {
		out[i] = r.URI
	}
	return out
}
