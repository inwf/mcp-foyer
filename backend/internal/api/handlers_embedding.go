package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/embedding"
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
