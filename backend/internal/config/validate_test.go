package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/config"
)

// fieldsOf returns the dotted paths reported by a validation failure.
func fieldsOf(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		return nil
	}
	var ve *config.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error %v is not a *config.ValidationError", err)
	}
	fields := make([]string, len(ve.Errors))
	for i, fe := range ve.Errors {
		fields[i] = fe.Field
	}
	return fields
}

func hasField(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

func TestValidateAcceptsDefaults(t *testing.T) {
	if err := config.Default().Validate(); err != nil {
		t.Fatalf("the default configuration does not validate: %v", err)
	}
}

// A default configuration with one well-formed server of each transport
// is the baseline every negative case deviates from.
func TestValidateAcceptsEachTransport(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"by-stdio": {
			Transport: config.TransportStdio,
			Command:   "npx",
			Timeout:   time.Minute,
		},
		"by-http": {
			Transport: config.TransportStreamableHTTP,
			URL:       "https://example.com/mcp",
			Timeout:   time.Minute,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateTopLevelRules(t *testing.T) {
	tests := []struct {
		name   string
		mutcfg func(*config.Config)
		field  string
	}{
		{"unknown version", func(c *config.Config) { c.Version = 99 }, "version"},

		{"empty host", func(c *config.Config) { c.Listen.Host = "" }, "listen.host"},
		{"host with scheme", func(c *config.Config) { c.Listen.Host = "http://127.0.0.1" }, "listen.host"},
		{"host with port", func(c *config.Config) { c.Listen.Host = "127.0.0.1:7788" }, "listen.host"},
		{"port too large", func(c *config.Config) { c.Listen.Port = 70000 }, "listen.port"},
		{"port negative", func(c *config.Config) { c.Listen.Port = -1 }, "listen.port"},

		{"bad level", func(c *config.Config) { c.Logging.Level = "verbose" }, "logging.level"},
		{"bad format", func(c *config.Config) { c.Logging.Format = "xml" }, "logging.format"},
		{"zero maxAge", func(c *config.Config) { c.Logging.MaxAge = 0 }, "logging.maxAge"},
		{"zero maxSizeMB", func(c *config.Config) { c.Logging.MaxSizeMB = 0 }, "logging.maxSizeMB"},

		{"bad network", func(c *config.Config) {
			c.Security.AllowedNetworks = []string{"not-an-address"}
		}, "security.allowedNetworks[0]"},
		{"bad network among good ones", func(c *config.Config) {
			c.Security.AllowedNetworks = []string{"10.0.0.0/8", "999.1.1.1"}
		}, "security.allowedNetworks[1]"},
		{"zero maxConnections", func(c *config.Config) { c.Security.MaxConnections = 0 }, "security.maxConnections"},
		{"zero maxConcurrentRequests", func(c *config.Config) {
			c.Security.MaxConcurrentRequests = 0
		}, "security.maxConcurrentRequests"},
		{"zero connectionTimeout", func(c *config.Config) {
			c.Security.ConnectionTimeout = 0
		}, "security.connectionTimeout"},
		{"idle timeout below connection timeout", func(c *config.Config) {
			c.Security.ConnectionTimeout = time.Minute
			c.Security.IdleConnectionTimeout = 30 * time.Second
		}, "security.idleConnectionTimeout"},
		{"idle timeout equal to connection timeout", func(c *config.Config) {
			c.Security.ConnectionTimeout = time.Minute
			c.Security.IdleConnectionTimeout = time.Minute
		}, "security.idleConnectionTimeout"},

		{"bad session mode", func(c *config.Config) {
			c.Gateway.DefaultSessionMode = "sticky"
		}, "gateway.defaultSessionMode"},
		{"zero sessionTimeout", func(c *config.Config) {
			c.Gateway.SessionTimeout = 0
		}, "gateway.sessionTimeout"},
		{"negative debounce", func(c *config.Config) {
			c.Gateway.NotifyDebounce = -time.Second
		}, "gateway.notifyDebounce"},
		{"keepAlive on with zero threshold", func(c *config.Config) {
			c.Gateway.KeepAlive = 30 * time.Second
			c.Gateway.KeepAliveFailureThreshold = 0
		}, "gateway.keepAliveFailureThreshold"},

		{"negative retries", func(c *config.Config) { c.Startup.MaxRetries = -1 }, "startup.maxRetries"},
		{"zero retryBackoff", func(c *config.Config) { c.Startup.RetryBackoff = 0 }, "startup.retryBackoff"},
		{"negative connectDelay", func(c *config.Config) {
			c.Startup.ConnectDelay = -time.Second
		}, "startup.connectDelay"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			tt.mutcfg(&cfg)

			fields := fieldsOf(t, cfg.Validate())
			if !hasField(fields, tt.field) {
				t.Errorf("reported %v, want a problem on %q", fields, tt.field)
			}
		})
	}
}

// Values that look wrong but are deliberate must not be rejected.
func TestValidateAcceptsMeaningfulEdgeValues(t *testing.T) {
	tests := []struct {
		name   string
		mutcfg func(*config.Config)
	}{
		{"empty allowlist means allow everyone", func(c *config.Config) {
			c.Security.AllowedNetworks = []string{}
		}},
		{"bare IP is shorthand for a single host", func(c *config.Config) {
			c.Security.AllowedNetworks = []string{"127.0.0.1", "::1"}
		}},
		{"IPv6 CIDR", func(c *config.Config) {
			c.Security.AllowedNetworks = []string{"fd00::/8"}
		}},
		{"zero debounce means notify immediately", func(c *config.Config) {
			c.Gateway.NotifyDebounce = 0
		}},
		{"zero keepAlive disables the probe", func(c *config.Config) {
			c.Gateway.KeepAlive = 0
			c.Gateway.KeepAliveFailureThreshold = 0
		}},
		{"zero retries means try once", func(c *config.Config) { c.Startup.MaxRetries = 0 }},
		{"zero connectDelay means no stagger", func(c *config.Config) { c.Startup.ConnectDelay = 0 }},
		{"hostname as listen address", func(c *config.Config) { c.Listen.Host = "localhost" }},
		{"wildcard as listen address", func(c *config.Config) { c.Listen.Host = "0.0.0.0" }},
		{"IPv6 as listen address", func(c *config.Config) { c.Listen.Host = "::1" }},
		{"port zero asks for any free port", func(c *config.Config) { c.Listen.Port = 0 }},
		// Disabling is independent of what the server offers right now: a
		// name for a tool it no longer has is kept, so that the tool stays
		// off if it comes back.
		{"a disabled tool that is not exposed, or no longer offered", func(c *config.Config) {
			c.MCPServers = map[string]config.MCPServer{"srv": {
				Transport: config.TransportStdio, Command: "npx", Timeout: time.Minute,
				ExposedTools: []string{"read"}, DisabledTools: []string{"write", "gone"},
			}}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			tt.mutcfg(&cfg)

			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate rejected a valid configuration: %v", err)
			}
		})
	}
}

func TestValidateServerRules(t *testing.T) {
	valid := config.MCPServer{
		Transport: config.TransportStdio,
		Command:   "npx",
		Timeout:   time.Minute,
	}

	tests := []struct {
		name   string
		server config.MCPServer
		field  string
	}{
		{"unknown transport",
			config.MCPServer{Transport: "carrier-pigeon", Timeout: time.Minute},
			"mcpServers.srv.transport"},

		{"stdio without command",
			config.MCPServer{Transport: config.TransportStdio, Timeout: time.Minute},
			"mcpServers.srv.command"},
		{"stdio with a url",
			config.MCPServer{Transport: config.TransportStdio, Command: "npx",
				URL: "https://example.com/mcp", Timeout: time.Minute},
			"mcpServers.srv.url"},

		{"streamable-http without url",
			config.MCPServer{Transport: config.TransportStreamableHTTP, Timeout: time.Minute},
			"mcpServers.srv.url"},
		{"streamable-http with a command",
			config.MCPServer{Transport: config.TransportStreamableHTTP, Command: "npx",
				URL: "https://example.com/mcp", Timeout: time.Minute},
			"mcpServers.srv.command"},
		{"streamable-http with a non-http url",
			config.MCPServer{Transport: config.TransportStreamableHTTP,
				URL: "ftp://example.com/mcp", Timeout: time.Minute},
			"mcpServers.srv.url"},
		{"streamable-http with a hostless url",
			config.MCPServer{Transport: config.TransportStreamableHTTP,
				URL: "http:///mcp", Timeout: time.Minute},
			"mcpServers.srv.url"},

		{"proxy on stdio",
			config.MCPServer{Transport: config.TransportStdio, Command: "npx",
				Proxy: "http://127.0.0.1:8080", Timeout: time.Minute},
			"mcpServers.srv.proxy"},
		{"malformed proxy",
			config.MCPServer{Transport: config.TransportStreamableHTTP,
				URL: "https://example.com/mcp", Proxy: "not a url", Timeout: time.Minute},
			"mcpServers.srv.proxy"},

		{"zero timeout", func() config.MCPServer {
			s := valid
			s.Timeout = 0
			return s
		}(), "mcpServers.srv.timeout"},

		// A ready pattern is a regular expression, and the moment to say so
		// is while it is being written — not when a server it was meant to
		// help will not come up.
		{"a ready pattern that does not compile", func() config.MCPServer {
			s := valid
			s.ReadyPatterns = []string{"listening on [0-9"}
			return s
		}(), "mcpServers.srv.readyPatterns[0]"},
		{"an empty ready pattern", func() config.MCPServer {
			s := valid
			s.ReadyPatterns = []string{""}
			return s
		}(), "mcpServers.srv.readyPatterns[0]"},
		{"ready patterns on a transport that starts no process",
			config.MCPServer{Transport: config.TransportStreamableHTTP,
				URL: "https://example.com/mcp", Timeout: time.Minute,
				ReadyPatterns: []string{"ready"}},
			"mcpServers.srv.readyPatterns"},
		{"a negative ready timeout", func() config.MCPServer {
			s := valid
			s.ReadyPatterns = []string{"ready"}
			s.ReadyTimeout = -time.Second
			return s
		}(), "mcpServers.srv.readyTimeout"},
		// Setting one alone does nothing, which is worth saying rather than
		// leaving someone to wonder why their timeout has no effect.
		{"a ready timeout with nothing to wait for", func() config.MCPServer {
			s := valid
			s.ReadyTimeout = time.Second
			return s
		}(), "mcpServers.srv.readyTimeout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.MCPServers = map[string]config.MCPServer{"srv": tt.server}

			fields := fieldsOf(t, cfg.Validate())
			if !hasField(fields, tt.field) {
				t.Errorf("reported %v, want a problem on %q", fields, tt.field)
			}
		})
	}
}

// A tool that is both exposed and disabled has no reading that is right
// for everyone, so it is refused. The problem is reported on
// exposedTools, the list a form lets someone tick, and it names the tool
// so they know which box.
func TestAToolCannotBeBothExposedAndDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{"srv": {
		Transport: config.TransportStdio, Command: "npx", Timeout: time.Minute,
		ExposedTools:  []string{"read", "write", "write"},
		DisabledTools: []string{"write", "delete"},
	}}

	err := cfg.Validate()
	fields := fieldsOf(t, err)
	if len(fields) != 1 || fields[0] != "mcpServers.srv.exposedTools" {
		t.Fatalf("reported %v, want one problem on mcpServers.srv.exposedTools", fields)
	}
	if !strings.Contains(err.Error(), `"write"`) || strings.Contains(err.Error(), `"read"`) {
		t.Errorf("the message should name the tool in both lists and only that one: %v", err)
	}
}

func TestValidateServerNames(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"files", true},
		{"my-server", true},
		{"my_server", true},
		{"server2", true},
		{"7z", true},
		{"", false},
		{"-leading-dash", false},
		{"_leading_underscore", false},
		{"has space", false},
		{"has.dot", false},
		{"has/slash", false},
		{"has:colon", false},
		{"emoji-🚀", false},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
	}

	for _, tt := range tests {
		label := tt.name
		if label == "" {
			label = "(empty)"
		}
		if len(label) > 20 {
			label = label[:20] + "..."
		}

		t.Run(label, func(t *testing.T) {
			cfg := config.Default()
			cfg.MCPServers = map[string]config.MCPServer{
				tt.name: {Transport: config.TransportStdio, Command: "npx", Timeout: time.Minute},
			}

			err := cfg.Validate()
			if tt.valid && err != nil {
				t.Errorf("name %q rejected: %v", tt.name, err)
			}
			if !tt.valid && err == nil {
				t.Errorf("name %q accepted, want rejected", tt.name)
			}
		})
	}
}

// The whole point of collecting errors is that one pass over a broken
// file reports everything wrong with it.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	cfg := config.Default()
	cfg.Listen.Port = 99999
	cfg.Logging.Level = "verbose"
	cfg.Gateway.DefaultSessionMode = "sticky"
	cfg.MCPServers = map[string]config.MCPServer{
		"broken": {Transport: config.TransportStdio, Timeout: 0},
	}

	fields := fieldsOf(t, cfg.Validate())

	want := []string{
		"listen.port",
		"logging.level",
		"gateway.defaultSessionMode",
		"mcpServers.broken.command",
		"mcpServers.broken.timeout",
	}
	for _, w := range want {
		if !hasField(fields, w) {
			t.Errorf("reported %v, missing %q", fields, w)
		}
	}
}

// Map iteration order is random, so without sorting the reported order
// would change between runs and make failures hard to compare.
func TestValidateReportsServersInStableOrder(t *testing.T) {
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"zulu":   {Transport: config.TransportStdio, Timeout: time.Minute},
		"alpha":  {Transport: config.TransportStdio, Timeout: time.Minute},
		"mike":   {Transport: config.TransportStdio, Timeout: time.Minute},
		"bravo":  {Transport: config.TransportStdio, Timeout: time.Minute},
		"yankee": {Transport: config.TransportStdio, Timeout: time.Minute},
	}

	first := fieldsOf(t, cfg.Validate())
	for i := 0; i < 20; i++ {
		again := fieldsOf(t, cfg.Validate())
		if len(again) != len(first) {
			t.Fatalf("run %d reported %d problems, first run reported %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("run %d reported %v, first run reported %v", i, again, first)
			}
		}
	}
}

func TestValidationErrorMessage(t *testing.T) {
	t.Run("single problem reads as one line", func(t *testing.T) {
		cfg := config.Default()
		cfg.Listen.Port = 99999

		msg := cfg.Validate().Error()
		if strings.Contains(msg, "\n") {
			t.Errorf("message %q spans lines for a single problem", msg)
		}
		if !strings.Contains(msg, "listen.port") {
			t.Errorf("message %q does not name the field", msg)
		}
	})

	t.Run("several problems are listed with a count", func(t *testing.T) {
		cfg := config.Default()
		cfg.Listen.Port = 99999
		cfg.Logging.Level = "verbose"

		msg := cfg.Validate().Error()
		if !strings.Contains(msg, "2 problems") {
			t.Errorf("message %q does not state how many problems were found", msg)
		}
		for _, field := range []string{"listen.port", "logging.level"} {
			if !strings.Contains(msg, field) {
				t.Errorf("message %q does not mention %q", msg, field)
			}
		}
	})
}

// Loading and validating are separate steps: a file may parse cleanly
// and still describe an unusable configuration.
func TestParseThenValidate(t *testing.T) {
	cfg, err := config.Parse([]byte("listen:\n  port: 99999\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted port 99999")
	}
}
