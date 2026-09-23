// Command mcp-foyer runs the MCP gateway: it proxies a set of configured
// upstream MCP servers behind a single MCP endpoint, and serves a web
// UI for managing them.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"mcp-foyer/internal/config"
)

// resolveConfigPath decides which configuration file to read, as an
// absolute path so that a later change of working directory cannot
// silently point at a different file.
func resolveConfigPath(paths config.Paths, configFlag string) (string, error) {
	if configFlag == "" {
		return paths.ConfigFile(), nil
	}
	absolute, err := filepath.Abs(configFlag)
	if err != nil {
		return "", fmt.Errorf("resolve config path %q: %w", configFlag, err)
	}
	return absolute, nil
}

// version is the human-readable release version. Plain `go build` already
// stamps the git revision into the binary (readable via runtime/debug), so
// this only needs overriding for tagged release builds, via
// -ldflags "-X main.version=<value>".
var version = "dev"

// Exit codes. Distinguishing usage errors from runtime failures lets a
// wrapper script tell "you typed it wrong" from "it did not work".
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	// The context ends on the first interrupt, which is what starts an
	// orderly shutdown. A second interrupt is left to the default
	// handler, so an operator can always force the point.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run holds everything main does, so that it can be exercised by a test
// without spawning a process or capturing the real standard streams.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := newRootCommand(stdout, stderr, webUI())
	root.SetArgs(args)

	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitOK
	}

	// A command that has already reported the problem itself only needs
	// the exit code. Printing "mcp-foyer: " and an empty message after a
	// list of validation problems would say nothing and look like a
	// second, mysterious failure.
	if errors.Is(err, errSilent) {
		return exitFailure
	}

	fmt.Fprintf(stderr, "mcp-foyer: %v\n", err)

	var usage *usageError
	if errors.As(err, &usage) {
		fmt.Fprintln(stderr, "run \"mcp-foyer --help\" for usage")
		return exitUsage
	}
	return exitFailure
}
