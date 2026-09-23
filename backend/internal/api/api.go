// Package api serves the management HTTP API, the aggregated MCP
// endpoint and, in a later step, the web UI — all on one listener.
//
// Every failure leaves through the same envelope (see errors.go), so a
// client has one shape to handle rather than one per endpoint.
package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/logging"
	"mcp-foyer/internal/upstream"
	"mcp-foyer/internal/usage"
)

// Route prefixes. These are constants because the middleware that
// applies to each differs, and because the frontend's API client and the
// static-file fallback both have to agree with them.
const (
	// APIPrefix covers the management API.
	APIPrefix = "/api"

	// MCPPath is the aggregated MCP endpoint that clients connect to.
	MCPPath = "/mcp"
)

// Options configures an [API].
type Options struct {
	// Version identifies this build to clients.
	Version string

	// Logger receives access log lines and rejections.
	Logger *slog.Logger

	// Security carries the allowlist and the concurrency limits.
	Security config.Security

	// MCP serves the aggregated MCP endpoint. It is supplied as a plain
	// handler so that this package does not depend on the gateway for
	// that route.
	MCP http.Handler

	// Configs is the configuration in force. It is required: every
	// management endpoint reads it.
	Configs *config.Manager

	// Upstreams is the connection manager. A nil manager leaves the
	// endpoints that need one reporting that it is not running, which is
	// what a test of the routing layer alone wants.
	Upstreams *upstream.Manager

	// Gateway is the aggregated MCP server, for reporting its sessions
	// and the tools it publishes.
	Gateway *gateway.Gateway

	// Logs is the in-memory log view the log endpoints query.
	Logs *logging.Store

	// Usage holds the tool usage counts. A nil counter leaves the usage
	// endpoints reporting that nothing is counted.
	Usage *usage.Counter

	// Bus carries the events the WebSocket stream forwards to browsers.
	// A nil bus leaves the endpoint serving clients that receive
	// nothing, which is what a test of the routing layer alone wants.
	Bus *events.Bus

	// WebUI holds the built frontend. A nil file system, or one with no
	// entry document, leaves paths outside the API reporting that no web
	// interface is available — which is what a backend-only build and a
	// development tree with an unbuilt frontend both are.
	WebUI fs.FS

	// Now overrides the clock, for tests that assert on uptime.
	Now func() time.Time
}

// API is the HTTP surface of mcp-foyer.
type API struct {
	opts    Options
	log     *slog.Logger
	engine  *gin.Engine
	started time.Time

	// connections is set when a connection limit is in force, so that the
	// health endpoint can report the count.
	connections *limitedListener

	// hub fans bus events out to the connected browsers.
	hub *hub

	// web serves the frontend when one is embedded.
	web *staticFiles
}

// New builds the API. It fails if the security configuration cannot be
// compiled, which is checked here rather than per request so that a bad
// allowlist is a startup error instead of a puzzling rejection later.
func New(opts Options) (*API, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(discardHandler{})
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	list, err := newAllowlist(opts.Security.AllowedNetworks)
	if err != nil {
		return nil, fmt.Errorf("compile the allowed networks: %w", err)
	}

	if opts.Configs == nil {
		return nil, errors.New("the api needs a configuration manager")
	}

	a := &API{opts: opts, log: opts.Logger, started: opts.Now()}
	a.hub = newHub(opts.Logger.With(logging.AttrModule, logging.ModuleWS))
	a.hub.watch(opts.Bus)
	if web, present := newStaticFiles(opts.WebUI); present {
		a.web = web
	}
	a.engine = a.buildEngine(list)
	return a, nil
}

// Handler serves every route.
func (a *API) Handler() http.Handler { return a.engine }

// Close disconnects every event-stream client and stops watching the
// bus. The HTTP server is shut down separately by whoever owns it.
func (a *API) Close() {
	a.hub.close()
}

// WatchingClients is how many browsers are receiving events.
func (a *API) WatchingClients() int { return a.hub.count() }

// Listen wraps a listener with the configured connection limit.
//
// This is separate from [API.Handler] because a connection limit cannot
// be enforced by a handler: one connection carries many requests under
// keep-alive, and a hijacked connection leaves the HTTP server's
// bookkeeping while still holding a socket.
func (a *API) Listen(inner net.Listener) net.Listener {
	limited := newLimitedListener(inner, a.opts.Security.MaxConnections, a.log)
	if counted, ok := limited.(*limitedListener); ok {
		a.connections = counted
	}
	return limited
}

// buildEngine assembles the router.
func (a *API) buildEngine(list *allowlist) *gin.Engine {
	// gin.New rather than gin.Default: the stock logger writes its own
	// format to stdout and the stock recovery writes a bare 500, neither
	// of which matches this package's log or its error envelope.
	engine := gin.New()

	// Client addresses must come from the connection, never from a
	// forwarding header. gin trusts every proxy by default, which would
	// let any caller present "X-Forwarded-For: 127.0.0.1" and appear to
	// be loopback — in the access log, and to anything else that reads
	// the client address.
	_ = engine.SetTrustedProxies(nil)

	engine.HandleMethodNotAllowed = true
	engine.NoRoute(a.handleNoRoute)
	engine.NoMethod(func(c *gin.Context) {
		fail(c, &Error{
			Code:    CodeNotFound,
			Message: fmt.Sprintf("%s is not allowed on %s", c.Request.Method, c.Request.URL.Path),
		})
	})

	// Order matters. The request id comes first so that everything after
	// it, including a rejection, can be traced. The access log wraps the
	// rest so that it sees the final status. Recovery sits inside the
	// access log so that a panic is still reported as a request.
	engine.Use(
		requestIDMiddleware(),
		accessLogMiddleware(a.log),
		recoveryMiddleware(a.log),
		allowlistMiddleware(list, a.log),
	)

	a.mountMCP(engine)

	// The event stream is mounted alongside the MCP endpoint rather than
	// under the API prefix, for the same reason: it is a long-lived
	// connection and must not consume a concurrent-request slot.
	engine.GET(WSPath, a.handleWS)

	// The concurrency limit applies to the management API only; see
	// concurrencyMiddleware for why streams are excluded.
	api := engine.Group(APIPrefix)
	api.Use(concurrencyMiddleware(a.opts.Security.MaxConcurrentRequests, a.log))
	a.registerRoutes(api)

	return engine
}

// mountMCP attaches the aggregated MCP endpoint.
//
// Every method reaches the same handler: the transport uses POST for
// requests, GET to open the notification stream and DELETE to end a
// session, and which of those are allowed depends on the session mode
// the handler resolves per request.
func (a *API) mountMCP(engine *gin.Engine) {
	if a.opts.MCP == nil {
		return
	}
	engine.Any(MCPPath, gin.WrapH(a.opts.MCP))
}

// registerRoutes adds the management API.
func (a *API) registerRoutes(api gin.IRoutes) {
	api.GET("/health", a.handleHealth)

	// The configuration as a whole.
	api.GET("/config", a.handleGetConfig)
	api.PUT("/config", a.handlePutConfig)
	api.POST("/config/validate", a.handleValidateConfig)
	api.GET("/config/export", a.handleExportConfig)

	// Servers, and what each one offers.
	api.GET("/servers", a.handleListServers)
	api.POST("/servers", a.handleCreateServer)
	api.POST("/servers/import", a.handleImportServers)
	api.GET("/servers/:name", a.handleGetServer)
	api.PUT("/servers/:name", a.handleUpdateServer)
	api.DELETE("/servers/:name", a.handleDeleteServer)
	api.POST("/servers/:name/connect", a.handleConnectServer)
	api.POST("/servers/:name/disconnect", a.handleDisconnectServer)
	api.GET("/servers/:name/tools", a.handleServerTools)
	api.POST("/servers/:name/tools/:tool/call", a.handleCallServerTool)
	api.GET("/servers/:name/resources", a.handleServerResources)
	api.GET("/servers/:name/resource", a.handleReadServerResource)

	// Across every server.
	api.GET("/tools", a.handleAggregatedTools)
	api.GET("/resources", a.handleAggregatedResources)

	// What the gateway itself is doing.
	api.GET("/gateway/status", a.handleGatewayStatus)
	api.GET("/gateway/sessions", a.handleGatewaySessions)
	api.GET("/gateway/tools", a.handleGatewayTools)
	api.POST("/gateway/tools/:tool/call", a.handleCallGatewayTool)
	api.GET("/gateway/usage", a.handleGatewayUsage)
	api.DELETE("/gateway/usage", a.handleResetGatewayUsage)

	// The log view.
	api.GET("/logs", a.handleQueryLogs)
	api.DELETE("/logs", a.handleClearLogs)
}

// handleNoRoute reports an unmatched path, or hands it to the web UI.
//
// An API path always reports JSON: a client parsing HTML where it
// expected an error envelope gets a confusing decode failure instead of
// the status that actually happened. Everything else belongs to the
// single-page app, which owns its own routing.
func (a *API) handleNoRoute(c *gin.Context) {
	isAPI := c.Request.URL.Path == APIPrefix ||
		strings.HasPrefix(c.Request.URL.Path, APIPrefix+"/")

	if !isAPI && a.web != nil && c.Request.Method == http.MethodGet {
		a.web.serve(c)
		return
	}

	fail(c, &Error{
		Code:    CodeNotFound,
		Message: fmt.Sprintf("no route for %s %s", c.Request.Method, c.Request.URL.Path),
	})
}

// Health is what the health endpoint reports.
type Health struct {
	Status    string    `json:"status"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`

	// UptimeSeconds is sent alongside StartedAt so that a client with a
	// skewed clock still gets a usable figure.
	UptimeSeconds int64 `json:"uptimeSeconds"`

	// Connections is how many are open, when a limit is in force.
	Connections int `json:"connections,omitempty"`
}

func (a *API) handleHealth(c *gin.Context) {
	health := Health{
		Status:        "ok",
		Version:       a.opts.Version,
		StartedAt:     a.started,
		UptimeSeconds: int64(a.opts.Now().Sub(a.started).Seconds()),
	}
	if a.connections != nil {
		health.Connections = a.connections.Open()
	}
	c.JSON(http.StatusOK, health)
}

// discardHandler drops every record, standing in for a nil logger.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
