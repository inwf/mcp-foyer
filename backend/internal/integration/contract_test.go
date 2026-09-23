package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/gateway"
)

// ===== initialize =====

// What a client learns from the handshake decides what it will attempt,
// so the gateway has to declare itself accurately.
func TestInitializeHandshake(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	result := session.InitializeResult()
	if result == nil {
		t.Fatal("the session carries no initialize result")
	}

	if result.ProtocolVersion == "" {
		t.Error("no protocol version was negotiated")
	}
	if result.ServerInfo == nil {
		t.Fatal("no server info was returned")
	}
	if result.ServerInfo.Name != "mcp-foyer" {
		t.Errorf("server name = %q, want mcp-foyer", result.ServerInfo.Name)
	}
	if result.ServerInfo.Version != "test" {
		t.Errorf("server version = %q, want the version it was built with", result.ServerInfo.Version)
	}
}

func TestInitializeDeclaresCapabilities(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	capabilities := session.InitializeResult().Capabilities
	if capabilities == nil {
		t.Fatal("no capabilities were declared")
	}
	// Declaring these is what tells a client it may call tools/list and
	// resources/list at all.
	if capabilities.Tools == nil {
		t.Error("the tools capability is not declared")
	}
	if capabilities.Resources == nil {
		t.Error("the resources capability is not declared")
	}
}

// The instructions are the first thing a model reads, and they are what
// point it at the discovery tools rather than leaving it to guess.
func TestInitializeCarriesInstructions(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	instructions := session.InitializeResult().Instructions
	if instructions == "" {
		t.Fatal("no instructions were returned")
	}
	for _, want := range []string{gateway.ToolListServers, gateway.ToolSearchTools, gateway.ToolGetToolDetails} {
		if !strings.Contains(instructions, want) {
			t.Errorf("instructions do not mention %q:\n%s", want, instructions)
		}
	}
}

// ===== tools/list =====

func TestToolsListIncludesSystemAndUpstreamTools(t *testing.T) {
	stack := start(t, map[string]string{"files": "full", "extra": "tools-only"})
	session := stack.connect(t)

	names := toolNames(t, session)

	for _, want := range gateway.SystemToolNames {
		if !contains(names, want) {
			t.Errorf("system tool %q is missing from %v", want, names)
		}
	}
	// Every upstream tool appears under its server's prefix.
	for _, want := range []string{
		"files_echo", "files_sleep", "files_grow", "files_fail",
		"extra_echo", "extra_sleep", "extra_grow", "extra_fail",
	} {
		if !contains(names, want) {
			t.Errorf("upstream tool %q is missing from %v", want, names)
		}
	}
}

// A client builds its call from the schema, so it has to arrive intact
// across the whole chain.
func TestToolsListCarriesUpstreamSchemas(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	var echo *mcp.Tool
	for _, tool := range listed.Tools {
		if tool.Name == "files_echo" {
			echo = tool
		}
	}
	if echo == nil {
		t.Fatal("files_echo was not listed")
	}
	if echo.InputSchema == nil {
		t.Fatal("files_echo has no input schema")
	}

	encoded, err := json.Marshal(echo.InputSchema)
	if err != nil {
		t.Fatalf("marshal the schema: %v", err)
	}
	// The upstream tool takes a message, and that has to survive.
	if !strings.Contains(string(encoded), "message") {
		t.Errorf("schema %s does not describe the message argument", encoded)
	}
}

func TestToolsListNamesTheSourceServer(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	for _, tool := range listed.Tools {
		if tool.Name != "files_echo" {
			continue
		}
		if !strings.Contains(tool.Description, "files") {
			t.Errorf("description %q does not name the server it came from", tool.Description)
		}
		return
	}
	t.Fatal("files_echo was not listed")
}

// ===== tools/call =====

func TestCallingAForwardedTool(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "files_echo",
		Arguments: map[string]any{"message": "through the gateway"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("the call failed: %s", resultText(result))
	}
	// The argument reached the real child process and came back.
	if got := resultText(result); got != "through the gateway" {
		t.Errorf("result = %q, want the message that was sent", got)
	}
}

func TestCallingASystemTool(t *testing.T) {
	stack := start(t, map[string]string{"files": "full", "extra": "tools-only"})
	session := stack.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolListServers})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("the call failed: %s", resultText(result))
	}

	var out struct {
		Servers []gateway.ServerSummary `json:"servers"`
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal the structured content: %v", err)
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("decode %s: %v", encoded, err)
	}

	if len(out.Servers) != 2 {
		t.Fatalf("listed %d servers, want 2: %+v", len(out.Servers), out.Servers)
	}
	for _, server := range out.Servers {
		if server.State != "connected" {
			t.Errorf("server %s is %q, want connected", server.Name, server.State)
		}
	}
}

// Routing through call_tool has to reach the same place as calling the
// forwarded tool directly.
func TestCallingThroughTheCallToolTool(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: gateway.ToolCallTool,
		Arguments: map[string]any{
			"server": "files",
			"tool":   "echo",
			"args":   map[string]any{"message": "indirect"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("the call failed: %s", resultText(result))
	}
	if got := resultText(result); got != "indirect" {
		t.Errorf("result = %q, want indirect", got)
	}
}

// An upstream tool reporting an error must arrive as an error the model
// can read, not as a protocol failure it never sees.
func TestAnUpstreamToolErrorReachesTheClient(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "files_fail"})
	if err != nil {
		t.Fatalf("CallTool returned a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("a failing tool reported success")
	}
	if got := resultText(result); !strings.Contains(got, "always fails") {
		t.Errorf("result = %q, want the upstream explanation", got)
	}
}

func TestCallingAToolThatDoesNotExist(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "files_teleport"})

	// Either layer may reject it; what matters is that the client is
	// told rather than left waiting.
	if err == nil && !result.IsError {
		t.Fatal("calling a tool that does not exist succeeded")
	}
}

// ===== resources =====

func TestReadingAnUpstreamResourceThroughTheGateway(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: gateway.ForwardedResourceURI("files", "test://greeting"),
	})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(result.Contents) == 0 {
		t.Fatal("the resource has no contents")
	}
	if got := result.Contents[0].Text; got != "hello" {
		t.Errorf("contents = %q, want the text from the upstream server", got)
	}
}

func TestListingResourcesAcrossServers(t *testing.T) {
	stack := start(t, map[string]string{"files": "full", "extra": "tools-only"})
	session := stack.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listed, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}

	var uris []string
	for _, resource := range listed.Resources {
		uris = append(uris, resource.URI)
	}

	// Both servers are described, but only the one that offers a
	// resource contributes a forwarded one.
	for _, want := range []string{
		gateway.ServerResourceURI("files"),
		gateway.ServerResourceURI("extra"),
		gateway.ForwardedResourceURI("files", "test://greeting"),
	} {
		if !contains(uris, want) {
			t.Errorf("resource %q is missing from %v", want, uris)
		}
	}
}
