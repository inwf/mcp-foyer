package upstream_test

import (
	"context"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/upstream"
)

// waitFor polls until the named server reaches a state, or the deadline
// passes.
func waitFor(t *testing.T, m *upstream.Manager, name string, state upstream.State) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	last := upstream.State("absent")
	for time.Now().Before(deadline) {
		if conn, ok := m.Get(name); ok {
			last = conn.Status().State
			if last == state {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server %q is %q, want %q within the deadline", name, last, state)
}

// kill stops the child process behind a stdio connection, the way a
// crash would.
func kill(t *testing.T, conn *upstream.Conn) {
	t.Helper()

	proc, err := os.FindProcess(conn.Status().PID)
	if err != nil {
		t.Fatalf("find the child process: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill the child process: %v", err)
	}
}

// The supervisor retries on the schedule startup uses: the base
// interval, doubling, capped — and it stops when the context ends.
func TestSupervisedReconnectFollowsTheBackoffSchedule(t *testing.T) {
	m, _ := managerFixture(t)
	broken := serverConfig(t, modeFull)
	broken.Command = "/nonexistent/definitely-not-a-program"
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{"broken": broken}
	m.Apply(cfg)

	// A deliberate connect that gives up leaves the server failed and
	// wanted, which is the state the supervisor inherits.
	if err := m.Connect(context.Background(), "broken", fastStartup()); err == nil {
		t.Fatal("connecting to a command that does not exist succeeded")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var waits []time.Duration
	policy := upstream.RetryPolicy{
		Backoff:    time.Second,
		MaxBackoff: time.Minute,
		Sleep: func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			waits = append(waits, d)
			enough := len(waits) >= 4
			mu.Unlock()
			if enough {
				cancel()
			}
			return ctx.Err()
		},
	}
	m.KeepConnected(ctx, policy)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(waits)
		mu.Unlock()
		if n >= 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	got := slices.Clone(waits)
	mu.Unlock()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if !slices.Equal(got, want) {
		t.Errorf("waits = %v, want %v", got, want)
	}
	waitFor(t, m, "broken", upstream.StateFailed)
}

// A server that drops mid-session comes back on its own, with a fresh
// child process.
func TestASupervisedServerComesBackAfterItDrops(t *testing.T) {
	m, _ := managerFixture(t)
	m.Apply(configWith(t, map[string]string{"alpha": modeFull}))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := m.Connect(ctx, "alpha", fastStartup()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn, _ := m.Get("alpha")
	before := conn.Status().PID

	m.KeepConnected(ctx, upstream.RetryPolicy{Backoff: time.Millisecond, MaxBackoff: time.Second})

	kill(t, conn)

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if status := conn.Status(); status.Connected() && status.PID != before && status.PID != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := conn.Status()
	t.Fatalf("the dropped server did not come back with a new process: state %q, PID %d (was %d)",
		status.State, status.PID, before)
}

// Disconnecting is an instruction, not a failure: the supervisor stops,
// and the server stays off.
func TestADisconnectedServerIsNotBroughtBack(t *testing.T) {
	m, _ := managerFixture(t)
	m.Apply(configWith(t, map[string]string{"alpha": modeFull}))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Connect(ctx, "alpha", fastStartup()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn, _ := m.Get("alpha")

	// The supervisor parks in this sleep until the test lets it go, so
	// the disconnect below happens while a retry is already pending.
	release := make(chan struct{})
	policy := upstream.RetryPolicy{
		Backoff: time.Second,
		Sleep: func(ctx context.Context, d time.Duration) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	m.KeepConnected(ctx, policy)

	kill(t, conn)
	waitFor(t, m, "alpha", upstream.StateFailed)

	if err := m.Disconnect("alpha"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	close(release)

	// Let the parked retry wake up and decide; it must not dial.
	time.Sleep(200 * time.Millisecond)
	if slices.Contains(m.Connected(), "alpha") {
		t.Error("a disconnected server was brought back")
	}
	waitFor(t, m, "alpha", upstream.StateDisconnected)
}
