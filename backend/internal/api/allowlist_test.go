package api_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/logging"
)

// router builds an API and returns its handler, so that a test can
// present any peer address it likes. A real connection would always
// arrive from loopback.
func router(t *testing.T, networks []string) (http.Handler, *logging.Store) {
	t.Helper()

	store := logging.NewStore(200)
	log, err := logging.New(logging.Options{Level: slog.LevelDebug, Store: store})
	if err != nil {
		t.Fatalf("build the logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })

	security := config.Default().Security
	security.AllowedNetworks = networks

	built, err := api.New(api.Options{
		Version:  "test",
		Logger:   log.For(logging.ModuleAPI),
		Security: security,
		Configs:  configs(t, nil),
		Logs:     store,
	})
	if err != nil {
		t.Fatalf("build the api: %v", err)
	}
	return built.Handler(), store
}

// reach sends a request from peer and reports the status.
func reach(t *testing.T, handler http.Handler, peer string, headers ...string) int {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = peer
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder.Code
}

func TestAllowlistMatrix(t *testing.T) {
	cases := []struct {
		name     string
		networks []string
		peer     string
		want     int
	}{
		// An exact IPv4 address, written without a mask, means that host.
		{"exact address allowed", []string{"192.168.1.5"}, "192.168.1.5:4000", http.StatusOK},
		{"exact address rejects a neighbour", []string{"192.168.1.5"}, "192.168.1.6:4000", http.StatusForbidden},

		// A block admits everything inside it and nothing outside.
		{"block allows a member", []string{"10.0.0.0/8"}, "10.4.5.6:4000", http.StatusOK},
		{"block rejects an outsider", []string{"10.0.0.0/8"}, "11.0.0.1:4000", http.StatusForbidden},
		{"host bits in a block are ignored", []string{"10.1.2.3/8"}, "10.9.9.9:4000", http.StatusOK},

		// Loopback is what the default permits, and what a local browser
		// and the CLI both arrive from.
		{"loopback v4", []string{"127.0.0.0/8"}, "127.0.0.1:4000", http.StatusOK},
		{"loopback v6", []string{"::1"}, "[::1]:4000", http.StatusOK},

		// IPv6, both as a single host and as a block.
		{"v6 host", []string{"2001:db8::1"}, "[2001:db8::1]:4000", http.StatusOK},
		{"v6 block allows", []string{"2001:db8::/32"}, "[2001:db8:1234::9]:4000", http.StatusOK},
		{"v6 block rejects", []string{"2001:db8::/32"}, "[2001:db9::1]:4000", http.StatusForbidden},

		// A client reaching a dual-stack listener over IPv6 presents an
		// IPv4-mapped address. A rule about a host has to match it: the
		// operator wrote about a machine, not about a wire format.
		{"v4-mapped matches a v4 rule", []string{"127.0.0.0/8"}, "[::ffff:127.0.0.1]:4000", http.StatusOK},
		{"v4-mapped is still checked", []string{"127.0.0.0/8"}, "[::ffff:8.8.8.8]:4000", http.StatusForbidden},

		// Several blocks are a union.
		{"first of several matches", []string{"127.0.0.0/8", "10.0.0.0/8"}, "127.0.0.1:1", http.StatusOK},
		{"last of several matches", []string{"127.0.0.0/8", "10.0.0.0/8"}, "10.0.0.1:1", http.StatusOK},
		{"none of several matches", []string{"127.0.0.0/8", "10.0.0.0/8"}, "8.8.8.8:1", http.StatusForbidden},

		// An empty list is an explicit decision to accept anyone, which
		// is not what leaving the key out does.
		{"empty list allows anyone", []string{}, "203.0.113.9:4000", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, _ := router(t, tc.networks)
			if got := reach(t, handler, tc.peer); got != tc.want {
				t.Errorf("peer %s against %v: status = %d, want %d",
					tc.peer, tc.networks, got, tc.want)
			}
		})
	}
}

// Leaving the key out asks for the safe default. This pins the
// difference from an explicitly empty list, because confusing the two
// silently exposes the listener.
func TestTheDefaultConfigurationPermitsLoopbackOnly(t *testing.T) {
	networks := config.Default().Security.AllowedNetworks
	if len(networks) == 0 {
		t.Fatal("the default allows every client; it should permit loopback only")
	}

	handler, _ := router(t, networks)
	if got := reach(t, handler, "127.0.0.1:4000"); got != http.StatusOK {
		t.Errorf("loopback: status = %d, want 200", got)
	}
	if got := reach(t, handler, "203.0.113.9:4000"); got != http.StatusForbidden {
		t.Errorf("a public address: status = %d, want 403", got)
	}
}

// A forwarding header is supplied by the client. Honouring one here
// would let anyone past the allowlist by claiming to be loopback, and
// gin trusts every proxy unless told otherwise — so this is a real
// bypass rather than a hypothetical one.
func TestAForwardedHeaderCannotBypassTheAllowlist(t *testing.T) {
	handler, _ := router(t, []string{"127.0.0.0/8"})

	spoofs := [][]string{
		{"X-Forwarded-For", "127.0.0.1"},
		{"X-Forwarded-For", "127.0.0.1, 10.0.0.1"},
		{"X-Real-Ip", "127.0.0.1"},
		{"Forwarded", "for=127.0.0.1"},
	}
	for _, headers := range spoofs {
		if got := reach(t, handler, "203.0.113.9:4000", headers...); got != http.StatusForbidden {
			t.Errorf("%s: %s let a public address through with status %d",
				headers[0], headers[1], got)
		}
	}
}

// The rejection has to name the address, since an operator locked out by
// their own configuration has nothing else to go on.
func TestARejectionIsLogged(t *testing.T) {
	handler, store := router(t, []string{"127.0.0.0/8"})
	reach(t, handler, "203.0.113.9:4000")

	if text := logText(store); !contains(text, "203.0.113.9") {
		t.Errorf("the log does not name the rejected address:\n%s", text)
	}
}

// A peer address that cannot be read cannot be checked, and letting it
// through would defeat the rules it could not be checked against.
func TestAnUnreadablePeerAddressIsRejected(t *testing.T) {
	handler, _ := router(t, []string{"127.0.0.0/8"})

	if got := reach(t, handler, "not-an-address"); got != http.StatusForbidden {
		t.Errorf("status = %d, want 403", got)
	}
}

// A malformed block is a startup error. Discovering it per request would
// turn a typo into a puzzling intermittent rejection.
func TestAMalformedNetworkIsRefusedAtStartup(t *testing.T) {
	security := config.Default().Security
	security.AllowedNetworks = []string{"127.0.0.0/8", "not-a-network"}

	_, err := api.New(api.Options{Version: "test", Security: security, Configs: configs(t, nil)})
	if err == nil {
		t.Fatal("New accepted a malformed allowed network")
	}
	if !contains(err.Error(), "not-a-network") {
		t.Errorf("error %q does not name the offending value", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
