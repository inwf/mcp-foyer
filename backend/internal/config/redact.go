package config

import (
	"net/url"
	"strings"
)

// RedactedValue replaces a secret. It is a fixed string rather than a
// length-preserving mask so that nothing about the original leaks.
const RedactedValue = "[redacted]"

// RedactedURLUser replaces the credentials embedded in a URL.
//
// It differs from [RedactedValue] because the userinfo component of a
// URL is percent-encoded when the URL is reassembled, which would turn
// the brackets into "%5B" and "%5D" and show a settings form something
// unreadable. The marker still round-trips: a URL sent back with this
// username means the credentials were not changed.
const RedactedURLUser = "redacted"

// secretKeyHints are matched case-insensitively against the *name* of an
// environment variable or header. Matching the name rather than the
// value keeps ordinary settings readable in the UI and in change logs,
// while still catching the values worth hiding.
//
// The list errs toward over-matching: hiding a harmless value is a minor
// annoyance, printing a live credential is not.
var secretKeyHints = []string{
	"auth",
	"credential",
	"key",
	"passwd",
	"password",
	"secret",
	"session",
	"signature",
	"token",
}

// IsSecretKey reports whether a setting with this name should have its
// value hidden.
func IsSecretKey(name string) bool {
	lower := strings.ToLower(name)
	for _, hint := range secretKeyHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// Redact returns a copy of c with secret values replaced by
// [RedactedValue]. Use it before sending a configuration to an API
// client or writing one to a log.
//
// The structure is preserved exactly: keys are never dropped, so a
// redacted configuration still shows which settings exist.
func (c Config) Redact() Config {
	out := c.Clone()

	// The embedding key sits at the top level rather than inside a
	// server, so it needs its own line here.
	if out.Embedding.APIKey != "" {
		out.Embedding.APIKey = RedactedValue
	}

	if out.MCPServers != nil {
		for name, server := range out.MCPServers {
			out.MCPServers[name] = server.Redact()
		}
	}

	return out
}

// Redact returns a copy of s with secret values replaced.
func (s MCPServer) Redact() MCPServer {
	out := s.Clone()

	redactSecretValues(out.Env)
	redactSecretValues(out.Headers)

	// Credentials embedded in a URL are secret regardless of the key
	// they are stored under.
	out.URL = redactURLCredentials(out.URL)
	out.Proxy = redactURLCredentials(out.Proxy)

	return out
}

func redactSecretValues(values map[string]string) {
	for name := range values {
		if IsSecretKey(name) {
			values[name] = RedactedValue
		}
	}
}

// redactURLCredentials hides the userinfo component of a URL, leaving
// the rest addressable.
func redactURLCredentials(raw string) string {
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		// A URL that does not parse cannot be picked apart safely, and
		// rewriting it would corrupt whatever the user typed.
		return raw
	}
	u.User = url.User(RedactedURLUser)
	return u.String()
}
