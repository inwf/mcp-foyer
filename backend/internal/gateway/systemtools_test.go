package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcphub/internal/config"
	"mcphub/internal/gateway"
	"mcphub/internal/upstream"
)

// fakeUpstreams stands in for the connection manager. The system tools
// only read a handful of things from it, and faking those lets every
// error path be provoked without running child processes.
type fakeUpstreams struct {
	statuses  []upstream.Status
	tools     map[string][]*mcp.Tool
	resources map[string][]*mcp.Resource

	// calls records what was forwarded, lastArgs keeps the arguments
	// exactly as they arrived, and callErr forces a failure.
	calls    []string
	lastArgs any
	callErr  error
	result   *mcp.CallToolResult
}

func (f *fakeUpstreams) Statuses() []upstream.Status           { return f.statuses }
func (f *fakeUpstreams) Tools() map[string][]*mcp.Tool         { return f.tools }
func (f *fakeUpstreams) Resources() map[string][]*mcp.Resource { return f.resources }

func (f *fakeUpstreams) CallTool(_ context.Context, server, tool string, args any) (*mcp.CallToolResult, error) {
	f.calls = append(f.calls, server+"/"+tool)
	f.lastArgs = args
	if f.callErr != nil {
		return nil, f.callErr
	}
	if f.result != nil {
		return f.result, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("called %s/%s with %s", server, tool, renderArgs(args))}},
	}, nil
}

// renderArgs shows arguments as JSON whether they arrived decoded or, as
// they do over the wire, as the raw bytes the client sent.
func renderArgs(args any) string {
	switch v := args.(type) {
	case nil:
		return "null"
	case json.RawMessage:
		return string(v)
	case []byte:
		return string(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(encoded)
	}
}

func (f *fakeUpstreams) ReadResource(_ context.Context, server, uri string) (*mcp.ReadResourceResult, error) {
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{URI: uri, Text: "from " + server}},
	}, nil
}

// gatewayFixture starts a real MCP server with the system tools
// registered and returns a connected client session. Going through the
// protocol means the tests cover schema inference and result shapes, not
// just the handler bodies.
func gatewayFixture(t *testing.T, ups gateway.Upstreams, cfgs gateway.Configs) *mcp.ClientSession {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "mcphub", Version: "test"}, nil)
	// Nil own-tools: this fixture registers the tools on a bare server it
	// does not own, so there is nothing to read them back off. Self
	// description is a whole-gateway behaviour and is tested against one.
	gateway.RegisterSystemTools(server, ups, cfgs, nil, nil)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect the server: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect the client: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	return session
}

// configFixture builds a real configuration manager backed by a
// temporary file, so reads use the same defaults and cloning as production.
func configFixture(t *testing.T, servers map[string]config.MCPServer) *config.Manager {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.MCPServers = servers
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save the configuration: %v", err)
	}

	m, err := config.NewManager(path)
	if err != nil {
		t.Fatalf("open the configuration: %v", err)
	}
	return m
}

// callSystemTool invokes one gateway tool and returns the result.
func callSystemTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return result
}

// structured decodes a tool's structured output into target.
func structured(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("the tool reported an error: %s", resultText(result))
	}
	if result.StructuredContent == nil {
		t.Fatalf("the tool returned no structured content; text was %q", resultText(result))
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal the structured content: %v", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
}

func resultText(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

// twoServers is the fixture most tests use: one connected server with
// two tools, one failed server.
// exposing builds a configuration that exposes exactly the named tools.
//
// Nothing is exposed unless it is listed — see gateway.FilterTools for
// why. That default is right for an installation and wrong for a test
// about forwarding: such a test is asking what happens to a tool that is
// on offer, so it has to put the tool on offer first. Spelling the names
// out here is what makes each test say which tools it expects to travel.
func exposing(t *testing.T, byServer map[string][]string) *config.Manager {
	t.Helper()

	servers := make(map[string]config.MCPServer, len(byServer))
	for name, tools := range byServer {
		servers[name] = config.MCPServer{
			Transport:    config.TransportStdio,
			Command:      "npx",
			Enabled:      true,
			Timeout:      time.Minute,
			ExposedTools: tools,
		}
	}
	return configFixture(t, servers)
}

// exposingEverything exposes every tool the given upstreams currently
// offer, for the tests that are not about exposure at all.
func exposingEverything(t *testing.T, ups gateway.Upstreams) *config.Manager {
	t.Helper()

	byServer := map[string][]string{}
	for server, tools := range ups.Tools() {
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			if tool != nil && tool.Name != "" {
				names = append(names, tool.Name)
			}
		}
		byServer[server] = names
	}
	return exposing(t, byServer)
}

func twoServers() *fakeUpstreams {
	return &fakeUpstreams{
		statuses: []upstream.Status{
			{Name: "files", State: upstream.StateConnected, ToolCount: 2, ResourceCount: 1},
			{Name: "broken", State: upstream.StateFailed, Error: "command not found"},
		},
		tools: map[string][]*mcp.Tool{
			"files": {
				{Name: "read", Description: "read a file from disk",
					InputSchema: map[string]any{"type": "object",
						"properties": map[string]any{"path": map[string]any{"type": "string"}}}},
				{Name: "write", Description: "write a file to disk"},
			},
		},
		resources: map[string][]*mcp.Resource{
			"files": {{URI: "file:///tmp/a", Name: "a"}},
		},
	}
}

// twoServersConfig exposes "read" but not "write", so that tests can see
// both an exposed tool and an unexposed one — which is what an ordinary
// installation looks like, and what the reported exposed name has to
// distinguish.
func twoServersConfig(t *testing.T) *config.Manager {
	return configFixture(t, map[string]config.MCPServer{
		"files": {
			Transport: config.TransportStdio, Command: "npx", Enabled: true,
			Timeout: time.Minute, Description: "local filesystem",
			ExposedTools: []string{"read"},
		},
		"broken": {
			Transport: config.TransportStdio, Command: "nope", Enabled: true,
			Timeout: time.Minute,
		},
	})
}

func TestEverySystemToolIsRegistered(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	found := map[string]bool{}
	for _, tool := range listed.Tools {
		found[tool.Name] = true
	}
	for _, want := range gateway.SystemToolNames {
		if !found[want] {
			t.Errorf("tool %q is not registered; have %v", want, slices.Sorted(maps.Keys(found)))
		}
	}
}

// The schemas are inferred from Go types, so this checks the inference
// produced something a client can actually use.
func TestSystemToolSchemasAreUsable(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	for _, tool := range listed.Tools {
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Errorf("tool %q has an unmarshalable schema: %v", tool.Name, err)
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Errorf("tool %q schema is not an object: %v", tool.Name, err)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("tool %q schema type = %v, want object", tool.Name, schema["type"])
		}
	}
}

func TestListServers(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	var out struct {
		Servers []gateway.ServerSummary `json:"servers"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolListServers, nil), &out)

	if len(out.Servers) != 2 {
		t.Fatalf("listed %d servers, want 2: %+v", len(out.Servers), out.Servers)
	}

	byName := map[string]gateway.ServerSummary{}
	for _, s := range out.Servers {
		byName[s.Name] = s
	}

	files := byName["files"]
	if files.State != string(upstream.StateConnected) {
		t.Errorf("files state = %q, want connected", files.State)
	}
	if files.Description != "local filesystem" {
		t.Errorf("files description = %q, want it from the configuration", files.Description)
	}
	if files.ToolCount != 2 {
		t.Errorf("files toolCount = %d, want 2", files.ToolCount)
	}

	// A failed server is still listed, with its explanation: knowing it
	// exists and why it is down is the point.
	broken := byName["broken"]
	if broken.State != string(upstream.StateFailed) {
		t.Errorf("broken state = %q, want failed", broken.State)
	}
	if broken.Error == "" {
		t.Error("broken carries no error message")
	}
	// A missing description stays absent even for a failed server.
	if broken.Description != "" {
		t.Errorf("broken description = %q, want none for a server that is down", broken.Description)
	}
}

// An overview is counts, not contents. A hundred servers answering with
// their tool names would put a directory into the context on every call,
// which is the thing this gateway exists to avoid; what one server offers
// is a question for search_tools(server).
func TestListServersDoesNotNameTools(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	var out struct {
		Servers []map[string]any `json:"servers"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolListServers, nil), &out)

	for _, server := range out.Servers {
		if _, exists := server["tools"]; exists {
			t.Errorf("server %v lists its tools; an overview carries counts only", server["name"])
		}
	}
}

// The configured name is a shorthand somebody chose for the file. When a
// server calls itself something else, that is the more useful name for
// deciding whether to look inside — and the handshake has already said it.
func TestListServersReportsWhatAServerCallsItself(t *testing.T) {
	ups := twoServers()
	ups.statuses[0].ServerName = "the local filesystem server"
	session := gatewayFixture(t, ups, twoServersConfig(t))

	var out struct {
		Servers []gateway.ServerSummary `json:"servers"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolListServers, nil), &out)

	for _, server := range out.Servers {
		switch server.Name {
		case "files":
			if server.Title != "the local filesystem server" {
				t.Errorf("files title = %q, want what the server called itself", server.Title)
			}
		case "broken":
			// Nothing was learned from a server that never connected, and
			// an empty title is not worth a field.
			if server.Title != "" {
				t.Errorf("broken title = %q, want none", server.Title)
			}
		}
	}
}

// A server that calls itself exactly what it is configured as adds nothing
// by saying so twice.
func TestAServerThatAgreesWithItsConfiguredNameHasNoTitle(t *testing.T) {
	ups := twoServers()
	ups.statuses[0].ServerName = "files"
	session := gatewayFixture(t, ups, twoServersConfig(t))

	var out struct {
		Servers []gateway.ServerSummary `json:"servers"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolListServers, nil), &out)

	for _, server := range out.Servers {
		if server.Name == "files" && server.Title != "" {
			t.Errorf("files title = %q, want it left out when it repeats the name", server.Title)
		}
	}
}

func TestAnUndescribedServerDoesNotRepeatInstructions(t *testing.T) {
	session := gatewayFixture(t, twoServers(), exposingEverything(t, twoServers()))
	var out struct {
		Servers []map[string]any `json:"servers"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolListServers, nil), &out)
	for _, server := range out.Servers {
		if _, exists := server["description"]; exists {
			t.Errorf("an undescribed server carries a description: %v", server)
		}
		if _, exists := server["tags"]; exists {
			t.Errorf("server tags are still returned: %v", server)
		}
	}
}

func TestBrowsingTools(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	var out struct {
		Server string              `json:"server"`
		Hits   []gateway.SearchHit `json:"hits"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolSearchTools,
		map[string]any{"server": "files"}), &out)

	if out.Server != "files" {
		t.Errorf("server = %q, want files", out.Server)
	}
	if len(out.Hits) != 2 {
		t.Fatalf("listed %d tools, want 2: %+v", len(out.Hits), out.Hits)
	}
	// Sorted, so a repeated call reads the same.
	if out.Hits[0].Tool != "read" || out.Hits[1].Tool != "write" {
		t.Errorf("tools = %+v, want read then write", out.Hits)
	}
	// The exposed name is what a client would call directly.
	if out.Hits[0].Exposed != "files_read" {
		t.Errorf("exposed = %q, want files_read", out.Hits[0].Exposed)
	}
	// "write" is not exposed, so there is no name to call it by. The empty
	// field is the signal to reach it through call_tool instead — and it
	// must be empty rather than a name that was never registered.
	if out.Hits[1].Exposed != "" {
		t.Errorf("exposed = %q for an unexposed tool, want no name", out.Hits[1].Exposed)
	}
}

func TestBrowsingToolsOnAnUnknownServer(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolSearchTools,
		map[string]any{"server": "ghost"})

	if !result.IsError {
		t.Fatal("listing tools on a server that does not exist succeeded")
	}
	text := resultText(result)
	if !strings.Contains(text, "ghost") {
		t.Errorf("error %q does not name the server that was asked for", text)
	}
	// Telling the model what does exist is what lets it recover.
	if !strings.Contains(text, "files") {
		t.Errorf("error %q does not list the servers that do exist", text)
	}
}

// A configured but disconnected server needs a different answer from one
// that does not exist: the fix is to connect it, not to rename it.
func TestBrowsingToolsOnADisconnectedServer(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolSearchTools,
		map[string]any{"server": "broken"})

	if !result.IsError {
		t.Fatal("listing tools on a failed server succeeded")
	}
	text := resultText(result)
	if !strings.Contains(text, "failed") {
		t.Errorf("error %q does not say the server is not connected", text)
	}
	// "Unavailable" is a dead end; the recorded reason is something the
	// caller can relay to whoever can fix it.
	if !strings.Contains(text, "command not found") {
		t.Errorf("error %q does not say why the server is down", text)
	}
}

func TestGetTool(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	var out struct {
		Server      string `json:"server"`
		Tool        string `json:"tool"`
		Exposed     string `json:"exposed"`
		Description string `json:"description"`
		InputSchema any    `json:"inputSchema"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolGetToolDetails,
		map[string]any{"server": "files", "tool": "read"}), &out)

	if out.Tool != "read" || out.Server != "files" {
		t.Errorf("got %s/%s, want files/read", out.Server, out.Tool)
	}
	if out.Exposed != "files_read" {
		t.Errorf("exposed = %q, want files_read", out.Exposed)
	}
	// The schema is the reason to call this tool at all.
	if out.InputSchema == nil {
		t.Fatal("no input schema was returned")
	}
	schema, ok := out.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("input schema is %T, want an object", out.InputSchema)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}

func TestGetToolThatDoesNotExist(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolGetToolDetails,
		map[string]any{"server": "files", "tool": "teleport"})

	if !result.IsError {
		t.Fatal("getting a tool that does not exist succeeded")
	}
	if text := resultText(result); !strings.Contains(text, "teleport") {
		t.Errorf("error %q does not name the tool", text)
	}
}

// A client knows the gateway by whatever name its own configuration gives
// it, which the gateway never sees. So a caller looking for call_tool's
// schema will guess a server name, and the guess will be wrong.
//
// Listing the configured servers answers "that name is not one of these".
// It does not answer the question actually being asked, and a caller that
// learns nothing from an error goes on making the same one.
func TestAskingSomeServerForAGatewayToolExplainsItself(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolGetToolDetails,
		map[string]any{"server": "my-mcp-gateway", "tool": gateway.ToolCallTool})

	if !result.IsError {
		t.Fatal("a made-up server name succeeded")
	}
	text := resultText(result)
	if !strings.Contains(text, "call it directly") {
		t.Errorf("error %q does not say how to reach a gateway tool", text)
	}
	// And it still says what the real servers are, which is the half that
	// was already right.
	if !strings.Contains(text, "files") {
		t.Errorf("error %q no longer names the configured servers", text)
	}
}

// The hint is about the gateway's own tools, not about the name that was
// guessed at — so an upstream server that happens to publish a tool called
// list_servers must still be served, not lectured.
func TestAnUpstreamToolMayShareAGatewayToolName(t *testing.T) {
	ups := twoServers()
	ups.tools["files"] = append(ups.tools["files"],
		&mcp.Tool{Name: gateway.ToolListServers, Description: "list the files server's own servers"})
	session := gatewayFixture(t, ups, twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolGetToolDetails,
		map[string]any{"server": "files", "tool": gateway.ToolListServers})

	if result.IsError {
		t.Fatalf("an upstream tool with a gateway tool's name was refused: %s", resultText(result))
	}
	var out struct {
		Description string `json:"description"`
	}
	structured(t, result, &out)
	if out.Description != "list the files server's own servers" {
		t.Errorf("description = %q, want the upstream tool's own", out.Description)
	}
}

func TestCallTool(t *testing.T) {
	ups := twoServers()
	session := gatewayFixture(t, ups, twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
		"server": "files",
		"tool":   "read",
		"args":   map[string]any{"path": "/tmp/x"},
	})

	if result.IsError {
		t.Fatalf("call failed: %s", resultText(result))
	}
	if want := []string{"files/read"}; !slices.Equal(ups.calls, want) {
		t.Errorf("forwarded %v, want %v", ups.calls, want)
	}
	if text := resultText(result); !strings.Contains(text, "/tmp/x") {
		t.Errorf("result %q does not show the arguments reaching the tool", text)
	}
}

func TestCallToolWithoutArguments(t *testing.T) {
	ups := twoServers()
	session := gatewayFixture(t, ups, twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
		"server": "files",
		"tool":   "write",
	})

	if result.IsError {
		t.Fatalf("call failed: %s", resultText(result))
	}
	if want := []string{"files/write"}; !slices.Equal(ups.calls, want) {
		t.Errorf("forwarded %v, want %v", ups.calls, want)
	}
}

// Reaching a gateway tool through call_tool would be a pointless
// indirection, and asking for call_tool itself would recurse.
func TestCallToolRefusesTheGatewaysOwnTools(t *testing.T) {
	ups := twoServers()
	session := gatewayFixture(t, ups, twoServersConfig(t))

	for _, name := range gateway.SystemToolNames {
		result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
			"server": gateway.Name,
			"tool":   name,
		})

		if !result.IsError {
			t.Errorf("call_tool accepted the gateway tool %q", name)
			continue
		}
		if text := resultText(result); !strings.Contains(text, "directly") {
			t.Errorf("error for %q was %q, want it to say to call the tool directly", name, text)
		}
	}
	if len(ups.calls) != 0 {
		t.Errorf("forwarded %v upstream, want nothing", ups.calls)
	}
}

func TestCallToolOnAnUnknownServer(t *testing.T) {
	ups := twoServers()
	session := gatewayFixture(t, ups, twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
		"server": "ghost", "tool": "read",
	})

	if !result.IsError {
		t.Fatal("call_tool accepted a server that does not exist")
	}
	if len(ups.calls) != 0 {
		t.Errorf("forwarded %v upstream, want nothing", ups.calls)
	}
}

// An upstream failure has to reach the model as something it can read,
// not vanish into a protocol error.
func TestCallToolReportsAnUpstreamFailure(t *testing.T) {
	ups := twoServers()
	ups.callErr = errors.New("the disk is on fire")
	session := gatewayFixture(t, ups, twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
		"server": "files", "tool": "read",
	})

	if !result.IsError {
		t.Fatal("an upstream failure was reported as success")
	}
	if text := resultText(result); !strings.Contains(text, "disk is on fire") {
		t.Errorf("error %q does not carry the upstream explanation", text)
	}
}

func TestSearchTools(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	var out struct {
		Query string              `json:"query"`
		Hits  []gateway.SearchHit `json:"hits"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolSearchTools,
		map[string]any{"query": "read"}), &out)

	if len(out.Hits) == 0 {
		t.Fatal("no hits for a query naming an existing tool")
	}
	if out.Hits[0].Tool != "read" {
		t.Errorf("best hit = %+v, want the read tool", out.Hits[0])
	}
	if out.Hits[0].Server != "files" {
		t.Errorf("best hit server = %q, want files", out.Hits[0].Server)
	}
}

func TestSearchToolsRespectsTheLimit(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	var out struct {
		Hits []gateway.SearchHit `json:"hits"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolSearchTools,
		map[string]any{"query": "disk", "limit": 1}), &out)

	if len(out.Hits) != 1 {
		t.Errorf("returned %d hits, want 1", len(out.Hits))
	}
}

func TestSearchToolsFindsNothing(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))

	var out struct {
		Hits []gateway.SearchHit `json:"hits"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolSearchTools,
		map[string]any{"query": "kubernetes"}), &out)

	if len(out.Hits) != 0 {
		t.Errorf("returned %+v, want nothing", out.Hits)
	}
}

func TestIsSystemTool(t *testing.T) {
	for _, name := range gateway.SystemToolNames {
		if !gateway.IsSystemTool(name) {
			t.Errorf("IsSystemTool(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "read", "files_read", "list_server"} {
		if gateway.IsSystemTool(name) {
			t.Errorf("IsSystemTool(%q) = true, want false", name)
		}
	}
}

// With nothing configured the tools must still answer, so a fresh
// installation is explorable rather than broken.
func TestSystemToolsWithNoServers(t *testing.T) {
	empty := &fakeUpstreams{}
	session := gatewayFixture(t, empty, configFixture(t, map[string]config.MCPServer{}))

	var servers struct {
		Servers []gateway.ServerSummary `json:"servers"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolListServers, nil), &servers)
	if len(servers.Servers) != 0 {
		t.Errorf("listed %+v, want nothing", servers.Servers)
	}

	var hits struct {
		Hits []gateway.SearchHit `json:"hits"`
	}
	structured(t, callSystemTool(t, session, gateway.ToolSearchTools,
		map[string]any{"query": "anything"}), &hits)
	if len(hits.Hits) != 0 {
		t.Errorf("search returned %+v, want nothing", hits.Hits)
	}

	result := callSystemTool(t, session, gateway.ToolSearchTools, map[string]any{"server": "any"})
	if !result.IsError {
		t.Error("listing tools with no servers configured succeeded")
	}
	if text := resultText(result); !strings.Contains(text, "no servers are configured") {
		t.Errorf("error %q does not explain that nothing is configured", text)
	}
}
