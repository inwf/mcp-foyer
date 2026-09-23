package upstream_test

import (
	"os"
	"testing"

	"mcp-foyer/internal/testmcp"
)

// The tests in this package need a real MCP server on the other end of a
// real pipe, which testmcp provides by re-executing this binary.
func TestMain(m *testing.M) {
	if served, code := testmcp.ServeIfRequested(); served {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// Local aliases, so the tests read without a package qualifier on every
// line.
const (
	serverModeEnv   = testmcp.ModeEnv
	modeFull        = testmcp.ModeFull
	modeToolsOnly   = testmcp.ModeToolsOnly
	modeNoisyStderr = testmcp.ModeNoisyStderr
	modeSlowReady   = testmcp.ModeSlowReady
	modeCrash       = testmcp.ModeCrash
)
