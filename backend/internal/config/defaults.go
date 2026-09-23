package config

import "time"

// CurrentVersion is the schema version this build reads and writes.
const CurrentVersion = 1

// Default returns a configuration that is safe to run as-is: it listens
// on loopback only, connects to no upstream servers, and has a timeout
// on every operation that can block.
//
// Every call returns independently owned maps and slices, so a caller
// may freely mutate the result.
func Default() Config {
	return Config{
		Version: CurrentVersion,

		Listen: Listen{
			// Loopback rather than a wildcard address: mcp-foyer's
			// management API can change which commands get executed as
			// child processes, so reaching it must be an explicit
			// choice rather than a side effect of the default.
			Host: "127.0.0.1",
			Port: 7788,
		},

		Logging: Logging{
			Level:            LevelInfo,
			Format:           "console",
			MaxAge:           7 * 24 * time.Hour,
			MaxSizeMB:        50,
			MCPWireDebug:     false,
			APIDebug:         false,
			GatewayDebug:     false,
			ShowTraceContext: true,
		},

		Security: Security{
			// Same reasoning as the listen address. Widening this to a
			// LAN range is a deliberate act, not a default.
			AllowedNetworks:       []string{"127.0.0.1/32", "::1/128"},
			MaxConnections:        50,
			MaxConcurrentRequests: 50,
			ConnectionTimeout:     30 * time.Second,
			// Must exceed ConnectionTimeout: a client holding an idle
			// event stream is expected, not evidence of a stuck peer.
			IdleConnectionTimeout: 5 * time.Minute,
		},

		Gateway: Gateway{
			DefaultSessionMode: SessionModeStateful,
			SessionModeRules: SessionModeRules{
				Stateful:  []string{},
				Stateless: []string{},
			},
			SessionTimeout: 30 * time.Minute,
			// Long enough to absorb a burst of upstream changes into one
			// notification, short enough that clients still feel it as
			// immediate.
			NotifyDebounce:            3 * time.Second,
			KeepAlive:                 30 * time.Second,
			KeepAliveFailureThreshold: 3,
		},

		Startup: Startup{
			ConnectDelay: 3 * time.Second,
			MaxRetries:   3,
			RetryBackoff: 5 * time.Second,
		},

		MCPServers: map[string]MCPServer{},
	}
}

// DefaultReadyTimeout is how long a server with ready patterns is waited
// for when it does not say.
//
// A compromise between two costs. Waiting longer helps a server that is
// genuinely slow to start — a package manager fetching one that has never
// run on this machine. Waiting less limits the damage when the pattern is
// wrong, which is the more likely mistake: that wait is paid in full on
// every connection attempt and every retry, before there is any evidence
// the server works at all. Anyone who needs longer can say so, in the
// field right next to the patterns.
const DefaultReadyTimeout = 30 * time.Second

// DefaultMCPServer returns the values an upstream server entry takes for
// the fields it does not specify.
//
// Enabled defaults to true, which is why loading seeds this value before
// decoding an entry: after decoding, an omitted "enabled" and an
// explicit "enabled: false" would be indistinguishable.
//
// ReadyTimeout is deliberately left at zero rather than seeded here: it
// only means anything alongside ready patterns, and seeding it would
// write a readyTimeout into the file of every server that has none.
func DefaultMCPServer() MCPServer {
	return MCPServer{
		Transport: TransportStdio,
		Enabled:   true,
		Timeout:   60 * time.Second,
	}
}
