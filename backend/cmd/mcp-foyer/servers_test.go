package main

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/testmcp"
)

// These cover the commands that are clients of a running gateway. The
// behaviour worth protecting is not the table layout but what happens
// when the gateway is absent, which is the common case for someone
// trying the command for the first time.

// hostPort strips the scheme from what running() returns, because that
// is the shape --address takes.
func hostPort(t *testing.T, baseURL string) string {
	t.Helper()
	return strings.TrimPrefix(baseURL, "http://")
}

func TestServersListShowsTheConfiguredServers(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		upstream, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		cfg.MCPServers = map[string]config.MCPServer{
			"alpha": upstream,
			"beta":  {Transport: config.TransportStdio, Command: "npx", Enabled: false, Timeout: upstream.Timeout},
		}
	})
	defer func() { stop(); <-done }()

	code, stdout, stderr := execute(t, "servers", "list", "--address", hostPort(t, base))

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	for _, want := range []string{"NAME", "TRANSPORT", "ENABLED", "STATE", "alpha", "beta"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output does not contain %q:\n%s", want, stdout)
		}
	}

	// The disabled server is reported as such rather than omitted: it is
	// still configured, and hiding it would make it hard to re-enable.
	beta := rowFor(t, stdout, "beta")
	if !strings.Contains(beta, "no") {
		t.Errorf("the disabled server is not shown as disabled: %q", beta)
	}
}

// Nothing is exposed unless the configuration says so, so a bare tool
// count would read as an offer the gateway is not making. The pair says
// both numbers.
func TestServersListShowsHowMuchIsExposed(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		full, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		// One tool of the several this server offers.
		some := full
		some.ExposedTools = []string{"echo"}

		quiet := full
		quiet.ExposedTools = nil

		cfg.MCPServers = map[string]config.MCPServer{"some": some, "quiet": quiet}
	})
	defer func() { stop(); <-done }()

	address := hostPort(t, base)
	waitForState(t, address, "some", "connected")
	waitForState(t, address, "quiet", "connected")

	code, stdout, stderr := execute(t, "servers", "list", "--address", address)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}

	if !strings.Contains(stdout, "EXPOSED/TOOLS") {
		t.Errorf("the column does not say it is a pair:\n%s", stdout)
	}

	some := rowFor(t, stdout, "some")
	if !strings.Contains(some, "1/") {
		t.Errorf("the exposed count is missing from %q", some)
	}
	// A server that exposes nothing still reports what it has, which is
	// the difference between "exposes nothing" and "offers nothing".
	quiet := rowFor(t, stdout, "quiet")
	if !strings.Contains(quiet, "0/") {
		t.Errorf("a server exposing nothing does not say so: %q", quiet)
	}
	if strings.Contains(quiet, "0/0") {
		t.Errorf("the upstream tool count was lost: %q", quiet)
	}
}

// The columns have to stay aligned, because that is the whole reason the
// output goes through a tabwriter rather than Printf.
func TestServersListAlignsItsColumns(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		cfg.MCPServers = map[string]config.MCPServer{
			"a":                       {Transport: config.TransportStdio, Command: "npx", Timeout: cfg.Security.ConnectionTimeout},
			"a-much-longer-name-here": {Transport: config.TransportStdio, Command: "npx", Timeout: cfg.Security.ConnectionTimeout},
		}
	})
	defer func() { stop(); <-done }()

	code, stdout, stderr := execute(t, "servers", "list", "--address", hostPort(t, base))
	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}

	short, long := rowFor(t, stdout, "a"), rowFor(t, stdout, "a-much-longer-name-here")
	if columnStart(short, 2) != columnStart(long, 2) {
		t.Errorf("the second column starts at different offsets:\n%q\n%q", short, long)
	}
}

// A server the gateway could not reach must say why. The message is a
// sentence, so it goes under the table rather than into a column.
func TestServersListReportsWhyAServerFailed(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		cfg.MCPServers = map[string]config.MCPServer{
			"broken": {
				Transport: config.TransportStdio,
				Command:   "definitely-not-a-real-command",
				Enabled:   true,
				Timeout:   cfg.Security.ConnectionTimeout,
			},
		}
	})
	defer func() { stop(); <-done }()

	// The connection attempt happens in the background, so wait for the
	// failure to be recorded rather than racing it.
	waitForState(t, hostPort(t, base), "broken", "failed")

	code, stdout, stderr := execute(t, "servers", "list", "--address", hostPort(t, base))
	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "failed") {
		t.Errorf("the state column does not report the failure:\n%s", stdout)
	}
	if !strings.Contains(stdout, "definitely-not-a-real-command") {
		t.Errorf("the reason for the failure is not reported:\n%s", stdout)
	}
}

func TestServersListVerboseAddsTheHandshakeColumns(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		upstream, err := testmcp.ServerConfig(testmcp.ModeFull)
		if err != nil {
			t.Fatalf("build the upstream configuration: %v", err)
		}
		cfg.MCPServers = map[string]config.MCPServer{"alpha": upstream}
	})
	defer func() { stop(); <-done }()

	waitForState(t, hostPort(t, base), "alpha", "connected")

	code, stdout, stderr := execute(t, "servers", "list", "--verbose", "--address", hostPort(t, base))
	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	for _, want := range []string{"SERVER", "VERSION", testmcp.ServerName, testmcp.ServerVersion} {
		if !strings.Contains(stdout, want) {
			t.Errorf("verbose output does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestServersListWithNothingConfiguredSaysSo(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()

	code, stdout, stderr := execute(t, "servers", "list", "--address", hostPort(t, base))

	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "no servers are configured") {
		t.Errorf("output does not say the list is empty:\n%s", stdout)
	}
	// An empty list is not a table with no rows: printing headings over
	// nothing reads as though the command failed to fetch anything.
	if strings.Contains(stdout, "NAME") {
		t.Errorf("headings were printed for an empty list:\n%s", stdout)
	}
}

// The case someone hits first: running a client command with no gateway
// up. It has to be a sentence that says what to do, not a dial error.
func TestClientCommandExplainsThatNoGatewayIsRunning(t *testing.T) {
	address := unusedAddress(t)

	code, _, stderr := execute(t, "servers", "list", "--address", address)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, address) {
		t.Errorf("the message does not say where it looked:\n%s", stderr)
	}
	if !strings.Contains(stderr, "mcp-foyer serve") {
		t.Errorf("the message does not say how to start one:\n%s", stderr)
	}
	// The Go dial error is what this exists to replace.
	if strings.Contains(stderr, "connection refused") || strings.Contains(stderr, "dial tcp") {
		t.Errorf("the raw dial error leaked into the message:\n%s", stderr)
	}
}

// With no --address the gateway is looked for where the configuration
// says it listens, and a port of zero cannot be guessed.
func TestClientCommandReportsAnUndiscoverablePort(t *testing.T) {
	dir := isolated(t)
	cfg := config.Default()
	cfg.Listen.Port = 0
	if err := config.Save(filepath.Join(dir, "config.yaml"), cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	code, _, stderr := execute(t, "servers", "list", "--data-dir", dir)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "--address") {
		t.Errorf("the message does not offer the way out:\n%s", stderr)
	}
}

// Without --address the address comes from the configuration, which is
// what makes the command usable with no arguments at all.
func TestClientCommandFindsTheAddressInTheConfiguration(t *testing.T) {
	dir := isolated(t)
	cfg := config.Default()
	cfg.Listen.Port = freePort(t)
	if err := config.Save(filepath.Join(dir, "config.yaml"), cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	code, _, stderr := execute(t, "servers", "list", "--data-dir", dir)

	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d (nothing is listening there)", code, exitFailure)
	}
	// It looked in the right place, which is the point: the port came
	// from the file rather than from a default.
	if !strings.Contains(stderr, config.Default().Listen.Host) {
		t.Errorf("the message does not name the configured host:\n%s", stderr)
	}
	if !strings.Contains(stderr, strconv.Itoa(cfg.Listen.Port)) {
		t.Errorf("the message does not name the configured port %d:\n%s", cfg.Listen.Port, stderr)
	}
}

// A parent command on its own is an incomplete instruction, and saying
// what the subcommands are beats printing the whole help.
func TestAParentCommandOnItsOwnIsAUsageError(t *testing.T) {
	code, _, stderr := execute(t, "servers")

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "list") {
		t.Errorf("the message does not name the available subcommands:\n%s", stderr)
	}
}

// ===== helpers =====

// rowFor returns the table row whose first column is name.
func rowFor(t *testing.T, output, name string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == name {
			return line
		}
	}
	t.Fatalf("no row for %q in:\n%s", name, output)
	return ""
}

// columnStart returns the offset at which the nth column begins, which is
// what has to match between rows for the table to look aligned.
func columnStart(row string, column int) int {
	seen := 0
	inColumn := false
	for i, r := range row {
		if r == ' ' || r == '\t' {
			inColumn = false
			continue
		}
		if !inColumn {
			inColumn = true
			seen++
			if seen == column {
				return i
			}
		}
	}
	return -1
}

// waitForState blocks until the named server reaches state, because
// connecting to the upstreams happens in the background and a test that
// looked immediately would race it.
func waitForState(t *testing.T, address, server, state string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + address + "/api/servers")
		if err == nil {
			var decoded serverListResponse
			decodeErr := json.NewDecoder(response.Body).Decode(&decoded)
			response.Body.Close()
			if decodeErr == nil {
				for _, row := range decoded.Servers {
					if row.Name != server {
						continue
					}
					last = row.Status.State
					if last == state {
						return
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server %q never reached state %q; last seen %q", server, state, last)
}

// unusedAddress returns an address nothing is listening on, by taking one
// and immediately giving it back.
func unusedAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return address
}

func freePort(t *testing.T) int {
	t.Helper()

	_, port, err := net.SplitHostPort(unusedAddress(t))
	if err != nil {
		t.Fatalf("split the address: %v", err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse the port: %v", err)
	}
	return number
}
