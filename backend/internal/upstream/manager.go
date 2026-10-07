package upstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/logging"
)

// ErrUnknownServer is returned for a name that is not configured.
var ErrUnknownServer = errors.New("no such server")

// Manager owns the connection to every configured upstream server and
// keeps that set in step with the configuration.
//
// It is safe for concurrent use.
type Manager struct {
	version string
	log     *logging.Logger
	bus     *events.Bus

	// own receives the manager's own records, as opposed to those about
	// a particular server. Never nil, so callers need no guard.
	own *slog.Logger

	mu    sync.RWMutex
	conns map[string]*Conn
	cfgs  map[string]config.MCPServer

	// desired marks the servers the manager means to keep connected:
	// connecting, at startup or on demand, sets it; Disconnect, removal
	// and shutdown clear it. A failure of a desired server is what the
	// supervisor retries.
	desired map[string]bool

	// busy marks a server a connect run is already working on: a
	// deliberate Connect, or a supervisor loop. One run per server at a
	// time, and a failure that arrives mid-run does not start a second.
	busy map[string]bool

	// Set once by KeepConnected, before any loop can read them.
	supervising bool
	superCtx    context.Context
	superPolicy RetryPolicy
}

// NewManager returns an empty manager. Call [Manager.Apply] to populate
// it from a configuration.
func NewManager(version string, log *logging.Logger, bus *events.Bus) *Manager {
	own := slog.New(discardHandler{})
	if log != nil {
		own = log.For(logging.ModuleUpstream)
	}
	return &Manager{
		version: version,
		log:     log,
		bus:     bus,
		own:     own,
		conns:   map[string]*Conn{},
		cfgs:    map[string]config.MCPServer{},
		desired: map[string]bool{},
		busy:    map[string]bool{},
	}
}

// Apply reconciles the managed set with cfg: servers that appeared are
// added, servers that vanished are closed and dropped, and servers whose
// settings changed are replaced.
//
// It returns the names that were added, removed and changed, which the
// caller uses to decide what to connect.
func (m *Manager) Apply(cfg config.Config) (added, removed, changed []string) {
	m.mu.Lock()

	for name, want := range cfg.MCPServers {
		have, exists := m.cfgs[name]
		switch {
		case !exists:
			m.conns[name] = m.newConn(name, want)
			m.cfgs[name] = want
			added = append(added, name)
		case !sameServer(have, want):
			// Settings that matter to a live session cannot be applied
			// in place, so the connection is rebuilt.
			changed = append(changed, name)
			m.cfgs[name] = want
		}
	}

	for name := range m.cfgs {
		if _, stillWanted := cfg.MCPServers[name]; !stillWanted {
			removed = append(removed, name)
		}
	}

	stale := make([]*Conn, 0, len(removed)+len(changed))
	for _, name := range removed {
		stale = append(stale, m.conns[name])
		delete(m.conns, name)
		delete(m.cfgs, name)
		delete(m.desired, name)
	}
	for _, name := range changed {
		stale = append(stale, m.conns[name])
		m.conns[name] = m.newConn(name, m.cfgs[name])
	}
	m.mu.Unlock()

	// Closing waits on child processes, so it happens outside the lock.
	for _, conn := range stale {
		if conn != nil {
			_ = conn.Close()
		}
	}

	slices.Sort(added)
	slices.Sort(removed)
	slices.Sort(changed)
	return added, removed, changed
}

// ConnectAll connects every enabled server, staggering the attempts.
//
// It blocks until every attempt has finished. Callers that want to serve
// traffic while servers come up should run it in a goroutine.
//
// One server failing does not affect the others: a broken entry in the
// configuration must not stop the rest of the gateway from working.
func (m *Manager) ConnectAll(ctx context.Context, startup config.Startup) {
	m.mu.RLock()
	names := make([]string, 0, len(m.conns))
	for name, cfg := range m.cfgs {
		if cfg.Enabled {
			names = append(names, name)
		}
	}
	m.mu.RUnlock()
	slices.Sort(names)

	policy := RetryPolicyFrom(startup)

	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		// Starting a dozen child processes at once turns a slow machine
		// into an unresponsive one, so attempts are spread out.
		stagger := time.Duration(i) * startup.ConnectDelay

		go func(name string, stagger time.Duration) {
			defer wg.Done()

			if stagger > 0 {
				timer := time.NewTimer(stagger)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return
				}
			}
			if err := m.connect(ctx, name, policy); err != nil {
				m.own.Warn("could not connect a server", "server", name, "error", err)
			}
		}(name, stagger)
	}
	wg.Wait()
}

// Connect brings up one server by name, retrying per the startup
// configuration.
func (m *Manager) Connect(ctx context.Context, name string, startup config.Startup) error {
	return m.connect(ctx, name, RetryPolicyFrom(startup))
}

func (m *Manager) connect(ctx context.Context, name string, policy RetryPolicy) error {
	m.mu.Lock()
	conn, ok := m.conns[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%s: %w", name, ErrUnknownServer)
	}
	m.desired[name] = true
	m.busy[name] = true
	m.mu.Unlock()

	defer m.releaseBusy(name)
	return conn.ConnectWithRetry(ctx, policy)
}

// Disconnect closes one server's session, leaving it configured.
func (m *Manager) Disconnect(name string) error {
	conn, ok := m.Get(name)
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrUnknownServer)
	}
	// Wanted first, so that a retry loop already waiting does not bring
	// the server back up after this call.
	m.mu.Lock()
	delete(m.desired, name)
	m.mu.Unlock()
	return conn.Close()
}

// CloseAll disconnects every server. Used at shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	conns := slices.Collect(maps.Values(m.conns))
	m.desired = map[string]bool{}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, conn := range conns {
		wg.Add(1)
		go func(c *Conn) {
			defer wg.Done()
			_ = c.Close()
		}(conn)
	}
	wg.Wait()
}

// KeepConnected retries servers the manager means to keep connected
// after they fail, until the context ends or they are disconnected.
//
// maxRetries and retryBackoff bound one connect run, and a dropped
// session is not a run at all. Without this, a server that was down at
// startup or died mid-session stays failed for the life of the process,
// which for a machine-local endpoint is the ordinary case of a child
// process that crashed. Attempts follow the same backoff, capped, and
// they stop the moment the server is disconnected: a server switched
// off on purpose stays off.
func (m *Manager) KeepConnected(ctx context.Context, policy RetryPolicy) {
	m.mu.Lock()
	m.superCtx, m.superPolicy = ctx, policy
	m.supervising = true
	stale := make([]string, 0, len(m.desired))
	for name := range m.desired {
		if conn, ok := m.conns[name]; ok && !conn.Status().Connected() {
			stale = append(stale, name)
		}
	}
	m.mu.Unlock()

	for _, name := range stale {
		m.noteFailure(name)
	}
}

// noteFailure starts a retry loop for a server that failed while the
// manager meant to keep it. It runs on the path a connection uses to
// announce every state change, so it stays cheap and never blocks, and
// the busy mark is what keeps repeated failures from stacking up
// repeated loops.
func (m *Manager) noteFailure(name string) {
	m.mu.Lock()
	// A finished context is shutdown, not a failure to retry: a loop
	// spawned now would wake, see the context, and hand the server
	// straight back here.
	if !m.supervising || m.busy[name] || !m.desired[name] || m.superCtx.Err() != nil {
		m.mu.Unlock()
		return
	}
	if _, ok := m.conns[name]; !ok {
		m.mu.Unlock()
		return
	}
	m.busy[name] = true
	ctx, policy := m.superCtx, m.superPolicy
	m.mu.Unlock()

	go m.reconnect(ctx, name, policy)
}

// reconnect keeps one server coming back until it is connected, the
// context ends, or someone disconnects it.
func (m *Manager) reconnect(ctx context.Context, name string, policy RetryPolicy) {
	defer m.releaseBusy(name)

	for attempt := 0; ; attempt++ {
		if err := policy.sleep(ctx, policy.delay(attempt)); err != nil {
			return
		}
		if !m.wantsConnected(name) {
			return
		}
		conn, ok := m.Get(name)
		if !ok {
			return
		}
		if err := conn.Connect(ctx); err == nil && conn.Status().Connected() {
			return
		}
	}
}

// releaseBusy gives up a server's connect slot, then looks again: a
// failure that arrived while the slot was held asked for a retry and was
// turned away, and this is the only place that can let it back in.
func (m *Manager) releaseBusy(name string) {
	m.mu.Lock()
	delete(m.busy, name)
	m.mu.Unlock()

	if m.failedAndWanted(name) {
		m.noteFailure(name)
	}
}

func (m *Manager) wantsConnected(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.supervising && m.desired[name]
}

func (m *Manager) failedAndWanted(name string) bool {
	m.mu.RLock()
	wanted := m.supervising && m.desired[name] && m.superCtx.Err() == nil
	m.mu.RUnlock()
	if !wanted {
		return false
	}
	conn, ok := m.Get(name)
	return ok && !conn.Status().Connected()
}

// Get returns the connection for a name.
func (m *Manager) Get(name string) (*Conn, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conn, ok := m.conns[name]
	return conn, ok
}

// Names lists the managed servers, sorted.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Sorted(maps.Keys(m.conns))
}

// Statuses reports every managed server, ordered by name so that a list
// in the UI does not reshuffle between refreshes.
func (m *Manager) Statuses() []Status {
	m.mu.RLock()
	conns := make([]*Conn, 0, len(m.conns))
	for _, name := range slices.Sorted(maps.Keys(m.conns)) {
		conns = append(conns, m.conns[name])
	}
	m.mu.RUnlock()

	out := make([]Status, 0, len(conns))
	for _, conn := range conns {
		out = append(out, conn.Status())
	}
	return out
}

// Connected lists the servers that are usable right now.
func (m *Manager) Connected() []string {
	var names []string
	for _, status := range m.Statuses() {
		if status.Connected() {
			names = append(names, status.Name)
		}
	}
	return names
}

// Tools returns the cached tools of every connected server, keyed by
// server name.
func (m *Manager) Tools() map[string][]*mcp.Tool {
	m.mu.RLock()
	conns := slices.Collect(maps.Values(m.conns))
	m.mu.RUnlock()

	out := map[string][]*mcp.Tool{}
	for _, conn := range conns {
		if tools := conn.Tools(); len(tools) > 0 {
			out[conn.Name()] = tools
		}
	}
	return out
}

// Resources returns the cached resources of every connected server,
// keyed by server name.
func (m *Manager) Resources() map[string][]*mcp.Resource {
	m.mu.RLock()
	conns := slices.Collect(maps.Values(m.conns))
	m.mu.RUnlock()

	out := map[string][]*mcp.Resource{}
	for _, conn := range conns {
		if resources := conn.Resources(); len(resources) > 0 {
			out[conn.Name()] = resources
		}
	}
	return out
}

func (m *Manager) newConn(name string, cfg config.MCPServer) *Conn {
	deps := Deps{OnChange: m.publish}
	if m.log != nil {
		deps.Logger = m.log.ForServer(logging.ModuleUpstream, name)
	}
	return New(name, cfg, m.version, deps)
}

// publish translates a connection's change notification into a bus
// event. Keeping the translation here is what lets the upstream package
// stay unaware of the bus.
func (m *Manager) publish(server string, kind ChangeKind) {
	// Supervision is independent of the bus: a manager without one still
	// brings its servers back.
	if kind == ChangeStatus {
		if conn, ok := m.Get(server); ok && conn.Status().State == StateFailed {
			m.noteFailure(server)
		}
	}

	if m.bus == nil {
		return
	}

	switch kind {
	case ChangeTools:
		m.bus.PublishServer(events.ToolsChanged, server, nil)
	case ChangeResources:
		m.bus.PublishServer(events.ResourcesChanged, server, nil)
	case ChangeStatus:
		conn, ok := m.Get(server)
		if !ok {
			return
		}
		status := conn.Status()
		m.bus.PublishServer(events.ServerStatus, server, status)

		// The transitions are published separately because they are what
		// a client actually reacts to, and are cheaper to filter on than
		// inspecting every status update.
		switch status.State {
		case StateConnected:
			m.bus.PublishServer(events.ServerConnected, server, status)
		case StateDisconnected:
			m.bus.PublishServer(events.ServerDisconnected, server, status)
		case StateFailed:
			m.bus.PublishServer(events.ServerFailed, server, status)
		}
	}
}

// sameServer reports whether two configurations describe the same live
// connection.
//
// The fields cleared here are the ones a connection never sees, so a
// change to them must not cost a reconnect. Description is metadata.
// Exposure and disabling are decided entirely on the gateway side —
// this package does not read either list — and rebuilding the
// connection for them would kill and respawn a child process every time
// someone flipped one tool on or off.
func sameServer(a, b config.MCPServer) bool {
	a.Description, b.Description = "", ""
	a.ExposedTools, b.ExposedTools = nil, nil
	a.DisabledTools, b.DisabledTools = nil, nil
	return reflect.DeepEqual(a, b)
}

// CallTool invokes a tool on one server by name.
func (m *Manager) CallTool(ctx context.Context, server, tool string, args any) (*mcp.CallToolResult, error) {
	conn, ok := m.Get(server)
	if !ok {
		return nil, fmt.Errorf("%s: %w", server, ErrUnknownServer)
	}
	return conn.CallTool(ctx, tool, args)
}

// ReadResource fetches a resource from one server by name.
func (m *Manager) ReadResource(ctx context.Context, server, uri string) (*mcp.ReadResourceResult, error) {
	conn, ok := m.Get(server)
	if !ok {
		return nil, fmt.Errorf("%s: %w", server, ErrUnknownServer)
	}
	return conn.ReadResource(ctx, uri)
}
