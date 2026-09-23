package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"mcp-foyer/internal/config"
)

func TestResolveDataDirPrecedence(t *testing.T) {
	explicit := t.TempDir()
	fromEnv := t.TempDir()

	t.Run("explicit value wins over the environment", func(t *testing.T) {
		t.Setenv(config.DataDirEnv, fromEnv)

		p, err := config.ResolveDataDir(explicit)
		if err != nil {
			t.Fatalf("ResolveDataDir: %v", err)
		}
		if p.Root() != explicit {
			t.Errorf("root = %q, want %q", p.Root(), explicit)
		}
	})

	t.Run("environment wins over the default", func(t *testing.T) {
		t.Setenv(config.DataDirEnv, fromEnv)

		p, err := config.ResolveDataDir("")
		if err != nil {
			t.Fatalf("ResolveDataDir: %v", err)
		}
		if p.Root() != fromEnv {
			t.Errorf("root = %q, want %q", p.Root(), fromEnv)
		}
	})

	t.Run("default is inside the working directory", func(t *testing.T) {
		t.Setenv(config.DataDirEnv, "")
		wd := t.TempDir()
		t.Chdir(wd)

		p, err := config.ResolveDataDir("")
		if err != nil {
			t.Fatalf("ResolveDataDir: %v", err)
		}
		if got, want := p.Root(), filepath.Join(wd, "data"); got != want {
			t.Errorf("root = %q, want %q", got, want)
		}
	})
}

// The working directory can change after startup; the data directory
// must not follow it.
func TestResolveDataDirIsAbsolute(t *testing.T) {
	t.Setenv(config.DataDirEnv, "")
	t.Chdir(t.TempDir())

	p, err := config.ResolveDataDir("relative/path")
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}
	if !filepath.IsAbs(p.Root()) {
		t.Errorf("root = %q, want an absolute path", p.Root())
	}
}

// Nothing mcp-foyer writes may land outside the data directory. This is the
// whole point of having one: deleting it removes every trace.
func TestEveryPathStaysInsideTheDataDirectory(t *testing.T) {
	root := t.TempDir()
	p, err := config.ResolveDataDir(root)
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}

	for name, path := range p.All() {
		if name == "root" {
			continue
		}
		rel, err := filepath.Rel(p.Root(), path)
		if err != nil {
			t.Errorf("%s (%q) is not relative to the data directory: %v", name, path, err)
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("%s resolves to %q, which escapes the data directory %q", name, path, p.Root())
		}
	}
}

func TestEnsureCreatesDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "data")
	p, err := config.ResolveDataDir(root)
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}

	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, dir := range []string{p.Root(), p.LogDir()} {
		if info, err := statDir(dir); err != nil {
			t.Errorf("%s: %v", dir, err)
		} else if !info.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
}

func TestEnsureIsRepeatable(t *testing.T) {
	p, err := config.ResolveDataDir(t.TempDir())
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := p.Ensure(); err != nil {
			t.Fatalf("Ensure call %d: %v", i+1, err)
		}
	}
}
