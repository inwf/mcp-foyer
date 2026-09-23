package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/testmcp"
)

// storedConfig is the server as the gateway persisted it. Reading it back
// is what proves a change was written rather than merely accepted.
//
// The durations are strings because that is how the API reports them, and
// asserting on that is the point: a CLI that re-encoded them as numbers
// would break the configuration file.
type storedConfig struct {
	Transport   string            `json:"transport"`
	Enabled     bool              `json:"enabled"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	URL         string            `json:"url"`
	Description string            `json:"description"`
	Timeout     string            `json:"timeout"`
}

func storedServer(t *testing.T, address, name string) storedConfig {
	t.Helper()

	response, err := http.Get("http://" + address + "/api/servers/" + name)
	if err != nil {
		t.Fatalf("fetch %s: %v", name, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("fetching %s answered %s", name, response.Status)
	}
	var view struct {
		Config storedConfig `json:"config"`
	}
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return view.Config
}

// requireListed fails unless the named server appears in the list, which
// is the user-visible evidence that it was added.
func requireListed(t *testing.T, address, name string) {
	t.Helper()

	code, stdout, stderr := execute(t, "servers", "list", "--address", address)
	if code != exitOK {
		t.Fatalf("servers list: exit %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, name) {
		t.Fatalf("the server %q is not in the list:\n%s", name, stdout)
	}
}

func TestServersAddCreatesAStdioServer(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()
	address := hostPort(t, base)

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}

	code, stdout, stderr := execute(t, "servers", "add", "probe",
		"--address", address,
		"--wait", "0",
		"--description", "the test server",
		"--env", testmcp.ModeEnv+"="+testmcp.ModeFull,
		"--", self)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "stdio") {
		t.Errorf("the transport was not inferred from the command:\n%s", stdout)
	}
	requireListed(t, address, "probe")
}

// The command's own flags must reach the child rather than being eaten by
// mcp-foyer, which is the whole reason they go after "--".
func TestServersAddPassesTheCommandArgumentsThrough(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()
	address := hostPort(t, base)

	code, _, stderr := execute(t, "servers", "add", "flagged",
		"--address", address, "--wait", "0",
		"--", "some-command", "--verbose", "-y", "--url", "not-our-flag")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}

	stored := storedServer(t, address, "flagged")
	if stored.Command != "some-command" {
		t.Errorf("command = %q, want %q", stored.Command, "some-command")
	}
	want := []string{"--verbose", "-y", "--url", "not-our-flag"}
	if strings.Join(stored.Args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", stored.Args, want)
	}
}

func TestServersAddCreatesAnHTTPServer(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()
	address := hostPort(t, base)

	code, stdout, stderr := execute(t, "servers", "add", "remote",
		"--address", address, "--wait", "0", "--disabled",
		"--url", "https://example.com/mcp",
		"--header", "Authorization=Bearer swordfish")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "streamable-http") {
		t.Errorf("the transport was not inferred from --url:\n%s", stdout)
	}

	stored := storedServer(t, address, "remote")
	if stored.URL != "https://example.com/mcp" {
		t.Errorf("url = %q", stored.URL)
	}
}

// A name that is already taken must be refused: replacing the existing
// server would silently discard whatever was there.
func TestServersAddRefusesADuplicateName(t *testing.T) {
	base, stop, done := running(t, func(cfg *config.Config) {
		cfg.MCPServers = map[string]config.MCPServer{
			"taken": {Transport: config.TransportStdio, Command: "npx", Timeout: cfg.Security.ConnectionTimeout},
		}
	})
	defer func() { stop(); <-done }()

	code, _, stderr := execute(t, "servers", "add", "taken",
		"--address", hostPort(t, base), "--wait", "0", "--", "npx")

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "taken") {
		t.Errorf("the message does not name the conflicting server:\n%s", stderr)
	}
}

// The gateway's validation is the one that matters, and its per-field
// messages have to survive the trip back through the CLI.
func TestServersAddSurfacesTheGatewaysFieldErrors(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()

	code, _, stderr := execute(t, "servers", "add", "odd",
		"--address", hostPort(t, base), "--wait", "0",
		"--transport", "carrier-pigeon", "--", "npx")

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "carrier-pigeon") {
		t.Errorf("the rejected value is not quoted back:\n%s", stderr)
	}
	if !strings.Contains(stderr, "transport") {
		t.Errorf("the offending field is not named:\n%s", stderr)
	}
}

func TestServersAddRejectsAnInvalidName(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()

	code, _, stderr := execute(t, "servers", "add", "has spaces",
		"--address", hostPort(t, base), "--wait", "0", "--", "npx")

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if stderr == "" {
		t.Error("nothing was written to standard error")
	}
}

// One of the two ways of naming a server is required, and giving both is
// a contradiction rather than something to resolve by precedence.
func TestServersAddNeedsExactlyOneOfCommandAndURL(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()
	address := hostPort(t, base)

	neither, _, neitherErr := execute(t, "servers", "add", "empty", "--address", address)
	if neither != exitUsage {
		t.Errorf("with neither: exit = %d, want %d", neither, exitUsage)
	}
	if !strings.Contains(neitherErr, "--url") {
		t.Errorf("the message does not say what is missing:\n%s", neitherErr)
	}

	both, _, bothErr := execute(t, "servers", "add", "both",
		"--address", address, "--url", "https://example.com/mcp", "--", "npx")
	if both != exitUsage {
		t.Errorf("with both: exit = %d, want %d", both, exitUsage)
	}
	if !strings.Contains(bothErr, "--url") {
		t.Errorf("the message does not name the conflict:\n%s", bothErr)
	}
}

func TestServersAddReadsADefinitionFromAFile(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()
	address := hostPort(t, base)

	path := filepath.Join(t.TempDir(), "server.yaml")
	definition := "transport: streamable-http\n" +
		"url: https://example.com/mcp\n" +
		"enabled: false\n" +
		"description: from a file\n" +
		"timeout: 45s\n"
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatalf("write the definition: %v", err)
	}

	code, _, stderr := execute(t, "servers", "add", "fromfile",
		"--address", address, "--wait", "0", "--from-file", path)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}

	stored := storedServer(t, address, "fromfile")
	if stored.URL != "https://example.com/mcp" {
		t.Errorf("url = %q", stored.URL)
	}
	// A duration written the way the configuration file writes it has to
	// survive the trip, which is what would break if the CLI re-encoded
	// it as a number.
	if stored.Timeout != "45s" {
		t.Errorf("timeout = %q, want %q", stored.Timeout, "45s")
	}
}

// A key the schema does not have is a typo, and the gateway rejects it
// rather than ignoring it. The CLI must not swallow it first by parsing
// the file into a struct of its own.
func TestServersAddRejectsAnUnknownKeyInAFile(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()

	path := filepath.Join(t.TempDir(), "server.yaml")
	definition := "transport: stdio\ncommand: npx\ncommnad: npx\n"
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatalf("write the definition: %v", err)
	}

	code, _, stderr := execute(t, "servers", "add", "typo",
		"--address", hostPort(t, base), "--wait", "0", "--from-file", path)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "commnad") {
		t.Errorf("the unknown key is not named:\n%s", stderr)
	}
}

// Adding a server that cannot start is the failure worth catching, and
// the gateway connects in the background — so the command has to wait to
// be able to say anything about it.
func TestServersAddReportsAServerThatDoesNotStart(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()

	code, stdout, stderr := execute(t, "servers", "add", "broken",
		"--address", hostPort(t, base),
		"--", "definitely-not-a-real-command")

	// It is in the configuration and will be retried, so this is a
	// warning rather than a failure: there is nothing to undo.
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "did not start") {
		t.Errorf("the output does not report the failure:\n%s", stdout)
	}
	if !strings.Contains(stdout, "definitely-not-a-real-command") {
		t.Errorf("the output does not say why:\n%s", stdout)
	}
}

func TestServersAddReportsAServerThatConnects(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}

	code, stdout, stderr := execute(t, "servers", "add", "probe",
		"--address", hostPort(t, base),
		"--env", testmcp.ModeEnv+"="+testmcp.ModeFull,
		"--", self)

	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "connected") {
		t.Errorf("the output does not report the connection:\n%s", stdout)
	}
	if !strings.Contains(stdout, "tools") {
		t.Errorf("the output does not say what it found:\n%s", stdout)
	}
}

// ===== key=value parsing =====

func TestKeyValueFlagsSplitAtTheFirstSeparatorOnly(t *testing.T) {
	parsed, err := parseKeyValues("--header", []string{"Authorization=Bearer a=b=c"})
	if err != nil {
		t.Fatalf("parseKeyValues: %v", err)
	}
	if got, want := parsed["Authorization"], "Bearer a=b=c"; got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
}

func TestKeyValueFlagsRejectAMissingSeparator(t *testing.T) {
	if _, err := parseKeyValues("--env", []string{"JUST_A_NAME"}); err == nil {
		t.Fatal("parseKeyValues accepted a value with no separator")
	}
}
