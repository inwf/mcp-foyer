package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/embedding"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/semantic"
)

// probeText is what a probe embeds. It is written in both languages the
// directory mixes, because a model that reads only one of them is the
// mistake the probe exists to catch, and an English-only sample passes
// on exactly that mistake.
const probeText = "read a file 读取文件"

// embeddingTestResult reports what a probe found.
//
// Reaching the service is the fact being reported, so a service that
// refuses, times out or answers with nonsense comes back as a result
// with ok false rather than as a failed request — the same way a failed
// server connection is a status rather than an API error. The one
// exception is a body that could not be read at all, which is a bad
// request like any other.
type embeddingTestResult struct {
	OK bool `json:"ok"`

	LatencyMs int64 `json:"latencyMs"`
	Dimension int   `json:"dimension"`

	Error string `json:"error,omitempty"`
}

// handleTestEmbedding probes the embedding service with the values the
// settings form holds, saved or not.
//
// The key may arrive as the redaction placeholder, meaning the form
// never saw the stored one; that is not a key, so the stored one is used
// instead. Testing the saved key is the point of the button in that
// case: it answers "is the key I already have still good".
func (a *API) handleTestEmbedding(c *gin.Context) {
	var body struct {
		Embedding struct {
			URL    string `json:"url"`
			APIKey string `json:"apiKey"`
			Model  string `json:"model"`
		} `json:"embedding"`
	}
	if err := bindJSON(c, &body); err != nil {
		fail(c, err)
		return
	}

	stored := a.opts.Configs.Get().Embedding
	probe := config.Embedding{
		// Enabled so that the validation below asks for what a probe
		// needs: an address and a model to send it to.
		Enabled: true,
		URL:     strings.TrimSpace(body.Embedding.URL),
		APIKey:  strings.TrimSpace(body.Embedding.APIKey),
		Model:   strings.TrimSpace(body.Embedding.Model),
		Timeout: stored.Timeout,
	}
	if probe.APIKey == config.RedactedValue {
		probe.APIKey = stored.APIKey
	}

	if err := config.ValidateEmbedding(probe); err != nil {
		c.JSON(http.StatusOK, embeddingTestResult{OK: false, Error: err.Error()})
		return
	}

	started := time.Now()
	vectors, err := embedding.New(probe).Embed(c.Request.Context(), []string{probeText})
	latency := time.Since(started)

	if err != nil {
		a.log.Info("the embedding service did not answer a probe",
			"requestId", RequestID(c), "url", probe.URL, "error", err)
		c.JSON(http.StatusOK, embeddingTestResult{OK: false, Error: err.Error()})
		return
	}

	a.log.Info("the embedding service answered a probe",
		"requestId", RequestID(c), "url", probe.URL, "dimension", len(vectors[0]))

	c.JSON(http.StatusOK, embeddingTestResult{
		OK:        true,
		LatencyMs: latency.Milliseconds(),
		Dimension: len(vectors[0]),
	})
}

// Semantic is the embedding index as this package uses it: its state for
// the status endpoint, and its ranking for the preview.
//
// An interface rather than the indexer itself, so that this package is
// not tied to how the index is built. A nil one means this process has
// none, which is what a test of the routing layer alone wants.
type Semantic interface {
	Status() semantic.Status

	// gateway.Semantics is the very method search_tools calls, so that the
	// preview cannot disagree with the search about what a caller gets.
	gateway.Semantics
}

// handleEmbeddingStatus reports how far the index has got, which is what
// the settings page shows while a build is running and after one failed.
func (a *API) handleEmbeddingStatus(c *gin.Context) {
	if a.opts.Semantic == nil {
		c.JSON(http.StatusOK, semantic.Status{Enabled: a.opts.Configs.Get().Embedding.Enabled})
		return
	}
	c.JSON(http.StatusOK, a.opts.Semantic.Status())
}

const (
	defaultPreviewLimit = 5
	maxPreviewLimit     = 20
)

// embeddingPreviewResult is one query ranked both ways.
//
// The two lists are what a search would have returned at the moment it was
// asked: the lexical order, and the order with the meaning fused in. The
// similarity floor has no universal value — cosines are a property of the
// model — so the way to choose it is to look at what it does to this
// directory.
type embeddingPreviewResult struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model,omitempty"`

	Literal []gateway.SearchHit `json:"literal"`
	Fused   []gateway.SearchHit `json:"fused"`

	Unmatched []string `json:"unmatched,omitempty"`
}

// handleEmbeddingPreview ranks one query both ways, through the same
// entry point search_tools uses, so that what the page shows is what a
// caller would get.
func (a *API) handleEmbeddingPreview(c *gin.Context) {
	var body struct {
		Query string `json:"query"`
		Limit *int   `json:"limit,omitempty"`
	}
	if err := bindJSON(c, &body); err != nil {
		fail(c, err)
		return
	}

	query := strings.TrimSpace(body.Query)
	if query == "" {
		fail(c, BadRequest("query is empty; the preview ranks a query"))
		return
	}
	limit := defaultPreviewLimit
	if body.Limit != nil {
		limit = *body.Limit
	}
	if limit < 1 || limit > maxPreviewLimit {
		fail(c, BadRequest(fmt.Sprintf("limit must be between 1 and %d", maxPreviewLimit)))
		return
	}
	if a.opts.Upstreams == nil {
		fail(c, Unavailable("no upstream connections are running"))
		return
	}

	// The narrowing search_tools applies to the same words, so that the
	// page shows what a caller naming a server would get.
	candidates := gateway.Searchables(a.opts.Upstreams, a.opts.Configs,
		gateway.ServerNamedIn(query, a.opts.Upstreams))
	literal, unmatched := gateway.RankTools(c.Request.Context(), query, candidates, limit, nil)

	enabled := a.opts.Semantic != nil && a.opts.Configs.Get().Embedding.Enabled
	fused := literal
	if enabled {
		fused, _ = gateway.RankTools(c.Request.Context(), query, candidates, limit, a.opts.Semantic)
	}

	result := embeddingPreviewResult{
		Enabled:   enabled,
		Literal:   literal,
		Fused:     fused,
		Unmatched: unmatched,
	}
	if a.opts.Semantic != nil {
		result.Model = a.opts.Semantic.Status().Model
	}
	c.JSON(http.StatusOK, result)
}
