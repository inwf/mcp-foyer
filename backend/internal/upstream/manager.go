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
	conn, ok := m.Get(name)
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrUnknownServer)
	}
	return conn.ConnectWithRetry(ctx, policy)
}

// Disconnect closes one server's session, leaving it configured.
func (m *Manager) Disconnect(name string) error {
	conn, ok := m.Get(name)
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrUnknownServer)
	}
	return conn.Close()
}

// CloseAll disconnects every server. Used at shutdown.
func (m *Manager) CloseAll() {
	m.mu.RLock()
	conns := slices.Collect(maps.Values(m.conns))
	m.mu.RUnlock()

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
// connection. Fields that only affect presentation are excluded, so
// renaming a description does not tear down a working session.
// sameServer reports whether two configurations describe the same live
// connection.
//
// The fields cleared here are the ones a connection never sees, so a
// change to them must not cost a reconnect. Description is
// metadata. Exposure is decided entirely on the gateway side — this
// package does not read the allow list at all — and rebuilding the
// connection for it would kill and respawn a child process every time
// someone flipped one tool on.
func sameServer(a, b config.MCPServer) bool {
	a.Description, b.Description = "", ""
	a.ExposedTools, b.ExposedTools = nil, nil
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
