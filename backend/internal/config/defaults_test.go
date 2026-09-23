package config_test

import (
	"testing"
	"time"

	"mcp-foyer/internal/config"
)

func TestDefaultListensOnLoopback(t *testing.T) {
	c := config.Default()

	if got, want := c.Listen.Host, "127.0.0.1"; got != want {
		t.Errorf("listen host = %q, want %q", got, want)
	}
	if got, want := c.Listen.Port, 7788; got != want {
		t.Errorf("listen port = %d, want %d", got, want)
	}
}

func TestDefaultAllowsOnlyLoopback(t *testing.T) {
	c := config.Default()

	want := []string{"127.0.0.1/32", "::1/128"}
	if len(c.Security.AllowedNetworks) != len(want) {
		t.Fatalf("allowed networks = %v, want %v", c.Security.AllowedNetworks, want)
	}
	for i, w := range want {
		if c.Security.AllowedNetworks[i] != w {
			t.Errorf("allowed networks[%d] = %q, want %q", i, c.Security.AllowedNetworks[i], w)
		}
	}
}

func TestDefaultLogging(t *testing.T) {
	c := config.Default()

	if got, want := c.Logging.Level, config.LevelInfo; got != want {
		t.Errorf("log level = %q, want %q", got, want)
	}
	if got, want := c.Logging.Format, "console"; got != want {
		t.Errorf("log format = %q, want %q", got, want)
	}
	if c.Logging.MCPWireDebug {
		t.Error("mcpWireDebug is on by default; verbose protocol tracing should be opt-in")
	}
	if c.Logging.APIDebug {
		t.Error("apiDebug is on by default; request body logging should be opt-in")
	}
}

func TestDefaultSessionMode(t *testing.T) {
	c := config.Default()

	if got, want := c.Gateway.DefaultSessionMode, config.SessionModeStateful; got != want {
		t.Errorf("default session mode = %q, want %q", got, want)
	}
}

func TestDefaultVersionIsCurrent(t *testing.T) {
	if got, want := config.Default().Version, config.CurrentVersion; got != want {
		t.Errorf("version = %d, want %d", got, want)
	}
}

// A zero duration silently means "no limit" for most of these, which
// turns a missing default into a hang rather than an error.
func TestDefaultDurationsArePositive(t *testing.T) {
	c := config.Default()

	durations := []struct {
		field string
		value time.Duration
	}{
		{"logging.maxAge", c.Logging.MaxAge},
		{"security.connectionTimeout", c.Security.ConnectionTimeout},
		{"security.idleConnectionTimeout", c.Security.IdleConnectionTimeout},
		{"gateway.sessionTimeout", c.Gateway.SessionTimeout},
		{"gateway.notifyDebounce", c.Gateway.NotifyDebounce},
		{"gateway.keepAlive", c.Gateway.KeepAlive},
		{"startup.connectDelay", c.Startup.ConnectDelay},
		{"startup.retryBackoff", c.Startup.RetryBackoff},
	}

	for _, d := range durations {
		if d.value <= 0 {
			t.Errorf("%s = %v, want a positive duration", d.field, d.value)
		}
	}
}

// An idle event stream is normal, so it must not be cut short by the
// timeout meant for connections that have stopped making progress.
func TestDefaultIdleTimeoutOutlivesConnectionTimeout(t *testing.T) {
	c := config.Default()

	if c.Security.IdleConnectionTimeout <= c.Security.ConnectionTimeout {
		t.Errorf("idleConnectionTimeout (%v) must exceed connectionTimeout (%v)",
			c.Security.IdleConnectionTimeout, c.Security.ConnectionTimeout)
	}
}

func TestDefaultCountsArePositive(t *testing.T) {
	c := config.Default()

	counts := []struct {
		field string
		value int
	}{
		{"logging.maxSizeMB", c.Logging.MaxSizeMB},
		{"security.maxConnections", c.Security.MaxConnections},
		{"security.maxConcurrentRequests", c.Security.MaxConcurrentRequests},
		{"gateway.keepAliveFailureThreshold", c.Gateway.KeepAliveFailureThreshold},
		{"startup.maxRetries", c.Startup.MaxRetries},
	}

	for _, n := range counts {
		if n.value <= 0 {
			t.Errorf("%s = %d, want a positive count", n.field, n.value)
		}
	}
}

func TestDefaultHasNoUpstreamsButIsWritable(t *testing.T) {
	c := config.Default()

	if len(c.MCPServers) != 0 {
		t.Errorf("mcpServers = %v, want empty", c.MCPServers)
	}
	// Writing to a nil map panics, so callers would need a nil check
	// before adding the first server.
	c.MCPServers["added"] = config.MCPServer{Transport: config.TransportStdio}
	if _, ok := c.MCPServers["added"]; !ok {
		t.Error("could not add a server to the default configuration")
	}
}

// Maps and slices are reference types, so a shared backing array would
// let one caller's edit leak into every other caller's configuration.
func TestDefaultReturnsIndependentValues(t *testing.T) {
	a := config.Default()
	b := config.Default()

	a.MCPServers["only-in-a"] = config.MCPServer{Transport: config.TransportStdio}
	a.Security.AllowedNetworks[0] = "0.0.0.0/0"
	a.Gateway.SessionModeRules.Stateful = append(a.Gateway.SessionModeRules.Stateful, "Mutant")

	if _, leaked := b.MCPServers["only-in-a"]; leaked {
		t.Error("mcpServers is shared between calls to Default")
	}
	if b.Security.AllowedNetworks[0] != "127.0.0.1/32" {
		t.Errorf("allowedNetworks is shared between calls to Default: got %q",
			b.Security.AllowedNetworks[0])
	}
	if len(b.Gateway.SessionModeRules.Stateful) != 0 {
		t.Errorf("sessionModeRules is shared between calls to Default: got %v",
			b.Gateway.SessionModeRules.Stateful)
	}
}
