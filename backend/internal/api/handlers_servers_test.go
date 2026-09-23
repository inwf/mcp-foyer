package api_test

import (
	"net/http"
	"testing"
	"time"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/upstream"
)

// twoServers is a configuration with something to list.
func twoServers(cfg *config.Config) {
	cfg.MCPServers = map[string]config.MCPServer{
		"files": server(func(s *config.MCPServer) {
			s.Description = "the file server"
		}),
		"notes": server(nil),
	}
}

type serversResponse struct {
	Servers []api.ServerView `json:"servers"`
}

// ===== listing =====

func TestListingServers(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, twoServers) })

	var got serversResponse
	decode(t, h.get(t, "/api/servers"), http.StatusOK, &got)

	if len(got.Servers) != 2 {
		t.Fatalf("listed %d servers, want 2: %+v", len(got.Servers), got.Servers)
	}
	// A stable order keeps the UI's list from reshuffling on every poll.
	if got.Servers[0].Name != "files" || got.Servers[1].Name != "notes" {
		t.Errorf("listed %q then %q, want them in name order",
			got.Servers[0].Name, got.Servers[1].Name)
	}
	if got.Servers[0].Config.Description != "the file server" {
		t.Errorf("the configuration was not returned: %+v", got.Servers[0].Config)
	}
}

// A server that is configured but not yet running still has a truthful
// state, and reporting nothing would leave the UI with a blank cell.
func TestAConfiguredServerReportsAState(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, twoServers) })

	var got serversResponse
	decode(t, h.get(t, "/api/servers"), http.StatusOK, &got)

	if state := got.Servers[0].Status.State; state != upstream.StateDisconnected {
		t.Errorf("state = %q, want disconnected", state)
	}
}

func TestListingServersHidesSecrets(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, withSecrets) })

	var got serversResponse
	decode(t, h.get(t, "/api/servers"), http.StatusOK, &got)

	if token := got.Servers[0].Config.Env["API_TOKEN"]; token != config.RedactedValue {
		t.Errorf("API_TOKEN = %q, want it hidden", token)
	}
}

func TestGettingOneServer(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, twoServers) })

	var got api.ServerView
	decode(t, h.get(t, "/api/servers/files"), http.StatusOK, &got)

	if got.Name != "files" {
		t.Errorf("name = %q, want files", got.Name)
	}
}

func TestGettingAServerThatDoesNotExist(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, twoServers) })

	resp := h.get(t, "/api/servers/nowhere")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if envelopeOf(t, resp).Error.Code != api.CodeNotFound {
		t.Error("the failure is not reported as not found")
	}
}

// ===== creating =====

// A configuration change has to be announced, because the client that
// made it is not the only one looking: another tab, or another person's
// browser, has no other way to learn that what it is showing is out of
// date. The frontend answers this event by refetching everything.
func TestAConfigurationChangeIsAnnounced(t *testing.T) {
	h, bus := watching(t, nil)

	seen, cancel := bus.Subscribe(events.ConfigUpdated)
	defer cancel()

	resp := h.do(t, http.MethodPost, "/api/servers", map[string]any{
		"name":   "added",
		"server": toWire(t, server(nil)),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	select {
	case event := <-seen:
		if event.Kind != events.ConfigUpdated {
			t.Errorf("kind = %q, want %q", event.Kind, events.ConfigUpdated)
		}
	case <-time.After(2 * time.Second):
		t.Error("nothing was published when the configuration changed")
	}
}

func TestCreatingAServer(t *testing.T) {
	h := start(t, nil)

	resp := h.do(t, http.MethodPost, "/api/servers", map[string]any{
		"name":   "added",
		"server": toWire(t, server(nil)),
	})

	var view api.ServerView
	decode(t, resp, http.StatusCreated, &view)
	if view.Name != "added" {
		t.Errorf("name = %q, want added", view.Name)
	}

	// The configuration on disk is what survives a restart, so that is
	// what has to have changed.
	if _, saved := h.Configs.Get().MCPServers["added"]; !saved {
		t.Error("the server was not written to the configuration")
	}
}

// Creating over an existing server would silently discard whatever was
// there, including credentials that cannot be recovered.
func TestCreatingAServerThatAlreadyExists(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, twoServers) })

	resp := h.do(t, http.MethodPost, "/api/servers", map[string]any{
		"name":   "files",
		"server": toWire(t, server(nil)),
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if envelopeOf(t, resp).Error.Code != api.CodeConflict {
		t.Error("the failure is not reported as a conflict")
	}

	// The original is untouched.
	if got := h.Configs.Get().MCPServers["files"].Description; got != "the file server" {
		t.Errorf("description = %q, want the original server left alone", got)
	}
}

func TestCreatingAServerWithoutAName(t *testing.T) {
	h := start(t, nil)

	resp := h.do(t, http.MethodPost, "/api/servers", map[string]any{
		"server": toWire(t, server(nil)),
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if len(envelopeOf(t, resp).Error.Fields) == 0 {
		t.Error("no field error was reported, so a form cannot mark the input")
	}
}

func TestCreatingAnInvalidServer(t *testing.T) {
	h := start(t, nil)

	resp := h.do(t, http.MethodPost, "/api/servers", map[string]any{
		"name":   "broken",
		"server": toWire(t, server(func(s *config.MCPServer) { s.Command = "" })),
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if _, saved := h.Configs.Get().MCPServers["broken"]; saved {
		t.Error("an invalid server was written to the configuration")
	}
}

// A name the rest of the system cannot use would produce tool names no
// client could call.
func TestCreatingAServerWithAnUnusableName(t *testing.T) {
	h := start(t, nil)

	resp := h.do(t, http.MethodPost, "/api/servers", map[string]any{
		"name":   "has spaces/and-slashes",
		"server": toWire(t, server(nil)),
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
}

// ===== updating =====

func TestUpdatingAServer(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, twoServers) })

	updated := server(func(s *config.MCPServer) { s.Description = "renamed" })
	decode(t, h.do(t, http.MethodPut, "/api/servers/files",
		map[string]any{"server": toWire(t, updated)}), http.StatusOK, nil)

	if got := h.Configs.Get().MCPServers["files"].Description; got != "renamed" {
		t.Errorf("description = %q, want renamed", got)
	}
}

func TestUpdatingAServerThatDoesNotExist(t *testing.T) {
	h := start(t, nil)

	resp := h.do(t, http.MethodPut, "/api/servers/nowhere",
		map[string]any{"server": toWire(t, server(nil))})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// The same destruction as the whole-configuration write: a form that
// reads a server, edits its description and saves must not wipe the
// credentials it was never shown.
func TestUpdatingAServerKeepsItsSecrets(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, withSecrets) })

	var read api.ServerView
	decode(t, h.get(t, "/api/servers/files"), http.StatusOK, &read)

	edited := read.Config.MCPServer
	edited.Description = "edited"
	decode(t, h.do(t, http.MethodPut, "/api/servers/files",
		map[string]any{"server": toWire(t, edited)}), http.StatusOK, nil)

	if got := h.Configs.Get().MCPServers["files"].Env["API_TOKEN"]; got != "the-real-token" {
		t.Errorf("API_TOKEN = %q, want the original secret to survive", got)
	}
	if got := h.Configs.Get().MCPServers["files"].Description; got != "edited" {
		t.Errorf("description = %q, want the edit applied", got)
	}
}

func TestUpdatingAServerWithInvalidValuesChangesNothing(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, twoServers) })

	resp := h.do(t, http.MethodPut, "/api/servers/files",
		map[string]any{"server": toWire(t, server(func(s *config.MCPServer) { s.Command = "" }))})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if got := h.Configs.Get().MCPServers["files"].Command; got == "" {
		t.Error("the rejected update was applied anyway")
	}
}

// ===== deleting =====

func TestDeletingAServer(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, twoServers) })

	resp := h.do(t, http.MethodDelete, "/api/servers/files", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	if _, still := h.Configs.Get().MCPServers["files"]; still {
		t.Error("the server is still in the configuration")
	}
	// The other one is untouched.
	if _, still := h.Configs.Get().MCPServers["notes"]; !still {
		t.Error("deleting one server removed another")
	}
}

func TestDeletingAServerThatDoesNotExist(t *testing.T) {
	h := start(t, nil)

	resp := h.do(t, http.MethodDelete, "/api/servers/nowhere", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// ===== connection control without a manager =====

// Every endpoint that needs the connection manager has to say so rather
// than reporting an empty result, which would send a caller looking for
// the wrong problem.
func TestEndpointsThatNeedAConnectionManagerSaySoWhenThereIsNone(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, twoServers) })

	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/servers/files/connect"},
		{http.MethodPost, "/api/servers/files/disconnect"},
		{http.MethodGet, "/api/servers/files/tools"},
		{http.MethodGet, "/api/servers/files/resources"},
		{http.MethodGet, "/api/servers/files/resource?uri=test://x"},
	}
	for _, tc := range cases {
		resp := h.do(t, tc.method, tc.path, nil)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", tc.method, tc.path, resp.StatusCode)
		}
	}
}

// A server that is not connected offers nothing, and an empty list would
// say "this server has no tools" — a different problem with a different
// fix.
func TestListingToolsOfADisconnectedServer(t *testing.T) {
	h := start(t, func(o *api.Options) {
		o.Configs = configs(t, twoServers)
		o.Upstreams = upstream.NewManager("test", nil, nil)
	})

	resp := h.get(t, "/api/servers/files/tools")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := envelopeOf(t, resp).Error.Message; !contains(got, "not connected") {
		t.Errorf("message = %q, want it to say the server is not connected", got)
	}
}

// A server with no process must not report a start time. The zero time
// marshals as the year 1, which a reader has to know to disbelieve —
// and a UI that formats it shows "1 Jan 0001" beside a server that is
// working perfectly well.
func TestAServerWithNoProcessReportsNoStartTime(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, withSecrets) })

	var raw struct {
		Servers []map[string]any `json:"servers"`
	}
	decode(t, h.get(t, "/api/servers"), http.StatusOK, &raw)

	if len(raw.Servers) == 0 {
		t.Fatal("no servers were reported")
	}
	status, ok := raw.Servers[0]["status"].(map[string]any)
	if !ok {
		t.Fatalf("no status: %+v", raw.Servers[0])
	}
	if value, present := status["startedAt"]; present {
		t.Errorf("startedAt = %#v, want the field absent for a server with no process", value)
	}
	if value, present := status["pid"]; present {
		t.Errorf("pid = %#v, want the field absent for a server with no process", value)
	}
	if value, present := status["lastCheck"]; present {
		t.Errorf("lastCheck = %#v, want the field absent for a server never checked", value)
	}
}

// A newly created server has never been reached for either, and the
// response the form reads back has to say so rather than report the
// year 1.
func TestACreatedServerReportsNoCheckTime(t *testing.T) {
	h := start(t, nil)

	var raw map[string]any
	decode(t, h.do(t, http.MethodPost, "/api/servers", map[string]any{
		"name":   "added",
		"server": toWire(t, server(nil)),
	}), http.StatusCreated, &raw)

	status, ok := raw["status"].(map[string]any)
	if !ok {
		t.Fatalf("no status: %+v", raw)
	}
	if got := status["state"]; got != string(upstream.StateDisconnected) {
		t.Errorf("state = %#v, want disconnected", got)
	}
	if value, present := status["lastCheck"]; present {
		t.Errorf("lastCheck = %#v, want the field absent", value)
	}
}
