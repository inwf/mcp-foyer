package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/spf13/cobra"

	"mcp-foyer/internal/config"
)

func newConfigCommand(global *globalOptions, stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Work with the configuration file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return usagef("specify a subcommand: %s", availableCommands(cmd))
		},
	}
	cmd.AddCommand(newConfigValidateCommand(global, stdout))
	return cmd
}

func newConfigValidateCommand(global *globalOptions, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "validate [file]",
		Short: "Check a configuration file and report every problem in it",
		Long: "Read a configuration file and report everything wrong with it.\n\n" +
			"Nothing is started and no instance has to be running, so this is what to\n" +
			"run before restarting one: a file that fails here would stop it from\n" +
			"starting at all.\n\n" +
			"With no argument the file this installation uses is checked, which is\n" +
			"what --data-dir and --config select.",
		Args: atMostOneArg("file"),
		RunE: func(_ *cobra.Command, args []string) error {
			path, err := configToCheck(global, args)
			if err != nil {
				return err
			}
			return validateConfig(path, stdout)
		},
	}
}

// configToCheck decides which file to read: the one named on the command
// line, or failing that the one this installation would use.
func configToCheck(global *globalOptions, args []string) (string, error) {
	if len(args) == 1 {
		return filepath.Abs(args[0])
	}

	paths, err := config.ResolveDataDir(global.DataDir)
	if err != nil {
		return "", err
	}
	return resolveConfigPath(paths, global.ConfigPath)
}

func validateConfig(path string, stdout io.Writer) error {
	cfg, err := config.Load(path)

	// A file that is not there is not a broken file. mcp-foyer starts on the
	// defaults and writes one when something is configured, so saying
	// "invalid" here would be wrong.
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stdout, "%s: not there yet; mcp-foyer would start on the defaults\n", path)
		return nil
	}

	// A document that cannot be parsed has no fields to report problems
	// against, so the parser's own message — which points at the line — is
	// the whole answer.
	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return reportProblems(path, err, stdout)
	}

	enabled := 0
	for _, server := range cfg.MCPServers {
		if server.Enabled {
			enabled++
		}
	}
	fmt.Fprintf(stdout, "%s: valid (%s, %d enabled)\n",
		path, count(len(cfg.MCPServers), "server"), enabled)
	return nil
}

// reportProblems lists every problem at once.
//
// Validation deliberately does not stop at the first failure, and the
// output has to preserve that: reporting one problem per run turns
// fixing a file into as many edit-and-rerun cycles as it has mistakes.
func reportProblems(path string, err error, stdout io.Writer) error {
	var invalid *config.ValidationError
	if !errors.As(err, &invalid) {
		return err
	}

	fmt.Fprintf(stdout, "%s: %s\n\n", path, count(len(invalid.Errors), "problem"))

	rows := newTable(stdout)
	for _, problem := range invalid.Errors {
		rows.row("  "+problem.Field, problem.Message)
	}
	rows.flush()

	// The problems are the output, so they go to standard output; this
	// only sets the exit code, and repeating them would print each twice.
	return errSilent
}

// errSilent is returned by a command that has already said everything
// there is to say, and only needs the exit code to be non-zero.
var errSilent = errors.New("")

func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// atMostOneArg names what the extra argument was, which a bare count
// mismatch does not.
func atMostOneArg(what string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) > 1 {
			return usagef("unexpected argument %q; only one %s is taken", args[1], what)
		}
		return nil
	}
}
