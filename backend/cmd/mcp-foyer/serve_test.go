package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/testmcp"
)

// running starts the serve command on a port the operating system picks
// and returns its base address.
//
// Port zero is what makes these tests safe to run in parallel with
// anything else: a fixed port would collide with a developer's own
// instance.
func running(t *testing.T, adjust func(*config.Config)) (baseURL string, stop func(), finished <-chan error) {
	t.Helper()
	baseURL, _, stop, finished = runningAt(t, adjust)
	return baseURL, stop, finished
}

// runningAt is the same, and also hands back the configuration file, for
// the tests that edit it while the gateway is running.
func runningAt(t *testing.T, adjust func(*config.Config)) (baseURL, cfgPath string, stop func(), finished <-chan error) {
	t.Helper()

	dir := isolated(t)
	cfg := config.Default()
	cfg.Listen.Port = 0
	cfg.Startup.ConnectDelay = 0
	cfg.Startup.MaxRetries = 0
	cfg.MCPServers = map[string]config.MCPServer{}
	if adjust != nil {
		adjust(&cfg)
	}

	path := filepath.Join(dir, "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save the configuration: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)

	go func() {
		done <- serve(ctx, serveOptions{
			DataDir: dir,
			Ready:   func(addr string) { ready <- addr },
		}, io.Discard, io.Discard)
	}()

	select {
	case addr := <-ready:
		return "http://" + addr, path, cancel, done
	case err := <-done:
		cancel()
		t.Fatalf("serve returned before it was listening: %v", err)
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("serve never reported that it was listening")
	}
	return "", "", nil, nil
}

// ===== serving =====

func TestServeAnswersTheHealthEndpoint(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()

	resp, err := http.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var health api.Health
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if health.Status != "ok" {
		t.Errorf("status = %q, want ok", health.Status)
	}
}

// The MCP endpoint is the point of the whole program, so it has to be
// mounted by the command that serves it and not only in tests that
// assemble the stack by hand.
func TestServeMountsTheMCPEndpoint(t *testing.T) {
	base, stop, done := running(t, nil)
	defer func() { stop(); <-done }()

	// A GET without a session is refused by the transport, but the
	// refusal proves the endpoint is there rather than a 404 from the
	// router.
	resp, err := http.Get(base + api.MCPPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Error("the mcp endpoint is not mounted")
	}
}

// The file is an interface of its own. Someone who edits config.yaml
// while the gateway is running should not have to restart it, and this
// is the whole path: the file is replaced by a rename, the running
// instance notices, applies it, and connects what appeared.
func TestServeAdoptsAnEditToTheConfigurationFile(t *testing.T) {
	base, cfgPath, stop, done := runningAt(t, nil)
	defer func() { stop(); <-done }()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load the configuration: %v", err)
	}
	added, err := testmcp.ServerConfig(testmcp.ModeFull)
	if err != nil {
		t.Fatalf("build the upstream configuration: %v", err)
	}
	cfg.MCPServers = map[string]config.MCPServer{"appeared": added}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save the configuration: %v", err)
	}

	// Connected, not merely configured: adopting the file has to reach the
	// connection manager, or the new server would sit there listed and
	// dead.
	waitForState(t, hostPort(t, base), "appeared", "connected")
}

// A configuration file that does not exist yet has to be created, or the
// settings page would have nothing to write back to.
func TestServeCreatesAMissingConfigurationFile(t *testing.T) {
	dir := isolated(t)

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	anyFreePort := 0
	go func() {
		done <- serve(ctx, serveOptions{
			DataDir: dir,
			// There is no configuration file to put a port in — that is
			// the point of this test — so the port comes from here
			// rather than from the default, which a developer running
			// this program is likely to be using.
			Port:  &anyFreePort,
			Ready: func(addr string) { ready <- addr },
		}, io.Discard, io.Discard)
	}()

	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("serve returned early: %v", err)
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("serve never started")
	}
	cancel()
	<-done

	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		t.Errorf("no configuration file was written: %v", err)
	}
}

// ===== graceful shutdown =====

// An interrupt has to be answered promptly. A shutdown that hangs gets
// killed by whatever supervises the process, which is what leaves
// orphaned child processes behind.
func TestShutdownFinishesPromptly(t *testing.T) {
	_, stop, done := running(t, nil)

	stop()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned an error on shutdown: %v", err)
		}
	case <-time.After(shutdownGrace + 20*time.Second):
		t.Fatal("serve did not return after its context was cancelled")
	}
}

// This is the case that makes shutdown slow if the order is wrong: an
// open MCP stream never becomes idle, so waiting for it to finish would
// mean waiting out the whole grace period on every shutdown.
func TestShutdownIsPromptWithAStreamOpen(t *testing.T) {
	base, stop, done := running(t, nil)

	// Open the notification stream and leave it open.
	req, _ := http.NewRequest(http.MethodGet, base+api.MCPPath, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
	}

	started := time.Now()
	stop()

	select {
	case <-done:
		t.Logf("shutdown with a stream open took %v", time.Since(started))
	case <-time.After(shutdownGrace + 20*time.Second):
		t.Fatal("shutdown waited for a stream that would never finish")
	}
}

// Child processes must not outlive the gateway: nothing else knows about
// them, so an orphan would sit there until the machine was restarted.
func TestShutdownStopsTheChildProcesses(t *testing.T) {
	server, err := testmcp.ServerConfig(testmcp.ModeFull)
	if err != nil {
		t.Fatalf("build the test server configuration: %v", err)
	}

	base, stop, done := running(t, func(cfg *config.Config) {
		cfg.MCPServers = map[string]config.MCPServer{"files": server}
	})

	// Wait for the child to be up, which happens in the background.
	var pid int
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && pid == 0 {
		var listed struct {
			Servers []struct {
				Status struct {
					PID   int    `json:"pid"`
					State string `json:"state"`
				} `json:"status"`
			} `json:"servers"`
		}
		resp, err := http.Get(base + "/api/servers")
		if err == nil {
			json.NewDecoder(resp.Body).Decode(&listed)
			resp.Body.Close()
			for _, s := range listed.Servers {
				if s.Status.State == "connected" && s.Status.PID != 0 {
					pid = s.Status.PID
				}
			}
		}
		if pid == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if pid == 0 {
		stop()
		<-done
		t.Fatal("the upstream server never connected")
	}

	stop()
	<-done

	if alive(pid) {
		t.Errorf("the child process %d is still running after shutdown", pid)
	}
}

// Shutting down must not panic, and a panic in a goroutine would take
// the test binary down with it rather than failing one test.
func TestShutdownIsSafeUnderLoad(t *testing.T) {
	base, stop, done := running(t, nil)

	// Keep requests in flight across the shutdown, so it happens while
	// the server is genuinely busy rather than idle.
	var wg sync.WaitGroup
	halt := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-halt:
					return
				default:
				}
				if resp, err := http.Get(base + "/api/health"); err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				} else {
					// Once the listener is gone, requests fail; that is
					// the expected end state, not a failure.
					return
				}
			}
		}()
	}

	time.Sleep(200 * time.Millisecond)
	stop()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned an error: %v", err)
		}
	case <-time.After(shutdownGrace + 20*time.Second):
		t.Fatal("serve did not return")
	}

	close(halt)
	wg.Wait()
}

// The listener has to be released, or restarting immediately — which is
// what a supervisor does — fails to bind.
func TestThePortIsReleasedOnShutdown(t *testing.T) {
	base, stop, done := running(t, nil)
	stop()
	<-done

	// Nothing is listening any more.
	client := http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get(base + "/api/health"); err == nil {
		resp.Body.Close()
		t.Error("the listener is still accepting after shutdown")
	}
}

// ===== configuration failures =====

// A configuration that cannot be served is a startup failure with an
// explanation, not a process that comes up in a broken state.
func TestServeRefusesAnInvalidConfiguration(t *testing.T) {
	dir := isolated(t)

	broken := "version: 1\nlisten:\n  port: 99999\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(broken), 0o600); err != nil {
		t.Fatalf("write the configuration: %v", err)
	}

	err := serve(context.Background(), serveOptions{DataDir: dir}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("serve accepted an invalid configuration")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("error %q does not name the offending field", err)
	}
}

// ===== the address on the command line =====

/*
 * --host and --port.
 *
 * The port override existed before any of this: it was added so that no
 * serve test would bind a fixed port, and its comment said as much — "tests
 * use it". Nothing reached it from the command line, so changing the
 * address a gateway listens on meant editing the configuration file, which
 * is the wrong shape for "just this once, somewhere else".
 *
 * These go through run() rather than calling serve directly, because what
 * was missing was the wiring: a test on serveOptions would have passed
 * before this change.
 */

// commandServing runs the whole command tree the way main does, and waits
// until it reports an address.
//
// The address is read back out of the startup line rather than assumed,
// which is the only honest way to check where a port-zero listener ended up
// — and the same line a person reads for it.
func commandServing(t *testing.T, args ...string) (addr string, stop func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	reported := make(chan string, 1)
	finished := make(chan int, 1)

	go func() {
		defer writer.Close()
		finished <- run(ctx, args, writer, io.Discard)
	}()

	// Draining the pipe is not optional: an undrained writer would block
	// the command being tested at its first line of output.
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			if _, where, found := strings.Cut(scanner.Text(), "listening on http://"); found {
				select {
				case reported <- where:
				default:
				}
			}
		}
	}()

	stop = func() {
		cancel()
		select {
		case code := <-finished:
			if code != exitOK {
				t.Errorf("exit code = %d, want %d", code, exitOK)
			}
		case <-time.After(30 * time.Second):
			t.Error("the command never returned after being interrupted")
		}
	}

	select {
	case where := <-reported:
		return where, stop
	case code := <-finished:
		cancel()
		t.Fatalf("the command exited with %d before it was listening", code)
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("the command never reported an address")
	}
	return "", nil
}

// configuredAt writes a configuration that asks for one particular address,
// so that an override has something to visibly beat.
//
// The port comes from freePort in the servers tests: a number chosen by hand
// would be a fixed port by another name, and this machine very likely has an
// instance of this very program on the default one.
func configuredAt(t *testing.T, host string, port int) string {
	t.Helper()

	dir := isolated(t)
	cfg := config.Default()
	cfg.Listen.Host = host
	cfg.Listen.Port = port
	cfg.Startup.ConnectDelay = 0
	cfg.MCPServers = map[string]config.MCPServer{}

	if err := config.Save(filepath.Join(dir, "config.yaml"), cfg); err != nil {
		t.Fatalf("save the configuration: %v", err)
	}
	return dir
}

func TestThePortFlagOverridesTheConfiguredPort(t *testing.T) {
	configured, wanted := freePort(t), freePort(t)
	dir := configuredAt(t, "127.0.0.1", configured)

	addr, stop := commandServing(t, "serve", "--data-dir", dir, "--port", strconv.Itoa(wanted))
	defer stop()

	if want := "127.0.0.1:" + strconv.Itoa(wanted); addr != want {
		t.Errorf("listening on %s, want %s", addr, want)
	}
}

// A flag says "this run". Writing it back would turn one invocation into a
// permanent change nobody asked for, and the next start without the flag
// would quietly move.
func TestThePortFlagIsNotWrittenBackToTheConfiguration(t *testing.T) {
	configured := freePort(t)
	dir := configuredAt(t, "127.0.0.1", configured)
	path := filepath.Join(dir, "config.yaml")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the configuration: %v", err)
	}

	_, stop := commandServing(t, "serve", "--data-dir", dir, "--port", strconv.Itoa(freePort(t)))
	stop()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the configuration back: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("the configuration file was rewritten:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// And it still asks for the port it always did.
	if !strings.Contains(string(after), strconv.Itoa(configured)) {
		t.Errorf("the configured port %d is gone from the file:\n%s", configured, after)
	}
}

// Serving is what mcp-foyer does with no subcommand, so both spellings have to
// take the flag — including the one where it comes before the subcommand,
// which cobra parses with the subcommand's own flag set.
func TestThePortFlagWorksInEveryPositionThatServes(t *testing.T) {
	for _, tt := range []struct {
		name string
		args func(dir, port string) []string
	}{
		{"after the subcommand", func(dir, port string) []string {
			return []string{"serve", "--data-dir", dir, "--port", port}
		}},
		{"before the subcommand", func(dir, port string) []string {
			return []string{"--data-dir", dir, "--port", port, "serve"}
		}},
		{"with no subcommand at all", func(dir, port string) []string {
			return []string{"--data-dir", dir, "--port", port}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wanted := freePort(t)
			dir := configuredAt(t, "127.0.0.1", freePort(t))

			addr, stop := commandServing(t, tt.args(dir, strconv.Itoa(wanted))...)
			defer stop()

			if want := "127.0.0.1:" + strconv.Itoa(wanted); addr != want {
				t.Errorf("listening on %s, want %s", addr, want)
			}
		})
	}
}

// Zero is a value, not an absence: it asks the operating system for any
// free port. Treating it as "no flag given" would leave the only way to say
// "pick one for me" unavailable from the command line.
func TestPortZeroOnTheCommandLineAsksForAnyFreePort(t *testing.T) {
	configured := freePort(t)
	dir := configuredAt(t, "127.0.0.1", configured)

	addr, stop := commandServing(t, "serve", "--data-dir", dir, "--port", "0")
	defer stop()

	if strings.HasSuffix(addr, ":"+strconv.Itoa(configured)) {
		t.Errorf("listening on %s, which is the configured port — the flag did nothing", addr)
	}
	if strings.HasSuffix(addr, ":0") {
		t.Errorf("listening on %s, which is not a real port", addr)
	}
}

// The host reaches the listener too, checked by giving the file an address
// this machine cannot bind and the flag one it can. Both halves are here on
// purpose: without the first, a passing test would only prove that
// 127.0.0.1 works, which it would have done with no flag at all.
func TestTheHostFlagOverridesTheConfiguredHost(t *testing.T) {
	// Valid as a configuration value and unbindable in fact, which is the
	// combination this needs: it passes validation and then fails to bind.
	const unbindable = "240.0.0.1"

	t.Run("the configured host is what fails without the flag", func(t *testing.T) {
		dir := configuredAt(t, unbindable, freePort(t))

		code, _, stderr := execute(t, "serve", "--data-dir", dir)

		if code != exitFailure {
			t.Errorf("exit code = %d, want %d", code, exitFailure)
		}
		if !strings.Contains(stderr, unbindable) {
			t.Errorf("the failure does not name the host it tried:\n%s", stderr)
		}
	})

	t.Run("the flag replaces it", func(t *testing.T) {
		dir := configuredAt(t, unbindable, freePort(t))

		addr, stop := commandServing(t, "serve", "--data-dir", dir, "--host", "localhost")
		defer stop()

		if strings.HasPrefix(addr, unbindable) {
			t.Errorf("listening on %s, so the flag did nothing", addr)
		}
	})
}

// A value typed on the command line is checked by the same rules as one
// read from the file — and reported as a usage error, because the person who
// typed it is the one who has to change it.
func TestAnImpossibleAddressOnTheCommandLineIsAUsageError(t *testing.T) {
	for _, tt := range []struct {
		name  string
		args  []string
		wants string
	}{
		{"a port above the range", []string{"--port", "70000"}, "--port"},
		{"a negative port", []string{"--port", "-1"}, "--port"},
		{"a host that is a URL", []string{"--host", "http://example.com/mcp"}, "--host"},
		{"an empty host", []string{"--host", ""}, "--host"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := configuredAt(t, "127.0.0.1", freePort(t))

			code, _, stderr := execute(t, append([]string{"serve", "--data-dir", dir}, tt.args...)...)

			if code != exitUsage {
				t.Errorf("exit code = %d, want %d (a usage error)\nstderr: %s", code, exitUsage, stderr)
			}
			// The flag, not `listen.port`: the rule is shared with the
			// configuration file, but the name has to be the one that was
			// typed or the reader goes looking in the wrong place.
			if !strings.Contains(stderr, tt.wants) {
				t.Errorf("the failure does not name %s:\n%s", tt.wants, stderr)
			}
		})
	}
}

// Deliberately not a persistent flag on the root command. The client
// commands talk to a gateway that is already running and say where with
// --address; a --port that sat on them and did nothing would be worse than
// not having one.
func TestTheClientCommandsHaveNoPortFlag(t *testing.T) {
	for _, args := range [][]string{
		{"servers", "list", "--port", "9000"},
		{"tools", "list", "--port", "9000"},
		{"status", "--port", "9000"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, _, stderr := execute(t, args...)

			if code != exitUsage {
				t.Errorf("exit code = %d, want %d (a usage error)\nstderr: %s", code, exitUsage, stderr)
			}
			if !strings.Contains(stderr, "unknown flag") {
				t.Errorf("the failure does not say the flag is unknown:\n%s", stderr)
			}
		})
	}
}

// alive reports whether a process still exists. Signal 0 performs the
// permission and existence checks without delivering anything, which is
// the usual way to ask.
func alive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
