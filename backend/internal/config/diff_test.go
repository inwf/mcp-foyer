package config_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/config"
)

func changeFor(changes []config.Change, field string) (config.Change, bool) {
	for _, c := range changes {
		if c.Field == field {
			return c, true
		}
	}
	return config.Change{}, false
}

func TestDiffFindsNoChangeBetweenEqualConfigurations(t *testing.T) {
	if got := config.Diff(populated(), populated()); len(got) != 0 {
		t.Errorf("Diff = %v, want no changes", got)
	}
}

func TestDiffReportsScalarChanges(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Listen.Port = 9000
	after.Logging.Level = config.LevelDebug
	after.Gateway.NotifyDebounce = 10 * time.Second

	changes := config.Diff(before, after)

	for _, want := range []struct{ field, old, new string }{
		{"listen.port", "7788", "9000"},
		{"logging.level", "info", "debug"},
		{"gateway.notifyDebounce", "3s", "10s"},
	} {
		got, ok := changeFor(changes, want.field)
		if !ok {
			t.Errorf("no change reported for %q, got %v", want.field, changes)
			continue
		}
		if got.Old != want.old || got.New != want.new {
			t.Errorf("%s = %q -> %q, want %q -> %q", want.field, got.Old, got.New, want.old, want.new)
		}
	}
}

func TestDiffReportsAddedAndRemovedServers(t *testing.T) {
	before := config.Default()
	before.MCPServers = map[string]config.MCPServer{
		"going": {Transport: config.TransportStdio, Command: "npx", Enabled: true, Timeout: time.Minute},
	}
	after := config.Default()
	after.MCPServers = map[string]config.MCPServer{
		"arriving": {Transport: config.TransportStdio, Command: "uvx", Enabled: true, Timeout: time.Minute},
	}

	changes := config.Diff(before, after)

	removed, ok := changeFor(changes, "mcpServers.going.command")
	if !ok {
		t.Fatalf("removal not reported, got %v", changes)
	}
	if removed.Old != "npx" || removed.New != config.UnsetValue {
		t.Errorf("removal = %q -> %q, want npx -> %s", removed.Old, removed.New, config.UnsetValue)
	}

	added, ok := changeFor(changes, "mcpServers.arriving.command")
	if !ok {
		t.Fatalf("addition not reported, got %v", changes)
	}
	if added.Old != config.UnsetValue || added.New != "uvx" {
		t.Errorf("addition = %q -> %q, want %s -> uvx", added.Old, added.New, config.UnsetValue)
	}
}

func TestDiffReportsNestedServerChanges(t *testing.T) {
	before := config.Default()
	before.MCPServers = map[string]config.MCPServer{
		"srv": {
			Transport: config.TransportStdio, Command: "npx", Enabled: true,
			Timeout: time.Minute, Env: map[string]string{"LOG": "info"},
		},
	}
	after := before.Clone()
	srv := after.MCPServers["srv"]
	srv.Enabled = false
	srv.Env["LOG"] = "debug"
	after.MCPServers["srv"] = srv

	changes := config.Diff(before, after)

	if got, ok := changeFor(changes, "mcpServers.srv.enabled"); !ok {
		t.Errorf("enabled change not reported, got %v", changes)
	} else if got.Old != "true" || got.New != "false" {
		t.Errorf("enabled = %q -> %q, want true -> false", got.Old, got.New)
	}
	if got, ok := changeFor(changes, "mcpServers.srv.env.LOG"); !ok {
		t.Errorf("env change not reported, got %v", changes)
	} else if got.Old != "info" || got.New != "debug" {
		t.Errorf("env.LOG = %q -> %q, want info -> debug", got.Old, got.New)
	}
}

// A change log is often the thing that gets pasted into an issue, so it
// must never carry a live credential.
func TestDiffRedactsSecretsOnBothSides(t *testing.T) {
	before := config.Default()
	before.MCPServers = map[string]config.MCPServer{
		"srv": {
			Transport: config.TransportStreamableHTTP,
			URL:       "https://user:old-password@example.com/mcp",
			Timeout:   time.Minute,
			Env:       map[string]string{"API_KEY": "sk-old-secret"},
		},
	}
	after := before.Clone()
	srv := after.MCPServers["srv"]
	srv.URL = "https://user:new-password@example.com/mcp"
	srv.Env["API_KEY"] = "sk-new-secret"
	after.MCPServers["srv"] = srv

	changes := config.Diff(before, after)

	rendered := ""
	for _, c := range changes {
		rendered += c.String() + "\n"
	}
	for _, leak := range []string{"sk-old-secret", "sk-new-secret", "old-password", "new-password"} {
		if strings.Contains(rendered, leak) {
			t.Errorf("the change log leaked %q:\n%s", leak, rendered)
		}
	}

	// The fact that a secret changed is still worth reporting, even
	// though the values are not.
	if _, ok := changeFor(changes, "mcpServers.srv.env.API_KEY"); !ok {
		t.Errorf("a changed secret was not reported at all:\n%s", rendered)
	}
}

// Both sides redact to the same placeholder, so a secret that did not
// change must not show up as a change either.
func TestDiffDoesNotInventChangesForUnchangedSecrets(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"srv": {
			Transport: config.TransportStdio, Command: "npx", Enabled: true,
			Timeout: time.Minute, Env: map[string]string{"API_KEY": "sk-secret"},
		},
	}

	if got := config.Diff(cfg, cfg.Clone()); len(got) != 0 {
		t.Errorf("Diff = %v, want no changes", got)
	}
}

func TestDiffOrdersChangesByField(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Startup.MaxRetries = 9
	after.Listen.Port = 9000
	after.Gateway.SessionTimeout = time.Hour
	after.Logging.Level = config.LevelError

	changes := config.Diff(before, after)
	if len(changes) < 4 {
		t.Fatalf("Diff = %v, want at least 4 changes", changes)
	}
	for i := 1; i < len(changes); i++ {
		if changes[i-1].Field > changes[i].Field {
			t.Errorf("changes are not ordered by field: %q comes before %q",
				changes[i-1].Field, changes[i].Field)
		}
	}
}

func TestChangeStringIsReadable(t *testing.T) {
	c := config.Change{Field: "listen.port", Old: "7788", New: "9000"}
	if got, want := c.String(), "listen.port: 7788 -> 9000"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// The web UI reads which field changed off the wire. Without these tags,
// encoding/json would emit "Field"/"Old"/"New" and the frontend's
// change.field would always be undefined — so the "restart to apply this"
// hint would never appear for a listen save.
func TestChangeSerializesWithTheNamesTheUIUses(t *testing.T) {
	c := config.Change{Field: "listen.port", Old: "7788", New: "9000"}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if got, want := string(data),
		`{"field":"listen.port","old":"7788","new":"9000"}`; got != want {
		t.Errorf("wire = %s, want %s", got, want)
	}
}

// Update hands back the same changes Diff would produce, which is what
// makes it usable as an audit trail.
func TestUpdateReportsTheChangesItMade(t *testing.T) {
	m, _ := newManager(t)

	changes, err := m.Update(func(c *config.Config) error {
		c.Listen.Port = 9005
		c.Logging.Level = config.LevelWarn
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if got, ok := changeFor(changes, "listen.port"); !ok {
		t.Errorf("port change not reported, got %v", changes)
	} else if got.New != "9005" {
		t.Errorf("port change = %v, want new value 9005", got)
	}
	if _, ok := changeFor(changes, "logging.level"); !ok {
		t.Errorf("level change not reported, got %v", changes)
	}
}
