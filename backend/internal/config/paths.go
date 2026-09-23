package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// DataDirEnv names the environment variable that overrides the data
// directory.
const DataDirEnv = "MCP_FOYER_DATA_DIR"

// defaultDataDir is relative to the working directory, which keeps
// everything mcp-foyer writes next to the project it belongs to rather than
// scattered through the user's home directory.
const defaultDataDir = "data"

// Paths locates every file mcp-foyer writes at runtime. Everything lives
// under a single root so that removing one directory removes all state.
type Paths struct {
	root string
}

// ResolveDataDir determines the data directory, in order of precedence:
// an explicit value (from a command-line flag), the MCP_FOYER_DATA_DIR
// environment variable, then "data" under the working directory.
//
// The result is absolute, so later changes to the working directory
// cannot move where logs are written.
func ResolveDataDir(explicit string) (Paths, error) {
	dir := explicit
	if dir == "" {
		dir = os.Getenv(DataDirEnv)
	}
	if dir == "" {
		dir = defaultDataDir
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve data directory %q: %w", dir, err)
	}
	return Paths{root: abs}, nil
}

// Root is the directory containing all runtime state.
func (p Paths) Root() string { return p.root }

// ConfigFile is the YAML configuration.
func (p Paths) ConfigFile() string { return filepath.Join(p.root, "config.yaml") }

// LogDir holds the current and rotated log files.
func (p Paths) LogDir() string { return filepath.Join(p.root, "logs") }

// LogFile is the log currently being written.
func (p Paths) LogFile() string { return filepath.Join(p.LogDir(), "mcp-foyer.log") }

// UsageFile holds the tool usage counts.
func (p Paths) UsageFile() string { return filepath.Join(p.root, "usage.json") }

// Ensure creates the directories mcp-foyer writes into.
func (p Paths) Ensure() error {
	for _, dir := range []string{p.root, p.LogDir()} {
		if err := os.MkdirAll(dir, configDirMode); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}

// All returns every path this type produces, which lets callers report
// the resolved layout and lets tests assert that nothing escapes Root.
func (p Paths) All() map[string]string {
	return map[string]string{
		"root":   p.Root(),
		"config": p.ConfigFile(),
		"logs":   p.LogDir(),
		"log":    p.LogFile(),
		"usage":  p.UsageFile(),
	}
}
