package semantic_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/semantic"
	"mcp-foyer/internal/upstream"
)

// A directory whose tool names decide their vectors: alpha_tool lands on
// the query's own direction, beta_tool at 0.6 to it, so a similarity in
// these tests is arithmetic the test can check.
func alphaSources() *fakeSources {
	return &fakeSources{
		statuses: []upstream.Status{{Name: "files", State: upstream.StateConnected, ToolCount: 2}},
		tools: map[string][]*mcp.Tool{"files": {
			{Name: "alpha_tool", Description: "does alpha things"},
			{Name: "beta_tool", Description: "does beta things"},
		}},
	}
}

func alphaCandidates() []gateway.Searchable {
	return []gateway.Searchable{
		{Server: "files", Tool: "alpha_tool"},
		{Server: "files", Tool: "beta_tool"},
	}
}

func vectorFor(text string) []float32 {
	if strings.Contains(text, "beta") {
		return []float32{0.6, 0.8}
	}
	return []float32{1, 0}
}

// vectorService answers each text with a vector the test decides, and
// counts what it was asked.
type vectorService struct {
	*httptest.Server

	mu       sync.Mutex
	requests int
	inputs   []string
}

func startVectorService(t *testing.T) *vectorService {
	t.Helper()

	service := &vectorService{}
	service.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		service.mu.Lock()
		service.requests++
		service.inputs = append(service.inputs, body.Input...)
		service.mu.Unlock()

		type vectorJSON struct {
			Embedding []float32 `json:"embedding"`
		}
		answer := struct {
			Data []vectorJSON `json:"data"`
		}{}
		for _, text := range body.Input {
			answer.Data = append(answer.Data, vectorJSON{Embedding: vectorFor(text)})
		}
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(service.Close)
	return service
}

func (s *vectorService) counts() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, append([]string(nil), s.inputs...)
}

// index builds the index against the service, which is what a running
// deployment does before any search asks for similarities.
func indexWith(t *testing.T, service *vectorService, adjust func(*config.Embedding)) *semantic.Indexer {
	t.Helper()

	ix := newIndexer(t, alphaSources(), managerFor(t, service.URL, adjust))
	if _, err := ix.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	return ix
}

func TestSimilaritiesAreWhatTheVectorsSay(t *testing.T) {
	ix := indexWith(t, startVectorService(t), nil)

	// A third candidate the index has never seen: nothing to say about it.
	candidates := append(alphaCandidates(), gateway.Searchable{Server: "other", Tool: "unindexed"})
	got := ix.Similarities(context.Background(), "alpha", candidates)

	if len(got) != len(candidates) {
		t.Fatalf("similarities = %v, want one per candidate", got)
	}
	if math.Abs(float64(got[0])-1) > 1e-6 {
		t.Errorf("similarities[0] = %v, want 1", got[0])
	}
	if math.Abs(float64(got[1])-0.6) > 1e-5 {
		t.Errorf("similarities[1] = %v, want 0.6", got[1])
	}
	if got[2] != 0 {
		t.Errorf("similarities[2] = %v, want 0 for a tool with no vector", got[2])
	}
}

func TestASimilarityBelowTheFloorIsNoSignal(t *testing.T) {
	ix := indexWith(t, startVectorService(t), func(e *config.Embedding) { e.MinSimilarity = 0.8 })

	got := ix.Similarities(context.Background(), "alpha", alphaCandidates())

	if got[0] == 0 {
		t.Error("a similarity of 1 was dropped at a floor of 0.8")
	}
	if got[1] != 0 {
		t.Errorf("similarities[1] = %v, want 0: 0.6 is below the floor", got[1])
	}
}

// A search's every page re-ranks the same query; the same question asked
// twice in a minute should cost one call.
func TestARepeatedQueryIsEmbeddedOnce(t *testing.T) {
	service := startVectorService(t)
	ix := indexWith(t, service, nil)
	before, _ := service.counts()

	ix.Similarities(context.Background(), "alpha", alphaCandidates())
	ix.Similarities(context.Background(), "  Alpha ", alphaCandidates())

	after, _ := service.counts()
	if after != before+1 {
		t.Errorf("the service was asked %d times, want once for one query", after-before)
	}
}

// The fallback the whole design rests on: a service that is gone leaves
// search to the words, quietly.
func TestADeadServiceIsNoSignal(t *testing.T) {
	service := startVectorService(t)
	ix := indexWith(t, service, nil)
	service.Close() // a service that answered the build and is gone for the query

	if got := ix.Similarities(context.Background(), "alpha", alphaCandidates()); got != nil {
		t.Errorf("similarities = %v, want nil when the service is gone", got)
	}
}

func TestNoSimilaritiesWhileTheSectionIsOff(t *testing.T) {
	service := startVectorService(t)
	m := managerFor(t, service.URL, nil)
	updateManager(t, m, func(c *config.Config) { c.Embedding.Enabled = false })

	ix := newIndexer(t, alphaSources(), m)
	if got := ix.Similarities(context.Background(), "alpha", alphaCandidates()); got != nil {
		t.Errorf("similarities = %v, want nil while the section is off", got)
	}
	if requests, _ := service.counts(); requests != 0 {
		t.Errorf("the service was asked %d times while the section is off", requests)
	}
}
