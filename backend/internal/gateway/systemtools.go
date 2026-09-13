package gateway

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcphub/internal/config"
	"mcphub/internal/upstream"
)

const (
	ToolListServers    = "list_servers"
	ToolSearchTools    = "search_tools"
	ToolGetToolDetails = "get_tool_details"
	ToolCallTool       = "call_tool"
)

// Name identifies the gateway in the handshake and in discovery calls
// that ask about its own tools.
const Name = "mcphub"

// SystemToolNames lists every gateway tool, for callers that need to
// tell them apart from forwarded ones.
var SystemToolNames = []string{
	ToolListServers,
	ToolSearchTools,
	ToolGetToolDetails,
	ToolCallTool,
}

func IsSystemTool(name string) bool {
	return slices.Contains(SystemToolNames, name)
}

// Upstreams is the cached upstream directory and the operations the
// gateway forwards. Discovery never opens new upstream connections.
type Upstreams interface {
	Statuses() []upstream.Status
	Tools() map[string][]*mcp.Tool
	Resources() map[string][]*mcp.Resource
	CallTool(ctx context.Context, server, tool string, args any) (*mcp.CallToolResult, error)
	ReadResource(ctx context.Context, server, uri string) (*mcp.ReadResourceResult, error)
}

// Configs is the read-only configuration view needed by system tools.
type Configs interface {
	Get() config.Config
}

// OwnTools reads the registered system tools, including their generated
// schemas. It may be nil when registration is used outside a Gateway.
type OwnTools func() []*mcp.Tool

type listServersInput struct{}

// ServerSummary describes one configured server.
//
// It carries counts, not tool names: an installation of a hundred servers
// would otherwise answer an overview with a directory. What one server
// offers is a question for search_tools(server).
type ServerSummary struct {
	Name          string `json:"name"`
	State         string `json:"state"`
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
	ToolCount     int    `json:"toolCount"`
	ResourceCount int    `json:"resourceCount"`
	Error         string `json:"error,omitempty"`
}

type listServersOutput struct {
	Servers []ServerSummary `json:"servers"`
}

type getToolDetailsInput struct {
	Server string `json:"server" jsonschema:"exact server name as listed by list_servers, or mcphub for this gateway's own tools"`
	Tool   string `json:"tool" jsonschema:"exact tool name on that server"`
}

type getToolDetailsOutput struct {
	Server      string `json:"server"`
	Tool        string `json:"tool"`
	Exposed     string `json:"exposed"`
	Description string `json:"description,omitempty"`
	Title       string `json:"title,omitempty"`

	// Using any here generates a boolean property schema that the
	// TypeScript MCP SDK rejects, invalidating the entire tools/list.
	InputSchema map[string]any       `json:"inputSchema,omitempty"`
	Annotations *mcp.ToolAnnotations `json:"annotations,omitempty"`
}

type callToolInput struct {
	Server string         `json:"server" jsonschema:"exact server name as listed by list_servers or returned by search_tools"`
	Tool   string         `json:"tool" jsonschema:"exact tool name on that server; it need not be in this tool list"`
	Args   map[string]any `json:"args,omitempty" jsonschema:"arguments as an object matching the tool's input schema; omit for a tool that takes none. They are checked against the schema before the call is forwarded"`
}

type searchToolsInput struct {
	Query         string `json:"query,omitempty" jsonschema:"words describing the capability, matched against tool and server names and descriptions in any spelling (readFile finds read_file); rarer words weigh more and a word matching nothing is ignored, not fatal. The directory may mix languages, so when unsure give the words in both. Supply query or server"`
	Server        string `json:"server,omitempty" jsonschema:"exact server name to search within, or to browse when query is omitted; mcphub for this gateway's own tools"`
	Limit         *int   `json:"limit,omitempty" jsonschema:"maximum number of results, from 1 to 20; default 5"`
	IncludeSchema bool   `json:"includeSchema,omitempty" jsonschema:"also return each hit's complete input schema and annotations, so a call can be prepared from this one result; default false. Combine with a limit of 1 to 3"`
	Cursor        string `json:"cursor,omitempty" jsonschema:"nextCursor from the previous result, with the same query and server, to get the next page"`
}

// The management API keeps SearchHit's existing shape. Schema-bearing
// discovery results extend it only on the system tool surface.
type searchToolHit struct {
	SearchHit
	Title       string               `json:"title,omitempty"`
	InputSchema map[string]any       `json:"inputSchema,omitempty"`
	Annotations *mcp.ToolAnnotations `json:"annotations,omitempty"`
}

type searchToolsOutput struct {
	Query      string          `json:"query,omitempty"`
	Server     string          `json:"server,omitempty"`
	Hits       []searchToolHit `json:"hits"`
	NextCursor string          `json:"nextCursor,omitempty"`
	Unmatched  []string        `json:"unmatched,omitempty"`
}

const (
	defaultSearchLimit = 5
	maxSearchLimit     = 20
)

// RegisterSystemTools adds the gateway's own tools to an MCP server.
//
// Each description stands on its own. Many clients never show a model the
// handshake instructions or any resource, so a description that leans on
// either leaves that model with half the rule. The same words are used
// throughout: a tool is either "in this tool list" or not, which is the
// only distinction a caller needs.
func RegisterSystemTools(server *mcp.Server, ups Upstreams, cfgs Configs, own OwnTools) {
	mcp.AddTool(server, &mcp.Tool{
		Name: ToolListServers,
		Description: "Overview of the servers behind this gateway: each one's name, state, description, " +
			"tool and resource counts, and the error when it is down. " +
			"Start here when the servers are unknown; when the capability is known, search_tools finds it directly, " +
			"and search_tools(server) lists what one server offers.",
		Annotations: readOnly("List servers"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listServersInput) (*mcp.CallToolResult, listServersOutput, error) {
		return nil, listServers(ups, cfgs), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: ToolSearchTools,
		Description: "Find tools on the servers behind this gateway by describing the capability, or browse one server by giving " +
			"server alone. Most tools are not in this tool list and are only reachable this way; every hit can then be called " +
			"with call_tool(server, tool, args). Results are ranked best first, five per page by default; includeSchema=true " +
			"returns each hit's input schema so the call can be prepared from the same result. " +
			"Words that matched nothing are reported in unmatched, so a thin result can be retried with other words.",
		Annotations: readOnly("Search tools"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchToolsInput) (*mcp.CallToolResult, searchToolsOutput, error) {
		out, err := searchTools(ups, cfgs, own, in)
		if err != nil {
			return toolError(err), searchToolsOutput{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: ToolGetToolDetails,
		Description: "Complete input schema, description and annotations of one tool, named by server and tool. " +
			"Use before call_tool when the arguments are not yet known and the search did not include the schema.",
		Annotations: readOnly("Get tool details"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in getToolDetailsInput) (*mcp.CallToolResult, getToolDetailsOutput, error) {
		out, err := getToolDetails(ups, cfgs, own, in.Server, in.Tool)
		if err != nil {
			return toolError(err), getToolDetailsOutput{}, nil
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: ToolCallTool,
		Description: "Call a tool on one of the servers behind this gateway by server name, tool name and arguments, " +
			"whether or not the tool is in this tool list. The arguments are checked against the tool's input schema " +
			"before forwarding, and the result is returned as the tool produced it. " +
			"Skip discovery when server, tool and arguments are already known. " +
			"This gateway's own four tools are called directly, not through call_tool.",
		Annotations: &mcp.ToolAnnotations{
			Title:         "Call a tool on a server",
			OpenWorldHint: boolPtr(true),
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in callToolInput) (*mcp.CallToolResult, any, error) {
		result, err := callTool(ctx, ups, in)
		if err != nil {
			return toolError(err), nil, nil
		}
		return result, nil, nil
	})
}

func listServers(ups Upstreams, cfgs Configs) listServersOutput {
	cfg := cfgs.Get()
	statuses := ups.Statuses()
	out := listServersOutput{Servers: make([]ServerSummary, 0, len(statuses))}
	for _, status := range statuses {
		summary := ServerSummary{
			Name:          status.Name,
			State:         string(status.State),
			Description:   cfg.MCPServers[status.Name].Description,
			ToolCount:     status.ToolCount,
			ResourceCount: status.ResourceCount,
			Error:         status.Error,
		}
		if status.ServerName != status.Name {
			summary.Title = status.ServerName
		}
		out.Servers = append(out.Servers, summary)
	}
	return out
}

func getToolDetails(ups Upstreams, cfgs Configs, own OwnTools, server, tool string) (getToolDetailsOutput, error) {
	if server == Name {
		if own != nil {
			for _, candidate := range own() {
				if candidate != nil && candidate.Name == tool {
					return describeTool(Name, candidate, candidate.Name), nil
				}
			}
		}
		return getToolDetailsOutput{}, fmt.Errorf("%s has no tool named %q; its own tools are %s",
			Name, tool, joinNames(SystemToolNames))
	}
	if err := requireServer(ups, server); err != nil {
		return getToolDetailsOutput{}, withSystemToolHint(err, tool)
	}
	all := ups.Tools()
	for _, candidate := range all[server] {
		if candidate != nil && candidate.Name == tool {
			exposed, _ := PublishedNames(all, cfgs.Get()).Exposed(server, tool)
			return describeTool(server, candidate, exposed), nil
		}
	}
	return getToolDetailsOutput{}, fmt.Errorf("server %q has no tool named %q", server, tool)
}

func describeTool(server string, tool *mcp.Tool, exposed string) getToolDetailsOutput {
	schema, ok := decodeObjectSchema(tool.InputSchema)
	if !ok {
		schema = emptyObjectSchema()
	}
	return getToolDetailsOutput{
		Server: server, Tool: tool.Name, Exposed: exposed,
		Description: tool.Description, Title: tool.Title,
		InputSchema: schema, Annotations: tool.Annotations,
	}
}

func callTool(ctx context.Context, ups Upstreams, in callToolInput) (*mcp.CallToolResult, error) {
	if err := requireServer(ups, in.Server); err != nil {
		return nil, withSystemToolHint(err, in.Tool)
	}
	// Checked against the cached schema when there is one. The upstream is
	// authoritative: cached discovery metadata can lag a tools/list_changed
	// notification, so a tool the cache does not know is forwarded as is.
	if tool := cachedTool(ups, in.Server, in.Tool); tool != nil {
		if err := checkArguments(tool, in.Args); err != nil {
			return nil, err
		}
	}
	var args any
	if in.Args != nil {
		args = in.Args
	}
	return ups.CallTool(ctx, in.Server, in.Tool, args)
}

// cachedTool finds a tool in the discovery cache, or nil.
func cachedTool(ups Upstreams, server, tool string) *mcp.Tool {
	for _, candidate := range ups.Tools()[server] {
		if candidate != nil && candidate.Name == tool {
			return candidate
		}
	}
	return nil
}

func searchTools(ups Upstreams, cfgs Configs, own OwnTools, in searchToolsInput) (searchToolsOutput, error) {
	in.Query = strings.TrimSpace(in.Query)
	in.Server = strings.TrimSpace(in.Server)
	if in.Query == "" && in.Server == "" {
		return searchToolsOutput{}, fmt.Errorf("provide a query or server; use list_servers for an overview")
	}
	limit := defaultSearchLimit
	if in.Limit != nil {
		limit = *in.Limit
	}
	if limit < 1 || limit > maxSearchLimit {
		return searchToolsOutput{}, fmt.Errorf("limit must be between 1 and %d", maxSearchLimit)
	}
	if in.Server != "" && in.Server != Name {
		if err := requireServer(ups, in.Server); err != nil {
			return searchToolsOutput{}, err
		}
	}

	cfg := cfgs.Get()
	all := ups.Tools()
	// Names are assigned over the complete exposed set before filtering;
	// otherwise a collision on another server would give the wrong name.
	names := PublishedNames(all, cfg)
	if in.Server == Name {
		if own == nil {
			return searchToolsOutput{}, fmt.Errorf("no server named %q", Name)
		}
		all = map[string][]*mcp.Tool{Name: own()}
	}
	serverTitles := make(map[string]string)
	for _, status := range ups.Statuses() {
		serverTitles[status.Name] = status.ServerName
	}
	servers := []string{in.Server}
	if in.Server == "" {
		// Group ties in their final order before ranking; a broad query
		// should not have to sort randomized map order on every request.
		servers = slices.Sorted(maps.Keys(all))
	}
	capacity := 0
	for _, server := range servers {
		capacity += len(all[server])
	}
	candidates := make([]Searchable, 0, capacity)
	byOrigin := make(map[Route]*mcp.Tool, capacity)
	for _, server := range servers {
		title, description := serverTitles[server], cfg.MCPServers[server].Description
		for _, tool := range all[server] {
			if tool == nil || tool.Name == "" {
				continue
			}
			origin := Route{Server: server, Tool: tool.Name}
			if _, exists := byOrigin[origin]; exists {
				continue
			}
			byOrigin[origin] = tool
			exposed, _ := names.Exposed(server, tool.Name)
			if in.Server == Name {
				exposed = tool.Name
			}
			candidates = append(candidates, Searchable{
				Server: server, Tool: tool.Name, Exposed: exposed, Description: tool.Description,
				ServerTitle: title, ServerDescription: description,
			})
		}
	}
	hits, unmatched := searchCandidates(in.Query, candidates, 0)
	start, nextCursor, err := searchPageCursor(in, hits, limit)
	if err != nil {
		return searchToolsOutput{}, err
	}
	end := min(start+limit, len(hits))
	out := searchToolsOutput{
		Query: in.Query, Server: in.Server,
		Hits: make([]searchToolHit, 0, end-start), NextCursor: nextCursor,
		Unmatched: unmatched,
	}
	for _, hit := range hits[start:end] {
		result := searchToolHit{SearchHit: hit}
		if in.IncludeSchema {
			detail := describeTool(hit.Server, byOrigin[Route{Server: hit.Server, Tool: hit.Tool}], hit.Exposed)
			result.Title = detail.Title
			result.InputSchema = detail.InputSchema
			result.Annotations = detail.Annotations
		}
		out.Hits = append(out.Hits, result)
	}
	return out, nil
}

// requireServer reports a usable error when a server is unknown or not
// currently connected, since those need different responses from the
// caller.
func requireServer(ups Upstreams, server string) error {
	if server == "" {
		return fmt.Errorf("no server was named")
	}
	for _, status := range ups.Statuses() {
		if status.Name != server {
			continue
		}
		if !status.Connected() {
			// The state says the tools are out of reach; the recorded error
			// says why, which is what a caller can relay to a person who
			// could fix it.
			if status.Error != "" {
				return fmt.Errorf("server %q is %s (%s), so its tools are unavailable",
					server, status.State, status.Error)
			}
			return fmt.Errorf("server %q is %s, so its tools are unavailable", server, status.State)
		}
		return nil
	}

	known := make([]string, 0)
	for _, status := range ups.Statuses() {
		known = append(known, status.Name)
	}
	return unknownServer(server, known)
}

func unknownServer(server string, known []string) error {
	// Reached for the gateway's own name only where it genuinely is not a
	// server: call_tool forwards to upstreams; the gateway tools are called directly.
	if server == Name {
		return fmt.Errorf("%s is this gateway itself rather than one of the servers it proxies; "+
			"its own tools are in the tool list and are called directly, and search_tools and get_tool_details "+
			"take %q as a server name if you want to see them", Name, Name)
	}
	if len(known) == 0 {
		return fmt.Errorf("no server named %q; no servers are configured", server)
	}
	return fmt.Errorf("no server named %q; the configured servers are %s", server, joinNames(known))
}

// withSystemToolHint supplies the half of the answer the caller actually
// needed, when it asked some server for one of the gateway's own tools.
//
// Listing the configured servers tells a caller that its guess was wrong.
// It does not tell it the thing it is looking for needs no server at all —
// so a caller that guessed the gateway's own name learns nothing it can
// act on, and goes on guessing. The check is on the tool rather than on
// the name guessed at, because the gateway cannot know what a client calls
// it, but it does know which tools are its own.
//
// Only reached once the server has already been rejected: an upstream
// server is free to have a tool called list_servers, and asking that
// server for it must still work.
func withSystemToolHint(err error, tool string) error {
	if !IsSystemTool(tool) {
		return err
	}
	return fmt.Errorf("%w; note that %q is one of this gateway's own tools — it is already in the tool list, "+
		"call it directly and pass no server name", err, tool)
}

// toolError turns a failure into a result the model can read and act on,
// rather than a protocol error that it cannot see.
func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:          title,
		ReadOnlyHint:   true,
		IdempotentHint: true,
	}
}

func boolPtr(b bool) *bool { return &b }

func joinNames(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	}
	return fmt.Sprintf("%s and %s",
		joinWithCommas(quoted[:len(quoted)-1]), quoted[len(quoted)-1])
}

func joinWithCommas(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
