package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"mcp-foyer/internal/api"
	"mcp-foyer/internal/config"
	"mcp-foyer/internal/events"
	"mcp-foyer/internal/gateway"
	"mcp-foyer/internal/logging"
	"mcp-foyer/internal/upstream"
	"mcp-foyer/internal/usage"
)

// shutdownGrace is how long in-flight requests have to finish once the
// listener has stopped accepting.
//
// It is deliberately short. The requests that would take longer are the
// long-lived streams, and those are severed on purpose rather than
// waited for: an MCP notification stream never becomes idle, so waiting
// for one means waiting for the whole grace period every time.
const shutdownGrace = 5 * time.Second

// serveOptions is what the serve command was asked to do.
type serveOptions struct {
	DataDir    string
	ConfigPath string

	// WebUI is the built frontend, when one is embedded.
	WebUI fs.FS

	// Ready is called with the address actually being listened on. Tests
	// use it to find a listener bound to port zero.
	Ready func(addr string)

	// Host and Port override the configured listen address when set, for
	// this run only: nothing is written back to the configuration file,
	// because a command-line flag means "this time".
	//
	// Pointers rather than zero values, since both zeros are meaningful —
	// port zero asks the operating system for any free port, and that is
	// how every test here avoids binding a fixed one. The default port is
	// one a developer is likely to have an instance of this very program
	// listening on, and a test that collides with it fails for a reason
	// that has nothing to do with what it was checking.
	Host *string
	Port *int
}

// applyListen folds the command-line overrides into the loaded
// configuration.
//
// The result is validated rather than trusted: the configuration file was
// checked when it was loaded, and a flag arriving afterwards would
// otherwise be the one listen address nobody had looked at. The rules live
// in the config package, so a flag and a file are held to the same ones.
//
// Reported as a usage error, which is what it is — the value was typed on
// the command line, so this run cannot succeed until the person who typed
// it changes it, and a wrapper script should be told that rather than
// "it ran and did not work".
func (o serveOptions) applyListen(cfg *config.Config) error {
	if o.Host == nil && o.Port == nil {
		return nil
	}

	listen := cfg.Listen
	if o.Host != nil {
		listen.Host = *o.Host
	}
	if o.Port != nil {
		listen.Port = *o.Port
	}

	if err := config.ValidateListen(listen); err != nil {
		return usagef("%s", flagWording(err))
	}

	cfg.Listen = listen
	return nil
}

// flagWording restates a listen problem in terms of the flag that carries
// it.
//
// The rule and its message come from the config package, which knows them
// in terms of `listen.port` — the right name in a file, and the wrong one
// for someone who just typed `--port`. Only the name is swapped, so the two
// surfaces cannot drift into explaining the same limit differently.
func flagWording(err error) string {
	var invalid *config.ValidationError
	if !errors.As(err, &invalid) {
		return err.Error()
	}

	flags := map[string]string{"listen.host": "--host", "listen.port": "--port"}
	said := make([]string, 0, len(invalid.Errors))
	for _, problem := range invalid.Errors {
		name, ok := flags[problem.Field]
		if !ok {
			// A new listen field with no flag of its own. Its own name is
			// still better than dropping the problem.
			name = problem.Field
		}
		said = append(said, name+" "+problem.Message)
	}
	return strings.Join(said, "; ")
}

// serve runs the gateway until ctx is cancelled, then shuts down.
func serve(ctx context.Context, opts serveOptions, stdout, stderr io.Writer) error {
	paths, cfgPath, cfg, err := resolve(opts.DataDir, opts.ConfigPath)
	if err != nil {
		return err
	}

	// Before anything is opened or started: a mistyped address should cost
	// nothing, and it must not be discovered after the child processes are
	// running.
	if err := opts.applyListen(&cfg); err != nil {
		return err
	}

	store := logging.NewStore(logging.DefaultCapacity)
	logOpts, err := logging.OptionsFrom(cfg.Logging, paths, store)
	if err != nil {
		return err
	}
	logOpts.Stdout = stderr
	log, err := logging.New(logOpts)
	if err != nil {
		return err
	}
	// Closing the log last is what keeps the shutdown sequence itself
	// visible in the log file.
	defer log.Close()

	cli := log.For(logging.ModuleCLI)
	cli.Info("starting", "version", version, "config", cfgPath, "dataDir", paths.Root())

	// The router's own debug mode prints a route table and a banner to
	// standard output on every start. Which mode to run in is the
	// binary's decision, not the API package's, so it is made here.
	gin.SetMode(gin.ReleaseMode)

	bus := events.NewBus()
	defer bus.Close()

	ups := upstream.NewManager(version, log, bus)
	ups.Apply(cfg)

	configs, err := config.NewManager(cfgPath)
	if err != nil {
		return err
	}

	// The counts are a convenience, so a damaged file is logged and
	// started over rather than being allowed to stop the gateway.
	counts, err := usage.Open(usage.Options{
		Path: paths.UsageFile(),
		OnError: func(err error) {
			cli.Warn("could not save the tool usage counts", "error", err)
		},
	})
	if err != nil {
		cli.Warn("starting the tool usage counts from zero", "error", err)
	}
	defer func() {
		if err := counts.Flush(); err != nil {
			cli.Warn("could not save the tool usage counts", "error", err)
		}
	}()

	g := gateway.New(gateway.Options{
		Version:   version,
		Upstreams: ups,
		Configs:   configs,
		Logger:    log.For(logging.ModuleGateway),
		Gateway:   cfg.Gateway,
		WireDebug: cfg.Logging.MCPWireDebug,
		Usage:     counts,
	})
	g.Sync()
	g.Watch(ctx, bus)

	served, err := api.New(api.Options{
		Version:   version,
		Logger:    log.For(logging.ModuleAPI),
		Security:  cfg.Security,
		MCP:       g.Handler(),
		Configs:   configs,
		Upstreams: ups,
		Gateway:   g,
		Logs:      store,
		Usage:     counts,
		Bus:       bus,
		WebUI:     opts.WebUI,
	})
	if err != nil {
		return err
	}

	// The file is an interface of its own: someone can edit config.yaml
	// while the gateway is running, and until now that took a restart.
	// Applying it goes through the API's own path, so an edit on disk and
	// an edit through the web interface do the same thing.
	configs.Watch(ctx, config.WatchOptions{
		OnChange: func(changes []config.Change) {
			cli.Info("the configuration file changed", "changes", len(changes),
				"fields", fieldsOf(changes))
			served.ApplyConfiguration()
		},
		OnError: func(err error) {
			// Still running on the last good configuration, so this is a
			// warning rather than a failure — but the edit someone just made
			// is not in effect, and only they can fix it.
			cli.Warn("the configuration file changed but could not be loaded; "+
				"the previous configuration is still running", "error", err)
		},
	})

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.Listen.Host, cfg.Listen.Port))
	if err != nil {
		return fmt.Errorf("listen on %s:%d: %w", cfg.Listen.Host, cfg.Listen.Port, err)
	}
	listener = served.Listen(listener)

	httpServer := &http.Server{
		Handler: served.Handler(),
		// A long-lived stream must not be severed by a read timeout, so
		// only the header read is bounded. The connection limit and the
		// idle timeout are what bound a connection that goes quiet.
		ReadHeaderTimeout: cfg.Security.ConnectionTimeout,
		IdleTimeout:       cfg.Security.IdleConnectionTimeout,
	}

	// Connecting happens in the background: a slow upstream must not hold
	// up the endpoint. A client that connects first sees no tools and is
	// told when they arrive, which is what the notification is for.
	go func() {
		ups.ConnectAll(ctx, cfg.Startup)
		g.Sync()
	}()

	addr := listener.Addr().String()
	cli.Info("listening", "addr", addr, "servers", len(cfg.MCPServers))
	fmt.Fprintf(stdout, "mcp-foyer %s listening on http://%s\n", version, addr)
	if opts.Ready != nil {
		opts.Ready(addr)
	}

	failed := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
		close(failed)
	}()

	select {
	case err, bad := <-failed:
		if bad {
			return fmt.Errorf("serve: %w", err)
		}
		// The server stopped on its own without an error, which only
		// happens if something else closed it.
		return nil
	case <-ctx.Done():
		cli.Info("shutting down")
		fmt.Fprintln(stdout, "shutting down")
	}

	shutdown(cli, served, httpServer, ups)
	return nil
}

// fieldsOf names the settings that changed, for the log line. The values
// are left out on purpose: a configuration holds credentials, and the
// interesting part of a change here is which setting moved.
func fieldsOf(changes []config.Change) []string {
	fields := make([]string, 0, len(changes))
	for _, change := range changes {
		fields = append(fields, change.Field)
	}
	return fields
}

// shutdown stops serving and releases everything, in the one order that
// works.
func shutdown(log *slog.Logger, served *api.API, httpServer *http.Server, ups *upstream.Manager) {
	// The event-stream clients go first. They are hijacked connections
	// that the HTTP server does not track, so nothing else would ever
	// close them.
	served.Close()

	// Then stop accepting and let ordinary requests finish. A long-lived
	// MCP stream never becomes idle, so this is expected to reach its
	// deadline whenever one is open; Close then severs what is left.
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Debug("some connections were still open at the deadline", "error", err)
		_ = httpServer.Close()
	}

	// Last, the child processes. Doing this before the HTTP server was
	// down would fail in-flight tool calls that could still have
	// succeeded.
	ups.CloseAll()
	log.Info("stopped")
}

// resolve works out where everything lives and loads the configuration.
func resolve(dataDirFlag, configFlag string) (config.Paths, string, config.Config, error) {
	paths, err := config.ResolveDataDir(dataDirFlag)
	if err != nil {
		return config.Paths{}, "", config.Config{}, err
	}
	if err := paths.Ensure(); err != nil {
		return config.Paths{}, "", config.Config{}, err
	}

	cfgPath, err := resolveConfigPath(paths, configFlag)
	if err != nil {
		return config.Paths{}, "", config.Config{}, err
	}

	cfg, err := config.LoadOrDefault(cfgPath)
	if err != nil {
		return config.Paths{}, "", config.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return config.Paths{}, "", config.Config{}, fmt.Errorf("%s: %w", cfgPath, err)
	}

	// The configuration manager needs a file to write back to, so one is
	// created from the defaults if it is missing.
	if _, statErr := config.Load(cfgPath); errors.Is(statErr, fs.ErrNotExist) {
		if err := config.Save(cfgPath, cfg); err != nil {
			return config.Paths{}, "", config.Config{}, fmt.Errorf("write the initial configuration: %w", err)
		}
	}

	return paths, cfgPath, cfg, nil
}
