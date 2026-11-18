package gateway

import (
	"context"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const searchInstructions = "Find tools on the servers behind this endpoint by describing the capability, or browse one server by giving " +
	"server alone. Most tools are not in this tool list and are only reachable this way; every hit can then be called " +
	"with call_tool(server, tool, args). Results are ranked best first, five per page by default, each hit naming " +
	"the arguments the tool requires. The first two hits on each page include their complete input schemas. " +
	"Set includeSchema=true for all schemas on the page or false for summaries only. " +
	"The query describes what a tool does; the particular thing to act on — a repository, a person, a file name — " +
	"goes into call_tool's arguments, since no tool is named after it. " +
	"Words that matched nothing are reported in unmatched; words too common to tell tools apart " +
	"(the, what, my) are ignored, so a query made only of them comes back empty and names them. " +
	"A query naming one server searches that server alone and reports it in server; " +
	"a cross-server search spreads its hits, at most two per server unless only one matched; " +
	"browse one server to see all it has. " +
	"Unavailable servers are named in unavailable, up to five, with unavailableMore counting the rest; " +
	"browsing one gives the reason."

func searchDescription(ups Upstreams, cfgs Configs) string {
	cfg := cfgs.Get()
	tools := UsableTools(ups.Tools(), cfg)
	descriptions := make(map[string]string)
	for _, status := range ups.Statuses() {
		if status.Available() {
			descriptions[status.Name] = status.Description
		}
	}
	var entries []string
	for _, name := range slices.Sorted(maps.Keys(descriptions)) {
		if len(tools[name]) == 0 {
			continue
		}
		about := strings.TrimSpace(cfg.MCPServers[name].Description)
		if about == "" {
			about = descriptions[name]
		}
		if about == "" {
			var names []string
			for _, tool := range tools[name] {
				if tool != nil {
					names = append(names, tool.Name)
				}
			}
			slices.Sort(names)
			about = strings.Join(names[:min(3, len(names))], ", ")
		}
		entry := name + " (" + clipSummary(strings.Join(strings.Fields(about), " "), 80) + ")"
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return searchInstructions
	}
	return "Available tools from " + clipSummary(strings.Join(entries, ", "), 400) + ".\n" + searchInstructions
}

func clipSummary(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}

func (g *Gateway) syncSearchDescription() {
	opts := g.opts
	description := searchDescription(opts.Upstreams, opts.Configs)
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, tool := range g.systemTools {
		if tool.Name == ToolSearchTools && tool.Description != description {
			registerSearchTool(g.server, description, opts.Upstreams, opts.Configs, opts.Semantics, g.SystemTools, opts.Usage)
			updated := *tool
			updated.Description = description
			g.systemTools[i] = &updated
			return
		}
	}
}

func registerSearchTool(server *mcp.Server, description string, ups Upstreams, cfgs Configs, sem Semantics, own OwnTools, use Usage) {
	mcp.AddTool(server, &mcp.Tool{
		Name: ToolSearchTools, Description: description, Annotations: readOnly("Search tools"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchToolsInput) (*mcp.CallToolResult, searchToolsOutput, error) {
		out, err := searchTools(ctx, ups, cfgs, sem, own, in)
		if err != nil {
			return toolError(err), searchToolsOutput{}, nil
		}
		if in.Server != Name {
			for _, hit := range out.Hits {
				use.Searched(hit.Server, hit.Tool)
			}
		}
		return nil, out, nil
	})
}
