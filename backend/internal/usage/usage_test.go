package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A fake clock and timer, so that the tests neither sleep nor race the
// real debounce.
type fakeTimer struct {
	fn      func()
	stopped bool
}

func (t *fakeTimer) Stop() bool { t.stopped = true; return true }

type clock struct {
	now    time.Time
	timers []*fakeTimer
}

func (c *clock) Now() time.Time { return c.now }
func (c *clock) After(_ time.Duration, fn func()) stopper {
	t := &fakeTimer{fn: fn}
	c.timers = append(c.timers, t)
	return t
}

// fire runs every timer that is due and has not been stopped.
func (c *clock) fire() {
	for _, t := range c.timers {
		if !t.stopped {
			t.stopped = true
			t.fn()
		}
	}
}

func openIn(t *testing.T, dir string, clk *clock) *Counter {
	t.Helper()
	c, err := Open(Options{
		Path:    filepath.Join(dir, "usage.json"),
		Now:     clk.Now,
		After:   clk.After,
		OnError: func(err error) { t.Errorf("background write failed: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCountsAccumulatePerTool(t *testing.T) {
	c := New()

	c.Searched("files", "read")
	c.Searched("files", "read")
	c.Called("files", "read", false)
	c.Called("files", "read", true)
	c.Called("git", "status", false)

	got := c.Snapshot()
	if len(got.Entries) != 2 {
		t.Fatalf("snapshot has %d entries, want 2: %+v", len(got.Entries), got.Entries)
	}
	read := got.Entries[0]
	if read.Server != "files" || read.Tool != "read" {
		t.Errorf("first entry is %s/%s, want files/read (sorted)", read.Server, read.Tool)
	}
	if read.Searched != 2 || read.Called != 2 || read.Failed != 1 {
		t.Errorf("files/read = searched %d, called %d, failed %d; want 2, 2, 1",
			read.Searched, read.Called, read.Failed)
	}
	if read.LastCalled.IsZero() {
		t.Error("a called tool has no lastCalled")
	}
	if got.Entries[1].LastCalled.IsZero() {
		t.Error("git/status was called and has no lastCalled")
	}
}

// A tool nobody has searched for still has an entry once it is called,
// and one that was only searched for has one too: both halves are what an
// operator reads — "found but never used" is as telling as "used a lot".
func TestAnUnnamedToolIsIgnored(t *testing.T) {
	c := New()
	c.Called("", "read", false)
	c.Searched("files", "")
	if got := c.Snapshot().Entries; len(got) != 0 {
		t.Errorf("entries with a blank server or tool were recorded: %+v", got)
	}
}

func TestCountsSurviveReopening(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{now: time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)}

	first := openIn(t, dir, clk)
	first.Searched("files", "read")
	first.Called("files", "read", false)
	if err := first.Flush(); err != nil {
		t.Fatal(err)
	}

	clk.now = clk.now.Add(time.Hour)
	second := openIn(t, dir, clk)
	got := second.Snapshot()
	if len(got.Entries) != 1 || got.Entries[0].Searched != 1 || got.Entries[0].Called != 1 {
		t.Errorf("reopened counts = %+v, want the one entry with searched 1 and called 1", got.Entries)
	}
	// Since is when counting began, not when the file was last opened.
	if !got.Since.Equal(time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("since = %v, want the original start", got.Since)
	}
}

// A burst of changes is one write. Twenty tool calls in a row are the
// normal shape of a model's work, and twenty rewrites of the file would
// be the normal cost of counting them.
func TestChangesAreWrittenOnceAfterTheDelay(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{now: time.Now()}
	c := openIn(t, dir, clk)
	path := filepath.Join(dir, "usage.json")

	for range 20 {
		c.Called("files", "read", false)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the file was written before the delay elapsed")
	}
	if len(clk.timers) != 1 {
		t.Fatalf("%d writes were scheduled for one burst, want 1", len(clk.timers))
	}

	clk.fire()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file was not written when the delay elapsed: %v", err)
	}

	// The next change schedules the next write; nothing is lost between.
	c.Called("files", "read", false)
	if len(clk.timers) != 2 {
		t.Errorf("%d timers after a change following a write, want 2", len(clk.timers))
	}
}

// The counts are a convenience. A damaged file must not stop the gateway
// from starting, and must not be preserved either: the next write
// replaces it.
func TestADamagedFileStartsFromZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Open(Options{Path: path})
	if err == nil {
		t.Error("a damaged file was opened without a word")
	}
	if c == nil {
		t.Fatal("a damaged file left no usable counter")
	}
	if got := c.Snapshot().Entries; len(got) != 0 {
		t.Errorf("a damaged file yielded entries: %+v", got)
	}
	c.Called("files", "read", false)
	if err := c.Flush(); err != nil {
		t.Fatalf("writing over the damaged file failed: %v", err)
	}
	if _, err := Open(Options{Path: path}); err != nil {
		t.Errorf("the rewritten file does not read back: %v", err)
	}
}

func TestResetForgetsEverything(t *testing.T) {
	clk := &clock{now: time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)}
	c := openIn(t, t.TempDir(), clk)
	c.Called("files", "read", false)

	clk.now = clk.now.Add(time.Hour)
	c.Reset()

	got := c.Snapshot()
	if len(got.Entries) != 0 {
		t.Errorf("entries after reset: %+v", got.Entries)
	}
	if !got.Since.Equal(clk.now) {
		t.Errorf("since after reset = %v, want the reset time %v", got.Since, clk.now)
	}
}

func TestFlushWithNothingChangedWritesNothing(t *testing.T) {
	dir := t.TempDir()
	c := openIn(t, dir, &clock{now: time.Now()})
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "usage.json")); err == nil {
		t.Error("an unchanged counter wrote a file")
	}
}
