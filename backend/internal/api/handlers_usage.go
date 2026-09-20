package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"mcphub/internal/usage"
)

// handleGatewayUsage reports how the connected models have used the
// tools: which were found by search, which were called, and how often
// those calls failed. It is what tells an operator which tools are worth
// exposing and which descriptions never get found.
//
// The counts are the model's use only; calls made from this API are not
// in them.
func (a *API) handleGatewayUsage(c *gin.Context) {
	if a.opts.Usage == nil {
		c.JSON(http.StatusOK, usage.Snapshot{Entries: []usage.Entry{}})
		return
	}
	c.JSON(http.StatusOK, a.opts.Usage.Snapshot())
}

// handleResetGatewayUsage starts the counts again from zero.
func (a *API) handleResetGatewayUsage(c *gin.Context) {
	if a.opts.Usage == nil {
		fail(c, Unavailable("no usage counts are kept"))
		return
	}
	a.opts.Usage.Reset()
	a.log.Info("the tool usage counts were reset", "requestId", RequestID(c))
	c.Status(http.StatusNoContent)
}
