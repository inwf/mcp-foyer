package logging_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/logging"
)

func TestParseLevel(t *testing.T) {
	cases := map[config.LogLevel]slog.Level{
		config.LevelDebug: slog.LevelDebug,
		config.LevelInfo:  slog.LevelInfo,
		config.LevelWarn:  slog.LevelWarn,
		config.LevelError: slog.LevelError,
	}
	for name, want := range cases {
		got, err := logging.ParseLevel(name)
		if err != nil {
			t.Errorf("ParseLevel(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", name, got, want)
		}
	}

	if _, err := logging.ParseLevel("verbose"); err == nil {
		t.Error("ParseLevel accepted an unknown level")
	}
}

func TestLevelFiltering(t *testing.T) {
	var out bytes.Buffer
	log, err := logging.New(logging.Options{Level: slog.LevelWarn, Stdout: &out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	log.Debug("a debug line")
	log.Info("an info line")
	log.Warn("a warning line")
	log.Error("an error line")

	text := out.String()
	for _, dropped := range []string{"a debug line", "an info line"} {
		if strings.Contains(text, dropped) {
			t.Errorf("record below the level was written: %q\n%s", dropped, text)
		}
	}
	for _, kept := range []string{"a warning line", "an error line"} {
		if !strings.Contains(text, kept) {
			t.Errorf("record at or above the level was dropped: %q\n%s", kept, text)
		}
	}
}

func TestJSONFormatIsParseable(t *testing.T) {
	var out bytes.Buffer
	log, err := logging.New(logging.Options{
		Level:  slog.LevelInfo,
		Format: logging.FormatJSON,
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	log.For(logging.ModuleGateway).Info("started", "port", 7788)

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &record); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if record["msg"] != "started" {
		t.Errorf("msg = %v, want started", record["msg"])
	}
	if record[logging.AttrModule] != logging.ModuleGateway {
		t.Errorf("module = %v, want %s", record[logging.AttrModule], logging.ModuleGateway)
	}
}

func TestConsoleFormatIsTheDefault(t *testing.T) {
	var out bytes.Buffer
	log, err := logging.New(logging.Options{Level: slog.LevelInfo, Stdout: &out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	log.Info("hello")

	if json.Valid(bytes.TrimSpace(out.Bytes())) {
		t.Errorf("the default format produced JSON:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("message missing from output:\n%s", out.String())
	}
}

func TestModuleTagging(t *testing.T) {
	store := logging.NewStore(10)
	log, err := logging.New(logging.Options{Level: slog.LevelInfo, Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	log.For(logging.ModuleAPI).Info("request served")
	log.ForServer(logging.ModuleUpstream, "files").Info("connected")

	entries := store.Query(logging.Query{})
	if len(entries) != 2 {
		t.Fatalf("retained %d entries, want 2", len(entries))
	}
	if entries[0].Module != logging.ModuleAPI || entries[0].Server != "" {
		t.Errorf("first entry = module %q server %q, want module api with no server",
			entries[0].Module, entries[0].Server)
	}
	if entries[1].Module != logging.ModuleUpstream || entries[1].Server != "files" {
		t.Errorf("second entry = module %q server %q, want upstream/files",
			entries[1].Module, entries[1].Server)
	}
}

// Every destination must see the same records; a file sink must not
// swallow what standard output shows.
func TestRecordsReachEveryDestination(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	store := logging.NewStore(10)

	log, err := logging.New(logging.Options{
		Level:     slog.LevelInfo,
		Stdout:    &out,
		Store:     store,
		FilePath:  filepath.Join(dir, "mcp-foyer.log"),
		MaxSizeMB: 10,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	log.Info("everywhere")
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !strings.Contains(out.String(), "everywhere") {
		t.Error("record missing from standard output")
	}
	fileText := readFile(t, filepath.Join(dir, "mcp-foyer.log"))
	if !strings.Contains(fileText, "everywhere") {
		t.Errorf("record missing from the log file:\n%s", fileText)
	}
	if entries := store.Query(logging.Query{}); len(entries) != 1 {
		t.Errorf("store retained %d entries, want 1", len(entries))
	}
}

// The whole-program level is a blunt instrument: at debug it carries
// every upstream server's chatter and every HTTP request, and the thing
// being looked for goes past in the middle of it. One module can be
// followed instead.
func TestOneModuleCanBeFollowedInDetail(t *testing.T) {
	var out bytes.Buffer
	log, err := logging.New(logging.Options{
		Level:        slog.LevelInfo,
		Stdout:       &out,
		ModuleLevels: map[string]slog.Level{logging.ModuleGateway: slog.LevelDebug},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	log.For(logging.ModuleGateway).Debug("a gateway detail")
	log.For(logging.ModuleUpstream).Debug("an upstream detail")
	log.For(logging.ModuleUpstream).Info("an upstream event")

	got := out.String()
	if !strings.Contains(got, "a gateway detail") {
		t.Errorf("the followed module's debug record is missing:\n%s", got)
	}
	if strings.Contains(got, "an upstream detail") {
		t.Errorf("another module's debug record came through:\n%s", got)
	}
	// The rest of the program is unaffected: it still logs from info up.
	if !strings.Contains(got, "an upstream event") {
		t.Errorf("another module's info record was lost:\n%s", got)
	}
}

// The switch has to reach what a server-tagged logger writes too, which
// is a second layer of WithAttrs over the same handler.
func TestFollowingAModuleReachesItsServerLoggers(t *testing.T) {
	var out bytes.Buffer
	log, err := logging.New(logging.Options{
		Level:        slog.LevelInfo,
		Stdout:       &out,
		ModuleLevels: map[string]slog.Level{logging.ModuleUpstream: slog.LevelDebug},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	log.ForServer(logging.ModuleUpstream, "files").Debug("a detail about one server")

	if got := out.String(); !strings.Contains(got, "a detail about one server") {
		t.Errorf("the record is missing:\n%s", got)
	}
}

// Hiding the correlation ids shortens a line for reading. It is a
// display choice, so what the log viewer holds must not change: it
// filters on those attributes.
func TestTraceContextCanBeLeftOutOfTheOutputOnly(t *testing.T) {
	var out bytes.Buffer
	store := logging.NewStore(10)
	log, err := logging.New(logging.Options{
		Level:            slog.LevelInfo,
		Stdout:           &out,
		Store:            store,
		HideTraceContext: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	log.Info("a request was served", "requestId", "abc123", "server", "files")

	if got := out.String(); strings.Contains(got, "abc123") {
		t.Errorf("the request id is still in the output:\n%s", got)
	}
	// Only the correlation ids go: everything else is what the line is for.
	if got := out.String(); !strings.Contains(got, "files") {
		t.Errorf("an unrelated attribute was dropped too:\n%s", got)
	}

	entries := store.Query(logging.Query{})
	if len(entries) != 1 {
		t.Fatalf("the store holds %d records, want 1", len(entries))
	}
	if !strings.Contains(fmt.Sprint(entries[0].Attrs), "abc123") {
		t.Errorf("the store lost the request id: %v", entries[0].Attrs)
	}
}

func TestTraceContextIsShownByDefault(t *testing.T) {
	var out bytes.Buffer
	log, err := logging.New(logging.Options{Level: slog.LevelInfo, Stdout: &out})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	log.Info("a request was served", "requestId", "abc123")

	if got := out.String(); !strings.Contains(got, "abc123") {
		t.Errorf("the request id is missing:\n%s", got)
	}
}

func TestOptionsFromConfig(t *testing.T) {
	paths, err := config.ResolveDataDir(t.TempDir())
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}
	cfg := config.Default().Logging
	cfg.Level = config.LevelWarn
	cfg.Format = logging.FormatJSON

	opts, err := logging.OptionsFrom(cfg, paths, nil)
	if err != nil {
		t.Fatalf("OptionsFrom: %v", err)
	}

	if opts.Level != slog.LevelWarn {
		t.Errorf("level = %v, want warn", opts.Level)
	}
	if opts.FilePath != paths.LogFile() {
		t.Errorf("file = %q, want %q", opts.FilePath, paths.LogFile())
	}
	if opts.MaxAge != cfg.MaxAge || opts.MaxSizeMB != cfg.MaxSizeMB {
		t.Errorf("retention = %v/%dMB, want %v/%dMB",
			opts.MaxAge, opts.MaxSizeMB, cfg.MaxAge, cfg.MaxSizeMB)
	}
	// The trace context is on unless someone turns it off, which is why
	// the option is the negative of the setting.
	if opts.HideTraceContext {
		t.Error("the correlation ids are hidden by default")
	}
	if len(opts.ModuleLevels) != 0 {
		t.Errorf("moduleLevels = %v, want none without gatewayDebug", opts.ModuleLevels)
	}
}

// The two debug switches are per-layer on purpose: turning one on must
// not turn the level down for everything.
func TestOptionsFromConfigCarriesTheDebugSwitches(t *testing.T) {
	paths, err := config.ResolveDataDir(t.TempDir())
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}
	cfg := config.Default().Logging
	cfg.GatewayDebug = true
	cfg.ShowTraceContext = false

	opts, err := logging.OptionsFrom(cfg, paths, nil)
	if err != nil {
		t.Fatalf("OptionsFrom: %v", err)
	}

	if got := opts.ModuleLevels[logging.ModuleGateway]; got != slog.LevelDebug {
		t.Errorf("the gateway's level = %v, want debug", got)
	}
	if opts.Level != slog.LevelInfo {
		t.Errorf("the whole-program level became %v; gatewayDebug is not a global switch", opts.Level)
	}
	if !opts.HideTraceContext {
		t.Error("showTraceContext: false did not reach the logger")
	}
}

func TestOptionsFromRejectsAnUnknownLevel(t *testing.T) {
	paths, err := config.ResolveDataDir(t.TempDir())
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}
	cfg := config.Default().Logging
	cfg.Level = "chatty"

	if _, err := logging.OptionsFrom(cfg, paths, nil); err == nil {
		t.Error("OptionsFrom accepted an unknown level")
	}
}

func TestCloseWithoutAFileIsHarmless(t *testing.T) {
	log, err := logging.New(logging.Options{Level: slog.LevelInfo, Stdout: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// A logger must be usable from many goroutines, which is the normal case
// once upstream connections are running.
func TestConcurrentLogging(t *testing.T) {
	store := logging.NewStore(500)
	log, err := logging.New(logging.Options{
		Level:     slog.LevelInfo,
		Store:     store,
		FilePath:  filepath.Join(t.TempDir(), "mcp-foyer.log"),
		MaxSizeMB: 10,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	done := make(chan struct{})
	for w := 0; w < 8; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			l := log.ForServer(logging.ModuleUpstream, "srv")
			for i := 0; i < 50; i++ {
				l.Info("working", "worker", w, "round", i)
			}
		}(w)
	}
	for w := 0; w < 8; w++ {
		<-done
	}

	if got := len(store.Query(logging.Query{})); got != 400 {
		t.Errorf("retained %d entries, want 400", got)
	}
}
