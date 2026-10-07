package semantic

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/upstream"
)

// These tests live inside the package because they drive the loop's
// timer, which is unexported on purpose: it is a seam for tests, not a
// setting.

// changeable is a directory whose tools a test can replace between
// rounds.
type changeable struct {
	mu    sync.Mutex
	tools map[string][]*mcp.Tool
}

func (c *changeable) Statuses() []upstream.Status {
	return []upstream.Status{{Name: "files", State: upstream.StateConnected, ToolCount: 1}}
}

func (c *changeable) Tools() map[string][]*mcp.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tools
}

func (c *changeable) set(tools map[string][]*mcp.Tool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tools = tools
}

// A round must wait for the directory to settle: events alone change
// nothing, and each due runs one round.
//
// The wait itself is driven by an injected channel, so the test states
// the order of events instead of racing a clock. The one small real
// wait is the check that nothing happened — a claim about absence needs
// some window to be absent in.
func TestALoopRoundWaitsForTheSettleAndRunsForEachDue(t *testing.T) {
	fires := make(chan time.Time, 8)
	called := make(chan struct{}, 8)

	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		called <- struct{}{}

		type vector struct {
			Embedding []float32 `json:"embedding"`
		}
		answer := struct {
			Data []vector `json:"data"`
		}{}
		for range body.Input {
			answer.Data = append(answer.Data, vector{Embedding: []float32{1, 2, 3}})
		}
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(service.Close)

	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{}
	cfg.Embedding = config.Embedding{
		Enabled: true, URL: service.URL, Model: "bge-m3", Timeout: 5 * time.Second,
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save the configuration: %v", err)
	}
	manager, err := config.NewManager(path)
	if err != nil {
		t.Fatalf("open the configuration: %v", err)
	}

	src := &changeable{tools: map[string][]*mcp.Tool{"files": {{Name: "read"}}}}

	bus := events.NewBus()
	t.Cleanup(bus.Close)

	ix := New(Options{
		Configs:   manager,
		Upstreams: src,
		Bus:       bus,
		Path:      filepath.Join(t.TempDir(), "embeddings.bin"),
		Logger:    slog.New(slog.DiscardHandler),
		// The duration is never used: the injected channel stands in for
		// the clock.
		Settle: time.Hour,
		after:  func(time.Duration) <-chan time.Time { return fires },
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ix.Start(ctx)

	// Events alone do not run a round; the loop is waiting for the settle
	// to fire.
	bus.Publish(events.Event{Kind: events.ToolsChanged})
	select {
	case <-called:
		t.Fatal("a round ran before the settle fired")
	case <-time.After(20 * time.Millisecond):
	}

	fires <- time.Now()
	<-called

	// A changed directory and a second due: one more round, which has one
	// new text to embed.
	src.set(map[string][]*mcp.Tool{"files": {{Name: "read"}, {Name: "write"}}})
	bus.Publish(events.Event{Kind: events.ToolsChanged})
	fires <- time.Now()
	<-called
}
