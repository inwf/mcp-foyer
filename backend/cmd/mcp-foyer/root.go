package main

import (
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/spf13/cobra"

	"mcp-foyer/internal/config"
)

// globalOptions are the settings every command shares: where mcp-foyer keeps
// its files, and which configuration to read.
type globalOptions struct {
	DataDir    string
	ConfigPath string
}

// listenOverride holds the address given on the command line, for this run
// only. Whether it was given at all is the flag's own `Changed`, because
// both zeros mean something: port zero asks for any free port.
type listenOverride struct {
	Host string
	Port int
}

// bind declares the override on one command's own flags.
//
// Only the two commands that listen get them — the serve subcommand and
// the root command, which does the same thing with no subcommand typed. Not
// a persistent flag on the root, which would offer `--port` to
// `servers list` as well: the client commands already say where to reach a
// running gateway with `--address`, and a knob that does nothing where it
// appears is worse than no knob.
func (o *listenOverride) bind(cmd *cobra.Command) {
	flags := cmd.Flags()
	flags.StringVar(&o.Host, "host", "",
		"address to listen on for this run, overriding listen.host")
	flags.IntVar(&o.Port, "port", 0,
		"port to listen on for this run, overriding listen.port (0 asks for any free port)")
}

// from reads whichever of the two was actually typed. The command is the
// one that ran, so `mcp-foyer --port 9000` and `mcp-foyer serve --port 9000` are
// each read from their own flag set.
func (o *listenOverride) from(cmd *cobra.Command) (host *string, port *int) {
	if cmd.Flags().Changed("host") {
		host = &o.Host
	}
	if cmd.Flags().Changed("port") {
		port = &o.Port
	}
	return host, port
}

// usageError marks a failure caused by how the command was typed rather
// than by what it went on to do.
//
// Cobra reports both as a plain error, but the two deserve different exit
// codes: a wrapper script should be able to tell "you typed it wrong"
// from "it ran and did not work".
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return &usageError{err: fmt.Errorf(format, args...)}
}

// newRootCommand builds the command tree.
//
// With no subcommand mcp-foyer serves, because that is what it is for; the
// subcommands are the things you occasionally want instead of that.
func newRootCommand(stdout, stderr io.Writer, web fs.FS) *cobra.Command {
	var global globalOptions
	// One override shared by the two commands that listen, so that
	// `mcp-foyer --port 9000 serve` — where the flag is parsed by the
	// subcommand it precedes — reaches the same place as either command's
	// own flag.
	var listen listenOverride

	root := &cobra.Command{
		Use:   "mcp-foyer",
		Short: "One MCP endpoint in front of several upstream servers",
		Long: "mcp-foyer proxies a set of configured upstream MCP servers behind a single\n" +
			"MCP endpoint, and serves a web interface for managing them.\n\n" +
			"With no subcommand it starts serving.",
		Version: version,

		// This package prints the errors and picks the exit code, so cobra
		// is told to do neither. Left to itself it would print every
		// failure identically and always exit 1.
		SilenceUsage:  true,
		SilenceErrors: true,

		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			// Anything left here is a word that matched no subcommand.
			// Naming the alternatives saves a trip to the help text.
			return usagef("unknown command %q; try one of %s",
				args[0], availableCommands(cmd))
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd, &global, &listen, web, stdout, stderr)
		},
	}

	// Help and the version were asked for, so they are output rather than
	// diagnostics and belong on standard output. Everything the user did
	// not ask for goes to standard error.
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("mcp-foyer {{.Version}}\n")

	// A flag the parser rejects is a usage problem like any other, and has
	// to be wrapped to be recognised as one.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &usageError{err: err}
	})

	flags := root.PersistentFlags()
	flags.StringVar(&global.DataDir, "data-dir", "",
		"directory for configuration, logs and runtime state "+
			"(default \"./data\", or $"+config.DataDirEnv+")")
	flags.StringVar(&global.ConfigPath, "config", "",
		"configuration file to read (default <data-dir>/config.yaml)")

	// On the root's own flags rather than its persistent ones: serving is
	// what the root command does, and these belong to serving.
	listen.bind(root)

	root.AddCommand(
		newServeCommand(&global, &listen, web, stdout, stderr),
		newCheckCommand(&global, stdout),
		newConfigCommand(&global, stdout),
		newGuideCommand(stdout),
		newServersCommand(&global, stdout),
		newStatusCommand(&global, stdout),
		newToolsCommand(&global, stdout),
		newUICommand(&global, stdout, openInBrowser()),
		newVersionCommand(stdout),
	)
	return root
}

func newServeCommand(global *globalOptions, listen *listenOverride, web fs.FS, stdout, stderr io.Writer) *cobra.Command {
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve until interrupted",
		Long: "Connect to the configured upstream servers and serve the\n" +
			"aggregated MCP endpoint, the management API and the web interface.\n\n" +
			"This is what mcp-foyer does with no subcommand, so `mcp-foyer` and\n" +
			"`mcp-foyer serve` are the same thing.\n\n" +
			"--host and --port override listen.host and listen.port for this run\n" +
			"without changing the configuration file.",
		Args: noPositionalArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd, global, listen, web, stdout, stderr)
		},
	}
	listen.bind(serve)
	return serve
}

func newVersionCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version and exit",
		Args:  noPositionalArgs,
		RunE: func(*cobra.Command, []string) error {
			fmt.Fprintf(stdout, "mcp-foyer %s\n", version)
			return nil
		},
	}
}

// runServe is shared by the serve subcommand and the root command, which
// do the same thing.
func runServe(cmd *cobra.Command, global *globalOptions, listen *listenOverride, web fs.FS, stdout, stderr io.Writer) error {
	host, port := listen.from(cmd)
	return serve(cmd.Context(), serveOptions{
		DataDir:    global.DataDir,
		ConfigPath: global.ConfigPath,
		WebUI:      web,
		Host:       host,
		Port:       port,
	}, stdout, stderr)
}

// noPositionalArgs rejects leftover words. None of these commands takes
// an argument, and silently ignoring one hides a typo.
func noPositionalArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return usagef("unexpected argument %q", args[0])
}

func availableCommands(cmd *cobra.Command) string {
	var names []string
	for _, child := range cmd.Commands() {
		if child.IsAvailableCommand() {
			names = append(names, child.Name())
		}
	}
	return strings.Join(names, ", ")
}
