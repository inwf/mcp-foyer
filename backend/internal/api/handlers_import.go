package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"mcp-foyer/internal/config"
)

// Importing servers from another client's configuration.
//
// This is how an installation starts: someone already has a working
// mcpServers block in Claude Desktop, Cursor or VS Code, and the
// alternative to pasting it is retyping every server by hand through a
// form. That is the main path onto this gateway, so it takes the file as
// it is rather than a shape of this project's choosing.

// ImportResult is what came of one entry in an imported document.
type ImportResult struct {
	Name string `json:"name"`

	// Error is why the entry was not imported, and is empty when it was.
	Error string `json:"error,omitempty"`
}

// handleImportServers adds every server in a pasted configuration.
//
// A failure applies to one entry rather than to the request: a document
// with nine good servers and one typo should leave nine servers
// configured and one message about the tenth. Rejecting the lot would
// mean editing someone else's file until it passes, which is exactly the
// work this is here to avoid.
func (a *API) handleImportServers(c *gin.Context) {
	document, err := readImportDocument(c)
	if err != nil {
		fail(c, err)
		return
	}
	if len(document) == 0 {
		fail(c, BadRequest("the document has no mcpServers in it"))
		return
	}

	names := make([]string, 0, len(document))
	for name := range document {
		names = append(names, name)
	}
	sort.Strings(names)

	// Two passes. Everything is read and checked first, then whatever
	// survived is written in one update: one file write and one reload
	// rather than one per server, and no half-applied document if the
	// write itself fails.
	accepted := map[string]config.MCPServer{}
	results := make([]ImportResult, 0, len(names))

	existing := a.opts.Configs.Get().MCPServers
	for _, name := range names {
		server, err := readImportedServer(name, document[name], existing, accepted)
		if err != nil {
			results = append(results, ImportResult{Name: name, Error: err.Error()})
			continue
		}
		accepted[name] = server
	}

	if len(accepted) > 0 {
		if _, err := a.opts.Configs.Update(func(current *config.Config) error {
			if current.MCPServers == nil {
				current.MCPServers = map[string]config.MCPServer{}
			}
			for name, server := range accepted {
				current.MCPServers[name] = server
			}
			return current.Validate()
		}); err != nil {
			// The document was checked entry by entry, so reaching here
			// means the configuration as a whole rejected the combination
			// or the file could not be written. Either way nothing was
			// applied, and saying which servers "succeeded" would be false.
			fail(c, FromValidation("the imported servers could not be saved", err))
			return
		}
	}

	for _, name := range names {
		if _, ok := accepted[name]; ok {
			results = append(results, ImportResult{Name: name})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })

	// A document from which nothing could be imported is a failed request,
	// and leaves through the same envelope as every other failure — with
	// one field error per entry, so the caller still learns why each one
	// was refused. A caller reading only the status code must not be told
	// that a request which changed nothing worked.
	if len(accepted) == 0 {
		fields := make([]FieldError, 0, len(results))
		for _, result := range results {
			fields = append(fields, FieldError{Field: result.Name, Message: result.Error})
		}
		fail(c, Invalid("no server in the document could be imported", fields...))
		return
	}

	a.log.Info("servers were imported", "requestId", RequestID(c),
		"imported", len(accepted), "failed", len(results)-len(accepted))
	a.applyConfig(c)

	c.JSON(http.StatusOK, gin.H{
		"results":  results,
		"imported": len(accepted),
		"failed":   len(results) - len(accepted),
	})
}

// readImportDocument pulls the mcpServers block out of the request.
//
// Unknown keys beside it are ignored, unlike everywhere else in this API.
// The document is another program's configuration file — Claude Desktop's
// carries globalShortcut, and others carry their own settings — and
// refusing it over a key that is none of this gateway's business would
// defeat the point of accepting the file at all. The entries themselves
// are still read strictly.
func readImportDocument(c *gin.Context) (map[string]json.RawMessage, error) {
	body := http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBody)

	var document struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&document); err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			return nil, BadRequest(fmt.Sprintf("the document is larger than %d bytes", maxRequestBody))
		}
		return nil, BadRequest("the document is not valid JSON: " + err.Error())
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return nil, BadRequest("the request body carries more than one JSON document")
	}
	return document.MCPServers, nil
}

// readImportedServer turns one entry into a server, or says why it cannot.
func readImportedServer(name string, raw json.RawMessage,
	existing, pending map[string]config.MCPServer) (config.MCPServer, error) {

	if strings.TrimSpace(name) == "" {
		return config.MCPServer{}, fmt.Errorf("a server needs a name")
	}
	if _, taken := existing[name]; taken {
		// Overwriting would discard whatever is configured now, including
		// credentials the pasted document does not carry.
		return config.MCPServer{}, fmt.Errorf(
			"a server named %q already exists; remove it first to replace it", name)
	}
	if _, twice := pending[name]; twice {
		return config.MCPServer{}, fmt.Errorf("the document names %q more than once", name)
	}

	translated, err := translateImported(raw)
	if err != nil {
		return config.MCPServer{}, err
	}

	var server wireServer
	if err := server.UnmarshalJSON(translated); err != nil {
		// The wire decoder reports through the API's error envelope, which
		// is not what belongs in a per-entry message.
		var apiError *Error
		if errors.As(err, &apiError) {
			return config.MCPServer{}, fmt.Errorf("%s", apiError.Message)
		}
		return config.MCPServer{}, err
	}
	if err := config.ValidateServer(name, server.MCPServer); err != nil {
		return config.MCPServer{}, err
	}
	return server.MCPServer, nil
}

// translateImported rewrites the fields other clients spell differently.
//
// The two that matter are the transport and how it is named. Claude
// Desktop and the editors write "type" where this project writes
// "transport", and a stdio server carries no marker at all — it is
// recognised by having a command. Everything else is left exactly as it
// arrived, so that a field this gateway does not know about is still
// reported as one rather than quietly dropped.
func translateImported(raw json.RawMessage) (json.RawMessage, error) {
	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, fmt.Errorf("this entry is not a JSON object: %w", err)
	}

	if declared, present := entry["type"]; present {
		text, ok := declared.(string)
		if !ok {
			return nil, fmt.Errorf("\"type\" should be a string, and is %T", declared)
		}
		if _, already := entry["transport"]; already {
			return nil, fmt.Errorf("this entry sets both \"type\" and \"transport\"")
		}
		transport, err := transportFor(text)
		if err != nil {
			return nil, err
		}
		delete(entry, "type")
		entry["transport"] = string(transport)
	}

	// No transport and a url means a server running somewhere else, which
	// is the only thing a url can mean here.
	if _, declared := entry["transport"]; !declared {
		if _, remote := entry["url"]; remote {
			entry["transport"] = string(config.TransportStreamableHTTP)
		}
	}

	return json.Marshal(entry)
}

// transportFor maps the names other clients use onto this project's.
func transportFor(declared string) (config.Transport, error) {
	switch strings.ToLower(strings.TrimSpace(declared)) {
	case "stdio":
		return config.TransportStdio, nil
	case "http", "streamable-http", "streamablehttp":
		return config.TransportStreamableHTTP, nil
	case "sse":
		// Saying nothing and treating it as streamable HTTP would produce
		// a server that fails to connect for reasons the message would not
		// explain.
		return "", fmt.Errorf("mcp-foyer does not speak SSE to upstream servers; " +
			"if the server also offers streamable HTTP, set the type to \"http\"")
	default:
		return "", fmt.Errorf("%q is not a transport mcp-foyer knows; "+
			"use \"stdio\" or \"http\"", declared)
	}
}
