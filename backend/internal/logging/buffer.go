package logging

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"mcp-foyer/internal/config"
)

// DefaultCapacity is how many records each buffer retains. Log history
// is a diagnostic convenience, not storage: the file on disk is the
// record of what happened.
const DefaultCapacity = 1000

// Entry is one retained log record, flattened into the shape the
// management API returns.
type Entry struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Module  string
	Server  string
	Attrs   map[string]string
}

// Query selects entries from a [Store].
type Query struct {
	// Server limits results to one upstream server. Empty means every
	// record, including those not tied to a server.
	Server string

	// Module limits results to one subsystem. Empty means all.
	Module string

	// MinLevel drops records below this severity. Empty means every
	// level.
	//
	// This is a level *name* rather than a [slog.Level] because
	// slog.LevelInfo is zero: a slog.Level field could not tell "no
	// filter" apart from "info and above", and would silently hide debug
	// records from a caller that passed an empty query.
	MinLevel config.LogLevel

	// Since drops records at or before this time. The zero value means
	// no lower bound.
	Since time.Time

	// Limit caps the number of results, keeping the most recent. Zero
	// means no cap.
	Limit int
}

// threshold resolves MinLevel, reporting whether a filter applies at all.
func (q Query) threshold() (slog.Level, bool) {
	if q.MinLevel == "" {
		return 0, false
	}
	level, err := ParseLevel(q.MinLevel)
	if err != nil {
		// An unrecognised name filters nothing rather than everything:
		// a bad query parameter should not look like an empty log.
		return 0, false
	}
	return level, true
}

// buffer is a fixed-capacity ring of entries. Once full, each write
// overwrites the oldest.
type buffer struct {
	entries []Entry
	next    int
	full    bool
}

func newBuffer(capacity int) *buffer {
	return &buffer{entries: make([]Entry, capacity)}
}

func (b *buffer) append(e Entry) {
	b.entries[b.next] = e
	b.next = (b.next + 1) % len(b.entries)
	if b.next == 0 {
		b.full = true
	}
}

// all returns the retained entries, oldest first.
func (b *buffer) all() []Entry {
	if !b.full {
		return slices.Clone(b.entries[:b.next])
	}
	out := make([]Entry, 0, len(b.entries))
	out = append(out, b.entries[b.next:]...)
	out = append(out, b.entries[:b.next]...)
	return out
}

// Store retains recent log records in memory so the management API can
// serve them without reading files.
//
// Each upstream server gets its own ring in addition to the shared one.
// Without that, one chatty server would evict every other server's
// history, which is exactly when the history is needed.
type Store struct {
	capacity int

	mu        sync.RWMutex
	global    *buffer
	perServer map[string]*buffer
}

// NewStore returns a store retaining capacity records globally and
// capacity records for each server. A capacity of zero or less uses
// [DefaultCapacity].
func NewStore(capacity int) *Store {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Store{
		capacity:  capacity,
		global:    newBuffer(capacity),
		perServer: map[string]*buffer{},
	}
}

// Append retains one entry.
func (s *Store) Append(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.global.append(e)

	if e.Server == "" {
		return
	}
	b, ok := s.perServer[e.Server]
	if !ok {
		b = newBuffer(s.capacity)
		s.perServer[e.Server] = b
	}
	b.append(e)
}

// Query returns the matching entries, most recent last.
func (s *Store) Query(q Query) []Entry {
	s.mu.RLock()
	source := s.global
	if q.Server != "" {
		b, ok := s.perServer[q.Server]
		if !ok {
			s.mu.RUnlock()
			return nil
		}
		source = b
	}
	entries := source.all()
	s.mu.RUnlock()

	minLevel, hasMinLevel := q.threshold()

	filtered := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if hasMinLevel && e.Level < minLevel {
			continue
		}
		if q.Module != "" && e.Module != q.Module {
			continue
		}
		if !q.Since.IsZero() && !e.Time.After(q.Since) {
			continue
		}
		filtered = append(filtered, e)
	}

	// Records arrive in time order, but a caller may have set a clock
	// back; sorting keeps the contract that results are chronological.
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].Time.Before(filtered[j].Time)
	})

	// Keep the newest when capping, since that is what a log view shows.
	if q.Limit > 0 && len(filtered) > q.Limit {
		filtered = filtered[len(filtered)-q.Limit:]
	}
	return filtered
}

// Clear discards the retained records for one server, or every record
// when server is empty.
//
// A server's records are dropped from the shared ring as well as from
// its own. Leaving them in the shared ring would make clearing one
// server's log look like it had done nothing as soon as the view was
// switched back to all servers.
func (s *Store) Clear(server string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if server == "" {
		s.global = newBuffer(s.capacity)
		s.perServer = map[string]*buffer{}
		return
	}

	delete(s.perServer, server)

	// Rebuilding preserves the order and the remaining history, which
	// filtering in place could not.
	kept := newBuffer(s.capacity)
	for _, entry := range s.global.all() {
		if entry.Server != server {
			kept.append(entry)
		}
	}
	s.global = kept
}

// Servers lists the servers that have retained records.
func (s *Store) Servers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.perServer))
	for name := range s.perServer {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// storeHandler is the slog handler that feeds a Store.
type storeHandler struct {
	store *Store
	level slog.Level
	now   func() time.Time
	attrs []slog.Attr
}

func newStoreHandler(store *Store, level slog.Level, now func() time.Time) slog.Handler {
	if now == nil {
		now = time.Now
	}
	return &storeHandler{store: store, level: level, now: now}
}

func (h *storeHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *storeHandler) Handle(_ context.Context, record slog.Record) error {
	entry := Entry{
		Time:    record.Time,
		Level:   record.Level,
		Message: record.Message,
		Attrs:   map[string]string{},
	}
	if entry.Time.IsZero() {
		entry.Time = h.now()
	}

	collect := func(a slog.Attr) {
		switch a.Key {
		case AttrModule:
			entry.Module = a.Value.String()
		case AttrServer:
			entry.Server = a.Value.String()
		default:
			entry.Attrs[a.Key] = a.Value.String()
		}
	}

	// Attributes bound with With come first, then the ones on the call.
	for _, a := range h.attrs {
		collect(a)
	}
	record.Attrs(func(a slog.Attr) bool {
		collect(a)
		return true
	})

	h.store.Append(entry)
	return nil
}

func (h *storeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := *h
	out.attrs = append(slices.Clip(h.attrs), attrs...)
	return &out
}

// WithGroup is a no-op: the store holds a flat set of attributes, and
// grouping only affects how text output is laid out.
func (h *storeHandler) WithGroup(string) slog.Handler { return h }
