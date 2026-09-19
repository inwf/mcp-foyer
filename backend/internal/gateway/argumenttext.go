package gateway

import (
	"maps"
	"slices"
	"strings"
)

// ArgumentText flattens a tool's input schema into the text a search can
// match: every property's name and description, at every depth, in a
// stable order. Nothing else from the schema — types, defaults, enums —
// is a word a caller would search by.
//
// It returns "" for anything that is not an object schema, which is also
// what a tool with no parameters gets. The schema may be in whatever
// form the upstream sent it, decoded or raw JSON.
//
// This runs over every cached tool on every search. A schema that is
// already a decoded map — which is what the SDK hands over — is walked as
// it is; only other forms pay for a trip through JSON.
func ArgumentText(schema any) string {
	decoded, ok := schema.(map[string]any)
	if !ok {
		decoded, ok = decodeObjectSchema(schema)
	}
	if !ok || decoded["type"] != "object" {
		return ""
	}
	var b strings.Builder
	appendPropertyText(&b, decoded, 0)
	return b.String()
}

// maxArgumentDepth bounds the walk. Schemas are trees a caller wrote by
// hand and are shallow; a recursive one ($ref to itself) is decoded
// through JSON and so cannot loop, but a pathological one could still be
// deep, and nothing below a few levels is a parameter anyone names.
const maxArgumentDepth = 4

func appendPropertyText(b *strings.Builder, schema map[string]any, depth int) {
	if depth >= maxArgumentDepth {
		return
	}
	properties, _ := schema["properties"].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(properties)) {
		property, _ := properties[name].(map[string]any)
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(name)
		if description, _ := property["description"].(string); description != "" {
			b.WriteByte(' ')
			b.WriteString(description)
		}
		// Arrays of objects carry their item's parameters; the caller
		// names those too ("edits" with "oldText" and "newText").
		if items, _ := property["items"].(map[string]any); items != nil {
			appendPropertyText(b, items, depth+1)
		}
		appendPropertyText(b, property, depth+1)
	}
}
