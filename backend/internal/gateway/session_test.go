package gateway_test

import (
	"testing"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/gateway"
)

func rules(stateful, stateless []string) config.Gateway {
	return config.Gateway{
		DefaultSessionMode: config.SessionModeStateful,
		SessionModeRules:   config.SessionModeRules{Stateful: stateful, Stateless: stateless},
	}
}

// An explicit header outranks everything: a client that knows what it
// wants should get it.
func TestHeaderWinsOverEverything(t *testing.T) {
	cfg := rules(nil, []string{"Probe"})
	cfg.DefaultSessionMode = config.SessionModeStateless

	got := gateway.ResolveSessionMode("stateful", "Probe/1.0", cfg)

	if got != config.SessionModeStateful {
		t.Errorf("mode = %q, want stateful from the header", got)
	}
}

func TestHeaderIsCaseAndSpaceInsensitive(t *testing.T) {
	cfg := rules(nil, nil)

	for _, header := range []string{"stateless", "Stateless", "STATELESS", "  stateless  "} {
		if got := gateway.ResolveSessionMode(header, "", cfg); got != config.SessionModeStateless {
			t.Errorf("ResolveSessionMode(%q) = %q, want stateless", header, got)
		}
	}
}

func TestUnrecognisedHeaderFallsThrough(t *testing.T) {
	cfg := rules(nil, []string{"Probe"})

	// A nonsense header must not silently select a mode of its own.
	if got := gateway.ResolveSessionMode("sticky", "Probe/1.0", cfg); got != config.SessionModeStateless {
		t.Errorf("mode = %q, want the User-Agent rule to apply", got)
	}
	if got := gateway.ResolveSessionMode("", "Unknown/1.0", cfg); got != config.SessionModeStateful {
		t.Errorf("mode = %q, want the configured default", got)
	}
}

func TestUserAgentRules(t *testing.T) {
	cfg := rules([]string{"Editor"}, []string{"Notebook"})

	tests := []struct {
		userAgent string
		want      config.SessionMode
	}{
		{"Editor/2.1", config.SessionModeStateful},
		{"Notebook/0.9", config.SessionModeStateless},
		{"editor/2.1", config.SessionModeStateful}, // matching ignores case
		{"NOTEBOOK/0.9", config.SessionModeStateless},
		{"Something/1.0", config.SessionModeStateful}, // no rule, so the default
		{"", config.SessionModeStateful},
	}

	for _, tt := range tests {
		if got := gateway.ResolveSessionMode("", tt.userAgent, cfg); got != tt.want {
			t.Errorf("ResolveSessionMode(user agent %q) = %q, want %q", tt.userAgent, got, tt.want)
		}
	}
}

// A general rule and a specific one have to be able to coexist, which
// only works if the more specific keyword wins.
func TestTheMoreSpecificRuleWins(t *testing.T) {
	cfg := rules([]string{"EditorPro"}, []string{"Editor"})

	if got := gateway.ResolveSessionMode("", "EditorPro/1.0", cfg); got != config.SessionModeStateful {
		t.Errorf("EditorPro got %q, want stateful from the longer keyword", got)
	}
	if got := gateway.ResolveSessionMode("", "EditorLite/1.0", cfg); got != config.SessionModeStateless {
		t.Errorf("EditorLite got %q, want stateless from the shorter keyword", got)
	}
}

// Listing the same keyword under both modes is a mistake in the
// configuration, so the outcome only has to be predictable. Stateful
// wins because it supports strictly more of the protocol.
func TestAnAmbiguousRuleResolvesToStateful(t *testing.T) {
	cfg := rules([]string{"Client"}, []string{"Client"})

	for i := 0; i < 20; i++ {
		if got := gateway.ResolveSessionMode("", "Client/1.0", cfg); got != config.SessionModeStateful {
			t.Fatalf("mode = %q, want stateful", got)
		}
	}
}

func TestConfiguredDefaultApplies(t *testing.T) {
	stateless := rules(nil, nil)
	stateless.DefaultSessionMode = config.SessionModeStateless

	if got := gateway.ResolveSessionMode("", "Anything/1.0", stateless); got != config.SessionModeStateless {
		t.Errorf("mode = %q, want the configured stateless default", got)
	}
}

// A configuration that never set a default must still resolve, and to
// the mode that supports the most.
func TestMissingDefaultResolvesToStateful(t *testing.T) {
	var empty config.Gateway

	if got := gateway.ResolveSessionMode("", "", empty); got != config.SessionModeStateful {
		t.Errorf("mode = %q, want stateful", got)
	}
}

func TestEmptyKeywordsAreIgnored(t *testing.T) {
	cfg := rules([]string{""}, []string{""})

	// An empty keyword would otherwise match every User-Agent.
	if got := gateway.ResolveSessionMode("", "Anything/1.0", cfg); got != config.SessionModeStateful {
		t.Errorf("mode = %q, want the default rather than an empty-keyword match", got)
	}
}
