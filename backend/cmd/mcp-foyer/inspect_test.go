package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mcp-foyer/internal/gateway"
)

// ===== tools show =====

// `tools list` shortens every description to one line so that the list
// stays a list. This is where the full text and the input schema live,
// and the schema is what someone building a call actually needs.

func TestToolsShowGivesTheFullDescriptionAndSchema(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "show", "probe_echo", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	for _, want := range []string{"probe_echo", "probe", "returns its argument", "message"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the output does not contain %q:\n%s", want, stdout)
		}
	}
	// The schema is the point of the command, so a rendering that dropped
	// it would leave nothing this command is for.
	if !strings.Contains(stdout, "INPUT SCHEMA") {
		t.Errorf("the input schema is missing:\n%s", stdout)
	}
}

// The argument table is what a reader scans before typing --arg. Which
// arguments are required is the part they cannot guess.
func TestToolsShowSaysWhichArgumentsAreRequired(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	_, stdout, _ := execute(t, "tools", "show", "probe_echo", "--address", address)

	if !strings.Contains(stdout, "REQUIRED") {
		t.Fatalf("there is no column saying what is required:\n%s", stdout)
	}
	row := rowFor(t, stdout, "message")
	if !strings.Contains(row, "yes") {
		t.Errorf("message is required and the row does not say so: %q", row)
	}
	if !strings.Contains(row, "string") {
		t.Errorf("the declared type is missing from the row: %q", row)
	}
}

func TestToolsShowWorksForTheGatewaysOwnTools(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "show", gateway.ToolSearchTools, "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	// A gateway tool is forwarded from nowhere. Printing a blank or a dash
	// under a "server" heading would leave a reader wondering which one.
	if !strings.Contains(stdout, "mcp-foyer itself") {
		t.Errorf("the output does not say where the tool comes from:\n%s", stdout)
	}
	if !strings.Contains(stdout, "server") {
		t.Errorf("the tool's own \"server\" argument is missing:\n%s", stdout)
	}
}

// Deciding whether to expose a tool means reading it first, so a tool
// that is not exposed has to be describable. It is named the way the full
// list names it.
func TestToolsShowDescribesAnUnexposedTool(t *testing.T) {
	address, cleanup := exposingOneTool(t)
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "show", "probe/sleep", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "not exposed") {
		t.Errorf("the output does not say that it is not exposed:\n%s", stdout)
	}
	// The schema is the reason to look at all, and an unexposed tool has one
	// like any other.
	if !strings.Contains(stdout, "INPUT SCHEMA") {
		t.Errorf("the input schema is missing:\n%s", stdout)
	}
	// And how to reach it, since its name is not in the gateway's tool list.
	if !strings.Contains(stdout, gateway.ToolCallTool) {
		t.Errorf("no way to call it is offered:\n%s", stdout)
	}
}

func TestToolsShowRefusesAToolThatIsNotThere(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, _, stderr := execute(t, "tools", "show", "nonesuch", "--address", address)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "nonesuch") {
		t.Errorf("the message does not name what was asked for:\n%s", stderr)
	}
}

func TestToolsShowCanPrintJSON(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "tools", "show", "probe_echo", "--json", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	var decoded struct {
		Exposed     string         `json:"exposed"`
		Server      string         `json:"server"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("--json did not print JSON: %v\n%s", err, stdout)
	}

	if decoded.Exposed != "probe_echo" {
		t.Errorf("exposed = %q, want %q", decoded.Exposed, "probe_echo")
	}
	if decoded.Server != "probe" {
		t.Errorf("server = %q, want %q", decoded.Server, "probe")
	}
	if decoded.InputSchema == nil {
		t.Error("the schema is missing, which is what --json is for")
	}
}

// ===== ui =====

// The URL is printed whether or not a browser opens, so that the command
// is still useful over ssh or in a container.
func TestUIPrintsTheAddress(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	code, stdout, stderr := execute(t, "ui", "--print", "--address", address)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "http://"+address) {
		t.Errorf("the address is missing from the output: %q", stdout)
	}
}

// A browser window on a refused connection is a worse answer than a
// sentence saying nothing is running.
func TestUIReportsThatNoGatewayIsRunning(t *testing.T) {
	code, _, stderr := execute(t, "ui", "--address", unusedAddress(t))

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if stderr == "" {
		t.Error("nothing was written to standard error")
	}
}

// What --print suppresses is the opening, not the printing.
func TestUIOpensWhatItPrints(t *testing.T) {
	address, cleanup := withTestServer(t, "probe")
	defer cleanup()

	// The command is built here with an opener that records rather than
	// launches: a test suite must not open real browser windows.
	var opened string
	stdout := &strings.Builder{}
	cmd := newUICommand(&globalOptions{}, stdout, func(_ context.Context, url string) error {
		opened = url
		return nil
	})
	cmd.SetArgs([]string{"--address", address})
	cmd.SetOut(stdout)
	cmd.SetErr(stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("ui: %v", err)
	}
	if opened != "http://"+address {
		t.Errorf("opened %q, want %q", opened, "http://"+address)
	}
	if !strings.Contains(stdout.String(), opened) {
		t.Errorf("what was opened was not printed: %q", stdout.String())
	}
}
