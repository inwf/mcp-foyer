package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"mcp-foyer/internal/api"
)

// Importing is how an installation starts: someone already has a working
// mcpServers block in Claude Desktop, Cursor or VS Code, and the
// alternative to pasting it is retyping every server by hand. So the
// endpoint takes that file as it is.

// importDocument posts a document and reads whichever shape came back.
//
// A partial success answers with the per-entry results; a document from
// which nothing could be imported is a failed request and answers through
// the API's error envelope, one field error per entry. Both carry the same
// information, so this flattens them into one value to assert on.
func importDocument(t *testing.T, h *harness, document string) (*http.Response, importResponse) {
	t.Helper()

	resp := h.raw(t, http.MethodPost, "/api/servers/import", document)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}

	if resp.StatusCode == http.StatusOK {
		var decoded importResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode the answer: %v\n%s", err, body)
		}
		return resp, decoded
	}

	var envelope api.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode the envelope: %v\n%s", err, body)
	}

	out := importResponse{Message: envelope.Error.Message}
	for _, field := range envelope.Error.Fields {
		out.Results = append(out.Results, importResult{Name: field.Field, Error: field.Message})
		out.Failed++
	}
	return resp, out
}

type importResult struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

type importResponse struct {
	Results  []importResult `json:"results"`
	Imported int            `json:"imported"`
	Failed   int            `json:"failed"`

	// Message is the envelope's, when the whole request failed.
	Message string `json:"-"`
}

func (r importResponse) errorFor(name string) string {
	for _, result := range r.Results {
		if result.Name == name {
			return result.Error
		}
	}
	return "<no result for " + name + ">"
}

func configuredNames(t *testing.T, h *harness) []string {
	t.Helper()

	resp := h.get(t, "/api/servers")
	var listed struct {
		Servers []struct{ Name string } `json:"servers"`
	}
	decode(t, resp, http.StatusOK, &listed)

	names := make([]string, 0, len(listed.Servers))
	for _, server := range listed.Servers {
		names = append(names, server.Name)
	}
	return names
}

// The pasted document is Claude Desktop's, and it says "command" with no
// transport at all for a child process.
func TestAStdioServerIsRecognisedByHavingACommand(t *testing.T) {
	h := start(t, nil)

	resp, answer := importDocument(t, h, `{
	  "mcpServers": {
	    "files": {
	      "command": "npx",
	      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
	      "env": {"LOG": "debug"}
	    }
	  }
	}`)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; results: %+v", resp.StatusCode, http.StatusOK, answer.Results)
	}
	if answer.Imported != 1 {
		t.Fatalf("imported = %d, want 1; results: %+v", answer.Imported, answer.Results)
	}

	stored := h.get(t, "/api/servers/files")
	var view struct {
		Config struct {
			Transport string            `json:"transport"`
			Command   string            `json:"command"`
			Args      []string          `json:"args"`
			Env       map[string]string `json:"env"`
			Enabled   bool              `json:"enabled"`
		} `json:"config"`
	}
	decode(t, stored, http.StatusOK, &view)

	if view.Config.Transport != "stdio" {
		t.Errorf("transport = %q, want %q", view.Config.Transport, "stdio")
	}
	if view.Config.Command != "npx" {
		t.Errorf("command = %q, want %q", view.Config.Command, "npx")
	}
	if len(view.Config.Args) != 3 {
		t.Errorf("args = %v, want three of them", view.Config.Args)
	}
	if view.Config.Env["LOG"] != "debug" {
		t.Errorf("env = %v, want LOG=debug", view.Config.Env)
	}
	// An imported server that arrived switched off would be a surprise:
	// the document it came from was a list of servers in use.
	if !view.Config.Enabled {
		t.Error("the imported server is disabled")
	}
}

// Other clients write "type" where this project writes "transport", and
// they spell streamable HTTP as "http".
func TestTheTypeFieldIsTranslated(t *testing.T) {
	for _, declared := range []string{"http", "streamable-http"} {
		t.Run(declared, func(t *testing.T) {
			h := start(t, nil)
			resp, answer := importDocument(t, h, `{
			  "mcpServers": {
			    "remote": {
			      "type": "`+declared+`",
			      "url": "https://example.com/mcp",
			      "headers": {"Authorization": "Bearer x"}
			    }
			  }
			}`)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d; results: %+v", resp.StatusCode, answer.Results)
			}

			var view struct {
				Config struct {
					Transport string `json:"transport"`
					URL       string `json:"url"`
				} `json:"config"`
			}
			decode(t, h.get(t, "/api/servers/remote"), http.StatusOK, &view)

			if view.Config.Transport != "streamable-http" {
				t.Errorf("transport = %q, want %q", view.Config.Transport, "streamable-http")
			}
			if view.Config.URL != "https://example.com/mcp" {
				t.Errorf("url = %q", view.Config.URL)
			}
		})
	}
}

// A url and nothing else can only mean a server running somewhere else.
func TestAURLWithNoTypeIsTakenAsStreamableHTTP(t *testing.T) {
	h := start(t, nil)

	_, answer := importDocument(t, h, `{
	  "mcpServers": {"remote": {"url": "https://example.com/mcp"}}
	}`)
	if answer.Imported != 1 {
		t.Fatalf("imported = %d, want 1; results: %+v", answer.Imported, answer.Results)
	}

	var view struct {
		Config struct {
			Transport string `json:"transport"`
		} `json:"config"`
	}
	decode(t, h.get(t, "/api/servers/remote"), http.StatusOK, &view)

	if view.Config.Transport != "streamable-http" {
		t.Errorf("transport = %q, want %q", view.Config.Transport, "streamable-http")
	}
}

// Treating SSE as streamable HTTP would produce a server that fails to
// connect for reasons no message would explain.
func TestAnSSEServerIsRefusedWithAnExplanation(t *testing.T) {
	h := start(t, nil)

	resp, answer := importDocument(t, h, `{
	  "mcpServers": {"legacy": {"type": "sse", "url": "https://example.com/sse"}}
	}`)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if answer.Imported != 0 {
		t.Errorf("imported = %d, want 0", answer.Imported)
	}
	if message := answer.errorFor("legacy"); !strings.Contains(message, "SSE") {
		t.Errorf("the message does not explain the refusal: %q", message)
	}
}

// This is the whole point of the endpoint: nine good servers and one typo
// leaves nine servers configured and one message about the tenth.
func TestOneBadEntryDoesNotStopTheRest(t *testing.T) {
	h := start(t, nil)

	resp, answer := importDocument(t, h, `{
	  "mcpServers": {
	    "good": {"command": "npx"},
	    "alsogood": {"url": "https://example.com/mcp"},
	    "bad": {"transport": "carrier-pigeon"}
	  }
	}`)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; results: %+v", resp.StatusCode, http.StatusOK, answer.Results)
	}
	if answer.Imported != 2 || answer.Failed != 1 {
		t.Fatalf("imported = %d and failed = %d, want 2 and 1; results: %+v",
			answer.Imported, answer.Failed, answer.Results)
	}

	names := configuredNames(t, h)
	if len(names) != 2 {
		t.Errorf("%d servers were configured, want 2: %v", len(names), names)
	}
	if message := answer.errorFor("bad"); message == "" {
		t.Error("the failed entry carries no reason")
	}
}

// A caller reading only the status code must not be told that a document
// which imported nothing worked.
func TestADocumentThatImportsNothingIsAFailure(t *testing.T) {
	h := start(t, nil)

	resp, answer := importDocument(t, h, `{
	  "mcpServers": {"bad": {"transport": "carrier-pigeon"}}
	}`)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if answer.Failed != 1 {
		t.Errorf("failed = %d, want 1", answer.Failed)
	}
	if len(configuredNames(t, h)) != 0 {
		t.Error("a server was configured despite the failure")
	}
}

// Overwriting would discard whatever is configured now, including
// credentials the pasted document does not carry.
func TestAnExistingServerIsNotOverwritten(t *testing.T) {
	h := start(t, nil)

	if _, answer := importDocument(t, h, `{
	  "mcpServers": {"files": {"command": "original"}}
	}`); answer.Imported != 1 {
		t.Fatalf("the first import failed: %+v", answer.Results)
	}

	resp, answer := importDocument(t, h, `{
	  "mcpServers": {"files": {"command": "replacement"}}
	}`)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if message := answer.errorFor("files"); !strings.Contains(message, "already exists") {
		t.Errorf("the message does not say why: %q", message)
	}

	var view struct {
		Config struct {
			Command string `json:"command"`
		} `json:"config"`
	}
	decode(t, h.get(t, "/api/servers/files"), http.StatusOK, &view)
	if view.Config.Command != "original" {
		t.Errorf("command = %q; the existing server was overwritten", view.Config.Command)
	}
}

// The document is another program's configuration file. Claude Desktop's
// carries globalShortcut beside mcpServers, and refusing the file over a
// key that is none of this gateway's business would defeat the point.
func TestKeysBesideMCPServersAreIgnored(t *testing.T) {
	h := start(t, nil)

	resp, answer := importDocument(t, h, `{
	  "globalShortcut": "Cmd+Space",
	  "someOtherClientSetting": {"nested": true},
	  "mcpServers": {"files": {"command": "npx"}}
	}`)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; results: %+v", resp.StatusCode, http.StatusOK, answer.Results)
	}
	if answer.Imported != 1 {
		t.Errorf("imported = %d, want 1; results: %+v", answer.Imported, answer.Results)
	}
}

// An unknown key inside an entry is a different matter: that one becomes
// this gateway's configuration, and silently dropping a setting would
// leave the caller believing it took effect.
func TestAnUnknownKeyInsideAnEntryIsReported(t *testing.T) {
	h := start(t, nil)

	_, answer := importDocument(t, h, `{
	  "mcpServers": {"files": {"command": "npx", "autoApprove": ["read"]}}
	}`)

	if answer.Imported != 0 {
		t.Fatalf("imported = %d, want 0", answer.Imported)
	}
	if message := answer.errorFor("files"); !strings.Contains(message, "autoApprove") {
		t.Errorf("the message does not name the unknown key: %q", message)
	}
}

func TestAnEmptyDocumentIsRefused(t *testing.T) {
	h := start(t, nil)

	for _, document := range []string{`{}`, `{"mcpServers": {}}`} {
		resp := h.raw(t, http.MethodPost, "/api/servers/import", document)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d for %s, want %d", resp.StatusCode, document, http.StatusBadRequest)
		}
	}
}

// The import path sits beside the per-server paths, and adding it must
// not have taken a route away from a server that happens to be called
// "import".
func TestImportingDoesNotShadowTheServerRoutes(t *testing.T) {
	h := start(t, nil)

	if _, answer := importDocument(t, h, `{
	  "mcpServers": {"import": {"command": "npx"}}
	}`); answer.Imported != 1 {
		t.Fatalf("a server named \"import\" could not be imported: %+v", answer.Results)
	}

	var view struct{ Name string }
	decode(t, h.get(t, "/api/servers/import"), http.StatusOK, &view)
	if view.Name != "import" {
		t.Errorf("name = %q, want %q", view.Name, "import")
	}
}
