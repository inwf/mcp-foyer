package config_test

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/config"
)

func TestCloneIsDeep(t *testing.T) {
	original := populated()
	clone := original.Clone()

	clone.Security.AllowedNetworks[0] = "0.0.0.0/0"
	clone.Gateway.SessionModeRules.Stateful[0] = "Mutant"
	srv := clone.MCPServers["files"]
	srv.Args[0] = "--mutated"
	srv.Env["LOG"] = "mutated"
	clone.MCPServers["files"] = srv
	clone.MCPServers["added-later"] = config.MCPServer{Transport: config.TransportStdio}

	if original.Security.AllowedNetworks[0] != "10.0.0.0/8" {
		t.Error("allowedNetworks is shared with the clone")
	}
	if original.Gateway.SessionModeRules.Stateful[0] != "ClientA" {
		t.Error("sessionModeRules is shared with the clone")
	}
	if original.MCPServers["files"].Args[0] != "-y" {
		t.Error("server args are shared with the clone")
	}
	if original.MCPServers["files"].Env["LOG"] != "debug" {
		t.Error("server env is shared with the clone")
	}
	if _, leaked := original.MCPServers["added-later"]; leaked {
		t.Error("the server map is shared with the clone")
	}
}

func TestCloneEqualsOriginal(t *testing.T) {
	original := populated()
	if !reflect.DeepEqual(original.Clone(), original) {
		t.Error("Clone did not reproduce the original")
	}
}

// Nil and empty are different in this schema, so cloning must not turn
// one into the other.
func TestClonePreservesNilVersusEmpty(t *testing.T) {
	original := config.Default()
	original.MCPServers = map[string]config.MCPServer{
		"nil-slices":   {Transport: config.TransportStdio},
		"empty-slices": {Transport: config.TransportStdio, Args: []string{}, Env: map[string]string{}},
	}
	clone := original.Clone()

	if clone.MCPServers["nil-slices"].Args != nil {
		t.Error("a nil slice became non-nil")
	}
	if clone.MCPServers["empty-slices"].Args == nil {
		t.Error("an empty slice became nil")
	}
	if clone.MCPServers["empty-slices"].Env == nil {
		t.Error("an empty map became nil")
	}
}

func TestIsSecretKey(t *testing.T) {
	secret := []string{
		"API_KEY", "api_key", "ApiKey",
		"TOKEN", "GITHUB_TOKEN", "access_token",
		"PASSWORD", "passwd", "DB_PASSWORD",
		"SECRET", "CLIENT_SECRET",
		"Authorization", "AUTH_HEADER",
		"AWS_CREDENTIALS", "session_id", "X-Signature",
	}
	for _, name := range secret {
		if !config.IsSecretKey(name) {
			t.Errorf("IsSecretKey(%q) = false, want true", name)
		}
	}

	public := []string{"LOG_LEVEL", "HOME", "PATH", "NODE_ENV", "Content-Type", "User-Agent", "timeout"}
	for _, name := range public {
		if config.IsSecretKey(name) {
			t.Errorf("IsSecretKey(%q) = true, want false", name)
		}
	}
}

func TestRedactHidesSecretValues(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"srv": {
			Transport: config.TransportStreamableHTTP,
			URL:       "https://user:hunter2@example.com/mcp",
			Proxy:     "http://proxyuser:proxypass@127.0.0.1:8080",
			Env: map[string]string{
				"API_KEY":   "sk-live-abcdef",
				"LOG_LEVEL": "debug",
			},
			Headers: map[string]string{
				"Authorization": "Bearer sk-live-abcdef",
				"Content-Type":  "application/json",
			},
		},
	}

	got := cfg.Redact().MCPServers["srv"]

	t.Run("secret env values are hidden", func(t *testing.T) {
		if got.Env["API_KEY"] != config.RedactedValue {
			t.Errorf("API_KEY = %q, want redacted", got.Env["API_KEY"])
		}
	})
	t.Run("ordinary env values stay readable", func(t *testing.T) {
		if got.Env["LOG_LEVEL"] != "debug" {
			t.Errorf("LOG_LEVEL = %q, want debug", got.Env["LOG_LEVEL"])
		}
	})
	t.Run("secret headers are hidden", func(t *testing.T) {
		if got.Headers["Authorization"] != config.RedactedValue {
			t.Errorf("Authorization = %q, want redacted", got.Headers["Authorization"])
		}
	})
	t.Run("ordinary headers stay readable", func(t *testing.T) {
		if got.Headers["Content-Type"] != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got.Headers["Content-Type"])
		}
	})
	t.Run("credentials in a URL are hidden but the address remains", func(t *testing.T) {
		if strings.Contains(got.URL, "hunter2") {
			t.Errorf("url = %q, still contains the password", got.URL)
		}
		if !strings.Contains(got.URL, "example.com/mcp") {
			t.Errorf("url = %q, lost the address", got.URL)
		}
	})
	t.Run("credentials in a proxy are hidden", func(t *testing.T) {
		if strings.Contains(got.Proxy, "proxypass") {
			t.Errorf("proxy = %q, still contains the password", got.Proxy)
		}
	})
}

// A redacted URL is shown in the settings form and sent back from it, so
// the marker has to survive being reassembled into a URL as something a
// person can read. A marker containing characters that need
// percent-encoding would arrive as "%5Bredacted%5D".
func TestARedactedURLStaysReadable(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"srv": {
			Transport: config.TransportStreamableHTTP,
			URL:       "https://user:hunter2@example.com/mcp",
			Proxy:     "http://proxyuser:proxypass@127.0.0.1:8080",
		},
	}

	got := cfg.Redact().MCPServers["srv"]
	for name, value := range map[string]string{"url": got.URL, "proxy": got.Proxy} {
		if strings.Contains(value, "%") {
			t.Errorf("%s = %q, want no percent-encoding in the marker", name, value)
		}
		if !strings.Contains(value, config.RedactedURLUser) {
			t.Errorf("%s = %q, want it to carry %q", name, value, config.RedactedURLUser)
		}
	}
}

// The marker is what tells a write endpoint the credentials were not
// changed, so parsing a redacted URL has to yield it back exactly.
func TestARedactedURLReportsTheMarkerWhenParsed(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"srv": {
			Transport: config.TransportStreamableHTTP,
			URL:       "https://user:hunter2@example.com/mcp",
		},
	}

	parsed, err := url.Parse(cfg.Redact().MCPServers["srv"].URL)
	if err != nil {
		t.Fatalf("the redacted url does not parse: %v", err)
	}
	if parsed.User == nil {
		t.Fatal("the redacted url carries no userinfo")
	}
	if got := parsed.User.Username(); got != config.RedactedURLUser {
		t.Errorf("username = %q, want %q", got, config.RedactedURLUser)
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		t.Error("the redacted url still carries a password")
	}
}

// Redacting is for display. It must not damage the configuration it was
// derived from, or the next save would write placeholders to disk.
func TestRedactDoesNotTouchTheOriginal(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"srv": {
			Transport: config.TransportStreamableHTTP,
			URL:       "https://user:hunter2@example.com/mcp",
			Env:       map[string]string{"API_KEY": "sk-live-abcdef"},
		},
	}

	_ = cfg.Redact()

	if got := cfg.MCPServers["srv"].Env["API_KEY"]; got != "sk-live-abcdef" {
		t.Errorf("the original API_KEY became %q", got)
	}
	if !strings.Contains(cfg.MCPServers["srv"].URL, "hunter2") {
		t.Errorf("the original url became %q", cfg.MCPServers["srv"].URL)
	}
}

// Hiding a value must not hide that the setting exists, or the UI would
// show an incomplete configuration.
func TestRedactKeepsStructureIntact(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"srv": {
			Transport: config.TransportStdio,
			Command:   "npx",
			Timeout:   time.Minute,
			Env:       map[string]string{"API_KEY": "secret", "LOG": "debug"},
		},
	}

	got := cfg.Redact().MCPServers["srv"]

	if len(got.Env) != 2 {
		t.Errorf("env = %v, want both keys present", got.Env)
	}
	if got.Command != "npx" {
		t.Errorf("command = %q, want npx", got.Command)
	}
}

// A malformed URL cannot be taken apart safely; rewriting it would
// corrupt what the user typed.
func TestRedactLeavesUnparseableURLsAlone(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"srv": {Transport: config.TransportStreamableHTTP, URL: "://not a url"},
	}

	if got := cfg.Redact().MCPServers["srv"].URL; got != "://not a url" {
		t.Errorf("url = %q, want it left as written", got)
	}
}
