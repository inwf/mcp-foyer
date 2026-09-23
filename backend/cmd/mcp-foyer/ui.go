package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

// browserOpener launches a URL in whatever the system considers the
// default browser. It is a variable so that tests can watch what would
// have been opened; opening a real browser during a test run is not
// something a test suite should do.
type browserOpener func(ctx context.Context, url string) error

func newUICommand(global *globalOptions, stdout io.Writer, open browserOpener) *cobra.Command {
	client := &clientOptions{global: global}
	var printOnly bool

	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the web interface in a browser",
		Long: "Open the running instance's web interface.\n\n" +
			"The address comes from the configuration unless --address says\n" +
			"otherwise. The URL is printed either way, so that this is still\n" +
			"useful where no browser can be opened — over ssh, or in a container.",
		Args: noPositionalArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gateway, err := client.connect()
			if err != nil {
				return err
			}

			// The gateway is asked whether it is there before a browser is
			// opened. A window on a refused connection is a worse answer
			// than a sentence saying nothing is running, and this is the
			// same check every other client command makes.
			var health struct {
				Status string `json:"status"`
			}
			if err := gateway.get(cmd.Context(), "/health", &health); err != nil {
				return err
			}

			// Printed before opening, so that the URL survives whatever the
			// browser does or fails to do.
			fmt.Fprintln(stdout, gateway.base)
			if printOnly {
				return nil
			}

			if open == nil {
				return fmt.Errorf("opening a browser is not supported on %s; the address is above", runtime.GOOS)
			}
			if err := open(cmd.Context(), gateway.base); err != nil {
				return fmt.Errorf("open a browser: %w", err)
			}
			return nil
		},
	}

	client.bind(cmd.Flags())
	cmd.Flags().BoolVar(&printOnly, "print", false,
		"only print the address, without opening anything")
	return cmd
}

// openInBrowser hands a URL to the platform's opener, or is nil where
// there is no such thing to hand it to.
//
// The command is chosen by platform rather than by trying each in turn:
// on a machine without a desktop, "xdg-open" is either missing or hangs,
// and a fallback chain would turn a clear failure into a wait.
func openInBrowser() browserOpener {
	var name string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		// Not "start": that is a shell builtin rather than a program, and
		// it treats its first quoted argument as a window title.
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	case "linux", "freebsd", "netbsd", "openbsd":
		name = "xdg-open"
	default:
		return nil
	}

	return func(ctx context.Context, url string) error {
		// The URL goes in as one argument to an exact program, never
		// through a shell, so nothing in it can be read as a command.
		return exec.CommandContext(ctx, name, append(args, url)...).Start()
	}
}
