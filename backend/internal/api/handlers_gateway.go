package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/upstream"
)

// defaultSearchLimit bounds a tool search that does not ask for a size.
const defaultSearchLimit = 50

// AggregatedTool is one upstream tool as the aggregated view reports it.
type AggregatedTool struct {
	// Server is where the tool came from, and Tool is its name there.
	Server string `json:"server"`
	Tool   string `json:"tool"`

	// Exposed is the name the gateway offers it under.
	Exposed string `json:"exposed"`

	Description string `json:"description,omitempty"`
	InputSchema any    `json:"inputSchema,omitempty"`

	// Score is set when the list came from a search, and orders it.
	Score int `json:"score,omitempty"`
}

// ===== step 64: aggregated tools and resources =====

func (a *API) handleAggregatedTools(c *gin.Context) {
	if a.opts.Upstreams == nil {
		c.JSON(http.StatusOK, gin.H{"tools": []AggregatedTool{}})
		return
	}

	limit, err := queryInt(c, "limit", defaultSearchLimit)
	if err != nil {
		fail(c, err)
		return
	}

	cfg := a.opts.Configs.Get()
	byServer := a.opts.Upstreams.Tools()
	// Names have to be worked out over the exposed tools alone, because
	// that is the set the gateway registers. Computing them over every
	// upstream tool would report names that were never registered, and
	// would see collisions the registered set does not have.
	names := gateway.PublishedNames(byServer, cfg)

	// `all` asks for every upstream tool rather than the exposed ones.
	//
	// The default is what an MCP client would see, which is what the CLI
	// wants. A management interface wants the opposite: it is where
	// exposure is decided, so showing only what is already exposed leaves
	// nothing to decide about — and on a fresh installation, nothing at
	// all. The unexposed ones come back with an empty exposed name, which
	// is the same signal search_tools gives.
	all, err := queryBool(c, "all", false)
	if err != nil {
		fail(c, err)
		return
	}

	tools := make([]AggregatedTool, 0, names.Len())

	for server, list := range byServer {
		serverCfg := cfg.MCPServers[server]
		offered := list
		if !all {
			offered = gateway.FilterTools(list, serverCfg.ExposedTools)
		}
		for _, tool := range offered {
			if tool == nil || tool.Name == "" {
				continue
			}
			exposed, _ := names.Exposed(server, tool.Name)
			tools = append(tools, AggregatedTool{
				Server:      server,
				Tool:        tool.Name,
				Exposed:     exposed,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			})
		}
	}

	if query := strings.TrimSpace(c.Query("q")); query != "" {
		tools = searchWithin(query, tools, limit)
	} else {
		// Sorted by where the tool came from, because an unexposed tool has
		// no exposed name to sort by and they would all collide at the
		// front of the list.
		sort.Slice(tools, func(i, j int) bool {
			if tools[i].Server != tools[j].Server {
				return tools[i].Server < tools[j].Server
			}
			return tools[i].Tool < tools[j].Tool
		})
		if len(tools) > limit {
			tools = tools[:limit]
		}
	}

	c.JSON(http.StatusOK, gin.H{"tools": tools, "total": len(tools)})
}

// searchWithin ranks tools after the exposure filter has been applied.
func searchWithin(query string, tools []AggregatedTool, limit int) []AggregatedTool {
	candidates := make([]gateway.Searchable, 0, len(tools))
	for _, tool := range tools {
		candidates = append(candidates, gateway.Searchable{
			Server:      tool.Server,
			Tool:        tool.Tool,
			Exposed:     tool.Exposed,
			Description: tool.Description,
			Arguments:   gateway.ArgumentText(tool.InputSchema),
		})
	}

	// Keyed by where the tool came from rather than by its exposed name.
	// An unexposed tool has no exposed name, so every one of them would
	// key on the empty string and all but one would be lost.
	type origin struct{ server, tool string }
	byOrigin := make(map[origin]AggregatedTool, len(tools))
	for _, tool := range tools {
		byOrigin[origin{tool.Server, tool.Tool}] = tool
	}

	hits := gateway.SearchTools(query, candidates, limit)
	out := make([]AggregatedTool, 0, len(hits))
	for _, hit := range hits {
		tool, known := byOrigin[origin{hit.Server, hit.Tool}]
		if !known {
			continue
		}
		tool.Score = hit.Score
		out = append(out, tool)
	}
	return out
}

// AggregatedResource is one upstream resource with its origin.
type AggregatedResource struct {
	Server string `json:"server"`

	// URI is the resource's own URI on its server.
	URI string `json:"uri"`

	// Exposed is the URI the gateway offers it under.
	Exposed string `json:"exposed"`

	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

func (a *API) handleAggregatedResources(c *gin.Context) {
	if a.opts.Upstreams == nil {
		c.JSON(http.StatusOK, gin.H{"resources": []AggregatedResource{}})
		return
	}

	out := []AggregatedResource{}
	for server, resources := range a.opts.Upstreams.Resources() {
		for _, resource := range resources {
			out = append(out, AggregatedResource{
				Server:      server,
				URI:         resource.URI,
				Exposed:     gateway.ForwardedResourceURI(server, resource.URI),
				Name:        resource.Name,
				Description: resource.Description,
				MIMEType:    resource.MIMEType,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].URI < out[j].URI
	})

	c.JSON(http.StatusOK, gin.H{"resources": out, "total": len(out)})
}

// ===== step 65: gateway state =====

func (a *API) handleGatewaySessions(c *gin.Context) {
	sessions := []gateway.SessionInfo{}
	if a.opts.Gateway != nil {
		sessions = append(sessions, a.opts.Gateway.Sessions()...)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })

	c.JSON(http.StatusOK, gin.H{"sessions": sessions, "total": len(sessions)})
}

// handleGatewayTools reports what the gateway currently offers its own
// clients, which is not the same as what the upstreams offer: the
// gateway adds its own tools and applies each server's exposure rules.
func (a *API) handleGatewayTools(c *gin.Context) {
	tools := []*mcp.Tool{}
	if a.opts.Gateway != nil {
		tools = append(tools, a.opts.Gateway.PublishedTools()...)
	}

	c.JSON(http.StatusOK, gin.H{
		"tools":       tools,
		"total":       len(tools),
		"systemTools": gateway.SystemToolNames,
	})
}

// handleCallGatewayTool calls one of the gateway's own tools.
//
// These need a route of their own because they belong to no upstream
// server: /servers/:name/tools/:tool/call has no name to put in it. The
// call goes through the gateway's MCP server, so it is validated and
// answered exactly as it would be for a connected client.
func (a *API) handleCallGatewayTool(c *gin.Context) {
	tool := c.Param("tool")

	if a.opts.Gateway == nil {
		fail(c, Unavailable("the MCP endpoint is not running"))
		return
	}
	if !gateway.IsSystemTool(tool) {
		fail(c, NotFound(fmt.Sprintf(
			"%q is not one of mcp-foyer's own tools; a tool from a server is called "+
				"through /servers/{server}/tools/{tool}/call", tool)))
		return
	}

	var body struct {
		Arguments map[string]any `json:"arguments"`
	}
	if err := bindJSON(c, &body); err != nil {
		fail(c, err)
		return
	}

	result, err := a.opts.Gateway.CallSystemTool(c.Request.Context(), tool, body.Arguments)
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

// handleGatewayStatus summarises the gateway in one request, which is
// what the dashboard needs rather than four.
func (a *API) handleGatewayStatus(c *gin.Context) {
	cfg := a.opts.Configs.Get()

	connected, failed := 0, 0
	if a.opts.Upstreams != nil {
		for _, status := range a.opts.Upstreams.Statuses() {
			switch status.State {
			case upstream.StateConnected:
				connected++
			case upstream.StateFailed:
				failed++
			}
		}
	}

	published, sessions := 0, 0
	if a.opts.Gateway != nil {
		published = len(a.opts.Gateway.PublishedTools())
		sessions = len(a.opts.Gateway.Sessions())
	}

	c.JSON(http.StatusOK, gin.H{
		"servers": gin.H{
			"configured": len(cfg.MCPServers),
			"connected":  connected,
			"failed":     failed,
		},
		"tools":       published,
		"sessions":    sessions,
		"sessionMode": defaultSessionMode(cfg),
	})
}

func defaultSessionMode(cfg config.Config) config.SessionMode {
	if cfg.Gateway.DefaultSessionMode == "" {
		return config.SessionModeStateful
	}
	return cfg.Gateway.DefaultSessionMode
}
