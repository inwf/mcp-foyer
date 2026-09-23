// Package upstream manages connections to the MCP servers that mcp-foyer
// proxies.
//
// Tools and resources are held as the SDK's own types rather than being
// converted into local structs. The gateway re-exposes upstream tools
// verbatim — including their input schemas, which are arbitrary JSON
// Schema — so any conversion layer would be a chance to lose detail for
// no benefit.
package upstream

import (
	"log/slog"
	"time"
)

// State is where a connection is in its lifecycle.
type State string

const (
	// StateDisconnected means no connection has been attempted, or one
	// was closed deliberately.
	StateDisconnected State = "disconnected"

	// StateConnecting means a connection attempt is in progress.
	StateConnecting State = "connecting"

	// StateConnected means the handshake completed and the server is
	// usable.
	StateConnected State = "connected"

	// StateFailed means a connection attempt did not succeed, or a
	// working connection dropped.
	StateFailed State = "failed"
)

// Status is what the management API and the web UI report about one
// upstream server.
type Status struct {
	Name  string `json:"name"`
	State State  `json:"state"`

	// Error explains a failed state. Empty otherwise.
	Error string `json:"error,omitempty"`

	// LastCheck is when this status was last updated. A server that has
	// never been reached for has no such time, and reports none: the
	// zero time would otherwise be sent as the year 1, which reads as a
	// check that happened rather than one that never did.
	LastCheck time.Time `json:"lastCheck,omitzero"`

	ToolCount     int `json:"toolCount"`
	ResourceCount int `json:"resourceCount"`

	// PID and StartedAt are set only for transports that run the server
	// as a child process.
	//
	// StartedAt uses omitzero rather than omitempty: a time is a struct,
	// and omitempty never considers a struct empty, so the field would
	// otherwise be reported as the year 1 for every server that has no
	// process — a value a reader has to know to disbelieve.
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"startedAt,omitzero"`

	// ServerName and ServerVersion come from the handshake and identify
	// the far side, which is what tells you whether an upgrade landed.
	ServerName      string `json:"serverName,omitempty"`
	ServerVersion   string `json:"serverVersion,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`

	// Capability flags, as declared by the server during the handshake.
	// They are reported because they explain why a server offers no
	// resources: it may not support them at all.
	HasTools     bool `json:"hasTools"`
	HasResources bool `json:"hasResources"`
	HasPrompts   bool `json:"hasPrompts"`
	HasLogging   bool `json:"hasLogging"`
}

// Connected reports whether the server is usable right now.
func (s Status) Connected() bool { return s.State == StateConnected }

// ChangeKind identifies what changed about an upstream server.
type ChangeKind string

const (
	ChangeStatus    ChangeKind = "status"
	ChangeTools     ChangeKind = "tools"
	ChangeResources ChangeKind = "resources"
)

// Deps are the collaborators a connection needs. Passing them in keeps
// the package free of globals and lets tests observe what happened.
type Deps struct {
	// Logger receives this package's own records. Records about a
	// specific server should already be tagged with it.
	Logger *slog.Logger

	// OnChange is called after the local view of a server changes: a
	// state transition, or a refreshed tool or resource list. It must
	// not block, and it must not call back into the connection.
	OnChange func(server string, kind ChangeKind)

	// Now overrides the clock, for tests that assert on timestamps.
	Now func() time.Time
}

func (d Deps) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.New(discardHandler{})
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) notify(server string, kind ChangeKind) {
	if d.OnChange != nil {
		d.OnChange(server, kind)
	}
}
