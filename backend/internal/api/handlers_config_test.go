package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
)

// server builds a valid server definition, starting from the defaults so
// that a test only states what it actually cares about.
func server(adjust func(*config.MCPServer)) config.MCPServer {
	s := config.DefaultMCPServer()
	s.Transport = config.TransportStdio
	s.Command = "echo"
	if adjust != nil {
		adjust(&s)
	}
	return s
}

// withSecrets is a configuration carrying values worth hiding.
func withSecrets(cfg *config.Config) {
	cfg.MCPServers = map[string]config.MCPServer{
		"files": server(func(s *config.MCPServer) {
			s.Env = map[string]string{
				"API_TOKEN": "the-real-token",
				"LOG_LEVEL": "debug",
			}
		}),
	}
}

type configResponse struct {
	Config json.RawMessage `json:"config"`
	Path   string          `json:"path"`
}

// ===== reading =====

func TestReadingTheConfigurationHidesSecrets(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, withSecrets) })

	var got configResponse
	decode(t, h.get(t, "/api/config"), http.StatusOK, &got)

	env := configFromWire(t, got.Config).MCPServers["files"].Env
	if env["API_TOKEN"] != config.RedactedValue {
		t.Errorf("API_TOKEN = %q, want it hidden", env["API_TOKEN"])
	}
	// An ordinary setting stays readable, or the settings page becomes
	// useless.
	if env["LOG_LEVEL"] != "debug" {
		t.Errorf("LOG_LEVEL = %q, want it readable", env["LOG_LEVEL"])
	}
	if got.Path == "" {
		t.Error("the response does not say where the configuration lives")
	}
}

// ===== writing =====

// This is the one that would destroy data. The configuration is handed
// out with secrets hidden; a settings page that reads it, changes one
// field and sends the whole thing back must not overwrite every
// credential with the placeholder text — which is unrecoverable, since
// the file has already been rewritten.
func TestSavingBackARedactedConfigurationKeepsTheSecrets(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, withSecrets) })

	var read configResponse
	decode(t, h.get(t, "/api/config"), http.StatusOK, &read)

	// Change something unrelated, exactly as a settings form would.
	edited := configFromWire(t, read.Config)
	edited.Logging.Level = config.LevelWarn

	decode(t, h.do(t, http.MethodPut, "/api/config", map[string]any{"config": toWire(t, edited)}),
		http.StatusOK, nil)

	stored := h.Configs.Get()
	if got := stored.MCPServers["files"].Env["API_TOKEN"]; got != "the-real-token" {
		t.Errorf("API_TOKEN = %q, want the original secret to survive", got)
	}
	if stored.Logging.Level != config.LevelWarn {
		t.Errorf("logging level = %q, want the edit to have been applied", stored.Logging.Level)
	}
}

// Changing a secret has to actually change it, or a rotated credential
// would silently keep the old value.
func TestANewSecretReplacesTheOldOne(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, withSecrets) })

	var read configResponse
	decode(t, h.get(t, "/api/config"), http.StatusOK, &read)

	edited := configFromWire(t, read.Config)
	server := edited.MCPServers["files"]
	server.Env = map[string]string{"API_TOKEN": "the-rotated-token", "LOG_LEVEL": "debug"}
	edited.MCPServers["files"] = server

	decode(t, h.do(t, http.MethodPut, "/api/config", map[string]any{"config": toWire(t, edited)}),
		http.StatusOK, nil)

	if got := h.Configs.Get().MCPServers["files"].Env["API_TOKEN"]; got != "the-rotated-token" {
		t.Errorf("API_TOKEN = %q, want the new secret", got)
	}
}

// Credentials in a URL are hidden the same way, so they have to be
// restored the same way.
func TestURLCredentialsSurviveARoundTrip(t *testing.T) {
	h := start(t, func(o *api.Options) {
		o.Configs = configs(t, func(cfg *config.Config) {
			cfg.MCPServers = map[string]config.MCPServer{
				"remote": server(func(s *config.MCPServer) {
					s.Transport = config.TransportStreamableHTTP
					s.Command = ""
					s.URL = "https://user:hunter2@example.com/mcp"
				}),
			}
		})
	})

	var read configResponse
	decode(t, h.get(t, "/api/config"), http.StatusOK, &read)

	if got := configFromWire(t, read.Config).MCPServers["remote"].URL; !contains(got, config.RedactedURLUser) {
		t.Fatalf("url = %q, want the credentials hidden", got)
	}

	decode(t, h.do(t, http.MethodPut, "/api/config", map[string]any{"config": read.Config}),
		http.StatusOK, nil)

	if got := h.Configs.Get().MCPServers["remote"].URL; got != "https://user:hunter2@example.com/mcp" {
		t.Errorf("url = %q, want the original credentials to survive", got)
	}
}

// A rejected configuration must leave the one in force untouched: half
// a configuration is worse than the old one.
func TestAnInvalidConfigurationIsRejectedAndChangesNothing(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Configs = configs(t, withSecrets) })
	before := h.Configs.Get()

	broken := before.Clone()
	broken.Listen.Port = 99999

	resp := h.do(t, http.MethodPut, "/api/config", map[string]any{"config": toWire(t, broken)})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}

	envelope := envelopeOf(t, resp)
	if len(envelope.Error.Fields) == 0 {
		t.Error("no field errors were reported, so a form cannot mark the bad input")
	}
	var named bool
	for _, field := range envelope.Error.Fields {
		if contains(field.Field, "port") {
			named = true
		}
	}
	if !named {
		t.Errorf("no field error names the port: %+v", envelope.Error.Fields)
	}

	if got := h.Configs.Get().Listen.Port; got != before.Listen.Port {
		t.Errorf("port = %d, want the previous configuration untouched (%d)", got, before.Listen.Port)
	}
}

// Every problem at once, so a form can mark all of them rather than
// making the user fix one, submit, and discover the next.
func TestEveryProblemIsReportedTogether(t *testing.T) {
	h := start(t, nil)

	broken := h.Configs.Get().Clone()
	broken.Listen.Port = -1
	broken.Logging.Level = "shouting"
	broken.MCPServers = map[string]config.MCPServer{
		"no-command": server(func(s *config.MCPServer) { s.Command = "" }),
	}

	resp := h.do(t, http.MethodPut, "/api/config", map[string]any{"config": toWire(t, broken)})
	envelope := envelopeOf(t, resp)

	if len(envelope.Error.Fields) < 3 {
		t.Errorf("reported %d problems, want all three: %+v",
			len(envelope.Error.Fields), envelope.Error.Fields)
	}
}

// ===== validation without saving =====

// The settings form checks as the user types, which must not write
// anything.
func TestValidatingDoesNotSave(t *testing.T) {
	h := start(t, nil)
	before := h.Configs.Get()

	proposed := before.Clone()
	proposed.Listen.Port = 9999

	var result struct {
		Valid  bool             `json:"valid"`
		Fields []api.FieldError `json:"fields"`
	}
	decode(t, h.do(t, http.MethodPost, "/api/config/validate",
		map[string]any{"config": toWire(t, proposed)}), http.StatusOK, &result)

	if !result.Valid {
		t.Errorf("a valid configuration was reported invalid: %+v", result.Fields)
	}
	if got := h.Configs.Get().Listen.Port; got != before.Listen.Port {
		t.Errorf("port = %d; validating wrote the configuration", got)
	}
}

func TestValidatingReportsProblemsWithoutSaving(t *testing.T) {
	h := start(t, nil)

	proposed := h.Configs.Get().Clone()
	proposed.Listen.Port = 99999

	var result struct {
		Valid  bool             `json:"valid"`
		Fields []api.FieldError `json:"fields"`
	}
	decode(t, h.do(t, http.MethodPost, "/api/config/validate",
		map[string]any{"config": toWire(t, proposed)}), http.StatusOK, &result)

	if result.Valid {
		t.Error("an invalid configuration was reported valid")
	}
	if len(result.Fields) == 0 {
		t.Error("no field errors were reported")
	}
}

// ===== malformed requests =====

func TestAMalformedBodyIsABadRequest(t *testing.T) {
	h := start(t, nil)

	resp := h.raw(t, http.MethodPut, "/api/config", "{not json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if envelopeOf(t, resp).Error.Code != api.CodeBadRequest {
		t.Error("the failure is not reported as a bad request")
	}
}

// A field the server does not know is almost always a typo or a client
// built against a different version. Accepting it silently would leave
// the caller believing a setting took effect when it was discarded.
func TestAnUnknownFieldIsRejected(t *testing.T) {
	h := start(t, nil)

	resp := h.raw(t, http.MethodPut, "/api/config",
		`{"config":{"version":1,"listne":{"port":9999}}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a misspelled field", resp.StatusCode)
	}
}

func TestASecondJSONDocumentIsRejected(t *testing.T) {
	h := start(t, nil)

	resp := h.raw(t, http.MethodPut, "/api/config", `{"config":{}} {"config":{}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// The change list is a list even when nothing changed: a caller that
// iterates it should not have to tell null from empty first.
func TestTheChangeListIsAlwaysAList(t *testing.T) {
	h := start(t, nil)

	var read struct {
		Config json.RawMessage `json:"config"`
	}
	decode(t, h.get(t, "/api/config"), http.StatusOK, &read)

	// Saving what was just read changes nothing, which is the case that
	// would otherwise report null.
	var result struct {
		Changes []config.Change `json:"changes"`
	}
	decode(t, h.do(t, http.MethodPut, "/api/config",
		map[string]any{"config": read.Config}), http.StatusOK, &result)

	if result.Changes == nil {
		t.Error("changes came back as null rather than an empty list")
	}
	if len(result.Changes) != 0 {
		t.Errorf("saving an unchanged configuration reported %d changes: %+v",
			len(result.Changes), result.Changes)
	}
}

// ===== step 109: exporting =====

// The export is a file, not a view: it goes to disk under a name, and the
// bytes are the ones the gateway would have written itself.

func TestTheExportIsOfferedAsAFile(t *testing.T) {
	h := start(t, nil)

	resp := h.get(t, "/api/config/export")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	disposition := resp.Header.Get("Content-Disposition")
	if !strings.Contains(disposition, "attachment") {
		t.Errorf("Content-Disposition = %q; a browser would show it rather than save it", disposition)
	}
	// The extension matters: this is the format the gateway reads, so the
	// download should be droppable straight back into a data directory.
	if !strings.Contains(disposition, ".yaml") {
		t.Errorf("Content-Disposition = %q, want a .yaml filename", disposition)
	}
}

// What comes down has to be a configuration this build can read back. An
// export that needs editing before it loads is not a backup.
func TestTheExportLoadsBackIn(t *testing.T) {
	h := start(t, nil)

	body := bodyOf(t, h.get(t, "/api/config/export"))

	parsed, err := config.Parse([]byte(body))
	if err != nil {
		t.Fatalf("the export does not parse as a configuration: %v\n%s", err, body)
	}
	if parsed.Version != config.CurrentVersion {
		t.Errorf("version = %d, want %d", parsed.Version, config.CurrentVersion)
	}
}

// The two uses are different and conflating them fails one of them: a
// configuration meant for a bug report must not carry an API token, and
// one meant as a backup is useless without them.
func TestSecretsAreHiddenUnlessAskedFor(t *testing.T) {
	const token = "sk-not-a-real-token"

	h := start(t, func(opts *api.Options) {
		opts.Configs = configs(t, func(cfg *config.Config) {
			cfg.MCPServers = map[string]config.MCPServer{
				"remote": {
					Transport: config.TransportStreamableHTTP,
					URL:       "https://example.com/mcp",
					Headers:   map[string]string{"Authorization": token},
					Enabled:   true,
					Timeout:   time.Minute,
				},
			}
		})
	})

	plain := bodyOf(t, h.get(t, "/api/config/export"))
	if strings.Contains(plain, token) {
		t.Error("the default export carries the token")
	}

	withSecrets := bodyOf(t, h.get(t, "/api/config/export?secrets=true"))
	if !strings.Contains(withSecrets, token) {
		t.Errorf("an export asked for with its secrets does not carry the token:\n%s", withSecrets)
	}
}

// A typo in the parameter that decides whether secrets come out must not
// read as a deliberate "no": the caller would get a file they believe is
// a backup and is not.
func TestAMisspeltSecretsParameterIsRefused(t *testing.T) {
	h := start(t, nil)

	resp := h.get(t, "/api/config/export?secrets=yes-please")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// Asking for secrets is worth a line in the log: it is the one request
// that hands credentials to whoever made it.
func TestExportingSecretsIsLogged(t *testing.T) {
	h := start(t, nil)

	h.get(t, "/api/config/export")
	if strings.Contains(logText(h.Logs), "exported with its secrets") {
		t.Error("a redacted export was logged as carrying secrets")
	}

	h.get(t, "/api/config/export?secrets=true")
	if !strings.Contains(logText(h.Logs), "exported with its secrets") {
		t.Errorf("exporting the secrets was not logged:\n%s", logText(h.Logs))
	}
}
