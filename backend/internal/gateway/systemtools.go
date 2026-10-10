package gateway

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/upstream"
)

const (
	ToolListServers    = "list_servers"
	ToolSearchTools    = "search_tools"
	ToolGetToolDetails = "get_tool_details"
	ToolCallTool       = "call_tool"
)

// Name identifies the gateway in the handshake and in discovery calls
// that ask about its own tools.
const Name = "mcp-foyer"

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

// Usage receives what the models connected to the gateway do with its
// tools. A nil Usage counts nothing.
//
// It is an interface so that the gateway does not decide where counts
// go; [usage.Counter] is the implementation the binary uses.
type Usage interface {
	// Searched is told of every hit search_tools returned to a model.
	Searched(server, tool string)
	// Called is told of every call a model made, and whether it failed.
	Called(server, tool string, failed bool)
}

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
	Server string `json:"server" jsonschema:"exact server name as listed by list_servers, or mcp-foyer for this endpoint's own tools"`
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
	Query         string `json:"query,omitempty" jsonschema:"words describing the capability, matched against tool and server names and descriptions and parameter names, in any spelling (readFile finds read_file); a word matching one server's name searches that server alone; rarer words weigh more and a word matching nothing is ignored, not fatal. The directory may mix languages, so when unsure give the words in both. Supply query or server"`
	Server        string `json:"server,omitempty" jsonschema:"exact server name to search within, or to browse when query is omitted; mcp-foyer for this endpoint's own tools"`
	Limit         *int   `json:"limit,omitempty" jsonschema:"maximum number of results, from 1 to 20; default 5"`
	IncludeSchema bool   `json:"includeSchema,omitempty" jsonschema:"also return each hit's complete input schema and annotations, so a call can be prepared from this one result; default false. Combine with a limit of 1 to 3"`
	Cursor        string `json:"cursor,omitempty" jsonschema:"nextCursor from the previous result, with the same query and server, to get the next page"`
}

// The management API keeps SearchHit's existing shape. Schema-bearing
// discovery results extend it only on the system tool surface.
type searchToolHit struct {
	SearchHit

	// RequiredArgs names the arguments a call has to provide, from the
	// tool's cached input schema. It is what decides whether the schema
	// must be fetched before calling, so it rides on every hit rather
	// than behind includeSchema; the names are small and bounded by the
	// page. Absent when the schema names none or cannot be read, which
	// is also what a tool taking no arguments reports.
	RequiredArgs []string `json:"requiredArgs,omitempty"`

	Title       string               `json:"title,omitempty"`
	InputSchema map[string]any       `json:"inputSchema,omitempty"`
	Annotations *mcp.ToolAnnotations `json:"annotations,omitempty"`
}

type searchToolsOutput struct {
	Query string `json:"query,omitempty"`

	// Server is the server the search was limited to: the one the caller
	// named, or the one its query named. Empty for a cross-server search.
	Server     string          `json:"server,omitempty"`
	Hits       []searchToolHit `json:"hits"`
	NextCursor string          `json:"nextCursor,omitempty"`
	Unmatched  []string        `json:"unmatched,omitempty"`

	// Unavailable names the servers a cross-server search could not look
	// at. Without it, "the directory has no such tool" and "the server
	// that had one is down" read the same, and the caller stops instead
	// of waiting or asking for the server to be fixed.
	Unavailable []serverState `json:"unavailable,omitempty"`

	// UnavailableMore counts the down servers past the ones named, so
	// that a directory with dozens down does not put dozens of entries
	// into every result.
	UnavailableMore int `json:"unavailableMore,omitempty"`
}

// serverState is one server a search skipped, and where it stands. The
// recorded reason stays out: that text can be long, this list rides in
// every cross-server result, and list_servers and browsing the one
// server already carry it.
type serverState struct {
	Name  string `json:"name"`
	State string `json:"state"`
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
func RegisterSystemTools(server *mcp.Server, ups Upstreams, cfgs Configs, sem Semantics, own OwnTools, use Usage) {
	if use == nil {
		use = noUsage{}
	}
	mcp.AddTool(server, &mcp.Tool{
		Name: ToolListServers,
		Description: "Overview of the servers behind this endpoint: each one's name, state, description, " +
			"tool and resource counts, and the error when it is down. " +
			"Use it to see which servers are connected and which are broken; to find a tool, search_tools " +
			"matches the capability directly, and search_tools(server) lists what one server offers.",
		Annotations: readOnly("List servers"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listServersInput) (*mcp.CallToolResult, listServersOutput, error) {
		return nil, listServers(ups, cfgs), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: ToolSearchTools,
		Description: "Find tools on the servers behind this endpoint by describing the capability, or browse one server by giving " +
			"server alone. Most tools are not in this tool list and are only reachable this way; every hit can then be called " +
			"with call_tool(server, tool, args). Results are ranked best first, five per page by default, each hit naming " +
			"the arguments the tool requires; includeSchema=true returns each hit's complete input schema so the call can " +
			"be prepared from the same result. " +
			"Words that matched nothing are reported in unmatched; words too common to tell tools apart " +
			"(the, what, my) are ignored, so a query made only of them comes back empty and names them. " +
			"A query naming one server searches that server alone and reports it in server; " +
			"a cross-server search spreads its hits, at most two per server unless only one matched; " +
			"browse one server to see all it has. " +
			"Servers that are not connected are named in unavailable, up to five, with unavailableMore counting the rest; " +
			"browsing one gives the reason.",
		Annotations: readOnly("Search tools"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchToolsInput) (*mcp.CallToolResult, searchToolsOutput, error) {
		out, err := searchTools(ctx, ups, cfgs, sem, own, in)
		if err != nil {
			return toolError(err), searchToolsOutput{}, nil
		}
		// What the model saw is the page, not everything that matched.
		// The gateway's own tools are not counted: they are always there.
		if in.Server != Name {
			for _, hit := range out.Hits {
				use.Searched(hit.Server, hit.Tool)
			}
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
		Description: "Call a tool on one of the servers behind this endpoint by server name, tool name and arguments, " +
			"whether or not the tool is in this tool list. The arguments are checked against the tool's input schema " +
			"before forwarding, and the result is returned as the tool produced it. " +
			"Skip discovery when server, tool and arguments are already known. " +
			"This endpoint's own four tools are called directly, not through call_tool.",
		Annotations: &mcp.ToolAnnotations{
			Title:         "Call a tool on a server",
			OpenWorldHint: boolPtr(true),
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in callToolInput) (*mcp.CallToolResult, any, error) {
		result, err := callTool(ctx, ups, cfgs, use, in)
		if err != nil {
			return toolError(err), nil, nil
		}
		return result, nil, nil
	})
}

// noUsage counts nothing, for a gateway built without a counter.
type noUsage struct{}

func (noUsage) Searched(string, string)     {}
func (noUsage) Called(string, string, bool) {}

func listServers(ups Upstreams, cfgs Configs) listServersOutput {
	cfg := cfgs.Get()
	statuses := ups.Statuses()
	all := ups.Tools()
	out := listServersOutput{Servers: make([]ServerSummary, 0, len(statuses))}
	for _, status := range statuses {
		summary := ServerSummary{
			Name:          status.Name,
			State:         string(status.State),
			Description:   cfg.MCPServers[status.Name].Description,
			ToolCount:     usableCount(all, cfg, status.Name, status.ToolCount),
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
	cfg := cfgs.Get()
	if IsDisabled(cfg, server, tool) {
		return getToolDetailsOutput{}, disabledError(server, tool)
	}
	all := ups.Tools()
	for _, candidate := range all[server] {
		if candidate != nil && candidate.Name == tool {
			exposed, _ := PublishedNames(all, cfg).Exposed(server, tool)
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

func callTool(ctx context.Context, ups Upstreams, cfgs Configs, use Usage, in callToolInput) (*mcp.CallToolResult, error) {
	if err := requireServer(ups, in.Server); err != nil {
		return nil, withSystemToolHint(err, in.Tool)
	}
	// Read at call time: the operator's switch takes effect on the next
	// call, without waiting for anything to be republished.
	if IsDisabled(cfgs.Get(), in.Server, in.Tool) {
		return nil, disabledError(in.Server, in.Tool)
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
	// Counted from here: a call the gateway refused above never reached
	// the tool and is not a use of it, but one the upstream failed is.
	result, err := ups.CallTool(ctx, in.Server, in.Tool, args)
	use.Called(in.Server, in.Tool, err != nil || (result != nil && result.IsError))
	return result, err
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

// ServerNamedIn reports the one connected server a query names, or "" when
// it names none or several.
//
// A caller that writes a server's name into its query has already said
// where to look; answering from every server anyway buries the tools it
// asked about under near-identical ones from elsewhere. The comparison
// absorbs the spellings a caller cannot be expected to keep apart: case,
// and pieces joined or separated ("mywiki" names my-wiki).
func ServerNamedIn(query string, ups Upstreams) string {
	statuses := ups.Statuses()
	named := ""
	for _, word := range strings.Fields(query) {
		for _, status := range statuses {
			if !status.Connected() || !namesServer(word, status.Name) {
				continue
			}
			if named != "" && named != status.Name {
				// Two servers in one query: there is no way to choose
				// between them, so nothing is narrowed.
				return ""
			}
			named = status.Name
		}
	}
	return named
}

// namesServer reports whether one query word is a server's name: the same
// pieces in the same order, or the same pieces joined, so mygit names
// my-git and readfile names read_file.
func namesServer(word, name string) bool {
	wordPieces := tokenizeVerbatim(word)
	namePieces := tokenizeVerbatim(name)
	if len(wordPieces) == 0 || len(namePieces) == 0 {
		return false
	}
	return slices.Equal(wordPieces, namePieces) ||
		strings.Join(wordPieces, "") == strings.Join(namePieces, "")
}

func searchTools(ctx context.Context, ups Upstreams, cfgs Configs, sem Semantics, own OwnTools, in searchToolsInput) (searchToolsOutput, error) {
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
	// A query that names one connected server is answered from it alone:
	// the caller has already said where to look, and a page spread over
	// the other servers would only carry tools it did not ask about.
	if in.Server == "" {
		in.Server = ServerNamedIn(in.Query, ups)
	}

	cfg := cfgs.Get()
	// Disabled tools are not there to be found, by a query or by browsing.
	all := UsableTools(ups.Tools(), cfg)
	if in.Server == Name {
		if own == nil {
			return searchToolsOutput{}, fmt.Errorf("no server named %q", Name)
		}
		all = map[string][]*mcp.Tool{Name: own()}
	}

	var candidates []Searchable
	if in.Server == Name {
		candidates = searchableOwn(all[Name])
	} else {
		candidates = Searchables(ups, cfgs, in.Server)
	}

	// byOrigin is what includeSchema is answered from: the tool an origin
	// points at, so its schema can ride along with the hit.
	byOrigin := make(map[Route]*mcp.Tool)
	for _, server := range slices.Sorted(maps.Keys(all)) {
		for _, tool := range all[server] {
			if tool == nil || tool.Name == "" {
				continue
			}
			origin := Route{Server: server, Tool: tool.Name}
			if _, exists := byOrigin[origin]; exists {
				continue
			}
			byOrigin[origin] = tool
		}
	}
	hits, unmatched := RankTools(ctx, in.Query, candidates, 0, sem)
	// A cross-server page shows each server at most a couple of hits, so
	// that one server's near-identical tools cannot fill it; browsing a
	// server is where a caller sees everything it has. The spread runs
	// before the page is cut and hashed, so cursors page over it.
	if in.Server == "" && spansServers(hits) {
		hits = spreadAcrossServers(hits, hitsPerServer)
	}
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
	// Only a cross-server search can skip servers: browsing one names it,
	// and requireServer has already refused a server that is down.
	if in.Server == "" {
		out.Unavailable, out.UnavailableMore = unavailableServers(ups)
	}
	for _, hit := range hits[start:end] {
		result := searchToolHit{SearchHit: hit}
		tool := byOrigin[Route{Server: hit.Server, Tool: hit.Tool}]
		if tool != nil {
			result.RequiredArgs = requiredArguments(tool.InputSchema)
		}
		if in.IncludeSchema {
			detail := describeTool(hit.Server, tool, hit.Exposed)
			result.Title = detail.Title
			result.InputSchema = detail.InputSchema
			result.Annotations = detail.Annotations
		}
		out.Hits = append(out.Hits, result)
	}
	return out, nil
}

// Searchables builds the candidates a search ranks: every usable tool of
// one server, or of every server when none is named, each carrying the
// text search matches it by.
//
// It is the list search_tools itself ranks, which is what lets the
// settings page's preview show a caller the same results it would get —
// and what keeps the two from drifting apart.
func Searchables(ups Upstreams, cfgs Configs, server string) []Searchable {
	cfg := cfgs.Get()
	all := UsableTools(ups.Tools(), cfg)
	// Names are assigned over the complete exposed set before filtering;
	// otherwise a collision on another server would give the wrong name.
	names := PublishedNames(all, cfg)

	titles := make(map[string]string)
	for _, status := range ups.Statuses() {
		titles[status.Name] = status.ServerName
	}

	servers := []string{server}
	if server == "" {
		// Group ties in their final order before ranking; a broad query
		// should not have to sort randomized map order on every request.
		servers = slices.Sorted(maps.Keys(all))
	}

	capacity := 0
	for _, name := range servers {
		capacity += len(all[name])
	}
	candidates := make([]Searchable, 0, capacity)
	seen := make(map[Route]bool, capacity)
	for _, name := range servers {
		title, description := titles[name], cfg.MCPServers[name].Description
		for _, tool := range all[name] {
			if tool == nil || tool.Name == "" {
				continue
			}
			origin := Route{Server: name, Tool: tool.Name}
			if seen[origin] {
				continue
			}
			seen[origin] = true
			exposed, _ := names.Exposed(name, tool.Name)
			candidates = append(candidates, Searchable{
				Server: name, Tool: tool.Name, Exposed: exposed, Description: tool.Description,
				ServerTitle: title, ServerDescription: description,
				Arguments: ArgumentText(tool.InputSchema),
			})
		}
	}
	return candidates
}

// searchableOwn is the gateway's own tools as candidates: offered under
// their own names, since nothing about them is exposed or hidden.
func searchableOwn(tools []*mcp.Tool) []Searchable {
	out := make([]Searchable, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || tool.Name == "" {
			continue
		}
		out = append(out, Searchable{
			Server: Name, Tool: tool.Name, Exposed: tool.Name, Description: tool.Description,
			Arguments: ArgumentText(tool.InputSchema),
		})
	}
	return out
}

// hitsPerServer bounds what one server contributes to a cross-server
// search. At directory scale the same capability sits on many servers,
// and without the bound one server's near-identical tools can take the
// whole page while the caller never learns the others exist. A search
// that only one server answered is left whole: there is nothing to
// spread to, and hiding the rest of that server's tools would just lose
// them. Browsing a single server is never bounded: that is where a
// caller goes for everything it has.
const hitsPerServer = 2

// spansServers reports whether the hits come from more than one server,
// which is when spreading them is worth anything.
func spansServers(hits []SearchHit) bool {
	first := ""
	for _, hit := range hits {
		if first == "" {
			first = hit.Server
			continue
		}
		if hit.Server != first {
			return true
		}
	}
	return false
}

// spreadAcrossServers keeps at most perServer hits of each server, in
// the order they were ranked. The slice is reused in place: it was built
// for this call and nothing else holds it.
func spreadAcrossServers(hits []SearchHit, perServer int) []SearchHit {
	counts := make(map[string]int)
	out := hits[:0]
	for _, hit := range hits {
		if counts[hit.Server] >= perServer {
			continue
		}
		counts[hit.Server]++
		out = append(out, hit)
	}
	return out
}

// unavailableShown bounds how many down servers a result names. A
// directory of a thousand servers will often have a couple of dozen
// down, and carrying them all in every result costs more than it says.
const unavailableShown = 5

// unavailableServers names the servers a search could not look at, in
// the order list_servers uses, and counts the ones beyond the bound.
func unavailableServers(ups Upstreams) ([]serverState, int) {
	var out []serverState
	more := 0
	for _, status := range ups.Statuses() {
		if status.Connected() {
			continue
		}
		if len(out) == unavailableShown {
			more++
			continue
		}
		out = append(out, serverState{Name: status.Name, State: string(status.State)})
	}
	return out, more
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
		return fmt.Errorf("%s is this endpoint itself rather than one of the servers it proxies; "+
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
	return fmt.Errorf("%w; note that %q is one of this endpoint's own tools — it is already in the tool list, "+
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
