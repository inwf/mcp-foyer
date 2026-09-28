package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/upstream"
)

// ServerView is one server as the API reports it: what it is configured
// to be, and what it is currently doing.
type ServerView struct {
	Name   string          `json:"name"`
	Config wireServer      `json:"config"`
	Status upstream.Status `json:"status"`

	// Exposed counts the tools this server actually contributes to the
	// gateway's tools/list.
	//
	// It is computed here rather than left to the caller because it needs
	// both halves at once: the configured allow list and the tools the
	// server currently offers. A caller holding only the configuration
	// would count names for tools that have since disappeared, and one
	// holding only the status has nothing to filter with.
	//
	// Nothing is exposed unless it is listed, so this is normally well
	// below Status.ToolCount — that is the design, not a fault, and the
	// number is reported so the difference is visible rather than silent.
	Exposed int `json:"exposedCount"`

	// Disabled counts the tools this server offers that the configuration
	// has switched off. Like Exposed, it counts what the server offers now:
	// a listed name whose tool has since disappeared switches nothing off.
	Disabled int `json:"disabledCount"`
}

// ===== step 60: server CRUD =====

func (a *API) handleListServers(c *gin.Context) {
	cfg := a.opts.Configs.Get()

	statuses := map[string]upstream.Status{}
	if a.opts.Upstreams != nil {
		for _, status := range a.opts.Upstreams.Statuses() {
			statuses[status.Name] = status
		}
	}

	views := make([]ServerView, 0, len(cfg.MCPServers))
	for name, server := range cfg.MCPServers {
		views = append(views, a.viewOf(name, server, statuses))
	}
	// A stable order keeps the UI's list from reshuffling on every poll.
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })

	c.JSON(http.StatusOK, gin.H{"servers": views})
}

func (a *API) viewOf(name string, server config.MCPServer, statuses map[string]upstream.Status) ServerView {
	status, known := statuses[name]
	if !known {
		// A server that has been configured but not yet taken up by the
		// connection manager still has a truthful state to report.
		status = upstream.Status{Name: name, State: upstream.StateDisconnected}
	}
	return ServerView{
		Name:     name,
		Config:   wireServer{server.Redact()},
		Status:   status,
		Exposed:  a.exposedCount(name, server),
		Disabled: a.disabledCount(name, server),
	}
}

// exposedCount counts the tools a server contributes to the gateway.
func (a *API) exposedCount(name string, server config.MCPServer) int {
	if a.opts.Upstreams == nil {
		return 0
	}
	return len(gateway.FilterTools(a.opts.Upstreams.Tools()[name], server.ExposedTools))
}

// disabledCount counts the tools a server offers that are switched off.
func (a *API) disabledCount(name string, server config.MCPServer) int {
	if a.opts.Upstreams == nil {
		return 0
	}
	count := 0
	for _, tool := range a.opts.Upstreams.Tools()[name] {
		if tool != nil && slices.Contains(server.DisabledTools, tool.Name) {
			count++
		}
	}
	return count
}

func (a *API) handleGetServer(c *gin.Context) {
	name, server, ok := a.requireServer(c)
	if !ok {
		return
	}

	statuses := map[string]upstream.Status{}
	if a.opts.Upstreams != nil {
		for _, status := range a.opts.Upstreams.Statuses() {
			statuses[status.Name] = status
		}
	}
	c.JSON(http.StatusOK, a.viewOf(name, server, statuses))
}

func (a *API) handleCreateServer(c *gin.Context) {
	var body struct {
		Name   string     `json:"name"`
		Server wireServer `json:"server"`
	}
	if err := bindJSON(c, &body); err != nil {
		fail(c, err)
		return
	}
	if body.Name == "" {
		fail(c, Invalid("the server needs a name",
			FieldError{Field: "name", Message: "is required"}))
		return
	}

	var conflict bool
	_, err := a.opts.Configs.Update(func(current *config.Config) error {
		if _, taken := current.MCPServers[body.Name]; taken {
			conflict = true
			return fmt.Errorf("a server named %q already exists", body.Name)
		}
		if current.MCPServers == nil {
			current.MCPServers = map[string]config.MCPServer{}
		}
		current.MCPServers[body.Name] = body.Server.MCPServer
		return current.Validate()
	})
	if err != nil {
		if conflict {
			// Replacing an existing server through the create endpoint
			// would silently discard whatever was there.
			fail(c, Conflict(err.Error()))
			return
		}
		fail(c, FromValidation("the server is not valid", err))
		return
	}

	a.log.Info("a server was added", "requestId", RequestID(c), "server", body.Name)
	a.applyConfig(c)

	c.JSON(http.StatusCreated, ServerView{
		Name:     body.Name,
		Config:   wireServer{body.Server.Redact()},
		Status:   upstream.Status{Name: body.Name, State: upstream.StateDisconnected},
		Exposed:  a.exposedCount(body.Name, body.Server.MCPServer),
		Disabled: a.disabledCount(body.Name, body.Server.MCPServer),
	})
}

func (a *API) handleUpdateServer(c *gin.Context) {
	name, existing, ok := a.requireServer(c)
	if !ok {
		return
	}

	var body struct {
		Server wireServer `json:"server"`
	}
	if err := bindJSON(c, &body); err != nil {
		fail(c, err)
		return
	}

	updated := restoreServerSecrets(body.Server.MCPServer, existing)
	if _, err := a.opts.Configs.Update(func(current *config.Config) error {
		current.MCPServers[name] = updated
		return current.Validate()
	}); err != nil {
		fail(c, FromValidation("the server is not valid", err))
		return
	}

	a.log.Info("a server was changed", "requestId", RequestID(c), "server", name)
	a.applyConfig(c)

	// The status is read back rather than left zero-valued: a view whose
	// state is the empty string would show as an unrecognised state in
	// the UI, when what is true is that the server is still in whatever
	// state it was before the edit.
	c.JSON(http.StatusOK, ServerView{
		Name:     name,
		Config:   wireServer{updated.Redact()},
		Status:   a.statusOfServer(name),
		Exposed:  a.exposedCount(name, updated),
		Disabled: a.disabledCount(name, updated),
	})
}

func (a *API) handleDeleteServer(c *gin.Context) {
	name, _, ok := a.requireServer(c)
	if !ok {
		return
	}

	if _, err := a.opts.Configs.Update(func(current *config.Config) error {
		delete(current.MCPServers, name)
		return nil
	}); err != nil {
		fail(c, Internal("the server could not be removed", err))
		return
	}

	// Applying the new configuration is what closes the connection and
	// stops its child process; leaving it running would orphan it.
	a.log.Info("a server was removed", "requestId", RequestID(c), "server", name)
	a.applyConfig(c)

	c.Status(http.StatusNoContent)
}

// ===== step 61: connection control =====

func (a *API) handleConnectServer(c *gin.Context) {
	name, _, ok := a.requireServer(c)
	if !ok {
		return
	}
	if a.opts.Upstreams == nil {
		fail(c, Unavailable("no connection manager is running"))
		return
	}

	// Connecting a server that is already connected is a no-op in the
	// connection manager, which is what makes this safe to retry.
	if err := a.opts.Upstreams.Connect(c.Request.Context(), name, a.opts.Configs.Get().Startup); err != nil {
		// The server is reachable as a concept but not as a process, which
		// is a state of the world rather than a bad request.
		fail(c, &Error{
			Code:    CodeUnavailable,
			Message: fmt.Sprintf("could not connect to %q: %s", name, err),
			cause:   err,
		})
		return
	}

	a.syncGateway()
	c.JSON(http.StatusOK, a.statusOfServer(name))
}

func (a *API) handleDisconnectServer(c *gin.Context) {
	name, _, ok := a.requireServer(c)
	if !ok {
		return
	}
	if a.opts.Upstreams == nil {
		fail(c, Unavailable("no connection manager is running"))
		return
	}

	// Disconnecting something already disconnected is not a failure: the
	// caller asked for a state, and that state already holds.
	if err := a.opts.Upstreams.Disconnect(name); err != nil && !errors.Is(err, upstream.ErrNotConnected) {
		fail(c, Internal(fmt.Sprintf("could not disconnect %q", name), err))
		return
	}

	a.syncGateway()
	c.JSON(http.StatusOK, a.statusOfServer(name))
}

func (a *API) statusOfServer(name string) upstream.Status {
	if a.opts.Upstreams != nil {
		for _, status := range a.opts.Upstreams.Statuses() {
			if status.Name == name {
				return status
			}
		}
	}
	return upstream.Status{Name: name, State: upstream.StateDisconnected}
}

// ===== step 62: one server's tools and resources =====

// requireConnection resolves a configured server that is also connected.
//
// A server that is merely not connected is reported as such rather than
// as an empty list: an empty list says "this server offers nothing",
// which would send a caller looking for the wrong problem.
func (a *API) requireConnection(c *gin.Context) (*upstream.Conn, bool) {
	name, _, ok := a.requireServer(c)
	if !ok {
		return nil, false
	}
	if a.opts.Upstreams == nil {
		fail(c, Unavailable("no connection manager is running"))
		return nil, false
	}

	conn, running := a.opts.Upstreams.Get(name)
	if !running || !conn.Status().Connected() {
		fail(c, Unavailable(fmt.Sprintf(
			"%q is not connected; connect it before listing or calling what it offers", name)))
		return nil, false
	}
	return conn, true
}

// ServerTool is one of a server's tools, with the name the gateway
// offers it under if it offers it at all.
//
// Exposed is empty when the tool is not in the gateway's tools/list. The
// name is filled in here rather than worked out by the caller because it
// depends on the whole exposed set: a collision between two servers is
// resolved by renaming, and only somewhere holding every server's
// configuration can see one. A second computation of these names is
// exactly what went wrong before.
type ServerTool struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"inputSchema,omitempty"`
	Exposed     string `json:"exposed,omitempty"`

	// Disabled marks a tool the configuration has switched off. It is
	// listed so that it can be switched back on, and it cannot be called
	// here any more than through the MCP endpoint.
	Disabled bool `json:"disabled,omitempty"`
}

func (a *API) handleServerTools(c *gin.Context) {
	name := c.Param("name")
	conn, ok := a.requireConnection(c)
	if !ok {
		return
	}

	cfg := a.opts.Configs.Get()
	var names gateway.NameMap
	if a.opts.Upstreams != nil {
		names = gateway.PublishedNames(a.opts.Upstreams.Tools(), cfg)
	}

	tools := nonNilTools(conn.Tools())
	out := make([]ServerTool, 0, len(tools))
	for _, tool := range tools {
		exposed, _ := names.Exposed(name, tool.Name)
		out = append(out, ServerTool{
			Name:        tool.Name,
			Title:       tool.Title,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Exposed:     exposed,
			Disabled:    gateway.IsDisabled(cfg, name, tool.Name),
		})
	}
	c.JSON(http.StatusOK, gin.H{"tools": out})
}

func (a *API) handleServerResources(c *gin.Context) {
	conn, ok := a.requireConnection(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"resources": nonNilResources(conn.Resources())})
}

func (a *API) handleCallServerTool(c *gin.Context) {
	conn, ok := a.requireConnection(c)
	if !ok {
		return
	}

	// Refused here as well as on the MCP endpoint. The web interface and
	// the CLI call through this route, and a tool the operator switched
	// off is off for them too.
	tool := c.Param("tool")
	if gateway.IsDisabled(a.opts.Configs.Get(), conn.Name(), tool) {
		fail(c, Conflict(fmt.Sprintf("%q on %q is disabled; enable it before calling it",
			tool, conn.Name())))
		return
	}

	var body struct {
		Arguments map[string]any `json:"arguments"`
	}
	if err := bindJSON(c, &body); err != nil {
		fail(c, err)
		return
	}

	result, err := conn.CallTool(c.Request.Context(), tool, body.Arguments)
	if err != nil {
		fail(c, &Error{
			Code:    CodeUnavailable,
			Message: fmt.Sprintf("calling %q failed: %s", tool, err),
			cause:   err,
		})
		return
	}

	// A tool that ran and reported a problem is a successful call with a
	// failed result, and the caller needs to see what it said.
	c.JSON(http.StatusOK, gin.H{
		"isError":           result.IsError,
		"content":           result.Content,
		"structuredContent": result.StructuredContent,
	})
}

func (a *API) handleReadServerResource(c *gin.Context) {
	conn, ok := a.requireConnection(c)
	if !ok {
		return
	}

	uri := c.Query("uri")
	if uri == "" {
		fail(c, BadRequest("uri is required"))
		return
	}

	result, err := conn.ReadResource(c.Request.Context(), uri)
	if err != nil {
		fail(c, &Error{
			Code:    CodeUnavailable,
			Message: fmt.Sprintf("reading %q failed: %s", uri, err),
			cause:   err,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"contents": result.Contents})
}

// nonNilTools makes an empty list encode as [] rather than null, which
// a client would otherwise have to guard against on every use.
func nonNilTools(tools []*mcp.Tool) []*mcp.Tool {
	if tools == nil {
		return []*mcp.Tool{}
	}
	return tools
}

func nonNilResources(resources []*mcp.Resource) []*mcp.Resource {
	if resources == nil {
		return []*mcp.Resource{}
	}
	return resources
}
