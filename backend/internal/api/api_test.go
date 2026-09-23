package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/logging"
)

func TestMain(m *testing.M) {
	// gin's debug mode writes route tables and warnings to stdout, which
	// buries the test output.
	gin.SetMode(gin.TestMode)
	m.Run()
}

// harness is an API under test together with the log it wrote.
type harness struct {
	*httptest.Server
	Logs    *logging.Store
	API     *api.API
	Configs *config.Manager
}

// configs writes a configuration to a temporary file and opens it.
//
// A real manager rather than a stand-in: it is what makes the write
// endpoints exercise validation, atomic saving and the change log the
// way a running instance does.
func configs(t *testing.T, adjust func(*config.Config)) *config.Manager {
	t.Helper()

	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{}
	if adjust != nil {
		adjust(&cfg)
	}

	path := t.TempDir() + "/config.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save the configuration: %v", err)
	}
	manager, err := config.NewManager(path)
	if err != nil {
		t.Fatalf("open the configuration: %v", err)
	}
	return manager
}

// start builds an API and serves it, adjusting the options first.
func start(t *testing.T, adjust func(*api.Options)) *harness {
	t.Helper()

	store := logging.NewStore(500)
	log, err := logging.New(logging.Options{Level: slog.LevelDebug, Store: store})
	if err != nil {
		t.Fatalf("build the logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })

	opts := api.Options{
		Version:  "test",
		Logger:   log.For(logging.ModuleAPI),
		Security: config.Default().Security,
		Configs:  configs(t, nil),
		Logs:     store,
	}
	// The default permits loopback only, which is where tests connect
	// from; a test that cares sets its own.
	if adjust != nil {
		adjust(&opts)
	}

	built, err := api.New(opts)
	if err != nil {
		t.Fatalf("build the api: %v", err)
	}

	server := httptest.NewUnstartedServer(built.Handler())
	server.Listener = built.Listen(server.Listener)
	server.Start()
	t.Cleanup(server.Close)

	return &harness{Server: server, Logs: store, API: built, Configs: opts.Configs}
}

// get performs a GET and returns the response.
func (h *harness) get(t *testing.T, path string) *http.Response {
	t.Helper()
	resp, err := h.Client().Get(h.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// do performs a request with an optional JSON body.
func (h *harness) do(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode the body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, h.URL+path, reader)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// raw performs a request with a literal body, for malformed input that
// could not be produced by encoding a value.
func (h *harness) raw(t *testing.T, method, path, body string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(method, h.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// decode reads a JSON response into target, failing on an unexpected
// status.
func decode(t *testing.T, resp *http.Response, want int, target any) {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, want, body)
	}
	if target == nil {
		return
	}
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

// envelopeOf decodes a failed response.
func envelopeOf(t *testing.T, resp *http.Response) api.Envelope {
	t.Helper()

	if got := resp.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("content type = %q, want JSON", got)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	var envelope api.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return envelope
}

func logText(store *logging.Store) string {
	var text string
	for _, entry := range store.Query(logging.Query{}) {
		text += entry.Message + " "
		for key, value := range entry.Attrs {
			text += key + "=" + value + " "
		}
		text += "\n"
	}
	return text
}

// ===== the configuration on the wire =====

// Configuration crosses the wire in the shape the file uses: the file's
// field names, and durations written the way a person writes them. These
// two helpers convert between that shape and a Go value, so that a test
// can say what it means in Go and still send what a browser would send.
//
// Neither helper is what pins the shape — a helper that agreed with a
// broken encoder would hide the breakage. The shape is pinned by
// TestTheConfigurationIsSpokenInTheShapeOfTheFile below, against
// literals.

func toWire(t *testing.T, value any) json.RawMessage {
	t.Helper()

	asYAML, err := yaml.Marshal(value)
	if err != nil {
		t.Fatalf("encode for the wire: %v", err)
	}
	asJSON, err := yaml.YAMLToJSON(asYAML)
	if err != nil {
		t.Fatalf("encode for the wire: %v", err)
	}
	return json.RawMessage(asJSON)
}

func configFromWire(t *testing.T, raw json.RawMessage) config.Config {
	t.Helper()

	asYAML, err := yaml.JSONToYAML(raw)
	if err != nil {
		t.Fatalf("decode from the wire: %v", err)
	}
	cfg, err := config.Parse(asYAML)
	if err != nil {
		t.Fatalf("decode from the wire: %v", err)
	}
	return cfg
}

// The web UI reads this, and the settings page and the raw-YAML editor
// have to address the same fields by the same names. Two things are
// pinned here: the names are the file's, and a duration is a string a
// person can read.
//
// The alternative, which is what Go produces if left alone, is the
// field names of the Go structs and durations as nanosecond counts —
// two naming conventions in one response, and a unit that reads as
// milliseconds to anyone who does not already know better. A duration
// as an integer is precisely the ambiguity this project set out to be
// rid of.
func TestTheConfigurationIsSpokenInTheShapeOfTheFile(t *testing.T) {
	h := start(t, func(o *api.Options) {
		o.Configs = configs(t, func(cfg *config.Config) {
			cfg.Logging.MaxAge = 168 * time.Hour
			cfg.MCPServers = map[string]config.MCPServer{
				"files": server(func(s *config.MCPServer) { s.Timeout = 90 * time.Second }),
			}
		})
	})

	var body struct {
		Config map[string]any `json:"config"`
	}
	decode(t, h.get(t, "/api/config"), http.StatusOK, &body)

	logging, ok := body.Config["logging"].(map[string]any)
	if !ok {
		t.Fatalf("no \"logging\" section: %+v", body.Config)
	}
	if got := logging["maxAge"]; got != "168h0m0s" {
		t.Errorf("logging.maxAge = %#v, want the string \"168h0m0s\"", got)
	}
	// The camelCase name, not Go's MaxSizeMB-style export.
	if _, present := logging["maxSizeMB"]; !present {
		t.Errorf("no \"maxSizeMB\" key: %+v", logging)
	}

	servers, _ := body.Config["mcpServers"].(map[string]any)
	files, ok := servers["files"].(map[string]any)
	if !ok {
		t.Fatalf("no \"mcpServers.files\" section: %+v", body.Config)
	}
	if got := files["timeout"]; got != "1m30s" {
		t.Errorf("mcpServers.files.timeout = %#v, want the string \"1m30s\"", got)
	}
	// A field the server does not use is absent rather than null, so the
	// UI can tell "not set" from "set to nothing".
	if _, present := files["url"]; present {
		t.Errorf("a stdio server reports a url: %+v", files)
	}
	if _, present := files["headers"]; present {
		t.Errorf("a stdio server reports headers: %+v", files)
	}
}

// The shape has to work in both directions, or the settings page can
// read the configuration and not save it.
func TestADurationIsWrittenBackAsItWasRead(t *testing.T) {
	h := start(t, nil)

	var read struct {
		Config json.RawMessage `json:"config"`
	}
	decode(t, h.get(t, "/api/config"), http.StatusOK, &read)

	resp := h.do(t, http.MethodPut, "/api/config", map[string]any{"config": read.Config})
	decode(t, resp, http.StatusOK, nil)

	if got := h.Configs.Get().Security.ConnectionTimeout; got != 30*time.Second {
		t.Errorf("connectionTimeout = %v, want it unchanged at 30s", got)
	}
}

// A misspelled key is refused rather than ignored. Ignoring it is the
// worse failure: the setting looks as though it was made, it was not,
// and nothing says why.
func TestAnUnknownConfigurationKeyIsRefused(t *testing.T) {
	h := start(t, nil)

	resp := h.do(t, http.MethodPut, "/api/config", map[string]any{
		"config": map[string]any{"listen": map[string]any{"prot": 9000}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if envelope := envelopeOf(t, resp); !contains(envelope.Error.Message, "prot") {
		t.Errorf("the failure does not name the offending key: %q", envelope.Error.Message)
	}
}

// ===== health =====

func TestHealthReportsTheBuild(t *testing.T) {
	h := start(t, nil)

	resp := h.get(t, "/api/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var health api.Health
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if health.Status != "ok" {
		t.Errorf("status = %q, want ok", health.Status)
	}
	if health.Version != "test" {
		t.Errorf("version = %q, want the version it was built with", health.Version)
	}
	if health.StartedAt.IsZero() {
		t.Error("startedAt is zero")
	}
}

// Uptime is what an operator reads to tell a long-running process from
// one that is crash-looping, so it has to advance.
func TestHealthReportsUptime(t *testing.T) {
	now := time.Now()
	h := start(t, func(o *api.Options) {
		o.Now = func() time.Time { return now }
	})

	// Move the clock forward without waiting for it.
	now = now.Add(90 * time.Second)

	var health api.Health
	if err := json.NewDecoder(h.get(t, "/api/health").Body).Decode(&health); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if health.UptimeSeconds != 90 {
		t.Errorf("uptimeSeconds = %d, want 90", health.UptimeSeconds)
	}
}

// ===== routing =====

// A client parsing HTML where it expected an error envelope gets a
// confusing parse failure rather than the 404 that actually happened.
func TestUnknownAPIPathReturnsTheEnvelope(t *testing.T) {
	h := start(t, nil)

	resp := h.get(t, "/api/nothing-here")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	envelope := envelopeOf(t, resp)
	if envelope.Error.Code != api.CodeNotFound {
		t.Errorf("code = %q, want %q", envelope.Error.Code, api.CodeNotFound)
	}
	if envelope.Error.Message == "" {
		t.Error("the failure carries no message")
	}
}

func TestWrongMethodIsReported(t *testing.T) {
	h := start(t, nil)

	req, _ := http.NewRequest(http.MethodDelete, h.URL+"/api/health", nil)
	resp, err := h.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("DELETE on a GET-only route succeeded")
	}
	if envelope := envelopeOf(t, resp); envelope.Error.Message == "" {
		t.Error("the failure carries no message")
	}
}
