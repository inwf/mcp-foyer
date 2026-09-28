package upstream_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/logging"
	"mcp-foyer/internal/upstream"
)

// managerFixture builds a manager wired to a bus and a log store.
func managerFixture(t *testing.T) (*upstream.Manager, *events.Bus) {
	t.Helper()

	store := logging.NewStore(200)
	log, err := logging.New(logging.Options{Level: slog.LevelDebug, Store: store})
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })

	bus := events.NewBus()
	t.Cleanup(bus.Close)

	m := upstream.NewManager("test", log, bus)
	t.Cleanup(m.CloseAll)

	return m, bus
}

// fastStartup removes the delays so tests do not wait on them.
func fastStartup() config.Startup {
	return config.Startup{
		ConnectDelay: 0,
		MaxRetries:   0,
		RetryBackoff: time.Millisecond,
	}
}

func configWith(t *testing.T, servers map[string]string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{}
	for name, mode := range servers {
		cfg.MCPServers[name] = serverConfig(t, mode)
	}
	return cfg
}

func TestApplyAddsServers(t *testing.T) {
	m, _ := managerFixture(t)

	added, removed, changed := m.Apply(configWith(t, map[string]string{
		"alpha": modeFull,
		"bravo": modeToolsOnly,
	}))

	if want := []string{"alpha", "bravo"}; !slices.Equal(added, want) {
		t.Errorf("added = %v, want %v", added, want)
	}
	if len(removed) != 0 || len(changed) != 0 {
		t.Errorf("removed = %v, changed = %v, want both empty", removed, changed)
	}
	if want := []string{"alpha", "bravo"}; !slices.Equal(m.Names(), want) {
		t.Errorf("Names = %v, want %v", m.Names(), want)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	m, _ := managerFixture(t)
	cfg := configWith(t, map[string]string{"alpha": modeFull})

	m.Apply(cfg)
	added, removed, changed := m.Apply(cfg)

	if len(added)+len(removed)+len(changed) != 0 {
		t.Errorf("applying the same configuration reported changes: +%v -%v ~%v",
			added, removed, changed)
	}
}

func TestApplyRemovesServers(t *testing.T) {
	m, _ := managerFixture(t)
	m.Apply(configWith(t, map[string]string{"alpha": modeFull, "bravo": modeFull}))

	_, removed, _ := m.Apply(configWith(t, map[string]string{"alpha": modeFull}))

	if want := []string{"bravo"}; !slices.Equal(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
	if _, still := m.Get("bravo"); still {
		t.Error("the removed server is still managed")
	}
}

func TestApplyReplacesAChangedServer(t *testing.T) {
	m, _ := managerFixture(t)
	cfg := configWith(t, map[string]string{"alpha": modeFull})
	m.Apply(cfg)
	before, _ := m.Get("alpha")

	server := cfg.MCPServers["alpha"]
	server.Args = append(server.Args, "--extra")
	cfg.MCPServers["alpha"] = server

	_, _, changed := m.Apply(cfg)

	if want := []string{"alpha"}; !slices.Equal(changed, want) {
		t.Errorf("changed = %v, want %v", changed, want)
	}
	after, _ := m.Get("alpha")
	if before == after {
		t.Error("the connection was reused even though its command line changed")
	}
}

// Editing a description must not tear down a working session:
// the user is labelling, not reconfiguring.
func TestApplyIgnoresPresentationOnlyChanges(t *testing.T) {
	m, _ := managerFixture(t)
	cfg := configWith(t, map[string]string{"alpha": modeFull})
	m.Apply(cfg)
	before, _ := m.Get("alpha")

	server := cfg.MCPServers["alpha"]
	server.Description = "a much better description"
	cfg.MCPServers["alpha"] = server

	_, _, changed := m.Apply(cfg)

	if len(changed) != 0 {
		t.Errorf("changed = %v, want none; a description change restarted the server", changed)
	}
	if after, _ := m.Get("alpha"); before != after {
		t.Error("the connection was rebuilt for a presentation-only change")
	}
}

// Exposure is decided on the gateway side; this package never reads the
// allow list. Rebuilding the connection for it would kill and respawn a
// child process every time someone switched one tool on, which — now that
// switching tools on is the ordinary way to use the thing — would be a
// process restart per click.
func TestApplyDoesNotRestartAServerForAnExposureChange(t *testing.T) {
	m, _ := managerFixture(t)
	cfg := configWith(t, map[string]string{"alpha": modeFull})
	m.Apply(cfg)
	before, _ := m.Get("alpha")

	server := cfg.MCPServers["alpha"]
	server.ExposedTools = []string{"echo"}
	cfg.MCPServers["alpha"] = server

	_, _, changed := m.Apply(cfg)

	if len(changed) != 0 {
		t.Errorf("changed = %v, want none; exposing one tool restarted the server", changed)
	}
	if after, _ := m.Get("alpha"); before != after {
		t.Error("the connection was rebuilt for a change the connection cannot see")
	}
}

// Disabling a tool is the same kind of change: the gateway enforces it,
// and the connection never sees it.
func TestApplyDoesNotRestartAServerForADisabledTool(t *testing.T) {
	m, _ := managerFixture(t)
	cfg := configWith(t, map[string]string{"alpha": modeFull})
	m.Apply(cfg)
	before, _ := m.Get("alpha")

	server := cfg.MCPServers["alpha"]
	server.DisabledTools = []string{"echo"}
	cfg.MCPServers["alpha"] = server

	_, _, changed := m.Apply(cfg)

	if len(changed) != 0 {
		t.Errorf("changed = %v, want none; disabling one tool restarted the server", changed)
	}
	if after, _ := m.Get("alpha"); before != after {
		t.Error("the connection was rebuilt for a change the connection cannot see")
	}
}

func TestConnectAllBringsUpEveryEnabledServer(t *testing.T) {
	m, _ := managerFixture(t)
	m.Apply(configWith(t, map[string]string{"alpha": modeFull, "bravo": modeToolsOnly}))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m.ConnectAll(ctx, fastStartup())

	if want := []string{"alpha", "bravo"}; !slices.Equal(m.Connected(), want) {
		t.Errorf("Connected = %v, want %v", m.Connected(), want)
	}
}

func TestConnectAllSkipsDisabledServers(t *testing.T) {
	m, _ := managerFixture(t)
	cfg := configWith(t, map[string]string{"on": modeFull, "off": modeFull})
	disabled := cfg.MCPServers["off"]
	disabled.Enabled = false
	cfg.MCPServers["off"] = disabled
	m.Apply(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m.ConnectAll(ctx, fastStartup())

	if want := []string{"on"}; !slices.Equal(m.Connected(), want) {
		t.Errorf("Connected = %v, want %v", m.Connected(), want)
	}
	// A disabled server stays configured and visible, just not connected.
	if _, managed := m.Get("off"); !managed {
		t.Error("a disabled server was dropped instead of left alone")
	}
}

// This is the property that makes the gateway usable at all: one broken
// entry in the configuration must not stop the rest from working.
func TestOneBrokenServerDoesNotAffectTheOthers(t *testing.T) {
	m, _ := managerFixture(t)

	cfg := configWith(t, map[string]string{"good": modeFull, "alsogood": modeToolsOnly})
	broken := serverConfig(t, modeFull)
	broken.Command = "/nonexistent/definitely-not-a-program"
	cfg.MCPServers["broken"] = broken
	m.Apply(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m.ConnectAll(ctx, fastStartup())

	if want := []string{"alsogood", "good"}; !slices.Equal(m.Connected(), want) {
		t.Errorf("Connected = %v, want %v", m.Connected(), want)
	}

	var brokenStatus upstream.Status
	for _, s := range m.Statuses() {
		if s.Name == "broken" {
			brokenStatus = s
		}
	}
	if brokenStatus.State != upstream.StateFailed {
		t.Errorf("the broken server is %q, want failed", brokenStatus.State)
	}
	if brokenStatus.Error == "" {
		t.Error("the broken server carries no explanation")
	}
}

func TestConnectAndDisconnectByName(t *testing.T) {
	m, _ := managerFixture(t)
	m.Apply(configWith(t, map[string]string{"alpha": modeFull}))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := m.Connect(ctx, "alpha", fastStartup()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !slices.Contains(m.Connected(), "alpha") {
		t.Fatal("alpha is not connected")
	}

	if err := m.Disconnect("alpha"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if slices.Contains(m.Connected(), "alpha") {
		t.Error("alpha is still connected after Disconnect")
	}
	// Disconnecting leaves it configured, so it can be reconnected.
	if _, managed := m.Get("alpha"); !managed {
		t.Error("Disconnect dropped the server from the managed set")
	}
}

func TestOperationsOnAnUnknownServer(t *testing.T) {
	m, _ := managerFixture(t)

	if err := m.Connect(context.Background(), "ghost", fastStartup()); !errors.Is(err, upstream.ErrUnknownServer) {
		t.Errorf("Connect error = %v, want ErrUnknownServer", err)
	}
	if err := m.Disconnect("ghost"); !errors.Is(err, upstream.ErrUnknownServer) {
		t.Errorf("Disconnect error = %v, want ErrUnknownServer", err)
	}
	if _, ok := m.Get("ghost"); ok {
		t.Error("Get returned a connection for a server that is not configured")
	}
}

// A list in the UI must not reshuffle between refreshes.
func TestStatusesAreOrderedByName(t *testing.T) {
	m, _ := managerFixture(t)
	m.Apply(configWith(t, map[string]string{
		"zulu": modeFull, "alpha": modeFull, "mike": modeFull, "bravo": modeFull,
	}))

	for i := 0; i < 10; i++ {
		var names []string
		for _, s := range m.Statuses() {
			names = append(names, s.Name)
		}
		if want := []string{"alpha", "bravo", "mike", "zulu"}; !slices.Equal(names, want) {
			t.Fatalf("Statuses order = %v, want %v", names, want)
		}
	}
}

func TestToolsAndResourcesAreAggregatedByServer(t *testing.T) {
	m, _ := managerFixture(t)
	m.Apply(configWith(t, map[string]string{"alpha": modeFull, "bravo": modeToolsOnly}))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m.ConnectAll(ctx, fastStartup())

	tools := m.Tools()
	if len(tools["alpha"]) == 0 || len(tools["bravo"]) == 0 {
		t.Errorf("Tools = %v, want entries for both servers", keysOf(tools))
	}

	resources := m.Resources()
	if len(resources["alpha"]) == 0 {
		t.Errorf("Resources = %v, want an entry for alpha", keysOf(resources))
	}
	// bravo declared no resources, so it should be absent rather than
	// present with an empty list.
	if _, present := resources["bravo"]; present {
		t.Errorf("Resources = %v, want bravo absent", keysOf(resources))
	}
}

func TestCloseAllDisconnectsEverything(t *testing.T) {
	m, _ := managerFixture(t)
	m.Apply(configWith(t, map[string]string{"alpha": modeFull, "bravo": modeFull}))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m.ConnectAll(ctx, fastStartup())

	m.CloseAll()

	if got := m.Connected(); len(got) != 0 {
		t.Errorf("Connected = %v after CloseAll, want none", got)
	}
	// They stay configured; only the sessions are gone.
	if want := []string{"alpha", "bravo"}; !slices.Equal(m.Names(), want) {
		t.Errorf("Names = %v, want %v", m.Names(), want)
	}
}

// The bus is how the WebSocket layer learns anything, so the manager has
// to feed it.
func TestManagerPublishesConnectionEvents(t *testing.T) {
	m, bus := managerFixture(t)
	seen, cancel := bus.Subscribe()
	defer cancel()

	m.Apply(configWith(t, map[string]string{"alpha": modeFull}))

	ctx, ctxCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer ctxCancel()
	m.ConnectAll(ctx, fastStartup())

	want := map[events.Kind]bool{
		events.ServerStatus:    false,
		events.ServerConnected: false,
		events.ToolsChanged:    false,
	}
	deadline := time.After(10 * time.Second)
	for {
		allSeen := true
		for _, got := range want {
			allSeen = allSeen && got
		}
		if allSeen {
			return
		}
		select {
		case e := <-seen:
			if _, tracked := want[e.Kind]; tracked {
				want[e.Kind] = true
			}
			if e.Server != "alpha" {
				t.Errorf("event %q names server %q, want alpha", e.Kind, e.Server)
			}
		case <-deadline:
			t.Fatalf("did not see every expected event: %v", want)
		}
	}
}

func TestManagerPublishesFailureEvents(t *testing.T) {
	m, bus := managerFixture(t)
	seen, cancel := bus.Subscribe(events.ServerFailed)
	defer cancel()

	cfg := config.Default()
	broken := serverConfig(t, modeFull)
	broken.Command = "/nonexistent/definitely-not-a-program"
	cfg.MCPServers = map[string]config.MCPServer{"broken": broken}
	m.Apply(cfg)

	m.ConnectAll(context.Background(), fastStartup())

	select {
	case e := <-seen:
		if e.Server != "broken" {
			t.Errorf("event names server %q, want broken", e.Server)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no failure event was published")
	}
}

// Starting a dozen child processes at once turns a slow machine into an
// unresponsive one.
func TestConnectAllStaggersItsAttempts(t *testing.T) {
	m, _ := managerFixture(t)
	m.Apply(configWith(t, map[string]string{
		"a": modeToolsOnly, "b": modeToolsOnly, "c": modeToolsOnly,
	}))

	startup := fastStartup()
	startup.ConnectDelay = 150 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	m.ConnectAll(ctx, startup)
	elapsed := time.Since(start)

	// Three servers at 150ms apart means the last one waits 300ms.
	if elapsed < 300*time.Millisecond {
		t.Errorf("ConnectAll finished in %v, too fast to have staggered the attempts", elapsed)
	}
	if len(m.Connected()) != 3 {
		t.Errorf("Connected = %v, want all three", m.Connected())
	}
}

func TestConnectAllStopsWhenTheContextEnds(t *testing.T) {
	m, _ := managerFixture(t)
	m.Apply(configWith(t, map[string]string{
		"a": modeToolsOnly, "b": modeToolsOnly, "c": modeToolsOnly,
	}))

	startup := fastStartup()
	startup.ConnectDelay = 5 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	m.ConnectAll(ctx, startup)

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("ConnectAll took %v, want it to abandon the stagger when the context ended", elapsed)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
