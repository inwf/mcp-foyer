package api

import (
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"mcp-foyer/internal/config"
)

// handleGetConfig returns the configuration with secrets hidden.
func (a *API) handleGetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"config": wireConfig{a.opts.Configs.Get().Redact()},
		"path":   a.opts.Configs.Path(),
	})
}

// handlePutConfig replaces the configuration.
func (a *API) handlePutConfig(c *gin.Context) {
	var body struct {
		Config wireConfig `json:"config"`
	}
	if err := bindJSON(c, &body); err != nil {
		fail(c, err)
		return
	}

	changes, err := a.opts.Configs.Update(func(current *config.Config) error {
		incoming := restoreSecrets(body.Config.Config, *current)
		if err := incoming.Validate(); err != nil {
			return err
		}
		*current = incoming
		return nil
	})
	if err != nil {
		fail(c, FromValidation("the configuration is not valid", err))
		return
	}

	a.log.Info("the configuration was replaced",
		"requestId", RequestID(c), "changes", len(changes))
	a.applyConfig(c)

	c.JSON(http.StatusOK, gin.H{
		"config": wireConfig{a.opts.Configs.Get().Redact()},
		// An empty list rather than null: a client that iterates the
		// changes should not have to guard against the nothing-changed
		// case separately.
		"changes": append(make([]config.Change, 0, len(changes)), changes...),
	})
}

// restoreSecrets puts back every secret the client did not actually
// change.
//
// The configuration is handed out with its secrets replaced by a
// placeholder, so a client that reads it, edits one field and sends the
// whole thing back would otherwise overwrite every credential with the
// placeholder text — destroying them, and with no way to recover them
// since the file has already been rewritten. A value that arrives still
// equal to the placeholder means "unchanged", and is taken from the
// configuration in force.
func restoreSecrets(incoming, current config.Config) config.Config {
	out := incoming.Clone()

	for name, server := range out.MCPServers {
		existing, known := current.MCPServers[name]
		if !known {
			// A new server has nothing to restore from. Its secrets are
			// whatever the client sent, which is the only source for them.
			continue
		}
		out.MCPServers[name] = restoreServerSecrets(server, existing)
	}

	return out
}

// restoreServerSecrets is [restoreSecrets] for a single server.
func restoreServerSecrets(incoming, existing config.MCPServer) config.MCPServer {
	out := incoming.Clone()

	restoreSecretValues(out.Env, existing.Env)
	restoreSecretValues(out.Headers, existing.Headers)
	out.URL = restoreURLCredentials(out.URL, existing.URL)
	out.Proxy = restoreURLCredentials(out.Proxy, existing.Proxy)

	return out
}

func restoreSecretValues(incoming, existing map[string]string) {
	for name, value := range incoming {
		if value == config.RedactedValue {
			if previous, had := existing[name]; had {
				incoming[name] = previous
			}
		}
	}
}

// restoreURLCredentials puts back the userinfo of a URL whose
// credentials came back as the placeholder.
//
// Only the userinfo is restored: the rest of the URL is whatever the
// client sent, so pointing a server at a new host while keeping its
// credentials works as expected.
func restoreURLCredentials(incoming, existing string) string {
	if incoming == "" || existing == "" {
		return incoming
	}

	parsed, err := url.Parse(incoming)
	if err != nil || parsed.User == nil || parsed.User.Username() != config.RedactedURLUser {
		return incoming
	}
	previous, err := url.Parse(existing)
	if err != nil || previous.User == nil {
		return incoming
	}

	parsed.User = previous.User
	return parsed.String()
}

// handleValidateConfig reports whether a configuration would be
// accepted, without saving it.
//
// This is what lets the settings form mark bad fields as they are
// edited, rather than only on submission.
func (a *API) handleValidateConfig(c *gin.Context) {
	var body struct {
		Config wireConfig `json:"config"`
	}
	if err := bindJSON(c, &body); err != nil {
		fail(c, err)
		return
	}

	if err := restoreSecrets(body.Config.Config, a.opts.Configs.Get()).Validate(); err != nil {
		structured := FromValidation("the configuration is not valid", err)
		c.JSON(http.StatusOK, gin.H{"valid": false, "fields": structured.Fields})
		return
	}
	c.JSON(http.StatusOK, gin.H{"valid": true, "fields": []FieldError{}})
}

// exportFilename is what the browser saves the configuration as. The
// extension matters: this is the file format the gateway reads, so a
// download should be droppable straight back into a data directory.
const exportFilename = "mcp-foyer-config.yaml"

// handleExportConfig sends the configuration as a file.
//
// Secrets are hidden unless they are asked for. The two are different
// things and conflating them fails one of them: a configuration meant for
// a bug report must not carry an API token, and one meant as a backup is
// useless without them. Neither can be the silent default, so the
// parameter is explicit and the caller chooses.
func (a *API) handleExportConfig(c *gin.Context) {
	cfg := a.opts.Configs.Get()

	withSecrets, err := queryBool(c, "secrets", false)
	if err != nil {
		fail(c, err)
		return
	}
	if !withSecrets {
		cfg = cfg.Redact()
	}

	// The same encoder the gateway writes its configuration file with, so
	// that what is downloaded is what would have been on disk.
	encoded, err := config.Marshal(cfg)
	if err != nil {
		fail(c, &Error{
			Code:    CodeInternal,
			Message: "the configuration could not be encoded",
			cause:   err,
		})
		return
	}

	if withSecrets {
		a.log.Warn("the configuration was exported with its secrets",
			"requestId", RequestID(c), "client", c.ClientIP())
	}

	c.Header("Content-Disposition", `attachment; filename="`+exportFilename+`"`)
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", encoded)
}
