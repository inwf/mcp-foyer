package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/testmcp"
)

// `status` is the answer to "is it running, and what is it doing" — the
// question someone asks before any other. It reports the instance rather
// than the configuration, so everything here starts a real one.

func TestStatusReportsTheRunningInstance(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "status", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	// The build that is running, not the build this command was compiled
	// from: they can differ, and the running one is what was asked about.
	if !strings.Contains(stdout, version) {
		t.Errorf("the output does not say which build is running:\n%s", stdout)
	}
	if !strings.Contains(stdout, address) {
		t.Errorf("the output does not say what it asked:\n%s", stdout)
	}
	if got := fieldValue(t, stdout, "servers"); got != "1 configured, 1 connected, 0 failed" {
		t.Errorf("servers = %q, want the one connected server", got)
	}
	// The four gateway tools are always on offer, so the count is never
	// zero on a running instance.
	if got := fieldValue(t, stdout, "tools"); !strings.Contains(got, "offered to clients") {
		t.Errorf("tools = %q, want what is on offer", got)
	}
	if got := fieldValue(t, stdout, "uptime"); got == "" {
		t.Error("the output does not say how long it has been up")
	}
}

// Nothing is exposed unless the configuration asks for it, so the tools
// count on its own reads as "this is all there is". It is not.
func TestStatusSaysHowMuchIsNotExposed(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		server, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		server.ExposedTools = []string{"echo"}
		cfg.MCPServers = map[string]config.MCPServer{"probe": server}
	})
	defer func() { stop(); <-done }()

	address := hostPort(t, base)
	waitForState(t, address, "probe", "connected")

	code, stdout, stderr := execute(t, "status", "--address", address)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}

	tools := fieldValue(t, stdout, "tools")
	if !strings.Contains(tools, "not exposed") {
		t.Errorf("tools = %q, want it to say that some are not exposed", tools)
	}
	// And where they went, rather than leaving a reader thinking they are
	// gone.
	if !strings.Contains(tools, "call_tool") {
		t.Errorf("tools = %q, want a way to reach the unexposed ones", tools)
	}
}

func TestStatusIsQuietWhenEverythingIsExposed(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	_, stdout, _ := execute(t, "status", "--address", address)

	if strings.Contains(stdout, "not exposed") {
		t.Errorf("a note about unexposed tools appears with none:\n%s", stdout)
	}
}

// A failed server is the one thing in this report that needs acting on,
// and the reason lives in another command — so it has to be named.
func TestStatusPointsAtWhereTheFailureIsExplained(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		broken, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		broken.Command = "/nonexistent/definitely-not-a-program"
		cfg.MCPServers = map[string]config.MCPServer{"broken": broken}
	})
	defer func() { stop(); <-done }()

	address := hostPort(t, base)
	waitForState(t, address, "broken", "failed")

	code, stdout, stderr := execute(t, "status", "--address", address)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}

	if got := fieldValue(t, stdout, "servers"); !strings.Contains(got, "1 failed") {
		t.Errorf("servers = %q, want the failure counted", got)
	}
	if !strings.Contains(stdout, "servers list") {
		t.Errorf("nothing says where the reason is:\n%s", stdout)
	}
}

// The session list is the part of this report that no other command
// answers, so it is exercised with a real client rather than assumed.
func TestStatusListsTheConnectedClients(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "a-test-client", Version: "9.9.9"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: base + api.MCPPath,
	}, nil)
	if err != nil {
		t.Fatalf("connect to the gateway: %v", err)
	}
	defer session.Close()

	address := hostPort(t, base)
	code, stdout, stderr := execute(t, "status", "--address", address)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}

	if !strings.Contains(stdout, "SESSIONS") {
		t.Fatalf("there is no session list:\n%s", stdout)
	}
	// The client's own name and version are what identify it to a person;
	// the session id means nothing on its own.
	for _, want := range []string{"a-test-client", "9.9.9"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the session list does not mention %q:\n%s", want, stdout)
		}
	}
	if got := fieldValue(t, stdout, "sessions"); !strings.Contains(got, "connected") {
		t.Errorf("sessions = %q, want the count of connected clients", got)
	}
}

// With nobody connected an empty table would be noise, and the line above
// it already says so.
func TestStatusOmitsTheSessionTableWhenNobodyIsConnected(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	_, stdout, _ := execute(t, "status", "--address", address)

	if strings.Contains(stdout, "SESSIONS") {
		t.Errorf("a session table appears with no clients:\n%s", stdout)
	}
	if got := fieldValue(t, stdout, "sessions"); !strings.Contains(got, "none") {
		t.Errorf("sessions = %q, want it to say there are none", got)
	}
	// The mode the next client will get is worth knowing before one arrives.
	if got := fieldValue(t, stdout, "sessions"); !strings.Contains(got, string(config.SessionModeStateful)) {
		t.Errorf("sessions = %q, want the mode a new client would get", got)
	}
}

// "Is it running" is the question this command exists to answer, so the
// answer "no" has to be a clear failure rather than an empty report.
func TestStatusReportsThatNothingIsRunning(t *testing.T) {
	code, _, stderr := execute(t, "status", "--address", unusedAddress(t))

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "mcp-foyer serve") {
		t.Errorf("the message does not say how to start one:\n%s", stderr)
	}
}
