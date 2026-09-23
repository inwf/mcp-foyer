package upstream_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/testmcp"
)

// Some servers cannot answer the moment they are started. These are
// about the wait that exists for them: that it ends when the server says
// so, and — more importantly — that it ends anyway when the server never
// does.

func TestConnectWaitsForTheServerToSayItIsReady(t *testing.T) {
	conn, store, _ := newConn(t, modeSlowReady, func(cfg *config.MCPServer) {
		cfg.ReadyPatterns = []string{"^ready:"}
		cfg.ReadyTimeout = 20 * time.Second
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if !conn.Status().Connected() {
		t.Fatalf("state = %q, want connected", conn.Status().State)
	}

	logged := logText(store)
	if !strings.Contains(logged, "reported that it is ready") {
		t.Errorf("nothing records that the ready line was seen:\n%s", logged)
	}
	// It must have waited for the right line rather than the first one:
	// this server prints something else before it is ready.
	if strings.Contains(logged, "none of the ready patterns matched") {
		t.Errorf("the wait timed out instead of matching:\n%s", logged)
	}
}

// A pattern that never matches is a typo, or a message the server
// stopped printing. Treating that as a failure would turn one wrong
// character in the configuration into a server that can never connect,
// so the handshake is attempted anyway and the retry policy — which is
// the mechanism that does decide whether a server is reachable — gets to
// make that call.
func TestAReadyPatternThatNeverMatchesDoesNotStopTheServer(t *testing.T) {
	conn, store, _ := newConn(t, modeFull, func(cfg *config.MCPServer) {
		cfg.ReadyPatterns = []string{"this will never be printed"}
		cfg.ReadyTimeout = 200 * time.Millisecond
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	started := time.Now()
	if err := conn.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	elapsed := time.Since(started)

	if !conn.Status().Connected() {
		t.Fatalf("state = %q, want connected despite the pattern never matching",
			conn.Status().State)
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("connected in %v, which is too fast to have waited for the timeout", elapsed)
	}

	// Warned about, because nothing else would tell someone that the
	// pattern they wrote is not the one their server prints.
	if logged := logText(store); !strings.Contains(logged, "none of the ready patterns matched") {
		t.Errorf("the timeout was not reported:\n%s", logged)
	}
}

// A server with no patterns is every ordinary server, and it must not
// pay for a feature it does not use.
func TestAServerWithoutReadyPatternsIsNotWaitedFor(t *testing.T) {
	conn, store, _ := newConn(t, modeFull, func(*config.MCPServer) {})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	logged := logText(store)
	for _, unwanted := range []string{"reported that it is ready", "none of the ready patterns"} {
		if strings.Contains(logged, unwanted) {
			t.Errorf("a server with no patterns went through the wait (%q):\n%s", unwanted, logged)
		}
	}
}

// The stdio output is read for two purposes at once — the log and the
// pattern — and the reading must not consume it for either.
func TestTheReadyLineIsStillLogged(t *testing.T) {
	conn, store, _ := newConn(t, modeSlowReady, func(cfg *config.MCPServer) {
		cfg.ReadyPatterns = []string{"^ready:"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if logged := logText(store); !strings.Contains(logged, testmcp.ReadyLine) {
		t.Errorf("the line the server printed is not in the log:\n%s", logged)
	}
}

// Exposure is a gateway-side decision, but readiness is not: it changes
// how the connection is made, so a change to it has to rebuild one.
func TestChangingTheReadyPatternsReconnects(t *testing.T) {
	m, _ := managerFixture(t)
	cfg := configWith(t, map[string]string{"alpha": modeFull})
	m.Apply(cfg)
	before, _ := m.Get("alpha")

	server := cfg.MCPServers["alpha"]
	server.ReadyPatterns = []string{"^ready:"}
	cfg.MCPServers["alpha"] = server

	_, _, changed := m.Apply(cfg)

	if len(changed) != 1 {
		t.Fatalf("changed = %v, want alpha; readiness is a dialling setting", changed)
	}
	if after, _ := m.Get("alpha"); before == after {
		t.Error("the connection was reused even though how it is made changed")
	}
}
