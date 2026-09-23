package upstream

import (
	"context"
	"testing"
	"time"

	"mcp-foyer/internal/config"
)

// The backoff schedule is arithmetic, so it is worth pinning exactly
// rather than only checking that it grows.
func TestRetryDelaySchedule(t *testing.T) {
	policy := RetryPolicy{Backoff: time.Second, MaxBackoff: time.Minute}

	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		time.Minute, // capped
		time.Minute,
	}
	for n, expected := range want {
		if got := policy.delay(n); got != expected {
			t.Errorf("delay(%d) = %v, want %v", n, got, expected)
		}
	}
}

// Without a cap, a handful of doublings on a server that is simply
// switched off would end up waiting hours.
func TestRetryDelayIsCappedByDefault(t *testing.T) {
	policy := RetryPolicy{Backoff: time.Second}

	if got := policy.delay(30); got != defaultMaxBackoff {
		t.Errorf("delay(30) = %v, want the default cap %v", got, defaultMaxBackoff)
	}
}

func TestRetryDelayWithoutBackoffIsZero(t *testing.T) {
	policy := RetryPolicy{}

	for n := 0; n < 3; n++ {
		if got := policy.delay(n); got != 0 {
			t.Errorf("delay(%d) = %v, want 0", n, got)
		}
	}
}

func TestRetryPolicyFromStartupConfig(t *testing.T) {
	cfg := config.Default().Startup
	policy := RetryPolicyFrom(cfg)

	if policy.MaxRetries != cfg.MaxRetries {
		t.Errorf("maxRetries = %d, want %d", policy.MaxRetries, cfg.MaxRetries)
	}
	if policy.Backoff != cfg.RetryBackoff {
		t.Errorf("backoff = %v, want %v", policy.Backoff, cfg.RetryBackoff)
	}
	if policy.MaxBackoff <= 0 {
		t.Error("maxBackoff is not set, so delays would grow without bound")
	}
}

// A failing connection must be retried exactly as many times as
// configured, and the delays must be the ones the schedule prescribes.
func TestConnectWithRetryExhaustsItsBudget(t *testing.T) {
	conn := New("broken", config.MCPServer{
		Transport: config.TransportStdio,
		Command:   "/nonexistent/definitely-not-a-program",
		Timeout:   time.Second,
	}, "test", Deps{})

	var waits []time.Duration
	policy := RetryPolicy{
		MaxRetries: 3,
		Backoff:    time.Second,
		MaxBackoff: time.Minute,
		// Record the delays instead of living through them.
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	policy.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}

	err := conn.ConnectWithRetry(context.Background(), policy)
	if err == nil {
		t.Fatal("ConnectWithRetry succeeded with a command that does not exist")
	}

	// Four attempts means three waits between them.
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waited %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("wait %d = %v, want %v", i, waits[i], want[i])
		}
	}
	if got := conn.Status().State; got != StateFailed {
		t.Errorf("state = %q, want failed", got)
	}
}

func TestConnectWithRetryReportsHowManyAttemptsItMade(t *testing.T) {
	conn := New("broken", config.MCPServer{
		Transport: config.TransportStdio,
		Command:   "/nonexistent/definitely-not-a-program",
		Timeout:   time.Second,
	}, "test", Deps{})

	policy := RetryPolicy{
		MaxRetries: 2,
		Backoff:    time.Millisecond,
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}

	err := conn.ConnectWithRetry(context.Background(), policy)
	if err == nil {
		t.Fatal("ConnectWithRetry succeeded unexpectedly")
	}
	if want := "3 attempt(s)"; !contains(err.Error(), want) {
		t.Errorf("error %q does not say how many attempts were made", err)
	}
}

// Zero retries means try once, not try forever and not skip entirely.
func TestZeroRetriesTriesOnce(t *testing.T) {
	conn := New("broken", config.MCPServer{
		Transport: config.TransportStdio,
		Command:   "/nonexistent/definitely-not-a-program",
		Timeout:   time.Second,
	}, "test", Deps{})

	slept := 0
	policy := RetryPolicy{
		MaxRetries: 0,
		Backoff:    time.Second,
		Sleep: func(context.Context, time.Duration) error {
			slept++
			return nil
		},
	}

	if err := conn.ConnectWithRetry(context.Background(), policy); err == nil {
		t.Fatal("ConnectWithRetry succeeded unexpectedly")
	}
	if slept != 0 {
		t.Errorf("waited %d times with no retries configured", slept)
	}
}

// A cancelled context will not improve on the next attempt, so the
// remaining budget should not be burned through.
func TestConnectWithRetryStopsWhenTheContextEnds(t *testing.T) {
	conn := New("broken", config.MCPServer{
		Transport: config.TransportStdio,
		Command:   "/nonexistent/definitely-not-a-program",
		Timeout:   time.Second,
	}, "test", Deps{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	attempts := 0
	policy := RetryPolicy{
		MaxRetries: 5,
		Backoff:    time.Millisecond,
		Sleep: func(context.Context, time.Duration) error {
			attempts++
			return nil
		},
	}

	if err := conn.ConnectWithRetry(ctx, policy); err == nil {
		t.Fatal("ConnectWithRetry succeeded with a cancelled context")
	}
	if attempts > 1 {
		t.Errorf("kept retrying %d times after the context was cancelled", attempts)
	}
}

func TestRetryGivesUpWhenWaitingIsInterrupted(t *testing.T) {
	conn := New("broken", config.MCPServer{
		Transport: config.TransportStdio,
		Command:   "/nonexistent/definitely-not-a-program",
		Timeout:   time.Second,
	}, "test", Deps{})

	policy := RetryPolicy{
		MaxRetries: 5,
		Backoff:    time.Millisecond,
		Sleep:      func(context.Context, time.Duration) error { return context.Canceled },
	}

	err := conn.ConnectWithRetry(context.Background(), policy)
	if err == nil {
		t.Fatal("ConnectWithRetry succeeded unexpectedly")
	}
	if !contains(err.Error(), "gave up waiting") {
		t.Errorf("error %q does not explain that waiting was interrupted", err)
	}
}

// The real sleep must return promptly when the context ends, or shutdown
// would hang for the length of the backoff.
func TestDefaultSleepRespectsTheContext(t *testing.T) {
	policy := RetryPolicy{}
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := policy.sleep(ctx, 10*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("sleep returned no error after the context was cancelled")
	}
	if elapsed > 2*time.Second {
		t.Errorf("sleep took %v, want it to end when the context did", elapsed)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
