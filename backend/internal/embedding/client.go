// Package embedding speaks the OpenAI embeddings API, which hosted
// providers and local runners both implement.
//
// It is a client and nothing else: it turns texts into vectors and
// reports what went wrong in a sentence a person can act on. What the
// vectors are kept for is the gateway's business.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"mcp-foyer/internal/config"
)

// Client calls one embedding service.
type Client struct {
	endpoint string
	apiKey   string
	model    string
	http     *http.Client
}

// New builds a client for one service.
//
// The timeout is per request, the same meaning it has elsewhere in this
// project: one call, from sending to answer. A batch of tool texts takes
// longer than a single query, which is why the configured value is
// generous rather than tight.
func New(cfg config.Embedding) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{
		endpoint: endpointFor(cfg.URL),
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		http:     &http.Client{Timeout: timeout},
	}
}

// endpointFor turns the configured address into the URL to post to.
//
// The address may be a base address, the versioned base, or the endpoint
// itself; all three are common in the wild, and guessing wrong in either
// direction produces a 404 that reads as if the service were missing.
func endpointFor(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	switch {
	case strings.HasSuffix(trimmed, "/embeddings"):
		return trimmed
	case strings.HasSuffix(trimmed, "/v1"):
		return trimmed + "/embeddings"
	default:
		return trimmed + "/v1/embeddings"
	}
}

// Embed returns one vector per input, in the order the inputs were given.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{Model: c.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("encode the request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// No key means no header, rather than an empty bearer token: a local
	// runner may reject the latter and does not want the former.
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the service answered HTTP %d: %s",
			resp.StatusCode, errorSnippet(resp.Body))
	}

	var decoded struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("the answer from the service could not be read: %w", err)
	}
	if len(decoded.Data) != len(texts) {
		return nil, fmt.Errorf("the service returned %d vectors for %d inputs",
			len(decoded.Data), len(texts))
	}

	// Position is taken as the order. The API documents that answers come
	// back in input order; the index field is not consulted because a
	// service that omits it would have every vector carrying index zero,
	// and honouring that would collapse the batch instead of catching it.
	vectors := make([][]float32, len(decoded.Data))
	for i, item := range decoded.Data {
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("the service returned an empty vector for input %d", i)
		}
		if i > 0 && len(item.Embedding) != len(vectors[0]) {
			return nil, fmt.Errorf("the service returned vectors of differing widths, %d and %d",
				len(vectors[0]), len(item.Embedding))
		}
		vectors[i] = item.Embedding
	}
	return vectors, nil
}

// errorSnippet reads the service's explanation of a failure.
//
// Providers put the reason in a JSON envelope; anything else is passed
// through as text, bounded so that a stray HTML page cannot flood a log
// line.
func errorSnippet(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, 4<<10))
	if err != nil {
		return "no explanation"
	}

	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		if envelope.Error.Message != "" {
			return envelope.Error.Message
		}
		if envelope.Message != "" {
			return envelope.Message
		}
	}

	text := strings.Join(strings.Fields(string(raw)), " ")
	if text == "" {
		return "no explanation"
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}
