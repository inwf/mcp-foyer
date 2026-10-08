package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/logging"
	"mcp-foyer/internal/semantic"
	"mcp-foyer/internal/upstream"
)

// fakeEmbedding stands in for an OpenAI-compatible service and records
// what it was asked. The recording is guarded because it is written from
// the server's goroutine and read from the test's.
type fakeEmbedding struct {
	*httptest.Server

	mu            sync.Mutex
	authorization string
	model         string
	inputs        []string
}

func startFakeEmbedding(t *testing.T) *fakeEmbedding {
	t.Helper()

	fake := &fakeEmbedding{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		fake.mu.Lock()
		fake.authorization = r.Header.Get("Authorization")
		fake.model = body.Model
		fake.inputs = body.Input
		fake.mu.Unlock()

		// Three numbers are enough to report a width.
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2,0.3]}]}`)
	}))
	t.Cleanup(fake.Close)
	return fake
}

func (f *fakeEmbedding) seen() (authorization, model string, inputs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorization, f.model, f.inputs
}

type embeddingProbeResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Dimension int    `json:"dimension"`
	Error     string `json:"error"`
}

func probe(t *testing.T, h *harness, values map[string]any) embeddingProbeResult {
	t.Helper()

	var result embeddingProbeResult
	decode(t, h.do(t, http.MethodPost, "/api/embedding/test", map[string]any{"embedding": values}),
		http.StatusOK, &result)
	return result
}

func TestAProbeReportsWhatTheServiceAnswered(t *testing.T) {
	fake := startFakeEmbedding(t)
	h := start(t, nil)

	result := probe(t, h, map[string]any{
		"url": fake.URL, "apiKey": "sk-live", "model": "bge-m3",
	})

	if !result.OK {
		t.Fatalf("ok = false: %s", result.Error)
	}
	if result.Dimension != 3 {
		t.Errorf("dimension = %d, want the width the service answered with", result.Dimension)
	}

	authorization, model, inputs := fake.seen()
	if model != "bge-m3" {
		t.Errorf("model = %q, want bge-m3", model)
	}
	if authorization != "Bearer sk-live" {
		t.Errorf("authorization = %q, want the key from the form", authorization)
	}
	// The sample is written in both languages the directory mixes, so a
	// model that reads only one of them is caught here rather than by a
	// Chinese query returning nothing later.
	if len(inputs) != 1 || !strings.Contains(inputs[0], "读取文件") {
		t.Errorf("inputs = %v, want one text carrying both languages", inputs)
	}
}

// The form never sees a stored key in clear, so it sends the placeholder
// back. Probing with that must mean "the key I already have".
func TestAProbeUsesTheStoredKeyWhenThePlaceholderComesBack(t *testing.T) {
	fake := startFakeEmbedding(t)
	h := start(t, func(o *api.Options) {
		o.Configs = configs(t, func(cfg *config.Config) {
			cfg.Embedding.APIKey = "sk-stored"
		})
	})

	result := probe(t, h, map[string]any{
		"url": fake.URL, "apiKey": config.RedactedValue, "model": "bge-m3",
	})

	if !result.OK {
		t.Fatalf("ok = false: %s", result.Error)
	}
	if authorization, _, _ := fake.seen(); authorization != "Bearer sk-stored" {
		t.Errorf("authorization = %q, want the stored key", authorization)
	}
}

// A probe with nothing to probe is an answer, not a failure: the button
// reports what would happen, and what would happen is that no request is
// made. The message names the field so the form can be fixed.
func TestAProbeWithNoAddressReportsItAsAResult(t *testing.T) {
	h := start(t, nil)

	result := probe(t, h, map[string]any{"url": "", "model": "bge-m3"})

	if result.OK {
		t.Error("ok = true for an empty address")
	}
	if !strings.Contains(result.Error, "embedding.url") {
		t.Errorf("error = %q, want it to name the field", result.Error)
	}
}

func TestAProbeAgainstADeadServiceSaysSo(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close() // nothing listens there any more

	h := start(t, nil)
	result := probe(t, h, map[string]any{"url": dead.URL, "model": "bge-m3"})

	if result.OK {
		t.Error("ok = true for a service nothing listens on")
	}
	if result.Error == "" {
		t.Error("no reason was reported")
	}
}

func TestAProbeAgainstARefusingServiceCarriesItsExplanation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	t.Cleanup(server.Close)

	h := start(t, nil)
	result := probe(t, h, map[string]any{"url": server.URL, "apiKey": "sk-wrong", "model": "bge-m3"})

	if result.OK {
		t.Error("ok = true for a service that refused")
	}
	if !strings.Contains(result.Error, "invalid api key") {
		t.Errorf("error = %q, want the service's own explanation", result.Error)
	}
}

// A body the endpoint cannot read is still a bad request: there is
// nothing to report a result about.
func TestAProbeWithAMalformedBodyIsABadRequest(t *testing.T) {
	h := start(t, nil)

	resp := h.raw(t, http.MethodPost, "/api/embedding/test", `{"embedding":{"urlr":"x"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a misspelled field", resp.StatusCode)
	}
}

// stubSemantic is an index standing in for the real one, so that what is
// under test is the endpoint and not the indexer.
type stubSemantic struct {
	status       semantic.Status
	similarities []float32
	queried      []string
}

func (s *stubSemantic) Status() semantic.Status { return s.status }

func (s *stubSemantic) Similarities(_ context.Context, query string, candidates []gateway.Searchable) []float32 {
	s.queried = append(s.queried, query)
	out := make([]float32, len(candidates))
	copy(out, s.similarities)
	return out
}

func TestTheEmbeddingStatusComesFromTheIndex(t *testing.T) {
	h := start(t, func(o *api.Options) {
		o.Semantic = &stubSemantic{status: semantic.Status{
			Enabled: true, Model: "bge-m3", Dimension: 8,
			Indexed: 3, Total: 10, Building: true, LastError: "the service is down",
		}}
	})

	var got struct {
		Enabled   bool   `json:"enabled"`
		Model     string `json:"model"`
		Indexed   int    `json:"indexed"`
		Total     int    `json:"total"`
		Building  bool   `json:"building"`
		LastError string `json:"lastError"`
	}
	decode(t, h.get(t, "/api/embedding/status"), http.StatusOK, &got)

	if !got.Enabled || got.Indexed != 3 || got.Total != 10 || !got.Building {
		t.Errorf("status = %+v, want the index's own numbers", got)
	}
	if got.LastError != "the service is down" {
		t.Errorf("lastError = %q, want the reason the index recorded", got.LastError)
	}
}

// A process with no index still answers, with what the configuration
// says — the page asks either way.
func TestTheEmbeddingStatusWithoutAnIndexSaysWhatTheConfigurationSays(t *testing.T) {
	// The address and model are not decoration: the configuration is
	// refused at open time when the feature is on without them, which is
	// the rule this fixture would otherwise be breaking.
	h := start(t, func(o *api.Options) {
		o.Configs = configs(t, func(cfg *config.Config) {
			cfg.Embedding.Enabled = true
			cfg.Embedding.URL = "https://api.example.com/v1"
			cfg.Embedding.Model = "bge-m3"
		})
	})

	var got struct {
		Enabled bool `json:"enabled"`
		Indexed int  `json:"indexed"`
	}
	decode(t, h.get(t, "/api/embedding/status"), http.StatusOK, &got)

	if !got.Enabled {
		t.Error("enabled = false, want what the configuration says")
	}
	if got.Indexed != 0 {
		t.Errorf("indexed = %d, want 0", got.Indexed)
	}
}

// emptyUpstreams is a connection manager with no servers connected: the
// preview has nothing to rank, which is all a routing test needs.
func emptyUpstreams(t *testing.T) *upstream.Manager {
	t.Helper()

	store := logging.NewStore(50)
	log, err := logging.New(logging.Options{Level: slog.LevelError, Store: store})
	if err != nil {
		t.Fatalf("build the logger: %v", err)
	}
	t.Cleanup(func() { log.Close() })

	bus := events.NewBus()
	t.Cleanup(bus.Close)
	return upstream.NewManager("test", log, bus)
}

// The preview ranks one query both ways. An empty directory ranks
// nothing, but the query still reaches the ranking — which is what says
// the page goes through the same entry point a search does.
func TestThePreviewRanksOneQueryBothWays(t *testing.T) {
	sem := &stubSemantic{status: semantic.Status{Enabled: true, Model: "bge-m3"}}
	h := start(t, func(o *api.Options) {
		o.Upstreams = emptyUpstreams(t)
		o.Semantic = sem
		o.Configs = configs(t, func(cfg *config.Config) {
			cfg.Embedding.Enabled = true
			cfg.Embedding.URL = "https://api.example.com/v1"
			cfg.Embedding.Model = "bge-m3"
		})
	})

	var got struct {
		Enabled   bool     `json:"enabled"`
		Model     string   `json:"model"`
		Literal   []any    `json:"literal"`
		Fused     []any    `json:"fused"`
		Unmatched []string `json:"unmatched"`
	}
	decode(t, h.do(t, http.MethodPost, "/api/embedding/preview", map[string]any{"query": "bug tracker"}),
		http.StatusOK, &got)

	if !got.Enabled {
		t.Error("enabled = false, want what the configuration says")
	}
	if got.Model != "bge-m3" {
		t.Errorf("model = %q, want the index's own model", got.Model)
	}
	if len(sem.queried) != 1 || sem.queried[0] != "bug tracker" {
		t.Errorf("queried = %v, want the preview's own words", sem.queried)
	}
	if len(got.Unmatched) != 2 {
		t.Errorf("unmatched = %v, want both words named", got.Unmatched)
	}
}

// Without an index the two halves are the same list, and the answer says
// so — the page has to know whether it is showing a difference at all.
func TestAPreviewWithoutAnIndexShowsTheLexicalOrderTwice(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Upstreams = emptyUpstreams(t) })

	var got struct {
		Enabled bool `json:"enabled"`
	}
	decode(t, h.do(t, http.MethodPost, "/api/embedding/preview", map[string]any{"query": "bug tracker"}),
		http.StatusOK, &got)

	if got.Enabled {
		t.Error("enabled = true without an index")
	}
}

func TestAPreviewWithoutAQueryIsRefused(t *testing.T) {
	h := start(t, func(o *api.Options) { o.Upstreams = emptyUpstreams(t) })

	resp := h.do(t, http.MethodPost, "/api/embedding/preview", map[string]any{"query": "   "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a blank query", resp.StatusCode)
	}
}

func TestAPreviewWithoutUpstreamsSaysSo(t *testing.T) {
	h := start(t, nil)

	resp := h.do(t, http.MethodPost, "/api/embedding/preview", map[string]any{"query": "x"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 without a connection manager", resp.StatusCode)
	}
}
