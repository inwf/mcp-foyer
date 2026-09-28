package config

import (
	"maps"
	"slices"
)

// Clone returns a deep copy of c. Configurations are handed out to
// concurrent readers, so they must not share the maps and slices that a
// caller could mutate.
//
// Nil and empty collections stay distinct, because that difference
// carries meaning in this schema.
func (c Config) Clone() Config {
	out := c

	out.Security.AllowedNetworks = slices.Clone(c.Security.AllowedNetworks)
	out.Gateway.SessionModeRules.Stateful = slices.Clone(c.Gateway.SessionModeRules.Stateful)
	out.Gateway.SessionModeRules.Stateless = slices.Clone(c.Gateway.SessionModeRules.Stateless)

	if c.MCPServers != nil {
		out.MCPServers = make(map[string]MCPServer, len(c.MCPServers))
		for name, server := range c.MCPServers {
			out.MCPServers[name] = server.Clone()
		}
	}

	return out
}

// Clone returns a deep copy of s.
func (s MCPServer) Clone() MCPServer {
	out := s

	out.Args = slices.Clone(s.Args)
	out.ExposedTools = slices.Clone(s.ExposedTools)
	out.DisabledTools = slices.Clone(s.DisabledTools)
	out.Env = maps.Clone(s.Env)
	out.Headers = maps.Clone(s.Headers)

	return out
}
