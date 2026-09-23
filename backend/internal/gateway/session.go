package gateway

import (
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/config"
)

// SessionModeHeader lets a client state outright how it wants to be
// served, which outranks any guess made from its User-Agent.
const SessionModeHeader = "X-MCP-Session-Mode"

// SessionInfo describes one connected client.
type SessionInfo struct {
	ID              string `json:"id"`
	ClientName      string `json:"clientName,omitempty"`
	ClientVersion   string `json:"clientVersion,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
}

// ResolveSessionMode decides how to serve a request.
//
// Clients differ in what they can cope with: some expect a persistent
// session with an event stream, others send independent requests and
// never read a stream at all. Three levels of decision, most specific
// first:
//
//  1. the request header, when the client says what it wants;
//  2. a User-Agent rule, for clients that cannot say;
//  3. the configured default.
func ResolveSessionMode(header, userAgent string, cfg config.Gateway) config.SessionMode {
	if mode, ok := parseSessionMode(header); ok {
		return mode
	}
	if mode, ok := matchUserAgent(userAgent, cfg.SessionModeRules); ok {
		return mode
	}
	if mode, ok := parseSessionMode(string(cfg.DefaultSessionMode)); ok {
		return mode
	}
	return config.SessionModeStateful
}

func parseSessionMode(value string) (config.SessionMode, bool) {
	switch config.SessionMode(strings.ToLower(strings.TrimSpace(value))) {
	case config.SessionModeStateful:
		return config.SessionModeStateful, true
	case config.SessionModeStateless:
		return config.SessionModeStateless, true
	default:
		return "", false
	}
}

// matchUserAgent finds the most specific rule that applies.
//
// The longest matching keyword wins, so that a general rule and a
// specific one can coexist: "Claude" can select one mode while
// "ClaudeCode" selects another. A tie goes to the stateful list, since
// that mode supports strictly more of the protocol.
func matchUserAgent(userAgent string, rules config.SessionModeRules) (config.SessionMode, bool) {
	if userAgent == "" {
		return "", false
	}
	lower := strings.ToLower(userAgent)

	best := 0
	var mode config.SessionMode

	for _, keyword := range rules.Stateful {
		if len(keyword) > best && containsFold(lower, keyword) {
			best, mode = len(keyword), config.SessionModeStateful
		}
	}
	for _, keyword := range rules.Stateless {
		if len(keyword) > best && containsFold(lower, keyword) {
			best, mode = len(keyword), config.SessionModeStateless
		}
	}

	return mode, best > 0
}

func containsFold(lowerHaystack, needle string) bool {
	if needle == "" {
		return false
	}
	return strings.Contains(lowerHaystack, strings.ToLower(needle))
}

// describe summarises one session for the management API.
func describe(session *mcp.ServerSession) SessionInfo {
	info := SessionInfo{ID: session.ID()}
	if params := session.InitializeParams(); params != nil {
		info.ProtocolVersion = params.ProtocolVersion
		if params.ClientInfo != nil {
			info.ClientName = params.ClientInfo.Name
			info.ClientVersion = params.ClientInfo.Version
		}
	}
	return info
}

// sortSessions orders sessions by id, so that a list in the UI does not
// reshuffle between refreshes.
func sortSessions(sessions []SessionInfo) {
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
}
