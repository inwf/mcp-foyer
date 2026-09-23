package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/config"
)

// The file is the interface someone edits by hand, so the running
// instance has to be able to catch up with it. These are about that
// catching up: what counts as a change, what must never be adopted, and
// what has to survive an edit that does not parse.

// watchInterval is short enough that a test does not wait on it and long
// enough that a loaded machine still ticks several times inside the
// deadline below.
const watchInterval = 5 * time.Millisecond

// managerAt builds a manager over a file that already holds cfg.
func managerAt(t *testing.T, cfg config.Config) (*config.Manager, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	m, err := config.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, path
}

func withPort(port int) config.Config {
	cfg := config.Default()
	cfg.Listen.Port = port
	return cfg
}

func TestReloadAdoptsWhatIsOnDisk(t *testing.T) {
	m, path := managerAt(t, withPort(7001))

	if err := config.Save(path, withPort(7002)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	changes, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("Reload reported no changes after the port changed")
	}
	if got := m.Get().Listen.Port; got != 7002 {
		t.Errorf("port = %d, want the value from the file", got)
	}
}

// Reload runs on a timer, and the manager's own writes reach the same
// file. A reload that announced every read would reconnect every server
// every couple of seconds.
func TestReloadOfAnUnchangedFileIsNotAChange(t *testing.T) {
	m, path := managerAt(t, withPort(7001))

	// Written again, byte for byte: a new modification time, same content.
	if err := config.Save(path, withPort(7001)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	changes, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("Reload reported %v for a file that did not change", changes)
	}
}

// Half an edit is not a configuration. Adopting one would take the
// gateway down over a syntax error someone was about to fix.
func TestReloadKeepsTheRunningConfigurationWhenTheFileIsBroken(t *testing.T) {
	m, path := managerAt(t, withPort(7001))

	if err := os.WriteFile(path, []byte("listen:\n  port: not-a-number\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := m.Reload(); err == nil {
		t.Fatal("Reload accepted a file that does not parse")
	}
	if got := m.Get().Listen.Port; got != 7001 {
		t.Errorf("port = %d, want the last good value", got)
	}
}

// The same, one level up: it parses but says something the program will
// not run with.
func TestReloadKeepsTheRunningConfigurationWhenTheFileIsInvalid(t *testing.T) {
	m, path := managerAt(t, withPort(7001))

	if err := os.WriteFile(path, []byte("version: 1\nlisten:\n  port: 99999\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := m.Reload()
	if err == nil {
		t.Fatal("Reload accepted an invalid port")
	}
	if !strings.Contains(err.Error(), "listen.port") {
		t.Errorf("error %q does not name the offending field", err)
	}
	if got := m.Get().Listen.Port; got != 7001 {
		t.Errorf("port = %d, want the last good value", got)
	}
}

// Moving the file aside is not an instruction to forget every server.
func TestReloadOfAMissingFileChangesNothing(t *testing.T) {
	m, path := managerAt(t, withPort(7001))

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	changes, err := m.Reload()
	if err != nil {
		t.Errorf("Reload: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("Reload reported %v for a file that is not there", changes)
	}
	if got := m.Get().Listen.Port; got != 7001 {
		t.Errorf("port = %d, want the configuration to have survived", got)
	}
}

// Subscribers are how the rest of the program hears about a change, and
// a reload has to reach them exactly as an update does.
func TestReloadNotifiesSubscribers(t *testing.T) {
	m, path := managerAt(t, withPort(7001))

	updates, cancel := m.Subscribe()
	defer cancel()

	if err := config.Save(path, withPort(7002)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	select {
	case cfg := <-updates:
		if cfg.Listen.Port != 7002 {
			t.Errorf("subscriber saw port %d, want 7002", cfg.Listen.Port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no subscriber was notified of the reload")
	}
}

// Waking every subscriber is what a notification is for, so a reload
// that found nothing must not send one: on a timer, that would be a
// wake-up every couple of seconds for the life of the process.
func TestReloadOfAnUnchangedFileDoesNotNotify(t *testing.T) {
	m, path := managerAt(t, withPort(7001))

	updates, cancel := m.Subscribe()
	defer cancel()

	// The same content again: a new modification time, nothing to adopt.
	if err := config.Save(path, withPort(7001)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	select {
	case <-updates:
		t.Error("a subscriber was woken for a file that did not change")
	case <-time.After(100 * time.Millisecond):
	}
}

// ===== watching =====
// This is the case the watching exists for, and the one an implementation
// following the file's identity rather than its path would miss: every
// save in this program writes a temporary file and renames it over the
// target, so the file that was there at startup is not the file that is
// there afterwards.
func TestWatchNoticesAnAtomicSave(t *testing.T) {
	m, path := managerAt(t, withPort(7001))

	changed := make(chan []config.Change, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.Watch(ctx, config.WatchOptions{
		Interval: watchInterval,
		OnChange: func(changes []config.Change) { changed <- changes },
	})

	if err := config.Save(path, withPort(7002)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	select {
	case changes := <-changed:
		if len(changes) == 0 {
			t.Error("the change was reported as empty")
		}
		if got := m.Get().Listen.Port; got != 7002 {
			t.Errorf("port = %d, want the file's value", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher never noticed the file being replaced")
	}
}

// An edit that does not load has to be reported: nothing is broken,
// because the last good configuration is still running, but what the
// person just typed is not in effect and only they can fix it.
func TestWatchReportsAFileItCannotLoad(t *testing.T) {
	m, path := managerAt(t, withPort(7001))

	failures := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.Watch(ctx, config.WatchOptions{
		Interval: watchInterval,
		OnChange: func([]config.Change) { t.Error("a broken file was adopted") },
		OnError:  func(err error) { failures <- err },
	})

	if err := os.WriteFile(path, []byte("listen:\n  port: not-a-number\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case err := <-failures:
		if err == nil {
			t.Error("the failure was reported as nil")
		}
		if got := m.Get().Listen.Port; got != 7001 {
			t.Errorf("port = %d, want the last good value", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher never reported the file it could not load")
	}
}

// Watching must not turn the manager's own writes into a storm of
// changes: every Update writes the file the watcher is looking at.
func TestWatchIsSilentAboutTheManagersOwnWrites(t *testing.T) {
	m, _ := managerAt(t, withPort(7001))

	var reported int
	changed := make(chan []config.Change, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.Watch(ctx, config.WatchOptions{
		Interval: watchInterval,
		OnChange: func(changes []config.Change) { changed <- changes },
	})

	if _, err := m.Update(func(cfg *config.Config) error {
		cfg.Listen.Port = 7002
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Long enough for many ticks to have run over the file Update wrote.
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case <-changed:
			reported++
		case <-deadline:
			if reported != 0 {
				t.Errorf("the watcher reported %d changes for a write the manager made itself", reported)
			}
			return
		}
	}
}

func TestWatchStopsWithItsContext(t *testing.T) {
	m, path := managerAt(t, withPort(7001))

	ctx, cancel := context.WithCancel(context.Background())
	m.Watch(ctx, config.WatchOptions{
		Interval: watchInterval,
		OnChange: func([]config.Change) { t.Error("the watcher ran after its context ended") },
	})
	cancel()

	// Give the goroutine time to see the cancellation, then change the file
	// it would have been watching.
	time.Sleep(50 * time.Millisecond)
	if err := config.Save(path, withPort(7002)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if got := m.Get().Listen.Port; got != 7001 {
		t.Errorf("port = %d, want the watcher to have stopped", got)
	}
}
