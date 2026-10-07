package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
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
