package gateway

import (
	"errors"
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
)

// A disabled tool is one the operator has taken out of use: listed in
// its server's disabledTools, it is not offered, search does not find it,
// and a call to it is refused. It stays visible to the operator, in the
// management API and the web interface, which is where it is switched
// back on.
//
// Exposure is the other list, and the two answer different questions. An
// unexposed tool is merely not in the opening tool list; it is found and
// called on demand, which is the whole design. A disabled one is not
// there to be found. The configuration refuses a name in both.
//
// Every path a model can reach goes through this file. Search and
// browsing read the directory through UsableTools, the published set is
// cut down in ExposedByServer, the counts come from usableCount, and each
// request that names a tool — its details, call_tool, and a call by
// published name — checks IsDisabled before anything else. The last of
// those is the one that is easy to miss: a published name is routed by a
// table built at the last sync, and a tool disabled since is still in it
// until the next one.

// IsDisabled reports whether the configuration has switched a server's
// tool off.
func IsDisabled(cfg config.Config, server, tool string) bool {
	return slices.Contains(cfg.MCPServers[server].DisabledTools, tool)
}

// DisabledTools removes a server's disabled tools from a list of its
// tools.
func DisabledTools(tools []*mcp.Tool, disabled []string) []*mcp.Tool {
	if len(disabled) == 0 {
		return tools
	}
	out := make([]*mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool != nil && !slices.Contains(disabled, tool.Name) {
			out = append(out, tool)
		}
	}
	return out
}

// UsableTools is the upstream directory with every disabled tool taken
// out, dropping the servers left with none.
func UsableTools(all map[string][]*mcp.Tool, cfg config.Config) map[string][]*mcp.Tool {
	out := make(map[string][]*mcp.Tool, len(all))
	for server, tools := range all {
		if usable := DisabledTools(tools, cfg.MCPServers[server].DisabledTools); len(usable) > 0 {
			out[server] = usable
		}
	}
	return out
}

// usableCount is how many of a server's tools a model can use: what the
// server offers, less what is disabled. The status counts everything the
// server offers, which would tell a model about tools it cannot reach.
func usableCount(all map[string][]*mcp.Tool, cfg config.Config, server string, offered int) int {
	disabled := cfg.MCPServers[server].DisabledTools
	if len(disabled) == 0 {
		return offered
	}
	return len(DisabledTools(all[server], disabled))
}

// ErrToolDisabled is the reason a call to a disabled tool is refused.
var ErrToolDisabled = errors.New("disabled")

// disabledError says why a named tool cannot be had. It names the state
// rather than claiming the tool does not exist: a model told "no such
// tool" goes looking for it elsewhere, and the person it reports to
// should learn that the operator switched it off.
func disabledError(server, tool string) error {
	return fmt.Errorf("tool %q on server %q is %w on this endpoint, so it cannot be called or inspected",
		tool, server, ErrToolDisabled)
}
