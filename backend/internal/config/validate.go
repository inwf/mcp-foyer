package config

import (
	"fmt"
	"maps"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// FieldError is a single problem found in a configuration, identified by
// its dotted path so that a UI can attach the message to the right input
// and a CLI can point at the right line.
type FieldError struct {
	Field   string
	Message string
}

func (e FieldError) Error() string {
	return e.Field + ": " + e.Message
}

// ValidationError collects every problem found in one pass. Validation
// does not stop at the first failure, so that a user editing a file sees
// all of it at once rather than one error per attempt.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	switch len(e.Errors) {
	case 0:
		return "invalid configuration"
	case 1:
		return e.Errors[0].Error()
	}

	messages := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		messages[i] = fe.Error()
	}
	return fmt.Sprintf("%d problems in configuration:\n  %s",
		len(e.Errors), strings.Join(messages, "\n  "))
}

// serverName must be usable unmodified as part of an aggregated tool
// name and as a path segment in a resource URI, which rules out spaces,
// dots and slashes. A leading dash would also be read as a flag by the
// CLI.
var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// maxServerNameLen keeps prefixed tool names within a length that
// clients display without truncating.
const maxServerNameLen = 64

// Validate reports every problem in c. It returns nil or a
// *[ValidationError].
func (c Config) Validate() error {
	v := &validator{}

	if c.Version != CurrentVersion {
		v.add("version", "is %d, but this build reads version %d", c.Version, CurrentVersion)
	}

	c.validateListen(v)
	c.validateLogging(v)
	c.validateSecurity(v)
	c.validateGateway(v)
	c.validateStartup(v)
	c.validateMCPServers(v)

	if len(v.errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: v.errs}
}

func (c Config) validateListen(v *validator) { validateListen(v, c.Listen) }

// ValidateListen reports every problem with a listen address. It returns
// nil or a *[ValidationError].
//
// This is for a caller that has an address from somewhere other than the
// configuration file — a command-line override, say. Validating a whole
// configuration to check one address would report every other setting's
// problems alongside, which is the wrong answer to "can I listen here".
func ValidateListen(l Listen) error {
	v := &validator{}
	validateListen(v, l)

	if len(v.errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: v.errs}
}

func validateListen(v *validator, l Listen) {
	switch {
	case l.Host == "":
		v.add("listen.host", "is empty")
	case strings.Contains(l.Host, "/"):
		v.add("listen.host", "looks like a URL; use a bare host such as %q", "127.0.0.1")
	case strings.Contains(l.Host, ":") && !isIP(l.Host):
		v.add("listen.host", "looks like it includes a port; set listen.port instead")
	}

	// Port zero asks the operating system for any free port. It is
	// unusual for a gateway, whose clients need a known address, but it
	// is the standard way to say "pick one" and the chosen port is
	// reported on startup. Omitting the key gets the default rather than
	// zero, so this cannot mask a forgotten setting.
	if l.Port < 0 || l.Port > 65535 {
		v.add("listen.port", "is %d, want 0-65535 (0 asks for any free port)", l.Port)
	}
}

func (c Config) validateLogging(v *validator) {
	switch c.Logging.Level {
	case LevelDebug, LevelInfo, LevelWarn, LevelError:
	default:
		v.add("logging.level", "is %q, want one of debug, info, warn, error", c.Logging.Level)
	}

	switch c.Logging.Format {
	case "console", "json":
	default:
		v.add("logging.format", "is %q, want console or json", c.Logging.Format)
	}

	v.positiveDuration("logging.maxAge", c.Logging.MaxAge)
	v.positiveInt("logging.maxSizeMB", c.Logging.MaxSizeMB)
}

func (c Config) validateSecurity(v *validator) {
	for i, network := range c.Security.AllowedNetworks {
		field := fmt.Sprintf("security.allowedNetworks[%d]", i)
		// A bare address is accepted as shorthand for a single host,
		// because writing "127.0.0.1" instead of "127.0.0.1/32" is the
		// common expectation.
		if _, err := netip.ParsePrefix(network); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(network); err == nil {
			continue
		}
		v.add(field, "is %q, want an IP address or CIDR block such as %q",
			network, "192.168.0.0/16")
	}

	v.positiveInt("security.maxConnections", c.Security.MaxConnections)
	v.positiveInt("security.maxConcurrentRequests", c.Security.MaxConcurrentRequests)
	v.positiveDuration("security.connectionTimeout", c.Security.ConnectionTimeout)
	v.positiveDuration("security.idleConnectionTimeout", c.Security.IdleConnectionTimeout)

	// An idle event stream is expected behaviour, so the socket timeout
	// must not fire before the stream's own idle limit. Otherwise every
	// quiet client is disconnected mid-stream.
	if c.Security.ConnectionTimeout > 0 && c.Security.IdleConnectionTimeout > 0 &&
		c.Security.IdleConnectionTimeout <= c.Security.ConnectionTimeout {
		v.add("security.idleConnectionTimeout",
			"is %v, which does not exceed security.connectionTimeout (%v); "+
				"idle event streams would be disconnected",
			c.Security.IdleConnectionTimeout, c.Security.ConnectionTimeout)
	}
}

func (c Config) validateGateway(v *validator) {
	switch c.Gateway.DefaultSessionMode {
	case SessionModeStateful, SessionModeStateless:
	default:
		v.add("gateway.defaultSessionMode", "is %q, want stateful or stateless",
			c.Gateway.DefaultSessionMode)
	}

	v.positiveDuration("gateway.sessionTimeout", c.Gateway.SessionTimeout)
	v.nonNegativeDuration("gateway.notifyDebounce", c.Gateway.NotifyDebounce)
	v.nonNegativeDuration("gateway.keepAlive", c.Gateway.KeepAlive)

	// The threshold only has meaning while the probe is running.
	if c.Gateway.KeepAlive > 0 {
		v.positiveInt("gateway.keepAliveFailureThreshold", c.Gateway.KeepAliveFailureThreshold)
	}
}

func (c Config) validateStartup(v *validator) {
	v.nonNegativeDuration("startup.connectDelay", c.Startup.ConnectDelay)
	v.nonNegativeInt("startup.maxRetries", c.Startup.MaxRetries)
	v.positiveDuration("startup.retryBackoff", c.Startup.RetryBackoff)
}

func (c Config) validateMCPServers(v *validator) {
	// Map iteration order is random, so sort to keep the reported
	// problems in a stable order.
	for _, name := range slices.Sorted(maps.Keys(c.MCPServers)) {
		validateServerName(v, name)
		validateServer(v, "mcpServers."+name, c.MCPServers[name])
	}
}

// ValidateServer reports every problem with one server entry, name
// included. It returns nil or a *[ValidationError].
//
// This is what a caller adding servers one at a time needs: validating a
// whole configuration to check one entry would report that entry's
// problems alongside every other server's, which is the wrong answer when
// the question is whether this one can be added.
func ValidateServer(name string, server MCPServer) error {
	v := &validator{}
	validateServerName(v, name)
	validateServer(v, "mcpServers."+name, server)

	if len(v.errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: v.errs}
}

func validateServerName(v *validator, name string) {
	field := "mcpServers." + name

	if len(name) > maxServerNameLen {
		v.add(field, "name is %d characters, want at most %d", len(name), maxServerNameLen)
		return
	}
	if !serverNamePattern.MatchString(name) {
		v.add(field, "name must start with a letter or digit and contain only "+
			"letters, digits, underscores and dashes")
	}
}

// The two transports are distinguished by where the server runs: stdio
// starts one as a child process and talks to it over its pipes;
// streamable-http dials one that is already running. Each therefore
// needs exactly one of command and url, and setting the other is an
// error rather than something to ignore — a url on a stdio server is
// most likely a transport chosen by mistake, and silently dropping it
// would leave mcp-foyer talking to something other than what was meant.
func validateServer(v *validator, field string, s MCPServer) {
	// Before the transport, which ends validation early when it is
	// unknown: this rule does not depend on it.
	validateToolLists(v, field, s)

	var spawns bool

	switch s.Transport {
	case TransportStdio:
		spawns = true
	case TransportStreamableHTTP:
		spawns = false
	default:
		v.add(field+".transport", "is %q, want one of %s, %s",
			s.Transport, TransportStdio, TransportStreamableHTTP)
		// The remaining rules depend on knowing the transport.
		return
	}

	if spawns && s.Command == "" {
		v.add(field+".command", "is required for the %s transport", s.Transport)
	}
	if !spawns && s.Command != "" {
		v.add(field+".command", "is set but the %s transport does not start a process",
			s.Transport)
	}

	if !spawns && s.URL == "" {
		v.add(field+".url", "is required for the %s transport", s.Transport)
	}
	if spawns && s.URL != "" {
		v.add(field+".url", "is set but the %s transport does not connect over HTTP",
			s.Transport)
	}
	if s.URL != "" {
		v.httpURL(field+".url", s.URL)
	}

	if s.Proxy != "" {
		if spawns {
			v.add(field+".proxy", "is set but the %s transport does not connect over HTTP",
				s.Transport)
		} else {
			v.httpURL(field+".proxy", s.Proxy)
		}
	}

	v.positiveDuration(field+".timeout", s.Timeout)
	validateReadiness(v, field, s, spawns)
}

// validateToolLists rejects a tool that is both exposed and disabled.
//
// Either way of reading such an entry would surprise someone: honouring
// the exposure offers a tool the operator switched off, and honouring
// the disabling leaves an exposure in the file that silently does
// nothing. Asking is the only reading that is never wrong.
//
// It is reported on exposedTools because that is the list a person
// edits by ticking boxes, so it is the field a form can mark.
func validateToolLists(v *validator, field string, s MCPServer) {
	if len(s.DisabledTools) == 0 {
		return
	}
	reported := map[string]bool{}
	for _, name := range s.ExposedTools {
		if reported[name] || !slices.Contains(s.DisabledTools, name) {
			continue
		}
		reported[name] = true
		v.add(field+".exposedTools",
			"names %q, which disabledTools also names; a disabled tool cannot be exposed", name)
	}
}

// validateReadiness checks the ready patterns here rather than at dial
// time, which is the difference between a mistake reported while it is
// being made and one reported when a server will not come up.
func validateReadiness(v *validator, field string, s MCPServer, spawns bool) {
	if len(s.ReadyPatterns) > 0 && !spawns {
		v.add(field+".readyPatterns", "is set but the %s transport does not start a process, "+
			"so there is no output to wait for", s.Transport)
	}

	for i, pattern := range s.ReadyPatterns {
		at := fmt.Sprintf("%s.readyPatterns[%d]", field, i)
		if pattern == "" {
			v.add(at, "is empty, which would match every line")
			continue
		}
		if _, err := regexp.Compile(pattern); err != nil {
			v.add(at, "is not a valid regular expression: %s", err)
		}
	}

	v.nonNegativeDuration(field+".readyTimeout", s.ReadyTimeout)
	if s.ReadyTimeout > 0 && len(s.ReadyPatterns) == 0 {
		v.add(field+".readyTimeout", "is set but there are no readyPatterns to wait for")
	}
}

// validator accumulates field errors in the order they are found.
type validator struct {
	errs []FieldError
}

func (v *validator) add(field, format string, args ...any) {
	v.errs = append(v.errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) positiveDuration(field string, d time.Duration) {
	if d <= 0 {
		v.add(field, "is %v, want a positive duration", d)
	}
}

func (v *validator) nonNegativeDuration(field string, d time.Duration) {
	if d < 0 {
		v.add(field, "is %v, want zero or a positive duration", d)
	}
}

func (v *validator) positiveInt(field string, n int) {
	if n <= 0 {
		v.add(field, "is %d, want a positive number", n)
	}
}

func (v *validator) nonNegativeInt(field string, n int) {
	if n < 0 {
		v.add(field, "is %d, want zero or a positive number", n)
	}
}

func (v *validator) httpURL(field, raw string) {
	u, err := url.Parse(raw)
	if err != nil {
		v.add(field, "is not a valid URL: %v", err)
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		v.add(field, "has scheme %q, want http or https", u.Scheme)
		return
	}
	if u.Host == "" {
		v.add(field, "is %q, which has no host", raw)
	}
}

func isIP(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}
