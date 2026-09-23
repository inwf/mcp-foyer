package config_test

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
	// Imported to ask the gateway what it actually does with an empty
	// exposure list, rather than restating the rule here. Legal because
	// this is the external test package: gateway depends on config, and
	// config_test is not config.
	"mcp-foyer/internal/gateway"
)

// The configuration reference was the one part of this project's account of
// itself that nothing was checking.
//
// internal/guide has four tests holding its document against the program —
// the commands it names, the flags it names, the system tools it explains,
// the constants it quotes — and they caught real drift three times during
// development. docs/configuration.md had none of that, and by the time
// these tests were written it was wrong in two ways at once: four fields
// added later were missing from it, and one field's description said the
// opposite of what the code does.
//
// So these check the document against the struct rather than against an
// expected text, for the same reason the guide tests do: an expected text
// only asserts that the file has not been edited, and has to be updated
// every time it is.
//
// What they cannot check is prose. A field name is either in the document
// or it is not; whether the sentence beside it is true is not something
// reflect can see. That boundary is real — the sentence that was wrong
// here needed an assertion of its own.

// The document lives in the repository, beside the other things a reader
// browses, rather than inside this module. A //go:embed directive cannot
// reach outside its own module directory; a test reading a file has no
// such limit.
const referencePath = "../../../docs/configuration.md"

// Every field someone can write in the file has to appear in the document,
// or it is a setting they can only find by reading the source.
func TestTheConfigurationReferenceDocumentsEveryField(t *testing.T) {
	doc := readReference(t)

	for _, name := range sortedNames(yamlFieldNames(t)) {
		// Anywhere in the document, not specifically in a table: the two
		// lists under sessionModeRules are explained by a worked example,
		// which is a better way to document them than a row each.
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("the configuration reference never mentions %q; someone "+
				"configuring mcp-foyer from that document cannot know the field exists", name)
		}
	}
}

// And every field the document describes has to exist, or it is telling
// someone to write a key that will now be rejected — config.Parse refuses
// unknown keys.
func TestTheConfigurationReferenceOnlyDocumentsRealFields(t *testing.T) {
	fields := yamlFieldNames(t)
	documented := documentedFields(t)

	if len(documented) == 0 {
		t.Fatal("no field rows were found in the configuration reference, " +
			"so this test proves nothing")
	}
	for _, name := range documented {
		if !fields[name] {
			t.Errorf("the configuration reference documents %q, which is not a field; "+
				"the fields are: %s", name, strings.Join(sortedNames(fields), ", "))
		}
	}
}

// yamlFieldNames walks Config for every name that can appear as a key in
// the file, including the keys of a server entry.
//
// Reflected rather than listed here on purpose: a list would be a second
// copy of the schema, and would drift from the first — which is the very
// thing these tests exist to catch.
func yamlFieldNames(t *testing.T) map[string]bool {
	t.Helper()

	names := map[string]bool{}
	seen := map[reflect.Type]bool{}

	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		// Through pointers and containers to whatever holds the fields:
		// mcpServers is a map whose values carry a server's own keys.
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice ||
			typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true

		for i := range typ.NumField() {
			field := typ.Field(i)
			tag := field.Tag.Get("yaml")
			if tag == "" || tag == "-" {
				continue
			}
			if name := strings.Split(tag, ",")[0]; name != "" {
				names[name] = true
			}
			walk(field.Type)
		}
	}
	walk(reflect.TypeOf(config.Config{}))

	if len(names) == 0 {
		t.Fatal("no yaml fields were found on config.Config, so this test proves nothing")
	}
	return names
}

// fieldRow matches the first cell of a field table: `name` and nothing else.
var fieldRow = regexp.MustCompile("^\\| `([A-Za-z][A-Za-z0-9]*)`")

// sectionHeading matches the heading of a section that describes a
// configuration section, which is written as the key itself.
var sectionHeading = regexp.MustCompile("^## `[A-Za-z]")

// The default for exposedTools is the one thing in this document that was
// documented backwards: it said an empty list exposes every tool, while
// FilterTools returns nothing unless a tool is named.
//
// That is the project's central design decision — tools are deliberately
// kept out of tools/list so their schemas do not fill every client's
// context — and the reference described the behaviour it was changed away
// from. Someone configuring from it would omit the field expecting
// everything, get only system tools, and go read the source.
//
// The two tests above cannot see this: the field name was present and
// correct, and prose has no type checker. So this one holds the sentence
// itself, which is as narrow as it looks and is the point — it is the
// only kind of assertion that could have caught it.
func TestTheReferenceDoesNotClaimAnEmptyExposedToolsListExposesEverything(t *testing.T) {
	// Asked of the code rather than assumed, so that reversing the rule
	// some day fails here too — and whoever reverses it is sent to the
	// document instead of leaving it describing the old rule a second time.
	if exposed := gateway.FilterTools([]*mcp.Tool{{Name: "read"}}, nil); len(exposed) != 0 {
		t.Fatalf("FilterTools exposed %d tools for an empty list, so the rule this "+
			"test encodes has changed; the document needs rereading", len(exposed))
	}

	line := rowFor(t, "exposedTools")
	for _, wrong := range []string{"全部暴露", "都暴露", "暴露全部"} {
		if strings.Contains(line, wrong) {
			t.Errorf("the exposedTools row says %q:\n  %s\n"+
				"an absent or empty list exposes nothing — see gateway.FilterTools",
				wrong, strings.TrimSpace(line))
		}
	}
	if !strings.Contains(line, "一个都不暴露") {
		t.Errorf("the exposedTools row does not say that nothing is exposed:\n  %s",
			strings.TrimSpace(line))
	}
}

// rowFor returns the table row describing one field.
func rowFor(t *testing.T, field string) string {
	t.Helper()

	for _, line := range strings.Split(readReference(t), "\n") {
		if match := fieldRow.FindStringSubmatch(line); match != nil && match[1] == field {
			return line
		}
	}
	t.Fatalf("the configuration reference has no table row for %q", field)
	return ""
}

// documentedFields collects the field names the document puts in a table.
//
// Only tables under a section named after a configuration key count. The
// prose sections carry tables of their own — the data directory's, for one
// — and their first column is not a field name.
func documentedFields(t *testing.T) []string {
	t.Helper()

	var out []string
	inSection := false

	for _, line := range strings.Split(readReference(t), "\n") {
		if strings.HasPrefix(line, "## ") {
			inSection = sectionHeading.MatchString(line)
			continue
		}
		if !inSection {
			continue
		}
		if match := fieldRow.FindStringSubmatch(line); match != nil {
			out = append(out, match[1])
		}
	}
	return out
}

func readReference(t *testing.T) string {
	t.Helper()

	// Fatal rather than skip when it cannot be read. A skip would leave
	// this checking nothing on the day the document is moved or renamed,
	// which is exactly when it is needed.
	text, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatalf("read the configuration reference: %v", err)
	}
	return string(text)
}

func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
