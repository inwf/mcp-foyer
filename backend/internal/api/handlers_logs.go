package api

import (
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/logging"
)

// defaultLogLimit bounds a log query that does not ask for a size. The
// store holds far more than a view can usefully show, and sending all of
// it by default would make the common case the slowest one.
const defaultLogLimit = 200

// maxLogLimit bounds what a caller may ask for.
const maxLogLimit = 5000

// LogEntry is one log record as the API reports it.
//
// This is a shape of its own rather than the store's: the level is a
// name instead of a number, because a client should not have to know
// that slog's info level happens to be zero.
type LogEntry struct {
	Time    time.Time         `json:"time"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Module  string            `json:"module,omitempty"`
	Server  string            `json:"server,omitempty"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

func (a *API) handleQueryLogs(c *gin.Context) {
	if a.opts.Logs == nil {
		fail(c, Unavailable("no log store is running"))
		return
	}

	limit, err := queryInt(c, "limit", defaultLogLimit)
	if err != nil {
		fail(c, err)
		return
	}
	if limit <= 0 || limit > maxLogLimit {
		fail(c, BadRequest(fmt.Sprintf("limit must be between 1 and %d", maxLogLimit)))
		return
	}

	since, err := queryTime(c, "since")
	if err != nil {
		fail(c, err)
		return
	}

	level := config.LogLevel(c.Query("level"))
	if level != "" {
		if _, err := logging.ParseLevel(level); err != nil {
			fail(c, BadRequest(fmt.Sprintf(
				"level must be one of debug, info, warn or error, not %q", level)))
			return
		}
	}

	module := c.Query("module")
	if module != "" && !slices.Contains(logging.Modules, module) {
		fail(c, BadRequest(fmt.Sprintf("module must be one of %v, not %q", logging.Modules, module)))
		return
	}

	entries := a.opts.Logs.Query(logging.Query{
		Server:   c.Query("server"),
		Module:   module,
		MinLevel: level,
		Since:    since,
		Limit:    limit,
	})

	out := make([]LogEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, LogEntry{
			Time:    entry.Time,
			Level:   string(logging.LevelName(entry.Level)),
			Message: entry.Message,
			Module:  entry.Module,
			Server:  entry.Server,
			Attrs:   entry.Attrs,
		})
	}

	c.JSON(http.StatusOK, gin.H{"entries": out, "servers": a.opts.Logs.Servers()})
}

// handleClearLogs empties the log view, either for one server or
// entirely.
func (a *API) handleClearLogs(c *gin.Context) {
	if a.opts.Logs == nil {
		fail(c, Unavailable("no log store is running"))
		return
	}

	server := c.Query("server")
	a.opts.Logs.Clear(server)

	// Recording the clearing is what stops a cleared log from being
	// indistinguishable from one that was never written.
	a.log.Info("the log view was cleared", "requestId", RequestID(c), "server", server)
	c.Status(http.StatusNoContent)
}
