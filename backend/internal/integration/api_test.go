package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/upstream"
)

// apiGet performs a management API request against the running stack.
func (s *stack) apiGet(t *testing.T, path string, target any) {
	t.Helper()
	s.apiDo(t, http.MethodGet, path, nil, http.StatusOK, target)
}

// apiDo performs a management API request and decodes the response.
func (s *stack) apiDo(t *testing.T, method, path string, body any, want int, target any) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode the body: %v", err)
		}
		reader = strings.NewReader(string(encoded))
	}

	req, err := http.NewRequest(method, s.BaseURL+path, reader)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status = %d, want %d; body: %s", method, path, resp.StatusCode, want, raw)
	}
	if target != nil {
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
	}
}

// ===== one server's tools and resources =====

func TestTheAPIListsAConnectedServersTools(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})

	var got struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	stack.apiGet(t, "/api/servers/files/tools", &got)

	names := make([]string, 0, len(got.Tools))
	for _, tool := range got.Tools {
		names = append(names, tool.Name)
	}
	// The upstream's own names, not the prefixed ones: this endpoint is
	// about that server, not about what the gateway exposes.
	for _, want := range []string{"echo", "sleep", "grow", "fail"} {
		if !contains(names, want) {
			t.Errorf("tool %q is missing from %v", want, names)
		}
	}
}

func TestTheAPICallsAToolOnOneServer(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})

	var got struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	stack.apiDo(t, http.MethodPost, "/api/servers/files/tools/echo/call",
		map[string]any{"arguments": map[string]any{"message": "through the api"}},
		http.StatusOK, &got)

	if got.IsError {
		t.Fatalf("the call reported an error: %+v", got.Content)
	}
	if len(got.Content) == 0 || got.Content[0].Text != "through the api" {
		t.Errorf("content = %+v, want the message that was sent", got.Content)
	}
}

// A tool that ran and reported a problem is a successful call with a
// failed result. Reporting it as an HTTP error would hide what the tool
// actually said.
func TestAToolErrorIsAnOKResponseWithAFailedResult(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})

	var got struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	stack.apiDo(t, http.MethodPost, "/api/servers/files/tools/fail/call",
		map[string]any{"arguments": map[string]any{}}, http.StatusOK, &got)

	if !got.IsError {
		t.Fatal("a failing tool reported success")
	}
	if len(got.Content) == 0 || !strings.Contains(got.Content[0].Text, "always fails") {
		t.Errorf("content = %+v, want the upstream explanation", got.Content)
	}
}

func TestTheAPIReadsAResourceFromOneServer(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})

	var got struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	stack.apiGet(t, "/api/servers/files/resource?uri=test%3A%2F%2Fgreeting", &got)

	if len(got.Contents) == 0 || got.Contents[0].Text != "hello" {
		t.Errorf("contents = %+v, want the upstream text", got.Contents)
	}
}

// ===== aggregated views =====

func TestTheAPIAggregatesToolsAcrossServers(t *testing.T) {
	stack := start(t, map[string]string{"files": "full", "notes": "tools-only"})

	var got struct {
		Tools []api.AggregatedTool `json:"tools"`
	}
	stack.apiGet(t, "/api/tools?limit=100", &got)

	byExposed := map[string]api.AggregatedTool{}
	for _, tool := range got.Tools {
		byExposed[tool.Exposed] = tool
	}

	for _, want := range []string{"files_echo", "notes_echo"} {
		tool, listed := byExposed[want]
		if !listed {
			t.Fatalf("%q is missing from the aggregate", want)
		}
		// Both the origin and the exposed name are reported, because the
		// UI needs to say where a tool came from and a caller needs the
		// name to invoke it by.
		if tool.Server == "" || tool.Tool == "" {
			t.Errorf("%q does not say where it came from: %+v", want, tool)
		}
		if tool.InputSchema == nil {
			t.Errorf("%q carries no schema, so a call dialog cannot be built", want)
		}
	}
}

// The management interface is where exposure is decided, so it has to be
// able to see the tools that are not exposed. Listing only the exposed
// ones leaves a fresh installation with nothing on the page and no way to
// change that — the same fault as a management view that under-reports,
// pointed the other way.
func TestTheAPICanListEveryUpstreamToolIncludingUnexposedOnes(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	exposeOnly(t, stack, "files", "echo")

	var exposedOnly struct {
		Tools []api.AggregatedTool `json:"tools"`
	}
	stack.apiGet(t, "/api/tools?limit=100", &exposedOnly)

	var everything struct {
		Tools []api.AggregatedTool `json:"tools"`
	}
	stack.apiGet(t, "/api/tools?limit=100&all=true", &everything)

	if len(everything.Tools) <= len(exposedOnly.Tools) {
		t.Fatalf("all=true returned %d tools, exposed-only returned %d; "+
			"the test server has tools it does not expose",
			len(everything.Tools), len(exposedOnly.Tools))
	}

	// Each unexposed tool still says where it came from, and says it has no
	// exposed name rather than borrowing one.
	var unexposed int
	for _, tool := range everything.Tools {
		if tool.Exposed != "" {
			continue
		}
		unexposed++
		if tool.Server == "" || tool.Tool == "" {
			t.Errorf("an unexposed tool does not say where it came from: %+v", tool)
		}
		if tool.InputSchema == nil {
			t.Errorf("%s/%s carries no schema, so a call dialog cannot be built",
				tool.Server, tool.Tool)
		}
	}
	if unexposed == 0 {
		t.Error("no tool came back unexposed, so nothing was added by all=true")
	}
}

// Every unexposed tool has the same empty exposed name, so anything that
// identifies a search result by that name keeps one of them and loses the
// rest.
func TestSearchingEveryToolKeepsTheUnexposedOnesApart(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	exposeOnly(t, stack, "files", "echo")

	var got struct {
		Tools []api.AggregatedTool `json:"tools"`
	}
	// "to" is a whole word in the descriptions of sleep ("to provoke a
	// timeout") and fail ("to exercise failure handling"), neither of
	// which is exposed.
	stack.apiGet(t, "/api/tools?all=true&limit=100&q=to", &got)

	seen := map[string]bool{}
	for _, tool := range got.Tools {
		key := tool.Server + "/" + tool.Tool
		if seen[key] {
			t.Errorf("%q appears twice in the results", key)
		}
		seen[key] = true
	}
	if len(seen) < 2 {
		t.Fatalf("a search matching several tools returned %d: %+v", len(seen), got.Tools)
	}
}

func TestSearchingAggregatedTools(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})

	var got struct {
		Tools []api.AggregatedTool `json:"tools"`
	}
	stack.apiGet(t, "/api/tools?q=echo", &got)

	if len(got.Tools) == 0 {
		t.Fatal("searching for a tool that exists returned nothing")
	}
	if !strings.Contains(got.Tools[0].Exposed, "echo") {
		t.Errorf("the best match is %q, want the tool that was searched for", got.Tools[0].Exposed)
	}
	for _, tool := range got.Tools {
		if tool.Score == 0 {
			t.Errorf("%q was returned from a search with no score", tool.Exposed)
		}
	}
}

// exposeOnly narrows a server's allow list to the named tools.
//
// The test harness exposes everything each server offers, because most
// tests are about forwarding. A test about the difference between exposed
// and merely available has to create that difference.
func exposeOnly(t *testing.T, stack *stack, server string, tools ...string) {
	t.Helper()

	if _, err := stack.Configs.Update(func(c *config.Config) error {
		entry := c.MCPServers[server]
		entry.ExposedTools = tools
		c.MCPServers[server] = entry
		return nil
	}); err != nil {
		t.Fatalf("narrow the allow list for %s: %v", server, err)
	}
	stack.Gateway.Sync()
}

func TestTheAPIAggregatesResourcesAcrossServers(t *testing.T) {
	stack := start(t, map[string]string{"files": "full", "notes": "full"})

	var got struct {
		Resources []api.AggregatedResource `json:"resources"`
	}
	stack.apiGet(t, "/api/resources", &got)

	if len(got.Resources) < 2 {
		t.Fatalf("listed %d resources, want one per server: %+v", len(got.Resources), got.Resources)
	}
	for _, resource := range got.Resources {
		if resource.Server == "" {
			t.Errorf("%q does not say which server it came from", resource.URI)
		}
		// Both URIs are reported: the upstream one identifies it there,
		// the exposed one is what an MCP client would ask for.
		if resource.Exposed == "" || resource.Exposed == resource.URI {
			t.Errorf("%q has no distinct exposed uri: %+v", resource.URI, resource)
		}
	}
}

// ===== gateway state =====

func TestTheAPIReportsGatewaySessions(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})
	session := stack.connect(t)

	var got struct {
		Sessions []struct {
			ID         string `json:"id"`
			ClientName string `json:"clientName"`
		} `json:"sessions"`
	}
	stack.apiGet(t, "/api/gateway/sessions", &got)

	if len(got.Sessions) == 0 {
		t.Fatal("a connected client is not reported as a session")
	}

	var found bool
	for _, reported := range got.Sessions {
		if reported.ID != session.ID() {
			continue
		}
		found = true
		// Knowing which client holds a session is what makes the list
		// actionable rather than a row of opaque identifiers.
		if reported.ClientName == "" {
			t.Error("the session does not say which client holds it")
		}
	}
	if !found {
		t.Errorf("the connected session %q is not in %+v", session.ID(), got.Sessions)
	}
}

func TestTheAPIReportsTheToolsTheGatewayPublishes(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})

	var got struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		SystemTools []string `json:"systemTools"`
	}
	stack.apiGet(t, "/api/gateway/tools", &got)

	names := make([]string, 0, len(got.Tools))
	for _, tool := range got.Tools {
		names = append(names, tool.Name)
	}
	if !contains(names, "files_echo") {
		t.Errorf("the published tools %v do not include the forwarded one", names)
	}
	if len(got.SystemTools) == 0 {
		t.Error("the gateway's own tools are not reported")
	}
}

func TestTheAPISummarisesTheGateway(t *testing.T) {
	stack := start(t, map[string]string{"files": "full", "notes": "full"})
	stack.connect(t)

	var got struct {
		Servers struct {
			Configured int `json:"configured"`
			Connected  int `json:"connected"`
			Failed     int `json:"failed"`
		} `json:"servers"`
		Tools    int `json:"tools"`
		Sessions int `json:"sessions"`
	}
	stack.apiGet(t, "/api/gateway/status", &got)

	if got.Servers.Configured != 2 {
		t.Errorf("configured = %d, want 2", got.Servers.Configured)
	}
	if got.Servers.Connected != 2 {
		t.Errorf("connected = %d, want 2", got.Servers.Connected)
	}
	if got.Tools == 0 {
		t.Error("no published tools were counted")
	}
	if got.Sessions == 0 {
		t.Error("no sessions were counted although a client is connected")
	}
}

// ===== connection control =====

func TestDisconnectingAndReconnectingThroughTheAPI(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})

	var disconnected upstream.Status
	stack.apiDo(t, http.MethodPost, "/api/servers/files/disconnect", nil,
		http.StatusOK, &disconnected)
	if disconnected.State != upstream.StateDisconnected {
		t.Errorf("state = %q, want disconnected", disconnected.State)
	}

	// Its tools go with it, which is what stops a model choosing one that
	// cannot work.
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	stack.apiGet(t, "/api/gateway/tools", &tools)
	for _, tool := range tools.Tools {
		if strings.HasPrefix(tool.Name, "files_") {
			t.Errorf("%q is still published after its server was disconnected", tool.Name)
		}
	}

	var reconnected upstream.Status
	stack.apiDo(t, http.MethodPost, "/api/servers/files/connect", nil,
		http.StatusOK, &reconnected)
	if reconnected.State != upstream.StateConnected {
		t.Errorf("state = %q, want connected", reconnected.State)
	}

	stack.apiGet(t, "/api/gateway/tools", &tools)
	var back bool
	for _, tool := range tools.Tools {
		if tool.Name == "files_echo" {
			back = true
		}
	}
	if !back {
		t.Error("the tools did not come back after reconnecting")
	}
}

// Asking for a state that already holds is not a failure: the caller
// asked for an outcome, and the outcome is already true.
func TestConnectionControlIsRepeatable(t *testing.T) {
	stack := start(t, map[string]string{"files": "full"})

	for i := 0; i < 2; i++ {
		stack.apiDo(t, http.MethodPost, "/api/servers/files/connect", nil, http.StatusOK, nil)
	}
	for i := 0; i < 2; i++ {
		stack.apiDo(t, http.MethodPost, "/api/servers/files/disconnect", nil, http.StatusOK, nil)
	}
}

// Deleting a server has to stop its child process, or the process is
// orphaned with nothing left that knows about it.
func TestDeletingAServerThroughTheAPIStopsItsProcess(t *testing.T) {
	stack := start(t, map[string]string{"files": "full", "notes": "full"})

	pid := statusOf(t, stack.Upstreams, "files").PID
	if pid == 0 {
		t.Fatal("the server has no child process")
	}

	stack.apiDo(t, http.MethodDelete, "/api/servers/files", nil, http.StatusNoContent, nil)

	eventually(t, "the deleted server to be gone from the connection manager", func() bool {
		return !contains(stack.Upstreams.Names(), "files")
	})
	eventually(t, "the child process to have exited", func() bool { return !alive(pid) })

	// The survivor is untouched.
	if got := statusOf(t, stack.Upstreams, "notes").State; got != upstream.StateConnected {
		t.Errorf("the other server is %q, want connected", got)
	}
}

// alive reports whether a process still exists. Signal 0 performs the
// permission and existence checks without delivering anything, which is
// the usual way to ask.
func alive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
