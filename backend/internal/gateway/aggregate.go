package gateway

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
)

// Aggregate is what the gateway exposes to its clients: the upstream
// tools under their assigned names, plus the mapping back.
type Aggregate struct {
	Names NameMap
	Tools []*mcp.Tool
}

// BuildAggregate renames every upstream tool and returns the combined
// list.
//
// Input schemas are carried across untouched. They are arbitrary JSON
// Schema written by the upstream author, and a client needs the original
// to build a valid call.
func BuildAggregate(tools map[string][]*mcp.Tool) Aggregate {
	names := BuildNames(tools)

	out := make([]*mcp.Tool, 0, names.Len())
	for _, server := range slices.Sorted(maps.Keys(tools)) {
		for _, tool := range tools[server] {
			if tool == nil || tool.Name == "" {
				continue
			}
			exposed, ok := names.Exposed(server, tool.Name)
			if !ok {
				continue
			}
			// A tool already emitted under this name is a duplicate
			// listing from the same server.
			if slices.ContainsFunc(out, func(t *mcp.Tool) bool { return t.Name == exposed }) {
				continue
			}
			out = append(out, renameTool(tool, exposed, server))
		}
	}

	slices.SortFunc(out, func(a, b *mcp.Tool) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	})

	return Aggregate{Names: names, Tools: out}
}

// renameTool returns a copy under the exposed name.
//
// It has to be a copy: the original belongs to the upstream connection's
// cache, and mutating it would corrupt what that connection reports.
func renameTool(tool *mcp.Tool, exposed, server string) *mcp.Tool {
	copied := *tool
	copied.Name = exposed
	copied.InputSchema = normalizeInputSchema(copied.InputSchema)

	// Saying which server a tool came from is what lets a model choose
	// between three tools that all claim to search something.
	if copied.Description == "" {
		copied.Description = "(from " + server + ")"
	} else {
		copied.Description = "[" + server + "] " + copied.Description
	}

	return &copied
}

// emptyObjectSchema accepts any arguments and constrains none.
func emptyObjectSchema() map[string]any {
	return map[string]any{"type": "object"}
}

// normalizeInputSchema guarantees a schema that describes an object.
//
// Registering a tool whose schema is missing, or whose type is anything
// other than "object", is rejected outright by the SDK — so one upstream
// server with a malformed schema would otherwise take the whole gateway
// down. Substituting a permissive schema keeps the tool callable and
// contains the damage to that one tool's argument validation.
func normalizeInputSchema(schema any) any {
	// The original is passed through rather than the decoded copy, so a
	// tool's schema reaches clients exactly as its server published it.
	if _, ok := decodeObjectSchema(schema); ok {
		return schema
	}
	return emptyObjectSchema()
}

// decodeObjectSchema reports whether schema describes a JSON object, and
// returns it as a map when it does.
//
// The schema arrives as whatever the upstream sent, so it has to be
// inspected through its JSON form rather than by type assertion.
func decodeObjectSchema(schema any) (map[string]any, bool) {
	if schema == nil {
		return nil, false
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, false
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, false
	}
	if decoded["type"] != "object" {
		return nil, false
	}
	return decoded, true
}

// ExposedByServer keeps only the tools each server's configuration
// exposes, dropping the servers left with none.
func ExposedByServer(all map[string][]*mcp.Tool, cfg config.Config) map[string][]*mcp.Tool {
	out := make(map[string][]*mcp.Tool, len(all))
	for server, tools := range all {
		if allowed := FilterTools(tools, cfg.MCPServers[server].ExposedTools); len(allowed) > 0 {
			out[server] = allowed
		}
	}
	return out
}

// PublishedNames maps every tool the gateway actually offers to the name
// it offers it under.
//
// This is the one place that answers "what does a client call this tool".
// Having two answers is what went wrong before: the system tools worked
// out names over every upstream tool while Sync registered only the
// exposed ones, so search_tools handed out names that were never
// registered. Worse, an unexposed tool sharing a name with an exposed one
// counted as a collision on one side and not the other, so even an
// exposed tool could be reported under the wrong name.
//
// A tool absent from the returned map has no name to be called by. It is
// still reachable through call_tool, which addresses a tool by its server
// and its own name rather than by a gateway-assigned one.
func PublishedNames(all map[string][]*mcp.Tool, cfg config.Config) NameMap {
	return BuildNames(ExposedByServer(all, cfg))
}

// FilterTools keeps only the tools a server's configuration exposes.
//
// Nothing is exposed unless it is listed, and that strict default is the
// point rather than an oversight. A client's tools/list would otherwise
// carry the full input schema of every tool on every configured server,
// and those schemas are not small — one upstream search tool here
// declares fifteen parameters. Spending a model's context on the
// arguments of tools it will never call is the cost this avoids.
//
// It costs nothing in reach. A tool that is not exposed is still
// callable: the gateway's own call_tool reads the unfiltered upstream
// state, as do get_tool_details and search_tools. A model finds what
// it needs through those and calls it by name. "Not exposed" means "not
// in the opening hand", not "unavailable" — one extra round trip in
// exchange for not paying for every tool up front.
//
// An empty list and an absent one mean the same thing, and there is
// deliberately no way to spell "expose everything": a set that grows on
// its own whenever an upstream adds a tool is exactly what this prevents.
func FilterTools(tools []*mcp.Tool, allowed []string) []*mcp.Tool {
	if len(allowed) == 0 {
		return nil
	}

	permitted := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		permitted[name] = true
	}

	out := make([]*mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool != nil && permitted[tool.Name] {
			out = append(out, tool)
		}
	}
	return out
}
