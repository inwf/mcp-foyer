package api

import (
	"encoding/json"
	"fmt"

	"github.com/goccy/go-yaml"

	"mcp-foyer/internal/config"
)

// The configuration types describe the file on disk, so they carry yaml
// tags and nothing else. Left to encoding/json they come out with Go's
// own field names and with every duration as a nanosecond count — two
// naming conventions in one API response, and a unit no reader would
// guess. Worse, writing a configuration would then *require* nanoseconds,
// which is the ambiguous integer duration this project set out to be rid
// of.
//
// So configuration values cross the wire in the shape the file uses:
// the same field names, and durations written the way a person writes
// them ("30s", "5m"). Going through YAML is what keeps it that way
// without a second set of structs to hold in step — a mirrored set would
// drift the first time a field was added to only one of them, and the
// symptom would be a setting the UI cannot see.
//
// It also means the settings form and the raw-YAML editor address fields
// by the same names, so what one shows is what the other saves.

// toWireJSON renders a configuration value in its YAML shape as JSON.
func toWireJSON(value any) (json.RawMessage, error) {
	asYAML, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	asJSON, err := yaml.YAMLToJSON(asYAML)
	if err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	return json.RawMessage(asJSON), nil
}

// toWireYAML converts an incoming JSON document to the YAML the config
// package parses.
func toWireYAML(raw []byte) ([]byte, error) {
	asYAML, err := yaml.JSONToYAML(raw)
	if err != nil {
		return nil, BadRequest("the configuration is not valid JSON: " + err.Error())
	}
	return asYAML, nil
}

// wireConfig is a whole configuration on the wire.
type wireConfig struct {
	config.Config
}

func (w wireConfig) MarshalJSON() ([]byte, error) { return toWireJSON(w.Config) }

// UnmarshalJSON decodes through [config.Parse] rather than straight into
// the struct, so that an omitted field takes the same default it would
// take in the file, and an unrecognised one is refused the same way.
func (w *wireConfig) UnmarshalJSON(raw []byte) error {
	asYAML, err := toWireYAML(raw)
	if err != nil {
		return err
	}
	parsed, err := config.Parse(asYAML)
	if err != nil {
		return BadRequest("the configuration could not be read: " + err.Error())
	}
	w.Config = parsed
	return nil
}

// wireServer is one server's configuration on the wire.
type wireServer struct {
	config.MCPServer
}

func (w wireServer) MarshalJSON() ([]byte, error) { return toWireJSON(w.MCPServer) }

func (w *wireServer) UnmarshalJSON(raw []byte) error {
	asYAML, err := toWireYAML(raw)
	if err != nil {
		return err
	}
	parsed, err := config.ParseServer(asYAML)
	if err != nil {
		return BadRequest("the server could not be read: " + err.Error())
	}
	w.MCPServer = parsed
	return nil
}
