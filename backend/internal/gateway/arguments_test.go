package gateway_test

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/gateway"
)

// A client that reaches a tool through call_tool never saw its schema, so
// nothing on its side checks the arguments. The gateway has the schema,
// and what it says about a bad call is the same every time — which an
// upstream's own complaint, if it makes one, is not.

func TestCallToolRefusesArgumentsMissingARequiredField(t *testing.T) {
	ups := twoServers()
	ups.tools["files"][0].InputSchema = map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required":   []any{"path"},
	}
	session := gatewayFixture(t, ups, twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
		"server": "files", "tool": "read", "args": map[string]any{},
	})

	if !result.IsError {
		t.Fatal("a call missing a required argument was forwarded")
	}
	if len(ups.calls) != 0 {
		t.Errorf("forwarded %v upstream, want nothing", ups.calls)
	}
	// The caller has to learn which field, and where to read the schema.
	text := resultText(result)
	for _, want := range []string{"path", gateway.ToolGetToolDetails} {
		if !strings.Contains(text, want) {
			t.Errorf("error %q does not mention %q", text, want)
		}
	}
}

func TestCallToolRefusesAnArgumentOfTheWrongType(t *testing.T) {
	ups := twoServers()
	session := gatewayFixture(t, ups, twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
		"server": "files", "tool": "read", "args": map[string]any{"path": 42},
	})

	if !result.IsError {
		t.Fatal("a number was forwarded where the schema asks for a string")
	}
	if len(ups.calls) != 0 {
		t.Errorf("forwarded %v upstream, want nothing", ups.calls)
	}
	if text := resultText(result); !strings.Contains(text, "path") {
		t.Errorf("error %q does not name the offending field", text)
	}
}

// Omitting args altogether is a call with none, and a tool whose schema
// requires none accepts it.
func TestCallToolWithoutArgumentsPassesAnEmptySchema(t *testing.T) {
	ups := twoServers()
	session := gatewayFixture(t, ups, twoServersConfig(t))

	result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
		"server": "files", "tool": "read",
	})

	if result.IsError {
		t.Fatalf("a call with no arguments to a tool requiring none was refused: %s", resultText(result))
	}
	if len(ups.calls) != 1 {
		t.Errorf("forwarded %v, want the one call", ups.calls)
	}
}

// The check must never be a new way for a working tool to fail. Whatever
// the gateway cannot judge, the upstream judges.
func TestCallToolForwardsWhatItCannotCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		tools []*mcp.Tool
		tool  string
	}{
		"a tool the cache does not know yet": {
			// The cache can lag a tools/list_changed notification.
			tools: []*mcp.Tool{{Name: "read"}},
			tool:  "grown",
		},
		"a tool with no schema": {
			tools: []*mcp.Tool{{Name: "read"}},
			tool:  "read",
		},
		"a schema the gateway cannot resolve": {
			tools: []*mcp.Tool{{Name: "read", InputSchema: map[string]any{
				"type": "object", "properties": map[string]any{"path": map[string]any{"$ref": "#/$defs/nowhere"}},
			}}},
			tool: "read",
		},
		"a schema version the validator does not support": {
			tools: []*mcp.Tool{{Name: "read", InputSchema: map[string]any{
				"$schema": "http://json-schema.org/draft-04/schema#",
				"type":    "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
			}}},
			tool: "read",
		},
	} {
		t.Run(name, func(t *testing.T) {
			ups := twoServers()
			ups.tools["files"] = tc.tools
			session := gatewayFixture(t, ups, twoServersConfig(t))

			result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
				"server": "files", "tool": tc.tool, "args": map[string]any{"path": 42},
			})

			if result.IsError {
				t.Fatalf("the gateway refused a call it could not judge: %s", resultText(result))
			}
			if len(ups.calls) != 1 {
				t.Errorf("forwarded %v, want the one call", ups.calls)
			}
		})
	}
}
