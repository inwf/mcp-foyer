// Package semantic keeps the embedding index in step with the upstream
// directory.
//
// It decides what gets embedded — every usable tool, described the way
// search describes it — when that happens (after the directory has
// settled), and with which service. Everything here is inert while the
// embedding section is off, which is how it ships.
package semantic

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/embedding"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/upstream"
)

// Sources is the directory the index is built from: the same one search
// reads.
type Sources interface {
	Statuses() []upstream.Status
	Tools() map[string][]*mcp.Tool
}

// Options configures an [Indexer].
type Options struct {
	Configs   *config.Manager
	Upstreams Sources
	Bus       *events.Bus

	// Path is where the index file lives.
	Path string

	Logger *slog.Logger

	// Settle is how long the indexer waits for a burst of upstream
	// changes to stop before it rebuilds. Zero means DefaultSettle.
	Settle time.Duration

	// after produces the timer channel, injectable so that tests can
	// drive the loop without waiting on the clock. Nil means time.After.
	after func(time.Duration) <-chan time.Time
}

// DefaultSettle absorbs a burst of servers connecting or changing their
// tools into one round. Long enough to cover a startup wave, short
// enough that a search just after a change does not go long without its
// vectors.
const DefaultSettle = 3 * time.Second

// Indexer keeps one index in line with the directory, in the background.
type Indexer struct {
	opts  Options
	index *embedding.Index
	log   *slog.Logger

	mu    sync.Mutex
	total int

	// queries remembers recent query vectors. Every page of a search
	// re-ranks the same query, and an agent trying several spellings
	// repeats itself sooner than it looks.
	queries    map[string][]float32
	queryOrder []string
}

// New opens the index — or starts over from an empty one, saying so in
// the log: a file that cannot be read is not a reason to refuse to run,
// and the next round rebuilds what it can.
func New(opts Options) *Indexer {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Settle <= 0 {
		opts.Settle = DefaultSettle
	}
	if opts.after == nil {
		opts.after = time.After
	}

	index, err := embedding.OpenIndex(opts.Path)
	if err != nil {
		opts.Logger.Warn("the embedding index could not be read and will be rebuilt from scratch",
			"error", err)
		index = embedding.NewIndex(opts.Path)
	}

	return &Indexer{opts: opts, index: index, log: opts.Logger}
}

// Start keeps the index up to date for as long as ctx lives.
func (ix *Indexer) Start(ctx context.Context) {
	go ix.run(ctx)
}

// Rebuild runs one round now and reports what it did. It is what the
// loop calls, and what tests drive directly.
func (ix *Indexer) Rebuild(ctx context.Context) (embedding.Result, error) {
	cfg := ix.opts.Configs.Get()
	section := cfg.Embedding
	if !section.Enabled {
		return embedding.Result{}, nil
	}
	if section.URL == "" || section.Model == "" {
		return embedding.Result{}, errors.New("embedding is enabled without an address or a model")
	}

	docs := Documents(ix.opts.Upstreams, cfg)
	ix.mu.Lock()
	ix.total = len(docs)
	ix.mu.Unlock()

	return ix.index.Sync(ctx, section.Model, docs, embedding.New(section))
}

// Index exposes the vectors themselves, which is what ranking reads.
func (ix *Indexer) Index() *embedding.Index { return ix.index }

// Status is what an operator can learn about the embedding index.
type Status struct {
	Enabled   bool   `json:"enabled"`
	Model     string `json:"model,omitempty"`
	Dimension int    `json:"dimension,omitempty"`
	Indexed   int    `json:"indexed"`
	Total     int    `json:"total"`
	Building  bool   `json:"building"`

	// LastError is the last round's failure, empty after a round that
	// finished. It is what the settings page shows when a service
	// silently stops working.
	LastError string `json:"lastError,omitempty"`

	// LastDone is when a round last finished, in unix seconds.
	LastDone int64 `json:"lastDone,omitempty"`
}

// Status is the current state, from the configuration and the index.
func (ix *Indexer) Status() Status {
	status := ix.index.Status()

	ix.mu.Lock()
	total := ix.total
	ix.mu.Unlock()

	return Status{
		Enabled:   ix.opts.Configs.Get().Embedding.Enabled,
		Model:     status.Model,
		Dimension: status.Dimension,
		Indexed:   status.Indexed,
		Total:     total,
		Building:  status.Building,
		LastError: status.LastError,
		LastDone:  status.LastDone,
	}
}

// run rebuilds after the directory has settled, for as long as the
// context lives.
//
// The first round is due immediately: a fresh process has no events to
// wait for, and whatever is on disk may be behind the directory already.
func (ix *Indexer) run(ctx context.Context) {
	incoming, cancel := ix.opts.Bus.Subscribe(
		events.ToolsChanged,
		events.ConfigUpdated,
		events.ServerConnected,
		events.ServerDisconnected,
		events.ServerFailed,
	)
	defer cancel()

	due := ix.opts.after(0)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-incoming:
			if !ok {
				return
			}
			// One wait per burst: an event arriving while the loop is
			// already waiting does not push the round further out, or a
			// directory that keeps chattering would never settle.
			if due == nil {
				due = ix.opts.after(ix.opts.Settle)
			}
		case <-due:
			due = nil
			ix.round(ctx)
		}
	}
}

// round runs one rebuild and says what it did. Nothing here is fatal:
// the index makes search better, and a round that fails leaves the
// previous one in place.
func (ix *Indexer) round(ctx context.Context) {
	started := time.Now()
	result, err := ix.Rebuild(ctx)
	if err != nil {
		ix.log.Warn("the embedding index could not be brought up to date", "error", err)
		return
	}
	if result.Total == 0 {
		return
	}

	ix.log.Info("the embedding index was brought up to date",
		"indexed", result.Indexed, "total", result.Total,
		"embedded", result.Embedded, "took", time.Since(started).Round(time.Millisecond))
}

// Documents is every usable tool as one text to embed: the tool's name
// and description, the server's configured description and handshake
// name, and the names and descriptions of the tool's parameters.
//
// It is deliberately the same set of fields that a lexical search
// matches — the tool name, the server name, the descriptions, the
// parameter prose — so that the two rankings cannot disagree about what
// a tool is about. Disabled tools are not here, exactly as they are not
// in search.
func Documents(ups Sources, cfg config.Config) []embedding.Document {
	titles := make(map[string]string)
	for _, status := range ups.Statuses() {
		titles[status.Name] = status.ServerName
	}

	usable := gateway.UsableTools(ups.Tools(), cfg)
	servers := slices.Sorted(maps.Keys(usable))

	var out []embedding.Document
	for _, server := range servers {
		title := titles[server]
		description := cfg.MCPServers[server].Description
		for _, tool := range usable[server] {
			if tool == nil || tool.Name == "" {
				continue
			}
			out = append(out, embedding.Document{
				Server: server,
				Tool:   tool.Name,
				Text:   documentText(server, title, description, tool),
			})
		}
	}
	return out
}

// documentText joins the parts a tool is embedded as, skipping the empty
// ones — and the title when it merely repeats the server name, which is
// what an upstream that never named itself produces.
func documentText(server, serverTitle, serverDescription string, tool *mcp.Tool) string {
	if serverTitle == server {
		serverTitle = ""
	}
	parts := []string{
		server,
		serverTitle,
		serverDescription,
		tool.Name,
		tool.Description,
		gateway.ArgumentText(tool.InputSchema),
	}

	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}

// The gateway asks for [gateway.Semantics]; this keeps the two from
// drifting apart.
var _ gateway.Semantics = (*Indexer)(nil)

// queryCacheSize caps how many query vectors are remembered. A few dozen
// covers the page-by-page repetition of one search without growing a
// second index.
const queryCacheSize = 64

// Similarities reports how close each candidate is to the query, one
// value per candidate in order, zero meaning nothing to say about it.
//
// Nothing to say covers everything that would otherwise be an error the
// search path has to handle: the section is off, the index is empty, the
// candidate has no vector yet, the closeness is below the configured
// floor, and the service did not answer. A nil result says it about every
// candidate at once, and the caller falls back to ranking by words.
func (ix *Indexer) Similarities(ctx context.Context, query string, candidates []gateway.Searchable) []float32 {
	section := ix.opts.Configs.Get().Embedding
	if !section.Enabled || len(candidates) == 0 {
		return nil
	}
	query = strings.TrimSpace(query)
	if query == "" || ix.index.Width() == 0 {
		return nil
	}

	vector, err := ix.queryVector(ctx, section, query)
	if err != nil {
		// Not an error the search reports: it ranks by words instead. The
		// log is where someone asks why the meaning stopped counting.
		ix.log.Warn("the query could not be embedded; searching by words only", "error", err)
		return nil
	}

	floor := float32(section.MinSimilarity)
	similarities := make([]float32, len(candidates))
	for i, candidate := range candidates {
		tool, ok := ix.index.Vector(embedding.Key{Server: candidate.Server, Tool: candidate.Tool})
		if !ok {
			continue
		}
		if similarity := embedding.Cosine(vector, tool); similarity > 0 && similarity >= floor {
			similarities[i] = similarity
		}
	}
	return similarities
}

// queryVector embeds one query, remembering recent answers: a search's
// every page re-ranks the same query, and the same question asked twice
// in a minute should cost one call.
func (ix *Indexer) queryVector(ctx context.Context, section config.Embedding, query string) ([]float32, error) {
	key := strings.ToLower(strings.Join(strings.Fields(query), " "))

	ix.mu.Lock()
	if vector, ok := ix.queries[key]; ok {
		ix.mu.Unlock()
		return vector, nil
	}
	ix.mu.Unlock()

	vectors, err := embedding.New(section).Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("the service answered %d vectors for one query", len(vectors))
	}
	if width := ix.index.Width(); width > 0 && len(vectors[0]) != width {
		return nil, fmt.Errorf("the service answered a vector of width %d, and the index holds width %d",
			len(vectors[0]), width)
	}

	ix.mu.Lock()
	if ix.queries == nil {
		ix.queries = make(map[string][]float32)
	}
	if len(ix.queryOrder) >= queryCacheSize {
		delete(ix.queries, ix.queryOrder[0])
		ix.queryOrder = ix.queryOrder[1:]
	}
	ix.queries[key] = vectors[0]
	ix.queryOrder = append(ix.queryOrder, key)
	ix.mu.Unlock()

	return vectors[0], nil
}
