package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/testmcp"
)

// TestMain lets this binary stand in for an upstream MCP server, which
// is how a test can start a real child process without depending on
// anything installed on the machine.
func TestMain(m *testing.M) {
	if served, code := testmcp.ServeIfRequested(); served {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// commandTimeout bounds a command run from a test.
//
// It replaces what used to be an already-cancelled context. That was a
// safety net against a test accidentally starting the server and blocking
// forever, and it worked while no command did any real work. The client
// commands do: they make HTTP requests under the command's context, and a
// cancelled one fails them before they start. A deadline keeps the safety
// net without lying about the context.
const commandTimeout = 60 * time.Second

// execute runs the command and returns its exit code and streams.
func execute(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	var out, errOut bytes.Buffer
	code = run(ctx, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// isolated points the data directory at a temporary location and clears
// the environment override, so a test never touches the developer's own
// data directory.
func isolated(t *testing.T) string {
	t.Helper()
	t.Setenv(config.DataDirEnv, "")
	return t.TempDir()
}

// fieldValue pulls "value" out of a "label: value" line of the report.
func fieldValue(t *testing.T, output, label string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) != label {
			continue
		}
		return strings.TrimSpace(value)
	}
	t.Fatalf("no %q line in output:\n%s", label, output)
	return ""
}

func TestVersionFlag(t *testing.T) {
	code, stdout, _ := execute(t, "--version")

	if code != exitOK {
		t.Errorf("exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout, version) {
		t.Errorf("output %q does not contain the version %q", stdout, version)
	}
}

func TestUnknownFlagIsAUsageError(t *testing.T) {
	code, _, stderr := execute(t, "--not-a-flag")

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if stderr == "" {
		t.Error("nothing was written to standard error")
	}
}

func TestAnUnknownCommandIsAUsageError(t *testing.T) {
	code, _, stderr := execute(t, "demolish")

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "demolish") {
		t.Errorf("stderr %q does not name the unknown command", stderr)
	}
	// Naming what is available saves a trip to the help text.
	if !strings.Contains(stderr, "serve") {
		t.Errorf("stderr %q does not list the available commands", stderr)
	}
}

func TestAnUnexpectedArgumentIsAUsageError(t *testing.T) {
	code, _, stderr := execute(t, "check", "leftover")

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "leftover") {
		t.Errorf("stderr %q does not name the unexpected argument", stderr)
	}
}

// Help that was asked for is output, not a diagnostic: it goes to
// standard output so it can be piped to a pager, and standard error
// stays empty. Usage text printed *because of* a mistake is the other
// case, and is covered by the usage-error tests above.
func TestHelpExitsSuccessfully(t *testing.T) {
	code, stdout, stderr := execute(t, "-h")

	if code != exitOK {
		t.Errorf("exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout, "data-dir") {
		t.Errorf("help does not mention --data-dir:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("requested help wrote to standard error:\n%s", stderr)
	}
}

// The help text has to name the subcommands, or there is no way to
// discover them.
func TestHelpListsTheSubcommands(t *testing.T) {
	code, stdout, _ := execute(t, "--help")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	for _, name := range []string{"serve", "check", "version"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("help does not list the %q command:\n%s", name, stdout)
		}
	}
}

// A subcommand carries its own help, which is where anything specific to
// it belongs.
func TestSubcommandHelpIsAvailable(t *testing.T) {
	code, stdout, stderr := execute(t, "serve", "--help")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "serve") {
		t.Errorf("serve --help does not describe the command:\n%s", stdout)
	}
	// The shared flags are declared once on the root and have to reach
	// every subcommand, or they would only work before the subcommand.
	if !strings.Contains(stdout, "data-dir") {
		t.Errorf("serve --help does not offer the inherited --data-dir flag:\n%s", stdout)
	}
}

// The version subcommand and the version flag are two spellings of one
// thing, and must not drift apart.
func TestVersionSubcommandMatchesTheFlag(t *testing.T) {
	codeFlag, fromFlag, _ := execute(t, "--version")
	codeCmd, fromCmd, _ := execute(t, "version")

	if codeFlag != exitOK || codeCmd != exitOK {
		t.Fatalf("exit codes = %d and %d, want %d", codeFlag, codeCmd, exitOK)
	}
	if fromFlag != fromCmd {
		t.Errorf("--version printed %q but the version subcommand printed %q",
			fromFlag, fromCmd)
	}
}

// The shared flags are declared on the root, so they have to work in
// either position — and a reader will try both.
func TestGlobalFlagsWorkBeforeAndAfterTheSubcommand(t *testing.T) {
	dir := isolated(t)

	before, stdout, stderr := execute(t, "--data-dir", dir, "check")
	if before != exitOK {
		t.Fatalf("flag before the subcommand: exit %d\nstderr: %s", before, stderr)
	}
	if got := fieldValue(t, stdout, "data dir"); got != dir {
		t.Errorf("data dir = %q, want %q", got, dir)
	}

	after, stdout, stderr := execute(t, "check", "--data-dir", dir)
	if after != exitOK {
		t.Fatalf("flag after the subcommand: exit %d\nstderr: %s", after, stderr)
	}
	if got := fieldValue(t, stdout, "data dir"); got != dir {
		t.Errorf("data dir = %q, want %q", got, dir)
	}
}

func TestRunsWithoutAConfigFile(t *testing.T) {
	dir := isolated(t)

	code, stdout, stderr := execute(t, "check", "--data-dir", dir)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "not found, using defaults") {
		t.Errorf("output does not say the configuration was missing:\n%s", stdout)
	}
	if got := fieldValue(t, stdout, "listen"); got != "127.0.0.1:7788" {
		t.Errorf("listen = %q, want the default 127.0.0.1:7788", got)
	}
}

// A missing configuration must not be written on startup: the file
// appears when the user configures something, not before.
func TestNoConfigFileIsCreatedOnStartup(t *testing.T) {
	dir := isolated(t)

	if code, _, stderr := execute(t, "check", "--data-dir", dir); code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}

	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err == nil {
		t.Error("a configuration file was created before the user configured anything")
	}
}

func TestReadsAnExistingConfig(t *testing.T) {
	dir := isolated(t)
	cfg := config.Default()
	cfg.Listen.Port = 9123
	cfg.Logging.Level = config.LevelWarn
	cfg.MCPServers = map[string]config.MCPServer{
		"on":  {Transport: config.TransportStdio, Command: "npx", Enabled: true, Timeout: cfg.Security.ConnectionTimeout},
		"off": {Transport: config.TransportStdio, Command: "npx", Enabled: false, Timeout: cfg.Security.ConnectionTimeout},
	}
	if err := config.Save(filepath.Join(dir, "config.yaml"), cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	code, stdout, stderr := execute(t, "check", "--data-dir", dir)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if got := fieldValue(t, stdout, "listen"); got != "127.0.0.1:9123" {
		t.Errorf("listen = %q, want port 9123", got)
	}
	if got := fieldValue(t, stdout, "log level"); got != "warn" {
		t.Errorf("log level = %q, want warn", got)
	}
	if got := fieldValue(t, stdout, "servers"); got != "2 configured, 1 enabled" {
		t.Errorf("servers = %q, want 2 configured and 1 enabled", got)
	}
	if strings.Contains(stdout, "not found") {
		t.Errorf("output claims the configuration is missing:\n%s", stdout)
	}
}

func TestConfigFlagOverridesTheDerivedPath(t *testing.T) {
	dir := isolated(t)
	elsewhere := filepath.Join(t.TempDir(), "custom.yaml")

	cfg := config.Default()
	cfg.Listen.Port = 9124
	if err := config.Save(elsewhere, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	code, stdout, stderr := execute(t, "check", "--data-dir", dir, "--config", elsewhere)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if got := fieldValue(t, stdout, "config"); got != elsewhere {
		t.Errorf("config = %q, want %q", got, elsewhere)
	}
	if got := fieldValue(t, stdout, "listen"); got != "127.0.0.1:9124" {
		t.Errorf("listen = %q, want port 9124; the override was not read", got)
	}
}

func TestDataDirEnvironmentVariableIsUsed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.DataDirEnv, dir)

	code, stdout, stderr := execute(t, "check")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if got := fieldValue(t, stdout, "data dir"); got != dir {
		t.Errorf("data dir = %q, want %q", got, dir)
	}
}

func TestDataDirFlagBeatsTheEnvironment(t *testing.T) {
	fromFlag := t.TempDir()
	t.Setenv(config.DataDirEnv, t.TempDir())

	code, stdout, stderr := execute(t, "check", "--data-dir", fromFlag)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if got := fieldValue(t, stdout, "data dir"); got != fromFlag {
		t.Errorf("data dir = %q, want the flag value %q", got, fromFlag)
	}
}

// With nothing specified, everything lands beside the project rather
// than in the user's home directory.
func TestDefaultDataDirIsInTheWorkingDirectory(t *testing.T) {
	t.Setenv(config.DataDirEnv, "")
	wd := t.TempDir()
	t.Chdir(wd)

	code, stdout, stderr := execute(t, "check")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if got, want := fieldValue(t, stdout, "data dir"), filepath.Join(wd, "data"); got != want {
		t.Errorf("data dir = %q, want %q", got, want)
	}
}

// The hard constraint: nothing mcp-foyer writes may escape the data
// directory. This asserts it on the paths the command actually reports,
// not just on the ones a unit test knows to ask about.
func TestEveryReportedPathIsInsideTheDataDir(t *testing.T) {
	dir := isolated(t)

	code, stdout, stderr := execute(t, "check", "--data-dir", dir)
	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}

	for _, label := range []string{"config", "log dir", "log file"} {
		path := fieldValue(t, stdout, label)
		// The report may append a note after the path.
		path = strings.Fields(path)[0]

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			t.Errorf("%s (%q) is not relative to the data dir: %v", label, path, err)
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("%s resolves to %q, outside the data dir %q", label, path, dir)
		}
	}
}

func TestLogDirectoryIsCreated(t *testing.T) {
	dir := isolated(t)

	if code, _, stderr := execute(t, "check", "--data-dir", dir); code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}

	info, err := os.Stat(filepath.Join(dir, "logs"))
	if err != nil {
		t.Fatalf("log directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("the log path is not a directory")
	}
}

// The self-check writes a record, which proves the file destination is
// actually wired up rather than merely constructed.
func TestStartupIsRecordedInTheLogFile(t *testing.T) {
	dir := isolated(t)

	if code, _, stderr := execute(t, "check", "--data-dir", dir); code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}

	data, err := os.ReadFile(filepath.Join(dir, "logs", "mcp-foyer.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "startup self-check") {
		t.Errorf("the log file does not record the startup:\n%s", data)
	}
}

// Log records must not be mixed into the report, which is what a caller
// would parse or read.
func TestReportIsNotPollutedByLogOutput(t *testing.T) {
	dir := isolated(t)

	code, stdout, _ := execute(t, "check", "--data-dir", dir)
	if code != exitOK {
		t.Fatalf("exit code = %d", code)
	}
	if strings.Contains(stdout, "level=") || strings.Contains(stdout, "startup self-check") {
		t.Errorf("log records leaked into the report:\n%s", stdout)
	}
}

// Running mcp-foyer with no subcommand serves, which is the behaviour the
// command tree has to preserve.
//
// Serving cannot be started here without binding a port, so this asserts
// it through a configuration both paths reject: if the bare command took
// a different path it would not fail, or would fail differently.
func TestNoSubcommandTakesTheSamePathAsServe(t *testing.T) {
	write := func(t *testing.T) string {
		t.Helper()
		dir := isolated(t)
		path := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(path, []byte("version: 1\nlisten:\n  port: 70000\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		return dir
	}

	bareCode, _, bareErr := execute(t, "--data-dir", write(t))
	serveCode, _, serveErr := execute(t, "serve", "--data-dir", write(t))

	if bareCode != exitFailure {
		t.Errorf("bare command exit = %d, want %d", bareCode, exitFailure)
	}
	if serveCode != bareCode {
		t.Errorf("exit codes differ: bare = %d, serve = %d", bareCode, serveCode)
	}
	if !strings.Contains(bareErr, "listen.port") {
		t.Errorf("bare command did not report the invalid field:\n%s", bareErr)
	}
	// The temporary directories differ, so compare the part that does not
	// name a path.
	if trim, trimServe := afterLastPathSep(bareErr), afterLastPathSep(serveErr); trim != trimServe {
		t.Errorf("the two paths failed differently:\n bare: %s\nserve: %s", trim, trimServe)
	}
}

// afterLastPathSep drops everything up to the last path separator, which
// is what varies between two runs in different temporary directories.
func afterLastPathSep(s string) string {
	if i := strings.LastIndex(s, string(filepath.Separator)); i >= 0 {
		return s[i+1:]
	}
	return s
}

func TestInvalidConfigIsRejectedWithTheFieldNamed(t *testing.T) {
	dir := isolated(t)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nlisten:\n  port: 70000\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, _, stderr := execute(t, "check", "--data-dir", dir)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "listen.port") {
		t.Errorf("stderr %q does not name the offending field", stderr)
	}
}

func TestMalformedConfigIsRejectedWithTheLineNumber(t *testing.T) {
	dir := isolated(t)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("listen:\n  port: not-a-number\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, _, stderr := execute(t, "check", "--data-dir", dir)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "2") {
		t.Errorf("stderr %q does not point at the offending line", stderr)
	}
}

// An explicit --config that does not exist is a mistake worth reporting,
// unlike the derived path simply not being there yet.
func TestMissingExplicitConfigStillFallsBackToDefaults(t *testing.T) {
	dir := isolated(t)
	missing := filepath.Join(t.TempDir(), "nope.yaml")

	code, stdout, stderr := execute(t, "check", "--data-dir", dir, "--config", missing)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "not found, using defaults") {
		t.Errorf("output does not report the missing file:\n%s", stdout)
	}
}
