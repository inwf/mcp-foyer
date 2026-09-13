package gateway

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// checkArguments validates a call's arguments against the tool's input
// schema before the call is forwarded.
//
// Clients that reach a tool through call_tool never see its schema, so
// nothing checks the arguments on their side, and what happens to wrong
// ones is up to the upstream: an SDK-built server refuses them clearly,
// a hand-built one may fail obscurely or act on them. The gateway holds
// the schema and can say what is wrong in the same words every time,
// without an upstream round trip.
//
// The check is advisory in the sense that the upstream stays the
// authority: when the gateway cannot check — the tool is not in its
// cache (which can lag a list-changed notification), or the schema is
// one it cannot resolve — the call goes through untouched. A validator
// that turned "I do not understand this schema" into a refusal would
// make the gateway a new way for a working tool to fail.
func checkArguments(tool *mcp.Tool, args map[string]any) error {
	if tool == nil || tool.InputSchema == nil {
		return nil
	}
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return nil
	}
	// The validator refuses versions it does not know at validation time,
	// and that refusal would otherwise read as an argument error.
	if !supportedSchemaVersion(schema.Schema) {
		return nil
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil
	}

	// Validate wants a JSON value; a nil map is the empty object, which
	// is what a call with no arguments means.
	instance := args
	if instance == nil {
		instance = map[string]any{}
	}
	if err := resolved.Validate(instance); err != nil {
		// The validator prefixes every message with where it was when it
		// failed, and for the arguments object that is always the root.
		detail := strings.TrimPrefix(err.Error(), "validating root: ")
		return fmt.Errorf("the arguments do not match %s's input schema: %s; "+
			"get_tool_details gives the schema", tool.Name, detail)
	}
	return nil
}

// supportedSchemaVersion mirrors what the validator accepts: no declared
// version, draft-07 or draft 2020-12. Kept here because the validator's
// own check is unexported and only runs once validation has begun.
func supportedSchemaVersion(version string) bool {
	switch version {
	case "",
		"http://json-schema.org/draft-07/schema#",
		"https://json-schema.org/draft-07/schema#",
		"https://json-schema.org/draft/2020-12/schema":
		return true
	}
	return false
}
