package config_test

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcp-foyer/internal/config"
)

func newManager(t *testing.T) (*config.Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	m, err := config.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, path
}

func TestNewManagerWithoutAFileUsesDefaults(t *testing.T) {
	m, path := newManager(t)

	if got := m.Get().Listen.Port; got != config.Default().Listen.Port {
		t.Errorf("port = %d, want the default", got)
	}
	// Nothing should be written until an update happens.
	if _, err := statDir(path); err == nil {
		t.Error("a configuration file was created before any update")
	}
}

func TestNewManagerRejectsAnInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, func() config.Config {
		c := config.Default()
		c.Listen.Port = 99999
		return c
	}()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := config.NewManager(path)
	if err == nil {
		t.Fatal("NewManager accepted a configuration with an invalid port")
	}
	if !strings.Contains(err.Error(), "listen.port") {
		t.Errorf("error %q does not name the offending field", err)
	}
}

// A caller must not be able to change the manager's state by holding on
// to what Get returned.
func TestGetReturnsAnIndependentCopy(t *testing.T) {
	m, _ := newManager(t)

	got := m.Get()
	got.Listen.Port = 1
	got.Security.AllowedNetworks[0] = "0.0.0.0/0"
	got.MCPServers["sneaky"] = config.MCPServer{Transport: config.TransportStdio}

	current := m.Get()
	if current.Listen.Port == 1 {
		t.Error("port was changed through a value returned by Get")
	}
	if current.Security.AllowedNetworks[0] == "0.0.0.0/0" {
		t.Error("allowedNetworks was changed through a value returned by Get")
	}
	if _, leaked := current.MCPServers["sneaky"]; leaked {
		t.Error("a server was added through a value returned by Get")
	}
}

func TestUpdatePersistsAndReplaces(t *testing.T) {
	m, path := newManager(t)

	changes, err := m.Update(func(c *config.Config) error {
		c.Listen.Port = 9001
		c.MCPServers["files"] = config.MCPServer{
			Transport: config.TransportStdio,
			Command:   "npx",
			Enabled:   true,
			Timeout:   time.Minute,
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if got := m.Get().Listen.Port; got != 9001 {
		t.Errorf("in-memory port = %d, want 9001", got)
	}
	onDisk, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if onDisk.Listen.Port != 9001 {
		t.Errorf("persisted port = %d, want 9001", onDisk.Listen.Port)
	}
	if len(changes) == 0 {
		t.Error("Update reported no changes")
	}
}

// If the new configuration cannot be persisted or is invalid, the
// running configuration must be left exactly as it was.
func TestUpdateRollsBackOnFailure(t *testing.T) {
	t.Run("invalid configuration", func(t *testing.T) {
		m, _ := newManager(t)
		before := m.Get()

		_, err := m.Update(func(c *config.Config) error {
			c.Listen.Port = 70000
			return nil
		})
		if err == nil {
			t.Fatal("Update accepted an invalid port")
		}
		if m.Get().Listen.Port != before.Listen.Port {
			t.Errorf("port = %d after a rejected update, want %d",
				m.Get().Listen.Port, before.Listen.Port)
		}
	})

	t.Run("caller aborts", func(t *testing.T) {
		m, _ := newManager(t)
		before := m.Get()
		sentinel := errors.New("changed my mind")

		_, err := m.Update(func(c *config.Config) error {
			c.Listen.Port = 9002
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want the caller's error", err)
		}
		if m.Get().Listen.Port != before.Listen.Port {
			t.Errorf("port = %d after an aborted update, want %d",
				m.Get().Listen.Port, before.Listen.Port)
		}
	})
}

func TestSubscribeReceivesUpdates(t *testing.T) {
	m, _ := newManager(t)
	updates, cancel := m.Subscribe()
	defer cancel()

	if _, err := m.Update(func(c *config.Config) error {
		c.Listen.Port = 9003
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	select {
	case got := <-updates:
		if got.Listen.Port != 9003 {
			t.Errorf("received port %d, want 9003", got.Listen.Port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no configuration was delivered")
	}
}

// A subscriber that stops reading must not be able to stall updates, and
// when it looks again it should see the newest configuration rather than
// a queue of stale ones.
func TestSlowSubscriberGetsTheLatestAndBlocksNothing(t *testing.T) {
	m, _ := newManager(t)
	updates, cancel := m.Subscribe()
	defer cancel()

	for port := 9010; port < 9020; port++ {
		if _, err := m.Update(func(c *config.Config) error {
			c.Listen.Port = port
			return nil
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}

	select {
	case got := <-updates:
		if got.Listen.Port != 9019 {
			t.Errorf("received port %d, want the most recent, 9019", got.Listen.Port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no configuration was delivered")
	}
}

func TestCancelStopsDelivery(t *testing.T) {
	m, _ := newManager(t)
	updates, cancel := m.Subscribe()

	cancel()
	// Cancelling twice must not panic on a closed channel.
	cancel()

	if _, ok := <-updates; ok {
		t.Error("the channel delivered a value after cancellation")
	}

	if _, err := m.Update(func(c *config.Config) error {
		c.Listen.Port = 9004
		return nil
	}); err != nil {
		t.Fatalf("Update after cancel: %v", err)
	}
}

func TestManagerUnderConcurrentUse(t *testing.T) {
	m, _ := newManager(t)

	var wg sync.WaitGroup
	errs := make(chan error, 32)

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 15; n++ {
				if _, err := m.Update(func(c *config.Config) error {
					c.Listen.Port = 9100 + i
					return nil
				}); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				cfg := m.Get()
				// Mutating a snapshot must never affect another reader.
				cfg.MCPServers["scratch"] = config.MCPServer{Transport: config.TransportStdio}
			}
		}()
	}

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			updates, cancel := m.Subscribe()
			defer cancel()
			for n := 0; n < 5; n++ {
				select {
				case <-updates:
				case <-time.After(500 * time.Millisecond):
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent use failed: %v", err)
	}
}
