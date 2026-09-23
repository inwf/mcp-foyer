package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/gateway"
)

type discoveryHit struct {
	gateway.SearchHit
	Title       string               `json:"title"`
	InputSchema map[string]any       `json:"inputSchema"`
	Annotations *mcp.ToolAnnotations `json:"annotations"`
}

type discoveryPage struct {
	Hits       []discoveryHit `json:"hits"`
	NextCursor string         `json:"nextCursor"`
	Unmatched  []string       `json:"unmatched"`
}

func discoveryFixture(t *testing.T, count int) (*fakeUpstreams, *mcp.ClientSession) {
	t.Helper()
	ups := twoServers()
	ups.tools["files"] = nil
	for i := range count {
		ups.tools["files"] = append(ups.tools["files"], &mcp.Tool{
			Name:        fmt.Sprintf("tool_%02d", i),
			Description: "work with documents",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}},
		})
	}
	return ups, gatewayFixture(t, ups, twoServersConfig(t))
}

func searchPage(t *testing.T, session *mcp.ClientSession, args map[string]any) discoveryPage {
	t.Helper()
	var page discoveryPage
	structured(t, callSystemTool(t, session, gateway.ToolSearchTools, args), &page)
	return page
}

func TestDiscoveryPublishesExactlyTheFourSupportedSystemTools(t *testing.T) {
	session := gatewayFixture(t, twoServers(), twoServersConfig(t))
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"call_tool", "get_tool_details", "list_servers", "search_tools"}
	if !slices.Equal(names, want) {
		t.Fatalf("published %v, want %v without legacy aliases", names, want)
	}
}

func TestSearchDefaultsToFiveResultsInEitherSchemaMode(t *testing.T) {
	_, session := discoveryFixture(t, 12)
	for _, includeSchema := range []bool{false, true} {
		t.Run(fmt.Sprint(includeSchema), func(t *testing.T) {
			page := searchPage(t, session, map[string]any{"server": "files", "includeSchema": includeSchema})
			if len(page.Hits) != 5 || page.NextCursor == "" {
				t.Fatalf("got %d hits, cursor %q; want five and a next page", len(page.Hits), page.NextCursor)
			}
			for _, hit := range page.Hits {
				if (hit.InputSchema != nil) != includeSchema {
					t.Errorf("%s input schema present = %v, want %v", hit.Tool, hit.InputSchema != nil, includeSchema)
				}
			}
		})
	}
}

func TestSchemaSearchReturnsCompleteSchemasWithoutASpecialResultCap(t *testing.T) {
	ups, session := discoveryFixture(t, 21)
	largeDescription := strings.Repeat("schema documentation ", 2500)
	ups.tools["files"][0].InputSchema.(map[string]any)["description"] = largeDescription
	ups.tools["files"][0].Title = "Document tool"
	ups.tools["files"][0].Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
	page := searchPage(t, session, map[string]any{"server": "files", "limit": 20, "includeSchema": true})
	if len(page.Hits) != 20 || page.NextCursor == "" {
		t.Fatalf("got %d hits, cursor %q; want twenty and a next page", len(page.Hits), page.NextCursor)
	}
	for i, hit := range page.Hits {
		if !reflect.DeepEqual(hit.InputSchema, ups.tools["files"][i].InputSchema) {
			t.Errorf("schema for %s was changed or truncated", hit.Tool)
		}
	}
	first := page.Hits[0]
	if first.Title != "Document tool" || first.Annotations == nil || !first.Annotations.ReadOnlyHint {
		t.Errorf("tool details are missing: title %q, annotations %+v", first.Title, first.Annotations)
	}
	var detail map[string]any
	structured(t, callSystemTool(t, session, "get_tool_details", map[string]any{
		"server": "files", "tool": first.Tool,
	}), &detail)
	if detail["server"] != first.Server || detail["tool"] != first.Tool || detail["name"] != nil {
		t.Errorf("detail identifiers = %v/%v, legacy name %v", detail["server"], detail["tool"], detail["name"])
	}
	if !reflect.DeepEqual(detail["inputSchema"], first.InputSchema) {
		t.Error("search and get_tool_details describe different schemas")
	}
}

func TestSearchRequiresAQueryOrServerAndALimitBetweenOneAndTwenty(t *testing.T) {
	_, session := discoveryFixture(t, 2)
	for _, args := range []map[string]any{
		{}, {"query": " \t "}, {"server": " \n "},
		{"query": "documents", "limit": 0},
		{"query": "documents", "limit": -1},
		{"query": "documents", "limit": 21},
	} {
		result := callSystemTool(t, session, gateway.ToolSearchTools, args)
		if !result.IsError {
			t.Errorf("accepted invalid search arguments %v", args)
		}
	}
}

func TestSearchMatchesServerMetadataForUnexposedTools(t *testing.T) {
	ups := twoServers()
	ups.statuses[0].ServerName = "Document service"
	session := gatewayFixture(t, ups, twoServersConfig(t))
	for _, query := range []string{"FILES", "filesystem", "service"} {
		page := searchPage(t, session, map[string]any{"query": query})
		if len(page.Hits) != 2 || len(page.Unmatched) != 0 {
			t.Errorf("query %q returned %+v; want both files tools", query, page)
		}
	}
}

func TestSearchPaginationHasNoDuplicatesAndCanChangePageSizeOrSchemaMode(t *testing.T) {
	_, session := discoveryFixture(t, 27)
	page := searchPage(t, session, map[string]any{"server": "files"})
	var got []string
	for calls := 0; ; calls++ {
		for _, hit := range page.Hits {
			got = append(got, hit.Tool)
		}
		if page.NextCursor == "" {
			break
		}
		if calls >= 10 {
			t.Fatal("pagination never ended")
		}
		page = searchPage(t, session, map[string]any{
			"server": "files", "cursor": page.NextCursor, "limit": 7, "includeSchema": true,
		})
		for _, hit := range page.Hits {
			if hit.InputSchema == nil {
				t.Error("changing to schema mode did not return schemas")
			}
		}
	}
	var want []string
	for i := range 27 {
		want = append(want, fmt.Sprintf("tool_%02d", i))
	}
	if !slices.Equal(got, want) {
		t.Errorf("paged tools = %v, want %v", got, want)
	}
}

func TestSearchCursorSurvivesChangesOutsideItsResults(t *testing.T) {
	ups, session := discoveryFixture(t, 8)
	page := searchPage(t, session, map[string]any{"query": "documents", "limit": 3})
	ups.tools["unrelated"] = []*mcp.Tool{{Name: "weather", Description: "daily forecast"}}
	// The old order still applies when a schema changes without changing
	// which tools match or how they are ranked. The next page uses fresh details.
	ups.tools["files"][3].InputSchema.(map[string]any)["description"] = "updated schema"
	page = searchPage(t, session, map[string]any{
		"query": " DOCUMENTS ", "cursor": page.NextCursor, "limit": 3, "includeSchema": true,
	})
	if len(page.Hits) != 3 || page.Hits[0].Tool != "tool_03" || page.Hits[0].InputSchema["description"] != "updated schema" {
		t.Fatalf("cursor lost its position or returned stale details: %+v", page)
	}
}

func TestSearchRejectsCursorsForAnotherQueryOrChangedResults(t *testing.T) {
	ups, session := discoveryFixture(t, 8)
	first := searchPage(t, session, map[string]any{"query": "documents", "limit": 3})
	for _, args := range []map[string]any{
		{"query": "documents", "cursor": "not-a-cursor"},
		{"query": "tool", "cursor": first.NextCursor},
		{"query": "documents", "server": "files", "cursor": first.NextCursor},
	} {
		result := callSystemTool(t, session, gateway.ToolSearchTools, args)
		if !result.IsError || !strings.Contains(strings.ToLower(resultText(result)), "cursor") {
			t.Errorf("invalid cursor did not produce a useful error: %s", resultText(result))
		}
	}
	ups.tools["files"] = ups.tools["files"][1:]
	result := callSystemTool(t, session, gateway.ToolSearchTools, map[string]any{
		"query": "documents", "cursor": first.NextCursor,
	})
	if !result.IsError || !strings.Contains(strings.ToLower(resultText(result)), "restart") {
		t.Errorf("changed results did not ask the caller to restart: %s", resultText(result))
	}
}

func TestSearchCursorRejectsReorderedMatchesButAllowsUnrelatedChangesWithinAServer(t *testing.T) {
	ups, session := discoveryFixture(t, 8)
	first := searchPage(t, session, map[string]any{"query": "documents special", "server": "files", "limit": 3})
	ups.tools["files"] = append(ups.tools["files"], &mcp.Tool{Name: "weather", Description: "forecast"})
	args := map[string]any{"query": "documents special", "server": "files", "limit": 3, "cursor": first.NextCursor}
	page := searchPage(t, session, args)
	if page.Hits[0].Tool != "tool_03" {
		t.Fatalf("an unrelated tool within the same server changed the page: %+v", page)
	}
	// All identities still match, but matching an additional term moves
	// an existing result to the front. Continuing would skip or repeat tools.
	ups.tools["files"][7].Description = "documents special"
	result := callSystemTool(t, session, gateway.ToolSearchTools, args)
	if !result.IsError || !strings.Contains(resultText(result), "restart") {
		t.Errorf("reordered matches did not invalidate the cursor: %s", resultText(result))
	}
}

func TestCallToolResolvesAConfiguredUpstreamBeforeInterpretingItsName(t *testing.T) {
	ups := twoServers()
	ups.statuses[0].Name = gateway.Name
	session := gatewayFixture(t, ups, twoServersConfig(t))
	result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
		"server": gateway.Name, "tool": "read",
	})
	if result.IsError || !slices.Equal(ups.calls, []string{"mcp-foyer/read"}) {
		t.Fatalf("an upstream configured under the gateway's name was not reached: %s, %v", resultText(result), ups.calls)
	}
}

func TestCallToolForwardsUpstreamNamesThatAreAlsoSystemTools(t *testing.T) {
	ups := twoServers()
	session := gatewayFixture(t, ups, twoServersConfig(t))
	for _, name := range []string{gateway.ToolSearchTools, gateway.ToolCallTool} {
		result := callSystemTool(t, session, gateway.ToolCallTool, map[string]any{
			"server": "files", "tool": name, "args": map[string]any{"tags": []string{"business", "data"}},
		})
		if result.IsError {
			t.Fatalf("upstream %s was rejected: %s", name, resultText(result))
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(renderArgs(ups.lastArgs)), &args); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(args["tags"], []any{"business", "data"}) {
			t.Errorf("upstream argument tags were modified: %v", args)
		}
	}
	if !slices.Equal(ups.calls, []string{"files/search_tools", "files/call_tool"}) {
		t.Errorf("calls were routed to %v", ups.calls)
	}
}
