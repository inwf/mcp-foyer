package gateway_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/gateway"
)

func TestAggregateExposesEveryTool(t *testing.T) {
	agg := gateway.BuildAggregate(toolSet(map[string][]string{
		"files":  {"read", "write"},
		"search": {"query"},
	}))

	if len(agg.Tools) != 3 {
		t.Fatalf("exposed %d tools, want 3", len(agg.Tools))
	}
	var names []string
	for _, tool := range agg.Tools {
		names = append(names, tool.Name)
	}
	if want := []string{"files_read", "files_write", "search_query"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

// A client needs the upstream author's original schema to build a valid
// call, so it has to survive aggregation byte for byte.
func TestAggregatePassesInputSchemasThroughUnchanged(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":      map[string]any{"type": "string", "description": "where to read"},
			"maxLines":  map[string]any{"type": "integer", "minimum": float64(1)},
			"recursive": map[string]any{"type": "boolean"},
		},
		"required":             []any{"path"},
		"additionalProperties": false,
	}

	agg := gateway.BuildAggregate(map[string][]*mcp.Tool{
		"files": {{Name: "read", InputSchema: schema}},
	})

	if len(agg.Tools) != 1 {
		t.Fatalf("exposed %d tools, want 1", len(agg.Tools))
	}

	before, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal the original schema: %v", err)
	}
	after, err := json.Marshal(agg.Tools[0].InputSchema)
	if err != nil {
		t.Fatalf("marshal the exposed schema: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("the schema changed\nbefore: %s\nafter:  %s", before, after)
	}
}

// The upstream connection owns the tools it caches. Aggregating must not
// write to them, or the connection would start reporting the gateway's
// renamed versions as its own.
func TestAggregateDoesNotMutateUpstreamTools(t *testing.T) {
	original := &mcp.Tool{Name: "read", Description: "reads a file"}
	tools := map[string][]*mcp.Tool{"files": {original}}

	gateway.BuildAggregate(tools)

	if original.Name != "read" {
		t.Errorf("upstream tool name became %q", original.Name)
	}
	if original.Description != "reads a file" {
		t.Errorf("upstream tool description became %q", original.Description)
	}
}

// Three tools that all claim to search something are only tellable apart
// by where they came from.
func TestAggregateNamesTheSourceServer(t *testing.T) {
	agg := gateway.BuildAggregate(map[string][]*mcp.Tool{
		"files": {{Name: "read", Description: "reads a file"}},
		"blank": {{Name: "read"}},
	})

	byName := map[string]*mcp.Tool{}
	for _, tool := range agg.Tools {
		byName[tool.Name] = tool
	}

	if got := byName["files_read"].Description; !strings.Contains(got, "files") {
		t.Errorf("description = %q, want it to name the server", got)
	}
	if got := byName["files_read"].Description; !strings.Contains(got, "reads a file") {
		t.Errorf("description = %q, want it to keep the original text", got)
	}
	if got := byName["blank_read"].Description; !strings.Contains(got, "blank") {
		t.Errorf("a tool with no description got %q, want it to name the server", got)
	}
}

// A list in a client must not reshuffle between calls.
func TestAggregateOrderIsStable(t *testing.T) {
	spec := map[string][]string{
		"zulu": {"c", "a"}, "alpha": {"b"}, "mike": {"z", "y"},
	}

	first := gateway.BuildAggregate(toolSet(spec))
	for i := 0; i < 25; i++ {
		again := gateway.BuildAggregate(toolSet(spec))
		if len(again.Tools) != len(first.Tools) {
			t.Fatalf("run %d exposed %d tools, first run exposed %d",
				i, len(again.Tools), len(first.Tools))
		}
		for j := range first.Tools {
			if again.Tools[j].Name != first.Tools[j].Name {
				t.Fatalf("run %d position %d is %q, first run had %q",
					i, j, again.Tools[j].Name, first.Tools[j].Name)
			}
		}
	}
}

func TestAggregateCarriesTheNameMap(t *testing.T) {
	agg := gateway.BuildAggregate(toolSet(map[string][]string{"files": {"read"}}))

	route, ok := agg.Names.Route("files_read")
	if !ok {
		t.Fatal("the aggregate's name map has no route for files_read")
	}
	if route.Server != "files" || route.Tool != "read" {
		t.Errorf("route = %+v, want files/read", route)
	}
}

func TestAggregateOfNothing(t *testing.T) {
	agg := gateway.BuildAggregate(nil)

	if len(agg.Tools) != 0 {
		t.Errorf("exposed %v, want nothing", agg.Tools)
	}
	if agg.Names.Len() != 0 {
		t.Errorf("name map has %d entries, want none", agg.Names.Len())
	}
}

// Nothing is exposed unless it is listed. The reason is in FilterTools'
// own comment: a tools/list carrying every schema of every server costs a
// model's context, and an unexposed tool is still reachable through
// call_tool. These assertions are what stops that default being read as a
// bug and "fixed" back the other way.
func TestFilterTools(t *testing.T) {
	tools := []*mcp.Tool{{Name: "read"}, {Name: "write"}, {Name: "delete"}}

	t.Run("nothing is exposed unless it is listed", func(t *testing.T) {
		if got := gateway.FilterTools(tools, nil); len(got) != 0 {
			t.Errorf("kept %d tools with no allow list, want none", len(got))
		}
		if got := gateway.FilterTools(tools, []string{}); len(got) != 0 {
			t.Errorf("kept %d tools with an empty allow list, want none", len(got))
		}
	})

	t.Run("only listed tools survive", func(t *testing.T) {
		got := gateway.FilterTools(tools, []string{"read", "write"})
		var names []string
		for _, tool := range got {
			names = append(names, tool.Name)
		}
		if want := []string{"read", "write"}; !slices.Equal(names, want) {
			t.Errorf("kept %v, want %v", names, want)
		}
	})

	t.Run("a name that does not exist is ignored", func(t *testing.T) {
		got := gateway.FilterTools(tools, []string{"read", "not-a-tool"})
		if len(got) != 1 || got[0].Name != "read" {
			t.Errorf("kept %v, want only read", got)
		}
	})

	t.Run("an allow list matching nothing exposes nothing", func(t *testing.T) {
		if got := gateway.FilterTools(tools, []string{"absent"}); len(got) != 0 {
			t.Errorf("kept %v, want nothing", got)
		}
	})
}
