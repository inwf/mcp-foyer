package gateway

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/guide"
	"mcp-foyer/internal/upstream"
)

// resourcePrefix begins every URI the gateway hands out. Its own scheme
// keeps gateway resources distinguishable from the upstream ones they
// stand for.
const resourcePrefix = "foyer://servers/"

// GuideResourceURI is the usage guide, served so that a model can read how
// to use this gateway without anyone pasting the text into its context.
//
// It is spelled "guide" rather than "use-guide" to match the CLI command
// that prints the same document. A URI naming something that appears
// nowhere in the interface would be a third name for one thing.
const GuideResourceURI = "foyer://guide"

// guideResource describes the guide. It is always present: it depends on
// nothing outside the binary, and a client that can reach the gateway at
// all can read it.
func guideResource() *mcp.Resource {
	return &mcp.Resource{
		URI:      GuideResourceURI,
		Name:     "guide",
		Title:    "mcp-foyer usage guide",
		MIMEType: guide.MIMEType,
		Description: "How to use this endpoint: what its own tools are for, how upstream " +
			"tools are named, and how to reach a tool that is not in tools/list.",
	}
}

// ServerResourceURI names the resource describing one upstream server.
func ServerResourceURI(server string) string {
	return resourcePrefix + url.PathEscape(server)
}

// ForwardedResourceURI names a gateway resource standing in for one
// upstream resource.
//
// The upstream URI is escaped into a single path segment. Upstream URIs
// contain slashes and colons of their own, and without escaping the
// result could not be taken apart again.
func ForwardedResourceURI(server, upstreamURI string) string {
	return resourcePrefix + url.PathEscape(server) + "/" + url.PathEscape(upstreamURI)
}

// ParseResourceURI takes a gateway URI apart. The returned upstream URI
// is empty for a server's own resource.
func ParseResourceURI(uri string) (server, upstreamURI string, ok bool) {
	rest, found := strings.CutPrefix(uri, resourcePrefix)
	if !found || rest == "" {
		return "", "", false
	}

	encodedServer, encodedUpstream, hasUpstream := strings.Cut(rest, "/")

	server, err := url.PathUnescape(encodedServer)
	if err != nil || server == "" {
		return "", "", false
	}
	if !hasUpstream {
		return server, "", true
	}

	upstreamURI, err = url.PathUnescape(encodedUpstream)
	if err != nil || upstreamURI == "" {
		return "", "", false
	}
	return server, upstreamURI, true
}

// BuildResources lists everything the gateway exposes as a resource.
//
// There are three kinds. The guide is always there, because it explains
// the rest. Each server then gets one resource describing it, which is
// what makes the set of servers discoverable through the protocol rather
// than only through the web API. Each upstream resource finally gets one
// standing in for it, so a client can read it without knowing which server
// it lives on.
func BuildResources(statuses []upstream.Status, resources map[string][]*mcp.Resource) []*mcp.Resource {
	out := make([]*mcp.Resource, 0, len(statuses)+1)
	out = append(out, guideResource())

	for _, status := range statuses {
		out = append(out, &mcp.Resource{
			URI:      ServerResourceURI(status.Name),
			Name:     status.Name,
			MIMEType: "application/json",
			Description: fmt.Sprintf("Status and capabilities of the %q MCP server (%s)",
				status.Name, status.State),
		})
	}

	for _, server := range slices.Sorted(maps.Keys(resources)) {
		for _, resource := range resources[server] {
			if resource == nil || resource.URI == "" {
				continue
			}
			copied := *resource
			copied.URI = ForwardedResourceURI(server, resource.URI)
			if copied.Description == "" {
				copied.Description = "(from " + server + ")"
			} else {
				copied.Description = "[" + server + "] " + copied.Description
			}
			out = append(out, &copied)
		}
	}

	slices.SortFunc(out, func(a, b *mcp.Resource) int {
		return strings.Compare(a.URI, b.URI)
	})
	return out
}
