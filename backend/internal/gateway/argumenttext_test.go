package gateway_test

import (
	"encoding/json"
	"testing"

	"mcp-foyer/internal/gateway"
)

// A caller that knows what it wants to pass often knows that better than
// what the tool is called: "dryRun" finds edit_file, "branch" finds the
// pull request tools. The search needs the parameter names and
// descriptions as text to match on.
func TestArgumentTextNamesEveryParameter(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string"},
			"dryRun": map[string]any{"type": "boolean", "description": "Preview changes using git-style diff format"},
			"edits": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"oldText": map[string]any{"type": "string", "description": "Text to search for"},
						"newText": map[string]any{"type": "string"},
					},
				},
			},
		},
	}

	got := gateway.ArgumentText(schema)

	// Sorted by name at each level, so that the same schema always gives
	// the same text; nested parameters follow their parent.
	want := "dryRun Preview changes using git-style diff format edits newText oldText Text to search for path"
	if got != want {
		t.Errorf("ArgumentText =\n %q\nwant\n %q", got, want)
	}
}

func TestArgumentTextOfSchemasThatNameNothing(t *testing.T) {
	for name, schema := range map[string]any{
		"nil":                  nil,
		"no properties":        map[string]any{"type": "object"},
		"not an object":        map[string]any{"type": "string"},
		"properties not a map": map[string]any{"type": "object", "properties": "oops"},
	} {
		if got := gateway.ArgumentText(schema); got != "" {
			t.Errorf("%s: ArgumentText = %q, want nothing", name, got)
		}
	}
}

// Schemas arrive from upstreams as whatever their JSON decoded to, and
// over some paths still as raw JSON. Both have to read the same.
func TestArgumentTextReadsRawJSON(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"words to search for"}}}`)

	if got, want := gateway.ArgumentText(raw), "query words to search for"; got != want {
		t.Errorf("ArgumentText(raw) = %q, want %q", got, want)
	}
}

// Parameters are the weakest signal: path and query are everywhere. A
// tool must be findable by an argument nothing else mentions, and a tool
// whose name says the thing must still beat one that only takes it as a
// parameter.
func TestSearchFindsAToolByItsArgument(t *testing.T) {
	directory := []gateway.Searchable{
		{Server: "files", Tool: "edit_file", Description: "Make line-based edits to a text file",
			Arguments: "dryRun Preview changes using git-style diff format edits newText oldText path"},
		{Server: "files", Tool: "write_file", Description: "Create a new file or overwrite an existing file",
			Arguments: "content path"},
		{Server: "git", Tool: "git_diff", Description: "Shows differences between branches or commits",
			Arguments: "target"},
	}

	hits := gateway.SearchTools("dryRun", directory, 0)
	if len(hits) != 1 || hits[0].Tool != "edit_file" {
		t.Errorf("SearchTools(\"dryRun\") = %v, want edit_file alone", hitNames(hits))
	}

	// "diff" is git_diff's name and edit_file's argument description.
	hits = gateway.SearchTools("diff", directory, 0)
	if len(hits) < 2 || hits[0].Tool != "git_diff" {
		t.Errorf("SearchTools(\"diff\") = %v, want git_diff first", hitNames(hits))
	}
}
