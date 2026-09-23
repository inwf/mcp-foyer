package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/guide"
	"mcp-foyer/internal/upstream"
)

// Options configures a [Gateway].
type Options struct {
	Version   string
	Upstreams Upstreams
	Configs   Configs
	Logger    *slog.Logger

	// Gateway carries the session, keep-alive and debounce settings.
	Gateway config.Gateway

	// WireDebug logs every MCP message in both directions. Verbose;
	// intended for diagnosing protocol-level problems.
	WireDebug bool

	// Usage receives what connected models do with the tools. Nil counts
	// nothing.
	Usage Usage

	// After overrides the timer used to coalesce notifications, for
	// tests that cannot wait for it.
	After func(time.Duration, func())
}

// instructions explains progressive discovery before the first call.
// Tests compare its names with the registered tools and the guide URI.
//
// Not every client shows a model these instructions, so each tool's own
// description repeats what it needs; this is the version for clients
// that do, and it says the same things in the same words.
const instructions = `This endpoint stands in front of several MCP servers. This tool list holds its four own tools plus whichever server tools the operator chose to list; most server tools are not in this list and are reached through the four.

To find a capability: search_tools(query). To see what one server offers: search_tools(server); list_servers gives every server with its state and tool count. Search covers tools whether or not they are in this list. Add includeSchema=true to get each hit's input schema in the same result, or ask get_tool_details(server, tool) for one tool's schema.

To use a tool: call_tool(server, tool, args). The arguments are checked against the tool's schema before forwarding. When server, tool and arguments are already known, call without searching first. A server tool that is in this list can also be called by its listed name.

The four own tools — list_servers, search_tools, get_tool_details, call_tool — are called directly, never through call_tool. Pass server="mcp-foyer" to search_tools or get_tool_details to inspect them.

For clients that read resources, foyer://guide is the full usage guide.`

// Gateway is the single MCP endpoint clients connect to. It exposes the
// gateway's own tools plus every tool of every connected upstream
// server, and forwards calls to whichever server a tool came from.
type Gateway struct {
	opts Options
	log  *slog.Logger

	server    *mcp.Server
	stateful  http.Handler
	stateless http.Handler

	mu sync.RWMutex
	// names maps an exposed tool name to the upstream tool it forwards
	// to. Handlers consult it at call time rather than capturing it,
	// because it is replaced whenever upstreams change.
	names NameMap
	// registered records what is currently on the server, and a
	// fingerprint of each entry, so that a resync only touches what
	// actually changed.
	registered map[string]string
	// published is the forwarded tool set as last synced, kept so that
	// the management API can report what is on offer. The SDK's server
	// does not enumerate its own tools.
	published []*mcp.Tool
	// publishedResources records the resource URIs currently on the
	// server, for the same reason.
	publishedResources map[string]string

	// systemTools is the gateway's own tools, as the server actually
	// publishes them. See [Gateway.SystemTools] for why they are read
	// back rather than kept from registration.
	systemToolsOnce sync.Once
	systemTools     []*mcp.Tool

	// internal tracks what the gateway knows about its own sessions that
	// the SDK does not report: which are the gateway talking to itself, and
	// which a client is actually using.
	internal sessionLedger

	resync *debouncer
}

// New builds a gateway. Call [Gateway.Sync] to publish the current set
// of upstream tools, and [Gateway.Watch] to keep it current.
func New(opts Options) *Gateway {
	log := opts.Logger
	if log == nil {
		log = slog.New(discardHandler{})
	}

	g := &Gateway{
		opts:               opts,
		log:                log,
		registered:         map[string]string{},
		publishedResources: map[string]string{},
	}
	if g.opts.Usage == nil {
		g.opts.Usage = noUsage{}
	}

	g.server = mcp.NewServer(
		&mcp.Implementation{
			Name:    Name,
			Version: opts.Version,
			Title:   "MCP Foyer",
		},
		&mcp.ServerOptions{
			Logger:       log,
			Instructions: instructions,

			// The SDK runs the liveness probe, which is what notices a
			// client that vanished without closing its session.
			KeepAlive:                 opts.Gateway.KeepAlive,
			KeepAliveFailureThreshold: opts.Gateway.KeepAliveFailureThreshold,

			HasTools:     true,
			HasResources: true,
		})

	RegisterSystemTools(g.server, opts.Upstreams, opts.Configs, g.SystemTools, g.opts.Usage)

	// Installed before anything can connect: a session that arrives before
	// the middleware is in place would never be marked, and so would never
	// be reported as a client.
	g.trackSessions()

	// Read the gateway's own tools back now rather than on the first call
	// that wants them. That read talks to this server over a pipe, and the
	// first caller to want it would be a request handler already running on
	// it — one round trip taken here is one that never happens from inside a
	// handler.
	g.SystemTools()

	if opts.WireDebug {
		g.installWireLogging()
	}

	g.resync = newDebouncer(opts.Gateway.NotifyDebounce, g.Sync, opts.After)

	// Two handlers over one server: the session behaviour differs, but
	// the tools and resources on offer do not.
	getServer := func(*http.Request) *mcp.Server { return g.server }
	g.stateful = mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Logger:         log,
		SessionTimeout: opts.Gateway.SessionTimeout,
	})
	g.stateless = mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Logger:    log,
		Stateless: true,
	})

	return g
}

// Server is the underlying MCP server, for callers that need to inspect
// its sessions.
func (g *Gateway) Server() *mcp.Server { return g.server }

// Handler serves the MCP endpoint, choosing a session mode per request.
func (g *Gateway) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := ResolveSessionMode(r.Header.Get(SessionModeHeader), r.UserAgent(), g.opts.Gateway)
		if mode == config.SessionModeStateless {
			g.stateless.ServeHTTP(w, r)
			return
		}
		g.stateful.ServeHTTP(w, r)
	})
}

// Sessions lists the clients currently connected.
//
// Two kinds of session are left out, both for the same reason — they are
// not somebody connected: the gateway's own sessions to itself, and
// sessions nobody got past the handshake on. See [sessionLedger].
func (g *Gateway) Sessions() []SessionInfo { return g.internal.report(g.server) }

// PublishedTools lists everything the gateway offers its clients: its own
// tools first, then the forwarded upstream ones under the names clients
// see them by.
//
// This is what the management API reports, and it has to match what an
// MCP client is actually served — the two are asserted equal by test.
func (g *Gateway) PublishedTools() []*mcp.Tool {
	g.mu.RLock()
	forwarded := slices.Clone(g.published)
	g.mu.RUnlock()

	// The gateway's own tools come first, which is the order a reader
	// wants: they are the ones that explain how to find the rest.
	return append(g.SystemTools(), forwarded...)
}

// Watch keeps the exposed tools in step with the upstream servers until
// the context ends.
func (g *Gateway) Watch(ctx context.Context, bus *events.Bus) {
	if bus == nil {
		return
	}
	updates, cancel := bus.Subscribe(
		events.ToolsChanged,
		events.ResourcesChanged,
		events.ServerConnected,
		events.ServerDisconnected,
		events.ServerFailed,
	)

	go func() {
		defer cancel()
		defer g.resync.stop()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-updates:
				if !ok {
					return
				}
				g.resync.trigger()
			}
		}
	}()
}

// Sync publishes the current set of upstream tools.
//
// Only what changed is touched. The SDK sends a list-changed
// notification for every individual tool added, so re-adding an
// unchanged tool would tell every connected client to refetch for
// nothing.
func (g *Gateway) Sync() {
	aggregate := BuildAggregate(g.collectTools())

	current := make(map[string]string, len(aggregate.Tools))
	for _, tool := range aggregate.Tools {
		current[tool.Name] = fingerprint(tool)
	}

	g.mu.Lock()
	previous := g.registered
	g.names = aggregate.Names
	g.registered = current
	g.published = aggregate.Tools
	g.mu.Unlock()

	var removed []string
	for name := range previous {
		if _, still := current[name]; !still {
			removed = append(removed, name)
		}
	}
	if len(removed) > 0 {
		slices.Sort(removed)
		// One call for all of them, so one notification.
		g.server.RemoveTools(removed...)
	}

	added := 0
	for _, tool := range aggregate.Tools {
		if was, existed := previous[tool.Name]; existed && was == current[tool.Name] {
			continue
		}
		g.server.AddTool(tool, g.forward)
		added++
	}

	if added > 0 || len(removed) > 0 {
		g.log.Info("published the upstream tools",
			"added", added, "removed", len(removed), "total", len(current))
		// The counts say how much moved; which names moved is the question
		// asked by someone whose tool is not on the list they expected.
		g.log.Debug("the published set changed",
			"withdrawn", removed, "offering", slices.Sorted(maps.Keys(current)))
	}

	g.syncResources()
}

// collectTools gathers the upstream tools each server's configuration
// allows to be exposed.
func (g *Gateway) collectTools() map[string][]*mcp.Tool {
	return ExposedByServer(g.opts.Upstreams.Tools(), g.opts.Configs.Get())
}

// forward sends a call to the upstream server the tool came from.
func (g *Gateway) forward(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := ""
	if req != nil && req.Params != nil {
		name = req.Params.Name
	}

	g.mu.RLock()
	route, ok := g.names.Route(name)
	g.mu.RUnlock()

	if !ok {
		// The tool existed when the client listed it and does not now,
		// which happens when a server disconnects mid-conversation.
		return toolError(fmt.Errorf("tool %q is no longer available; list the tools again", name)), nil
	}

	result, err := g.opts.Upstreams.CallTool(ctx, route.Server, route.Tool, req.Params.Arguments)
	if err != nil {
		// An upstream failure is something the model can read and react
		// to, so it belongs in the result rather than in a protocol
		// error it never sees.
		g.log.Debug("a forwarded call failed",
			"tool", name, "server", route.Server, "upstreamTool", route.Tool, "error", err)
		g.opts.Usage.Called(route.Server, route.Tool, true)
		return toolError(err), nil
	}
	g.log.Debug("forwarded a call",
		"tool", name, "server", route.Server, "upstreamTool", route.Tool)
	g.opts.Usage.Called(route.Server, route.Tool, result != nil && result.IsError)
	return result, nil
}

// syncResources publishes one resource per server plus one standing in
// for each upstream resource, touching only what changed.
func (g *Gateway) syncResources() {
	resources := BuildResources(g.opts.Upstreams.Statuses(), g.opts.Upstreams.Resources())

	current := make(map[string]string, len(resources))
	for _, resource := range resources {
		current[resource.URI] = resource.Name + "\x00" + resource.Description
	}

	g.mu.Lock()
	previous := g.publishedResources
	g.publishedResources = current
	g.mu.Unlock()

	var removed []string
	for uri := range previous {
		if _, still := current[uri]; !still {
			removed = append(removed, uri)
		}
	}
	if len(removed) > 0 {
		slices.Sort(removed)
		g.server.RemoveResources(removed...)
	}

	for _, resource := range resources {
		if was, existed := previous[resource.URI]; existed && was == current[resource.URI] {
			continue
		}
		g.server.AddResource(resource, g.readResource)
	}
}

func (g *Gateway) readResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := ""
	if req != nil && req.Params != nil {
		uri = req.Params.URI
	}

	// The guide is the gateway's own document rather than anything to do
	// with a server, so it is answered before the URI is taken apart.
	if uri == GuideResourceURI {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: uri, MIMEType: guide.MIMEType, Text: guide.Text()},
			},
		}, nil
	}

	server, upstreamURI, ok := ParseResourceURI(uri)
	if !ok {
		return nil, fmt.Errorf("resource %q does not belong to this endpoint", uri)
	}

	// No upstream URI means the caller asked about the server itself.
	if upstreamURI == "" {
		return g.describeServer(server, uri)
	}
	return g.opts.Upstreams.ReadResource(ctx, server, upstreamURI)
}

// serverDescription is what a client reads from foyer://servers/{name}.
// Descriptions are the operator's text; missing descriptions add no prose.
type serverDescription struct {
	upstream.Status
	Description string            `json:"description,omitempty"`
	Tools       map[string]string `json:"tools,omitempty"`
}

func (g *Gateway) describeServer(server, uri string) (*mcp.ReadResourceResult, error) {
	for _, status := range g.opts.Upstreams.Statuses() {
		if status.Name != server {
			continue
		}

		described := serverDescription{Status: status}
		if entry, ok := g.opts.Configs.Get().MCPServers[server]; ok {
			if entry.Description != "" {
				described.Description = entry.Description
			}
		}
		if tools := g.opts.Upstreams.Tools()[server]; len(tools) > 0 {
			described.Tools = make(map[string]string, len(tools))
			for _, tool := range tools {
				if tool == nil || tool.Name == "" {
					continue
				}
				described.Tools[tool.Name] = tool.Description
			}
		}

		encoded, err := json.MarshalIndent(described, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("describe server %s: %w", server, err)
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: uri, MIMEType: "application/json", Text: string(encoded)},
			},
		}, nil
	}
	return nil, fmt.Errorf("no server named %q", server)
}

// The two methods a client can open a session with. Neither says the
// client intends to use the session it just created: the SDK client tries
// server/discover, and starts over on a new session when the versions do
// not overlap.
//
// Spelled out here because the SDK keeps its method names private. If a
// third handshake method ever appears, a session opened with it counts as a
// client one message earlier than it should — which is the harmless
// direction to be wrong in.
const (
	methodInitialize = "initialize"
	methodDiscover   = "server/discover"
)

// trackSessions decides which sessions are clients.
//
// A session becomes a client when it sends something that is not a
// handshake. Nothing else available distinguishes a client from the debris
// of one: an abandoned session is not closed, announces the same client
// name as the real one, and stays until the session timeout.
//
// The mark is made before the message is handled rather than after, so that
// the initialize request cannot mark its own session — it is the message
// whose session might still turn out to be abandoned. What marks the
// session is the next thing to arrive, which for a conforming client is
// notifications/initialized and for any other is the first real request.
func (g *Gateway) trackSessions() {
	g.server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != methodInitialize && method != methodDiscover {
				if req != nil {
					if session, ok := req.GetSession().(*mcp.ServerSession); ok {
						g.internal.engage(session)
					}
				}
			}
			return next(ctx, method, req)
		}
	})
}

// installWireLogging records every message in both directions.
func (g *Gateway) installWireLogging() {
	log := g.log.With("wire", true)

	g.server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			started := time.Now()
			result, err := next(ctx, method, req)
			log.Debug("received", "method", method,
				"session", sessionID(req), "took", time.Since(started), "error", err)
			return result, err
		}
	})

	g.server.AddSendingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			log.Debug("sent", "method", method, "session", sessionID(req), "error", err)
			return result, err
		}
	})
}

func sessionID(req mcp.Request) string {
	if req == nil {
		return ""
	}
	if session := req.GetSession(); session != nil {
		return session.ID()
	}
	return ""
}

// fingerprint changes whenever anything a client would notice about a
// tool changes, and not otherwise.
func fingerprint(tool *mcp.Tool) string {
	schema, err := json.Marshal(tool.InputSchema)
	if err != nil {
		schema = []byte("?")
	}
	return tool.Description + "\x00" + string(schema)
}

// discardHandler drops every record, standing in for a nil logger.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
