package semantic_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/embedding"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/semantic"
	"mcp-foyer/internal/upstream"
)

// fakeSources is a directory standing still, which is all these tests
// need: the real one is the upstream manager.
type fakeSources struct {
	statuses []upstream.Status
	tools    map[string][]*mcp.Tool
}

func (f *fakeSources) Statuses() []upstream.Status   { return f.statuses }
func (f *fakeSources) Tools() map[string][]*mcp.Tool { return f.tools }

// fakeService is an OpenAI-compatible embedding service that records
// what it was asked for.
type fakeService struct {
	*httptest.Server

	mu       sync.Mutex
	requests int
	texts    []string
	fails    bool
}

func startService(t *testing.T, fails bool) *fakeService {
	t.Helper()

	fake := &fakeService{fails: fails}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		fake.mu.Lock()
		fake.requests++
		fake.texts = append(fake.texts, body.Input...)
		fake.mu.Unlock()

		if fake.fails {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"the service is down"}}`))
			return
		}

		type vector struct {
			Embedding []float32 `json:"embedding"`
		}
		answer := struct {
			Data []vector `json:"data"`
		}{}
		for range body.Input {
			answer.Data = append(answer.Data, vector{Embedding: []float32{0.1, 0.2, 0.3}})
		}
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(fake.Close)
	return fake
}

func (f *fakeService) seen() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, append([]string(nil), f.texts...)
}

// managerFor writes a configuration with the embedding section pointed
// at url and opens it.
func managerFor(t *testing.T, url string, adjust func(*config.Embedding)) *config.Manager {
	t.Helper()

	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{}
	cfg.Embedding = config.Embedding{
		Enabled: true, URL: url, Model: "bge-m3", Timeout: 5 * time.Second,
	}
	if adjust != nil {
		adjust(&cfg.Embedding)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save the configuration: %v", err)
	}
	manager, err := config.NewManager(path)
	if err != nil {
		t.Fatalf("open the configuration: %v", err)
	}
	return manager
}

func updateManager(t *testing.T, m *config.Manager, adjust func(*config.Config)) {
	t.Helper()

	if _, err := m.Update(func(c *config.Config) error {
		adjust(c)
		return nil
	}); err != nil {
		t.Fatalf("update the configuration: %v", err)
	}
}

func directory() *fakeSources {
	return &fakeSources{
		statuses: []upstream.Status{
			{Name: "files", State: upstream.StateConnected, ServerName: "Filesystem", ToolCount: 2},
			{Name: "notes", State: upstream.StateConnected, ToolCount: 1},
		},
		tools: map[string][]*mcp.Tool{
			"files": {
				{Name: "read", Description: "read a file", InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "the file to read"},
					},
				}},
				{Name: "write", Description: "write a file"},
			},
			"notes": {
				{Name: "list", Description: "list the notes"},
			},
		},
	}
}

func newIndexer(t *testing.T, src semantic.Sources, m *config.Manager) *semantic.Indexer {
	t.Helper()

	return semantic.New(semantic.Options{
		Configs:   m,
		Upstreams: src,
		Bus:       events.NewBus(),
		Path:      filepath.Join(t.TempDir(), "embeddings.bin"),
		Logger:    slog.New(slog.DiscardHandler),
	})
}

func TestARoundIndexesEveryUsableTool(t *testing.T) {
	service := startService(t, false)
	ix := newIndexer(t, directory(), managerFor(t, service.URL, nil))

	result, err := ix.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if result.Indexed != 3 || result.Total != 3 {
		t.Errorf("result = %+v, want three of three", result)
	}

	// What went out is what search would have matched, which is what
	// makes the two rankings agree about what a tool is about.
	_, texts := service.seen()
	joined := strings.Join(texts, "\n")
	for _, want := range []string{"Filesystem", "read a file", "the file to read", "list the notes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the embedded texts do not carry %q:\n%s", want, joined)
		}
	}
}

func TestADisabledToolIsNotEmbedded(t *testing.T) {
	service := startService(t, false)
	m := managerFor(t, service.URL, nil)
	updateManager(t, m, func(c *config.Config) {
		c.MCPServers["files"] = config.MCPServer{
			Transport: config.TransportStdio, Command: "echo", Timeout: time.Minute,
			DisabledTools: []string{"write"},
		}
	})

	ix := newIndexer(t, directory(), m)
	result, err := ix.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if result.Indexed != 2 {
		t.Errorf("indexed = %d, want the disabled tool left out", result.Indexed)
	}
	if _, texts := service.seen(); strings.Contains(strings.Join(texts, "\n"), "write a file") {
		t.Error("a disabled tool was embedded")
	}
}

func TestNothingIsEmbeddedWhileTheSectionIsOff(t *testing.T) {
	service := startService(t, false)
	m := managerFor(t, service.URL, nil)
	updateManager(t, m, func(c *config.Config) { c.Embedding.Enabled = false })

	ix := newIndexer(t, directory(), m)
	result, err := ix.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if result != (embedding.Result{}) {
		t.Errorf("result = %+v, want nothing done", result)
	}
	if requests, _ := service.seen(); requests != 0 {
		t.Errorf("the service was called %d times while the section is off", requests)
	}
	if status := ix.Status(); status.Enabled {
		t.Error("status says enabled while the section is off")
	}
}

func TestASecondRoundEmbedsNothing(t *testing.T) {
	service := startService(t, false)
	ix := newIndexer(t, directory(), managerFor(t, service.URL, nil))

	if _, err := ix.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	before, _ := service.seen()

	result, err := ix.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if result.Embedded != 0 {
		t.Errorf("embedded %d, want nothing on an unchanged directory", result.Embedded)
	}
	if after, _ := service.seen(); after != before {
		t.Errorf("the service was called again for an unchanged directory")
	}
}

func TestAFailingServiceIsRecorded(t *testing.T) {
	service := startService(t, true)
	ix := newIndexer(t, directory(), managerFor(t, service.URL, nil))

	if _, err := ix.Rebuild(context.Background()); err == nil {
		t.Fatal("Rebuild succeeded against a failing service")
	}

	status := ix.Status()
	if !strings.Contains(status.LastError, "the service is down") {
		t.Errorf("lastError = %q, want the service's own explanation", status.LastError)
	}
	if status.Indexed != 0 {
		t.Errorf("indexed = %d, want nothing", status.Indexed)
	}
	if status.Total != 3 {
		t.Errorf("total = %d, want the three tools it was asked for", status.Total)
	}
}

// Documents is the whole of what gets embedded: one text per usable
// tool, carrying the fields search matches.
func TestDocumentsCarriesTheFieldsSearchMatches(t *testing.T) {
	docs := semantic.Documents(directory(), config.Default())
	if len(docs) != 3 {
		t.Fatalf("documents = %d, want one per tool", len(docs))
	}

	byKey := make(map[embedding.Key]string, len(docs))
	for _, doc := range docs {
		byKey[embedding.Key{Server: doc.Server, Tool: doc.Tool}] = doc.Text
	}

	read, ok := byKey[embedding.Key{Server: "files", Tool: "read"}]
	if !ok {
		t.Fatalf("no document for files/read: %v", docs)
	}
	for _, want := range []string{"files", "Filesystem", "read", "read a file", "path", "the file to read"} {
		if !strings.Contains(read, want) {
			t.Errorf("the text for files/read does not carry %q:\n%s", want, read)
		}
	}
	if strings.Contains(read, "list the notes") {
		t.Errorf("the text for files/read carries another tool's description:\n%s", read)
	}
}

func TestTheServerDescriptionIsPartOfItsToolsText(t *testing.T) {
	src := &fakeSources{
		statuses: []upstream.Status{{Name: "files", State: upstream.StateConnected, ToolCount: 1}},
		tools:    map[string][]*mcp.Tool{"files": {{Name: "read"}}},
	}
	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"files": {
			Transport: config.TransportStdio, Command: "echo", Timeout: time.Minute,
			Description: "the team's document store",
		},
	}

	docs := semantic.Documents(src, cfg)
	if len(docs) != 1 {
		t.Fatalf("documents = %d, want one", len(docs))
	}
	if !strings.Contains(docs[0].Text, "the team's document store") {
		t.Errorf("the text does not carry the server description:\n%s", docs[0].Text)
	}
}

// A handshake name that merely repeats the configured name is dropped,
// so the text is not padded with a word twice over.
func TestARepeatedServerNameIsNotCopiedIntoTheText(t *testing.T) {
	src := &fakeSources{
		statuses: []upstream.Status{{Name: "files", State: upstream.StateConnected, ServerName: "files"}},
		tools:    map[string][]*mcp.Tool{"files": {{Name: "read"}}},
	}

	docs := semantic.Documents(src, config.Default())
	if len(docs) != 1 {
		t.Fatalf("documents = %d, want one", len(docs))
	}
	if got := strings.Count(docs[0].Text, "files"); got != 1 {
		t.Errorf("the text carries the server name %d times:\n%s", got, docs[0].Text)
	}
}
