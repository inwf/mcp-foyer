package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// Load reads and parses the configuration at path.
//
// Fields the file omits keep the value from [Default], so a file need
// only state what it changes. A missing file is reported as an error
// wrapping [fs.ErrNotExist]; use [LoadOrDefault] for the first-run case.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// LoadOrDefault behaves like [Load], except that a missing file yields
// the default configuration instead of an error. This is the first-run
// case: mcp-foyer starts with no upstream servers and writes a file once
// the user configures one.
func LoadOrDefault(path string) (Config, error) {
	cfg, err := Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	return cfg, err
}

// Parse decodes a configuration document over the defaults. An empty
// document yields exactly [Default].
//
// A key the configuration does not define is an error rather than
// something to ignore. Ignoring it is the worse failure: a misspelled
// key leaves the default silently in force, so the setting appears to
// have been made and has not been, and nothing in the logs says why.
func Parse(data []byte) (Config, error) {
	cfg := Default()

	// An empty document is not valid YAML to decode into a struct, but
	// it is a meaningful configuration: everything is defaulted.
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}

	if err := yaml.UnmarshalWithOptions(data, &cfg, yaml.DisallowUnknownField()); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	// The pass above decoded each upstream entry into a zero-valued
	// struct, so entries that omitted a field got Go's zero value rather
	// than the intended default. Decode them again over the per-server
	// defaults to correct that.
	servers, err := parseMCPServers(data)
	if err != nil {
		return Config{}, err
	}
	if servers != nil {
		cfg.MCPServers = servers
	}

	return cfg, nil
}

// parseMCPServers decodes each upstream entry over [DefaultMCPServer].
// It returns nil when the document has no mcpServers section at all,
// which the caller distinguishes from an empty section.
func parseMCPServers(data []byte) (map[string]MCPServer, error) {
	var doc struct {
		MCPServers map[string]ast.Node `yaml:"mcpServers"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if doc.MCPServers == nil {
		return nil, nil
	}

	servers := make(map[string]MCPServer, len(doc.MCPServers))
	for name, node := range doc.MCPServers {
		server := DefaultMCPServer()
		// A key with no body ("myserver:") decodes to a null node and
		// leaves the defaults in place, which is the useful reading.
		if node != nil {
			if err := yaml.NodeToValue(node, &server, yaml.DisallowUnknownField()); err != nil {
				return nil, fmt.Errorf("parse config: mcpServers.%s: %w", name, err)
			}
		}
		servers[name] = server
	}
	return servers, nil
}

// ParseServer decodes one server entry over [DefaultMCPServer].
//
// This is the single-entry counterpart to [Parse], for callers that
// accept one server rather than a whole configuration. It exists here
// rather than at the caller so that both paths default and reject
// unknown keys the same way; a server added through the management API
// must not behave differently from the same server written into the
// file by hand.
func ParseServer(data []byte) (MCPServer, error) {
	server := DefaultMCPServer()

	if len(bytes.TrimSpace(data)) == 0 {
		return server, nil
	}
	if err := yaml.UnmarshalWithOptions(data, &server, yaml.DisallowUnknownField()); err != nil {
		return MCPServer{}, fmt.Errorf("parse server: %w", err)
	}
	return server, nil
}
