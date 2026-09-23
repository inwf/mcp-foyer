package logging

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// These tests reach into the unexported rotating writer directly: file
// rotation is driven by byte counts and a clock, neither of which can be
// provoked through the public logger without writing megabytes or
// waiting days.

// fixedClock returns a clock the test advances by hand.
type fixedClock struct{ at time.Time }

func (c *fixedClock) now() time.Time      { return c.at }
func (c *fixedClock) add(d time.Duration) { c.at = c.at.Add(d) }

func logNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestRotatesWhenTheSizeLimitIsPassed(t *testing.T) {
	dir := t.TempDir()
	clock := &fixedClock{at: time.Date(2026, 8, 24, 10, 0, 0, 0, time.Local)}

	w, err := newRotatingFile(rotateOptions{
		Path:      filepath.Join(dir, "mcp-foyer.log"),
		MaxSizeMB: 1,
		Now:       clock.now,
	})
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}
	defer w.Close()

	// Two writes of about 600KB: the second crosses one megabyte.
	chunk := []byte(strings.Repeat("x", 600*1024))
	if _, err := w.Write(chunk); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if got := logNames(t, dir); len(got) != 1 {
		t.Fatalf("after the first write the directory holds %v, want one file", got)
	}

	clock.add(time.Minute)
	if _, err := w.Write(chunk); err != nil {
		t.Fatalf("second write: %v", err)
	}

	names := logNames(t, dir)
	if len(names) != 2 {
		t.Fatalf("after crossing the limit the directory holds %v, want two files", names)
	}
	if !contains(names, "mcp-foyer.log") {
		t.Errorf("the active log file is missing from %v", names)
	}
	if !contains(names, "mcp-foyer-20260824-100100.log") {
		t.Errorf("the archived file is not named after the rotation time: %v", names)
	}
}

// A record must land in one file or the other, never be split across a
// rotation.
func TestARecordIsNeverSplitAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	clock := &fixedClock{at: time.Date(2026, 8, 24, 10, 0, 0, 0, time.Local)}

	w, err := newRotatingFile(rotateOptions{
		Path:      filepath.Join(dir, "mcp-foyer.log"),
		MaxSizeMB: 1,
		Now:       clock.now,
	})
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}
	defer w.Close()

	marker := []byte(strings.Repeat("A", 700*1024) + "END\n")
	if _, err := w.Write([]byte(strings.Repeat("x", 600*1024))); err != nil {
		t.Fatalf("filler write: %v", err)
	}
	clock.add(time.Second)
	if _, err := w.Write(marker); err != nil {
		t.Fatalf("marker write: %v", err)
	}

	active := readWholeFile(t, filepath.Join(dir, "mcp-foyer.log"))
	if !strings.HasSuffix(strings.TrimRight(active, "\n"), "END") {
		t.Error("the record that triggered the rotation was not written whole to the new file")
	}
	if strings.Contains(active, "x") {
		t.Error("the new file contains bytes from before the rotation")
	}
}

func TestRotatingTwiceInTheSameSecondKeepsBothFiles(t *testing.T) {
	dir := t.TempDir()
	clock := &fixedClock{at: time.Date(2026, 8, 24, 10, 0, 0, 0, time.Local)}

	w, err := newRotatingFile(rotateOptions{
		Path:      filepath.Join(dir, "mcp-foyer.log"),
		MaxSizeMB: 1,
		Now:       clock.now,
	})
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}
	defer w.Close()

	chunk := []byte(strings.Repeat("x", 700*1024))
	for i := 0; i < 3; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	// Three writes at 700KB with a 1MB limit means two rotations, both
	// stamped with the same second.
	if names := logNames(t, dir); len(names) != 3 {
		t.Errorf("directory holds %v, want three files; a rotation overwrote another", names)
	}
}

func TestRetentionDeletesExpiredArchives(t *testing.T) {
	dir := t.TempDir()
	clock := &fixedClock{at: time.Date(2026, 8, 24, 10, 0, 0, 0, time.Local)}

	// Files named as though they were rotated at known times.
	writeArchive(t, dir, "mcp-foyer-20260801-120000.log") // 23 days old
	writeArchive(t, dir, "mcp-foyer-20260822-120000.log") // 2 days old
	writeArchive(t, dir, "unrelated.txt")                 // not ours
	writeArchive(t, dir, "mcp-foyer-not-a-timestamp.log") // ours, unparseable

	w, err := newRotatingFile(rotateOptions{
		Path:      filepath.Join(dir, "mcp-foyer.log"),
		MaxSizeMB: 1,
		MaxAge:    7 * 24 * time.Hour,
		Now:       clock.now,
	})
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}
	defer w.Close()

	names := logNames(t, dir)
	if contains(names, "mcp-foyer-20260801-120000.log") {
		t.Errorf("an archive older than the retention window survived: %v", names)
	}
	if !contains(names, "mcp-foyer-20260822-120000.log") {
		t.Errorf("an archive inside the retention window was deleted: %v", names)
	}
	if !contains(names, "unrelated.txt") {
		t.Errorf("a file that is not ours was deleted: %v", names)
	}
	if !contains(names, "mcp-foyer-not-a-timestamp.log") {
		t.Errorf("a file with an unreadable timestamp was deleted: %v", names)
	}
}

// Files pile up while the process is not running, so retention cannot
// wait for the next rotation to be applied.
func TestRetentionRunsAtStartup(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "mcp-foyer-20260101-120000.log")

	clock := &fixedClock{at: time.Date(2026, 8, 24, 10, 0, 0, 0, time.Local)}
	w, err := newRotatingFile(rotateOptions{
		Path:      filepath.Join(dir, "mcp-foyer.log"),
		MaxSizeMB: 1,
		MaxAge:    7 * 24 * time.Hour,
		Now:       clock.now,
	})
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}
	defer w.Close()

	if contains(logNames(t, dir), "mcp-foyer-20260101-120000.log") {
		t.Error("an expired archive survived startup")
	}
}

func TestZeroRetentionKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "mcp-foyer-20200101-120000.log")

	clock := &fixedClock{at: time.Date(2026, 8, 24, 10, 0, 0, 0, time.Local)}
	w, err := newRotatingFile(rotateOptions{
		Path:      filepath.Join(dir, "mcp-foyer.log"),
		MaxSizeMB: 1,
		MaxAge:    0,
		Now:       clock.now,
	})
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}
	defer w.Close()

	if !contains(logNames(t, dir), "mcp-foyer-20200101-120000.log") {
		t.Error("a very old archive was deleted even though retention is disabled")
	}
}

// Restarting appends rather than truncating, and the existing length has
// to count toward the size limit or the file grows without bound.
func TestReopeningAppendsAndCountsExistingBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-foyer.log")
	clock := &fixedClock{at: time.Date(2026, 8, 24, 10, 0, 0, 0, time.Local)}

	first, err := newRotatingFile(rotateOptions{Path: path, MaxSizeMB: 1, Now: clock.now})
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}
	if _, err := first.Write([]byte(strings.Repeat("x", 900*1024))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := newRotatingFile(rotateOptions{Path: path, MaxSizeMB: 1, Now: clock.now})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	if !strings.Contains(readWholeFile(t, path), "x") {
		t.Error("reopening truncated the existing log")
	}

	clock.add(time.Second)
	if _, err := second.Write([]byte(strings.Repeat("y", 200*1024))); err != nil {
		t.Fatalf("write after reopen: %v", err)
	}
	if names := logNames(t, dir); len(names) != 2 {
		t.Errorf("directory holds %v, want two files; the existing length was not counted", names)
	}
}

func TestCloseIsRepeatable(t *testing.T) {
	w, err := newRotatingFile(rotateOptions{
		Path:      filepath.Join(t.TempDir(), "mcp-foyer.log"),
		MaxSizeMB: 1,
	})
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := w.Close(); err != nil {
			t.Errorf("Close call %d: %v", i+1, err)
		}
	}
}

func writeArchive(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("old\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readWholeFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
