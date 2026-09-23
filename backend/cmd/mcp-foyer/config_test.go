package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-foyer/internal/config"
)

// writeConfig puts a document at a temporary path and returns it.
func writeConfig(t *testing.T, document string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write the configuration: %v", err)
	}
	return path
}

func TestConfigValidateAcceptsAGoodFile(t *testing.T) {
	dir := isolated(t)
	path := filepath.Join(dir, "config.yaml")

	cfg := config.Default()
	cfg.MCPServers = map[string]config.MCPServer{
		"on":  {Transport: config.TransportStdio, Command: "npx", Enabled: true, Timeout: cfg.Security.ConnectionTimeout},
		"off": {Transport: config.TransportStdio, Command: "npx", Enabled: false, Timeout: cfg.Security.ConnectionTimeout},
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	code, stdout, stderr := execute(t, "config", "validate", path)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "valid") {
		t.Errorf("output does not say the file is valid:\n%s", stdout)
	}
	if !strings.Contains(stdout, "2 servers, 1 enabled") {
		t.Errorf("output does not summarise the servers:\n%s", stdout)
	}
}

// Reporting one problem per run turns fixing a file into as many
// edit-and-rerun cycles as it has mistakes, so every problem has to come
// out at once.
func TestConfigValidateReportsEveryProblemAtOnce(t *testing.T) {
	path := writeConfig(t, ""+
		"version: 1\n"+
		"listen:\n"+
		"  port: 70000\n"+
		"logging:\n"+
		"  level: chatty\n"+
		"mcpServers:\n"+
		"  broken:\n"+
		"    transport: stdio\n"+
		"    url: https://example.com/mcp\n")

	code, stdout, _ := execute(t, "config", "validate", path)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}

	// Each of these is a separate mistake, and all of them must appear.
	for _, want := range []string{
		"listen.port",
		"logging.level",
		"mcpServers.broken.command",
		"mcpServers.broken.url",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not mention %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "problems") {
		t.Errorf("the report does not count the problems:\n%s", stdout)
	}
}

// The problems are the command's output, so they belong on standard
// output — and must not also be repeated as an error afterwards.
func TestConfigValidateDoesNotPrintTheProblemsTwice(t *testing.T) {
	path := writeConfig(t, "version: 1\nlisten:\n  port: 70000\n")

	code, stdout, stderr := execute(t, "config", "validate", path)

	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stdout, "listen.port") {
		t.Fatalf("the problem is missing from standard output:\n%s", stdout)
	}
	if strings.Contains(stderr, "listen.port") {
		t.Errorf("the problem was printed a second time on standard error:\n%s", stderr)
	}
	if strings.Contains(stderr, "mcp-foyer: \n") || stderr == "mcp-foyer: \n" {
		t.Errorf("an empty error message was printed:\n%q", stderr)
	}
}

// A document that cannot be parsed has no fields to report against, so
// the parser's message — which points at the line — is the answer.
func TestConfigValidateReportsTheLineOfASyntaxError(t *testing.T) {
	path := writeConfig(t, "listen:\n  port: not-a-number\n")

	code, _, stderr := execute(t, "config", "validate", path)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "2") {
		t.Errorf("the message does not point at the offending line:\n%s", stderr)
	}
}

// A misspelled key is the mistake strict parsing exists to catch, and the
// offline check has to catch it too.
func TestConfigValidateRejectsAnUnknownKey(t *testing.T) {
	path := writeConfig(t, "version: 1\nlisten:\n  prot: 7788\n")

	code, _, stderr := execute(t, "config", "validate", path)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "prot") {
		t.Errorf("the misspelled key is not named:\n%s", stderr)
	}
}

// A file that is not there is not a broken file: mcp-foyer starts on the
// defaults and writes one when something is configured.
func TestConfigValidateOnAMissingFileIsNotAFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")

	code, stdout, stderr := execute(t, "config", "validate", missing)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "defaults") {
		t.Errorf("output does not say what would happen instead:\n%s", stdout)
	}
}

// With no argument the file this installation uses is checked, which is
// what makes the command useful with nothing typed after it.
func TestConfigValidateWithNoArgumentChecksTheInstallation(t *testing.T) {
	dir := isolated(t)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nlisten:\n  port: 70000\n"), 0o600); err != nil {
		t.Fatalf("write the configuration: %v", err)
	}

	code, stdout, _ := execute(t, "config", "validate", "--data-dir", dir)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stdout, path) {
		t.Errorf("the report does not name the file it checked:\n%s", stdout)
	}
}

// Nothing has to be running for this, which is the point: it is what you
// run before restarting a gateway.
func TestConfigValidateNeedsNoGateway(t *testing.T) {
	dir := isolated(t)
	cfg := config.Default()
	// A port nothing is listening on, to make the absence explicit.
	cfg.Listen.Port = freePort(t)
	path := filepath.Join(dir, "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	code, stdout, stderr := execute(t, "config", "validate", "--data-dir", dir)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "valid") {
		t.Errorf("output does not say the file is valid:\n%s", stdout)
	}
	if strings.Contains(stderr, "listening") {
		t.Errorf("the command tried to reach a gateway:\n%s", stderr)
	}
}

func TestConfigValidateTakesOnlyOneFile(t *testing.T) {
	code, _, stderr := execute(t, "config", "validate", "one.yaml", "two.yaml")

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "two.yaml") {
		t.Errorf("the message does not name the extra argument:\n%s", stderr)
	}
}
