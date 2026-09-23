package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/config"
)

// saveAndReload writes cfg and reads it back, which is the round trip
// every test in this file is really about.
func saveAndReload(t *testing.T, cfg config.Config) config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return got
}

func savedText(t *testing.T, cfg config.Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(data)
}

// These are the values where "empty" and "absent" mean different things.
// Dropping them on save would silently change what the configuration
// says, so each one is pinned here against a future tidy-up.
//
// `exposedTools` is deliberately not one of them. Empty and absent both
// mean "expose nothing", so omitting it on save changes nothing, and
// there is no third meaning to reserve a spelling for. Anyone tempted to
// make the two distinguishable — so that one of them could mean "expose
// everything" — should read gateway.FilterTools first: a set that grows
// on its own when an upstream adds a tool is the thing being avoided.
func TestSavePreservesMeaningfulEmptyValues(t *testing.T) {
	t.Run("an empty allowlist admits every client", func(t *testing.T) {
		cfg := config.Default()
		cfg.Security.AllowedNetworks = []string{}

		got := saveAndReload(t, cfg)

		if len(got.Security.AllowedNetworks) != 0 {
			t.Errorf("allowedNetworks = %v after a round trip, want empty; "+
				"an omitted key falls back to loopback, which is the opposite meaning",
				got.Security.AllowedNetworks)
		}
	})

	t.Run("a disabled server stays disabled", func(t *testing.T) {
		cfg := config.Default()
		cfg.MCPServers = map[string]config.MCPServer{
			"off": {
				Transport: config.TransportStdio,
				Command:   "npx",
				Enabled:   false,
				Timeout:   time.Minute,
			},
		}

		got := saveAndReload(t, cfg)

		if got.MCPServers["off"].Enabled {
			t.Error("enabled = true after a round trip, want false; " +
				"an omitted key defaults to enabled, so a disabled server would start itself")
		}
	})
}

// Where empty and absent mean the same thing, the key is not worth the
// line it occupies.
func TestSaveOmitsMeaninglessEmptyValues(t *testing.T) {
	text := savedText(t, config.Default())

	for _, key := range []string{"stateful:", "stateless:"} {
		if strings.Contains(text, key) {
			t.Errorf("empty session mode rules are written out:\n%s", text)
		}
	}
}

func TestSaveOmitsUnsetServerFields(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"minimal": {
			Transport: config.TransportStdio,
			Command:   "npx",
			Enabled:   true,
			Timeout:   time.Minute,
		},
	}

	text := savedText(t, cfg)

	// A server that sets none of these should not carry empty
	// placeholders for them.
	for _, key := range []string{"args:", "env:", "headers:", "url:", "proxy:", "exposedTools:", "description:"} {
		if strings.Contains(text, key) {
			t.Errorf("unset field %q is written out:\n%s", key, text)
		}
	}
}

// Omitting an unset field must not change how it reads back.
func TestOmittedServerFieldsReloadAsEmpty(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"minimal": {
			Transport: config.TransportStdio,
			Command:   "npx",
			Enabled:   true,
			Timeout:   time.Minute,
		},
	}

	got := saveAndReload(t, cfg).MCPServers["minimal"]

	if len(got.Args) != 0 {
		t.Errorf("args = %v, want empty", got.Args)
	}
	if len(got.Env) != 0 {
		t.Errorf("env = %v, want empty", got.Env)
	}
	if len(got.ExposedTools) != 0 {
		t.Errorf("exposedTools = %v, want empty", got.ExposedTools)
	}
	if got.URL != "" {
		t.Errorf("url = %q, want empty", got.URL)
	}
}

// Saving a file that was just loaded must not change it, or every read
// of the configuration would produce a spurious diff.
func TestSaveIsIdempotent(t *testing.T) {
	first := savedText(t, populated())

	reloaded, err := config.Parse([]byte(first))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	second := savedText(t, reloaded)

	if first != second {
		t.Errorf("saving a reloaded configuration changed it\n--- first ---\n%s\n--- second ---\n%s",
			first, second)
	}
}

func TestSaveAlwaysWritesVersion(t *testing.T) {
	// Without it a reader cannot tell an old file from a new one.
	if !strings.Contains(savedText(t, config.Default()), "version:") {
		t.Error("version is missing from a saved configuration")
	}
}
