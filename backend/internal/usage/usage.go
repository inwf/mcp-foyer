// Package usage counts how the tools behind the gateway are used by the
// models connected to it, so that an operator can see which tools are
// worth exposing and which descriptions never get found.
//
// The counts are about the model's use only. What a person clicks in the
// web interface is not recorded: the question the counts answer is "what
// does a model reach for", and a person testing a tool is not that.
package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Entry is what is known about one tool's use.
type Entry struct {
	Server string `json:"server"`
	Tool   string `json:"tool"`

	// Searched is how many times search_tools returned the tool to a
	// model — on the page it actually saw, not among everything that
	// matched.
	Searched int `json:"searched"`

	// Called is how many times a model called the tool, by either route:
	// through call_tool, or directly under its published name.
	Called int `json:"called"`

	// Failed is how many of those calls the tool reported as failed, or
	// could not be forwarded at all.
	Failed int `json:"failed"`

	LastCalled time.Time `json:"lastCalled,omitzero"`
}

// Snapshot is the counts at one moment.
type Snapshot struct {
	// Since is when counting started, or was last reset.
	Since   time.Time `json:"since"`
	Entries []Entry   `json:"entries"`
}

// Counter records tool use. It is safe for concurrent use.
//
// A Counter with no file keeps its counts in memory only. One opened with
// [Open] writes them back, coalescing bursts: a model that calls twenty
// tools in a row causes one write, not twenty.
type Counter struct {
	mu      sync.Mutex
	since   time.Time
	entries map[key]*Entry

	path    string
	delay   time.Duration
	now     func() time.Time
	after   func(time.Duration, func()) stopper
	pending stopper
	dirty   bool
	onError func(error)
}

type key struct{ server, tool string }

type stopper interface{ Stop() bool }

// Options configures a Counter.
type Options struct {
	// Path is the file the counts are kept in. Empty keeps them in memory.
	Path string

	// Delay is how long after a change the file is written, so that a
	// burst of changes costs one write. Zero means the default.
	Delay time.Duration

	// OnError receives a failure to write the file. Counts are not worth
	// failing a tool call over, so the error goes here rather than to the
	// caller that changed them. Nil discards it.
	OnError func(error)

	// Now and After override the clock, for tests.
	Now   func() time.Time
	After func(time.Duration, func()) stopper
}

// DefaultDelay is how long a Counter waits after a change before writing.
const DefaultDelay = 5 * time.Second

// New makes an in-memory Counter.
func New() *Counter {
	c, _ := Open(Options{})
	return c
}

// Open makes a Counter backed by the file at opts.Path, loading whatever
// counts the file holds.
//
// A file that is missing, empty, or unreadable starts the counts from
// zero: the counts are a convenience, and a damaged one must not stop the
// gateway from starting. The damage is reported through the returned
// error so that it can be logged, and the Counter is usable regardless.
func Open(opts Options) (*Counter, error) {
	c := &Counter{
		entries: map[key]*Entry{},
		path:    opts.Path,
		delay:   opts.Delay,
		now:     opts.Now,
		after:   opts.After,
		onError: opts.OnError,
	}
	if c.delay <= 0 {
		c.delay = DefaultDelay
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.after == nil {
		c.after = func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }
	}
	if c.onError == nil {
		c.onError = func(error) {}
	}
	c.since = c.now()

	if c.path == "" {
		return c, nil
	}
	data, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read usage counts %s: %w", c.path, err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return c, fmt.Errorf("decode usage counts %s: %w", c.path, err)
	}
	if !snapshot.Since.IsZero() {
		c.since = snapshot.Since
	}
	for _, entry := range snapshot.Entries {
		if entry.Server == "" || entry.Tool == "" {
			continue
		}
		copied := entry
		c.entries[key{entry.Server, entry.Tool}] = &copied
	}
	return c, nil
}

// Searched records that search_tools returned the tool to a model.
func (c *Counter) Searched(server, tool string) {
	c.change(func(e *Entry) { e.Searched++ })(server, tool)
}

// Called records a call, and whether it failed.
func (c *Counter) Called(server, tool string, failed bool) {
	c.change(func(e *Entry) {
		e.Called++
		if failed {
			e.Failed++
		}
		e.LastCalled = c.now()
	})(server, tool)
}

func (c *Counter) change(apply func(*Entry)) func(server, tool string) {
	return func(server, tool string) {
		if server == "" || tool == "" {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		k := key{server, tool}
		entry := c.entries[k]
		if entry == nil {
			entry = &Entry{Server: server, Tool: tool}
			c.entries[k] = entry
		}
		apply(entry)
		c.scheduleLocked()
	}
}

// Snapshot returns the counts, ordered by server then tool.
func (c *Counter) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *Counter) snapshotLocked() Snapshot {
	out := Snapshot{Since: c.since, Entries: make([]Entry, 0, len(c.entries))}
	for _, k := range slices.SortedFunc(maps.Keys(c.entries), func(a, b key) int {
		if a.server != b.server {
			if a.server < b.server {
				return -1
			}
			return 1
		}
		if a.tool < b.tool {
			return -1
		}
		if a.tool > b.tool {
			return 1
		}
		return 0
	}) {
		out.Entries = append(out.Entries, *c.entries[k])
	}
	return out
}

// Reset forgets every count and starts again from now.
func (c *Counter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[key]*Entry{}
	c.since = c.now()
	c.scheduleLocked()
}

// scheduleLocked arranges a write after the delay, unless one is already
// arranged: the write picks up whatever has changed by then.
func (c *Counter) scheduleLocked() {
	c.dirty = true
	if c.path == "" || c.pending != nil {
		return
	}
	c.pending = c.after(c.delay, func() {
		if err := c.Flush(); err != nil {
			c.onError(err)
		}
	})
}

// Flush writes the counts now if anything changed since the last write.
// Call it on shutdown, when the delay has not elapsed.
func (c *Counter) Flush() error {
	c.mu.Lock()
	if c.pending != nil {
		c.pending.Stop()
		c.pending = nil
	}
	if c.path == "" || !c.dirty {
		c.mu.Unlock()
		return nil
	}
	c.dirty = false
	snapshot := c.snapshotLocked()
	c.mu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode usage counts: %w", err)
	}
	if err := writeFileAtomic(c.path, data); err != nil {
		// Still dirty: the next change, or the next Flush, tries again.
		c.mu.Lock()
		c.dirty = true
		c.mu.Unlock()
		return fmt.Errorf("write usage counts %s: %w", c.path, err)
	}
	return nil
}

const (
	fileMode os.FileMode = 0o600
	dirMode  os.FileMode = 0o700
)

// writeFileAtomic writes data to path by way of a temporary file in the
// same directory that is renamed into place, so that a crash mid-write
// leaves the old file rather than a truncated one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()
	if err := tmp.Chmod(fileMode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
