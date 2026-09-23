package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mcp-foyer/internal/config"
)

// populated returns a configuration with every section set to something
// other than its default, so a round trip cannot pass by accident.
func populated() config.Config {
	c := config.Default()
	c.Listen.Host = "0.0.0.0"
	c.Listen.Port = 9999
	c.Logging.Level = config.LevelDebug
	c.Logging.Format = "json"
	c.Logging.MaxAge = 48 * time.Hour
	c.Logging.MCPWireDebug = true
	c.Security.AllowedNetworks = []string{"10.0.0.0/8", "192.168.0.0/16"}
	c.Security.ConnectionTimeout = 11 * time.Second
	c.Security.IdleConnectionTimeout = 7 * time.Minute
	c.Gateway.DefaultSessionMode = config.SessionModeStateless
	c.Gateway.SessionModeRules.Stateful = []string{"ClientA"}
	c.Gateway.SessionModeRules.Stateless = []string{"ClientB", "ClientC"}
	c.Startup.MaxRetries = 9
	c.MCPServers = map[string]config.MCPServer{
		"files": {
			Transport:   config.TransportStdio,
			Enabled:     true,
			Description: "local filesystem access",
			Timeout:     90 * time.Second,
			Command:     "npx",
			Args:        []string{"-y", "server-filesystem", "/tmp"},
			Env:         map[string]string{"LOG": "debug"},
		},
		"remote": {
			Transport:    config.TransportStreamableHTTP,
			Enabled:      false,
			Timeout:      30 * time.Second,
			URL:          "https://example.com/mcp",
			Headers:      map[string]string{"Authorization": "Bearer secret"},
			ExposedTools: []string{"search", "fetch"},
		},
	}
	return c
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	want := populated()

	if err := config.Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed the configuration\n got: %+v\nwant: %+v", got, want)
	}
}

func TestSaveWritesReadableYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, populated()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	text := string(data)

	// Durations must stay in the form a human types, not nanoseconds.
	if !strings.Contains(text, "48h0m0s") {
		t.Errorf("durations are not written in duration syntax:\n%s", text)
	}
	if strings.Contains(text, "172800000000000") {
		t.Errorf("durations are written as nanoseconds:\n%s", text)
	}
}

func TestSaveUsesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := config.Save(path, config.Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// The file can hold API tokens and secrets from server environments.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600", perm)
	}
}

func TestSaveCreatesMissingParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deeply", "nested", "config.yaml")

	if err := config.Save(path, config.Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file was not created: %v", err)
	}
}

func TestSaveLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	for i := 0; i < 5; i++ {
		if err := config.Save(path, config.Default()); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.yaml" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory contains %v, want only config.yaml", names)
	}
}

// Replacing a large configuration with a small one must not leave the
// tail of the old file behind, which is what an in-place write would do.
func TestSaveFullyReplacesALargerFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	big := config.Default()
	big.MCPServers = map[string]config.MCPServer{}
	for i := 0; i < 50; i++ {
		big.MCPServers[strings.Repeat("s", 20)+string(rune('a'+i%26))+string(rune('0'+i/26))] =
			config.MCPServer{
				Transport: config.TransportStdio,
				Enabled:   true,
				Command:   strings.Repeat("long-command-name", 10),
				Timeout:   time.Minute,
			}
	}
	if err := config.Save(path, big); err != nil {
		t.Fatalf("Save big: %v", err)
	}
	bigSize := fileSize(t, path)

	small := config.Default()
	if err := config.Save(path, small); err != nil {
		t.Fatalf("Save small: %v", err)
	}

	if smallSize := fileSize(t, path); smallSize >= bigSize {
		t.Errorf("file is %d bytes after shrinking, was %d; old content was not removed",
			smallSize, bigSize)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load after shrink: %v", err)
	}
	if len(got.MCPServers) != 0 {
		t.Errorf("mcpServers = %v, want empty", got.MCPServers)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// A reader must never observe a partially written file, no matter how
// many writers are racing.
func TestSaveIsAtomicUnderConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	const writers, readers, rounds = 4, 4, 40

	var writersWG, readersWG sync.WaitGroup
	stop := make(chan struct{})
	errs := make(chan error, writers+readers)

	for w := 0; w < writers; w++ {
		writersWG.Add(1)
		go func(w int) {
			defer writersWG.Done()
			for i := 0; i < rounds; i++ {
				c := config.Default()
				c.Listen.Port = 1024 + w
				// Vary the payload size so a torn write would land
				// mid-document rather than at a stable boundary.
				c.MCPServers = map[string]config.MCPServer{
					"srv": {
						Transport: config.TransportStdio,
						Enabled:   true,
						Command:   strings.Repeat("x", 200*i),
						Timeout:   time.Minute,
					},
				}
				if err := config.Save(path, c); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Every read must land on a complete document.
				if _, err := config.Load(path); err != nil {
					errs <- err
					return
				}
			}
		}()
	}

	writersWG.Wait()
	close(stop)
	readersWG.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent access failed: %v", err)
	}
}

func TestSaveRejectsUnwritableLocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores permission bits")
	}

	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err := config.Save(filepath.Join(locked, "config.yaml"), config.Default())
	if err == nil {
		t.Fatal("Save succeeded in a read-only directory, want an error")
	}
	if !strings.Contains(err.Error(), "config.yaml") {
		t.Errorf("error %q does not name the target file", err)
	}
}
