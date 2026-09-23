package gateway

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The gateway talks to itself over a pipe for the two things it cannot do
// from the outside: reading back what its own tools look like, and
// calling one of them on behalf of the management API.
//
// Both go through a real MCP session rather than reaching for the Go
// functions behind the tools. That is the point: the SDK does the same
// schema generation, argument decoding, validation and result encoding it
// does for any other client, so a tool called from the web interface
// behaves exactly as it does when a model calls it. Every shortcut past
// the SDK would be a second implementation of one of those steps, and a
// second implementation is a thing that can disagree with the first.

// systemToolReadTimeout bounds the one round trip that reads the
// gateway's own tools back off its server. It talks to itself over a
// pipe, so this only guards against a deadlock.
const systemToolReadTimeout = 10 * time.Second

// internalClientName identifies the gateway to itself in the initialize
// handshake. It never reaches a real peer.
const internalClientName = "mcp-foyer-internal"

// sessionLedger records the two things the gateway knows about its own
// sessions that the SDK does not report, both of which decide whether a
// session is a connected client.
//
// An operator reading that list is asking who is connected. Two kinds of
// session are not an answer to that question:
//
// The gateway talking to itself. It opens in-process sessions to read its
// own tools back and to run a tool for the management API.
//
// A session nobody got past the handshake on. One connecting client can
// create two: the SDK client tries the modern server/discover handshake
// first and, when there is no version overlap, abandons that session and
// starts again with the legacy initialize on a fresh one. The abandoned
// session is never closed — it waits for the session timeout, half an hour
// by default — so during that time one client is reported as two. That was
// the bug: a list of who is connected that doubles every entry is not
// describing the installation.
//
// Sessions are tracked by identity rather than by the client name they
// announce, because a name is something a remote client chooses: filtering
// on one would let any client hide itself from the list by claiming to be
// this one.
type sessionLedger struct {
	mu sync.Mutex

	// open are the sessions the gateway opened to itself.
	open map[*mcp.ServerSession]struct{}

	// engaged are the sessions that have sent something after their
	// handshake. See [Gateway.trackSessions] for why that is the test.
	engaged map[*mcp.ServerSession]struct{}
}

// engage marks a session as one a client is actually using.
func (s *sessionLedger) engage(session *mcp.ServerSession) {
	if session == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.engaged == nil {
		s.engaged = map[*mcp.ServerSession]struct{}{}
	}
	s.engaged[session] = struct{}{}
}

// add registers a session, running connect under the lock.
//
// The lock spans the connection because the SDK registers a session
// before Connect returns: taking it afterwards would leave a window in
// which the session is on the server and not yet known to be internal,
// and a concurrent read of the session list would report it.
func (s *sessionLedger) add(connect func() (*mcp.ServerSession, error)) (*mcp.ServerSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := connect()
	if err != nil {
		return nil, err
	}
	if s.open == nil {
		s.open = map[*mcp.ServerSession]struct{}{}
	}
	s.open[session] = struct{}{}
	return session, nil
}

// report describes the clients the server has, and forgets the marks
// belonging to sessions it no longer has.
//
// The marks are dropped here rather than when an internal session closes,
// because closing does not remove it: the SDK deregisters a session from
// the goroutine that was reading it, after Wait has already returned.
// Dropping the mark at that point leaves the session listed and no longer
// known to be internal — the one state that must never be reachable,
// since it is a lie about who is connected. Walking the live set is the
// only moment at which "gone" can be established, and it is established
// here because this is the code that walks it.
//
// The walk happens under the lock. [mcp.Server.Sessions] snapshots the
// list when it is called rather than when it is ranged, so a snapshot
// taken outside would be a view of the sessions at one moment paired with
// a view of the marks at another — and a session marked between the two
// would be reported as a client.
func (s *sessionLedger) report(server *mcp.Server) []SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := []SessionInfo{}
	open := make(map[*mcp.ServerSession]struct{}, len(s.open))
	engaged := make(map[*mcp.ServerSession]struct{}, len(s.engaged))

	for session := range server.Sessions() {
		if _, internal := s.open[session]; internal {
			open[session] = struct{}{}
			continue
		}
		if _, using := s.engaged[session]; !using {
			// Either still mid-handshake, which lasts a millisecond, or
			// abandoned, which lasts until the session timeout. Neither is
			// a client to report.
			continue
		}
		engaged[session] = struct{}{}
		out = append(out, describe(session))
	}

	s.open = open
	s.engaged = engaged
	sortSessions(out)
	return out
}

// forget drops the marks for sessions the server no longer has, without
// building a report. It runs after an internal call so that the marks do
// not accumulate on an installation whose session list is never read.
func (s *sessionLedger) forget(server *mcp.Server) {
	s.mu.Lock()
	defer s.mu.Unlock()

	open := make(map[*mcp.ServerSession]struct{}, len(s.open))
	engaged := make(map[*mcp.ServerSession]struct{}, len(s.engaged))
	for session := range server.Sessions() {
		if _, internal := s.open[session]; internal {
			open[session] = struct{}{}
		}
		if _, using := s.engaged[session]; using {
			engaged[session] = struct{}{}
		}
	}
	s.open = open
	s.engaged = engaged
}

// withInternalSession runs fn against a session connected to the
// gateway's own server, and tears it down afterwards.
func (g *Gateway) withInternalSession(ctx context.Context, fn func(*mcp.ClientSession) error) error {
	clientSide, serverSide := mcp.NewInMemoryTransports()

	serverSession, err := g.internal.add(func() (*mcp.ServerSession, error) {
		return g.server.Connect(ctx, serverSide, nil)
	})
	if err != nil {
		return fmt.Errorf("connect the server side: %w", err)
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    internalClientName,
		Version: g.opts.Version,
	}, nil)

	session, err := client.Connect(ctx, clientSide, nil)
	if err != nil {
		serverSession.Close()
		serverSession.Wait()
		g.internal.forget(g.server)
		return fmt.Errorf("connect the client side: %w", err)
	}

	defer func() {
		session.Close()
		serverSession.Close()
		serverSession.Wait()
		g.internal.forget(g.server)
	}()

	return fn(session)
}

// SystemTools returns the gateway's own tools as the server actually
// publishes them, schemas included.
//
// They are read back from the server rather than kept from registration.
// [mcp.AddTool] infers each schema from the Go handler's argument type and
// does not write the result back into the tool it was given, and the SDK
// offers no way to enumerate a server's tools — so the alternatives were
// to hand-write the schemas (which would drift from the Go types they
// describe) or to re-run the SDK's own inference (which would be a copy of
// an internal code path). Asking the server is the only answer that cannot
// be wrong.
//
// One round trip is enough for the lifetime of the gateway: the system
// tools are registered once in [New] and never change afterwards, unlike
// the forwarded tools that [Gateway.Sync] maintains.
func (g *Gateway) SystemTools() []*mcp.Tool {
	g.systemToolsOnce.Do(func() {
		tools, err := g.readBackSystemTools()
		if err != nil {
			// A gateway that cannot describe its own tools still forwards
			// everything else, so this is degraded rather than fatal.
			g.log.Error("could not read back mcp-foyer's own tools", "error", err)
			return
		}
		g.systemTools = tools
	})
	return slices.Clone(g.systemTools)
}

// readBackSystemTools asks the server what it offers, and keeps the
// entries that are the gateway's own.
func (g *Gateway) readBackSystemTools() ([]*mcp.Tool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemToolReadTimeout)
	defer cancel()

	var tools []*mcp.Tool
	err := g.withInternalSession(ctx, func(session *mcp.ClientSession) error {
		for tool, err := range session.Tools(ctx, nil) {
			if err != nil {
				return fmt.Errorf("list tools: %w", err)
			}
			if IsSystemTool(tool.Name) {
				tools = append(tools, tool)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(tools) != len(SystemToolNames) {
		return nil, fmt.Errorf("the server published %d of its %d own tools",
			len(tools), len(SystemToolNames))
	}
	return tools, nil
}

// CallSystemTool calls one of the gateway's own tools and returns what it
// answered.
//
// This exists because the gateway's tools belong to no upstream server,
// so the management API cannot reach them the way it reaches a forwarded
// tool. Routing the call through the gateway's own MCP server means the
// arguments are validated against the same schema a model sees, and the
// answer is the same one a model would get.
func (g *Gateway) CallSystemTool(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	if !IsSystemTool(name) {
		return nil, fmt.Errorf("%q is not one of mcp-foyer's own tools; they are %s",
			name, joinNames(SystemToolNames))
	}

	var result *mcp.CallToolResult
	err := g.withInternalSession(ctx, func(session *mcp.ClientSession) error {
		answer, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      name,
			Arguments: arguments,
		})
		if err != nil {
			return err
		}
		result = answer
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
