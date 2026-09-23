package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"
)

// settleWait is how long `servers add` watches a new server before
// reporting.
//
// Adding a server that never starts is the failure worth catching, and
// the gateway connects in the background, so a command that returned the
// moment the file was written would report success for a server that is
// about to fail. Waiting a little turns that into an answer.
const settleWait = 10 * time.Second

// addFlags is the server definition as the command line expresses it.
// Only what the user actually set is sent, so everything else takes the
// gateway's own default rather than a second set of defaults defined
// here that could drift from it.
type addFlags struct {
	url         string
	transport   string
	env         []string
	headers     []string
	proxy       string
	description string
	timeout     string
	disabled    bool
	fromFile    string
	wait        time.Duration
}

func newServersAddCommand(client *clientOptions, stdout io.Writer) *cobra.Command {
	var flags addFlags

	cmd := &cobra.Command{
		Use:   "add <name> [-- command args...]",
		Short: "Add a server to the running instance's configuration",
		Long: "Add a server. The change goes through the running instance, which\n" +
			"validates it, writes it to the configuration file and connects to it.\n\n" +
			"A server that runs as a child process is given as a command after \"--\",\n" +
			"which is where the shell stops interpreting flags:\n\n" +
			"  mcp-foyer servers add files -- npx -y @modelcontextprotocol/server-filesystem /tmp\n\n" +
			"A server that is already running somewhere is given as a URL:\n\n" +
			"  mcp-foyer servers add remote --url https://example.com/mcp\n\n" +
			"The transport follows from which of the two was given, so it only has\n" +
			"to be named with --transport when overriding that.",
		Args: addArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			gateway, err := client.connect()
			if err != nil {
				return err
			}

			command, commandArgs := commandAfterDash(cmd, args)
			definition, err := buildServer(&flags, command, commandArgs)
			if err != nil {
				return err
			}
			return addServer(cmd.Context(), gateway, stdout, args[0], definition, flags.wait)
		},
	}

	f := cmd.Flags()
	f.StringVar(&flags.url, "url", "", "endpoint of a server that is already running")
	f.StringVar(&flags.transport, "transport", "",
		"transport to use (default: inferred from --url or the command)")
	f.StringArrayVar(&flags.env, "env", nil, "environment variable as KEY=VALUE; repeat for more")
	f.StringArrayVar(&flags.headers, "header", nil, "HTTP header as NAME=VALUE; repeat for more")
	f.StringVar(&flags.proxy, "proxy", "", "HTTP proxy to reach the server through")
	f.StringVar(&flags.description, "description", "", "what this server is for")
	f.StringVar(&flags.timeout, "timeout", "", "per-request timeout, such as 30s")
	f.BoolVar(&flags.disabled, "disabled", false, "add the server without connecting to it")
	f.StringVar(&flags.fromFile, "from-file", "", "read the server definition from a YAML file")
	f.DurationVar(&flags.wait, "wait", settleWait,
		"how long to wait for the server to connect before reporting; 0 to return immediately")

	return cmd
}

// addArgs accepts the name, plus a command after "--".
func addArgs(cmd *cobra.Command, args []string) error {
	dash := cmd.ArgsLenAtDash()

	if len(args) == 0 || dash == 0 {
		return usagef("no server name given")
	}
	if dash < 0 && len(args) > 1 {
		return usagef("unexpected argument %q; a command goes after \"--\"", args[1])
	}
	if dash > 1 {
		return usagef("unexpected argument %q before \"--\"", args[1])
	}
	return nil
}

func commandAfterDash(cmd *cobra.Command, args []string) (command string, rest []string) {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 || dash >= len(args) {
		return "", nil
	}
	after := args[dash:]
	if len(after) == 0 {
		return "", nil
	}
	return after[0], after[1:]
}

// buildServer renders the definition as the JSON the API takes.
//
// Durations travel as strings ("30s") because the API speaks the
// configuration file's shape, so nothing here converts them to a number.
func buildServer(flags *addFlags, command string, commandArgs []string) (json.RawMessage, error) {
	if flags.fromFile != "" {
		return serverFromFile(flags, command)
	}

	switch {
	case command == "" && flags.url == "":
		return nil, usagef("give either a command after \"--\" or --url")
	case command != "" && flags.url != "":
		return nil, usagef("--url and a command cannot both be given: " +
			"a server is either started here or reached over HTTP")
	}

	server := map[string]any{}

	// The transport follows from what was given. Naming it explicitly
	// overrides that, which is what makes an unsupported value reach the
	// gateway and be reported against the field rather than silently
	// corrected here.
	switch {
	case flags.transport != "":
		server["transport"] = flags.transport
	case command != "":
		server["transport"] = "stdio"
	default:
		server["transport"] = "streamable-http"
	}

	if command != "" {
		server["command"] = command
		if len(commandArgs) > 0 {
			server["args"] = commandArgs
		}
	}
	if flags.url != "" {
		server["url"] = flags.url
	}
	if flags.proxy != "" {
		server["proxy"] = flags.proxy
	}
	if flags.description != "" {
		server["description"] = flags.description
	}
	if flags.timeout != "" {
		server["timeout"] = flags.timeout
	}
	if flags.disabled {
		server["enabled"] = false
	}

	for _, pair := range []struct {
		flag   string
		values []string
		key    string
	}{
		{"--env", flags.env, "env"},
		{"--header", flags.headers, "headers"},
	} {
		if len(pair.values) == 0 {
			continue
		}
		parsed, err := parseKeyValues(pair.flag, pair.values)
		if err != nil {
			return nil, err
		}
		server[pair.key] = parsed
	}

	encoded, err := json.Marshal(server)
	if err != nil {
		return nil, fmt.Errorf("encode the server: %w", err)
	}
	return encoded, nil
}

// serverFromFile reads a definition written as YAML.
//
// The file is passed on as it was written rather than being parsed into a
// struct and re-encoded: the gateway rejects a key it does not recognise,
// and a round trip through a struct here would silently drop exactly
// those keys before it ever saw them.
func serverFromFile(flags *addFlags, command string) (json.RawMessage, error) {
	if command != "" || flags.url != "" {
		return nil, usagef("--from-file gives the whole definition, " +
			"so a command or --url cannot be given as well")
	}

	data, err := os.ReadFile(flags.fromFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", flags.fromFile, err)
	}
	asJSON, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid YAML: %w", flags.fromFile, err)
	}
	return asJSON, nil
}

func parseKeyValues(flag string, pairs []string) (map[string]string, error) {
	parsed := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		// Split at the first separator only: a value routinely contains
		// one, and a token or a URL would be cut in half otherwise.
		key, value, found := strings.Cut(pair, "=")
		if !found || key == "" {
			return nil, usagef("%s %q is not in KEY=VALUE form", flag, pair)
		}
		parsed[key] = value
	}
	return parsed, nil
}

func addServer(ctx context.Context, gateway *gatewayClient, stdout io.Writer,
	name string, definition json.RawMessage, wait time.Duration) error {

	body := struct {
		Name   string          `json:"name"`
		Server json.RawMessage `json:"server"`
	}{Name: name, Server: definition}

	var created serverRow
	if err := gateway.post(ctx, "/servers", body, &created); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "added %s (%s)\n", name, created.Config.Transport)

	if wait <= 0 || !created.Config.Enabled {
		return nil
	}
	return reportWhenSettled(ctx, gateway, stdout, name, wait)
}

// reportWhenSettled watches a newly added server until it stops being
// "connecting", so that one that fails to start says so here rather than
// leaving the user to discover it later.
func reportWhenSettled(ctx context.Context, gateway *gatewayClient, stdout io.Writer,
	name string, wait time.Duration) error {

	deadline := time.Now().Add(wait)
	path := "/servers/" + url.PathEscape(name)

	for {
		var current serverRow
		if err := gateway.get(ctx, path, &current); err != nil {
			return err
		}

		switch current.Status.State {
		case "connected":
			fmt.Fprintf(stdout, "connected: %d tools, %d resources\n",
				current.Status.ToolCount, current.Status.ResourceCount)
			return nil
		case "failed":
			// The server is in the configuration and will be retried, so
			// this is a warning rather than an error: nothing needs
			// undoing, and the exit code should not say it does.
			fmt.Fprintf(stdout, "added, but it did not start: %s\n", current.Status.Error)
			return nil
		}

		if time.Now().After(deadline) {
			fmt.Fprintf(stdout, "still %s after %s; check \"mcp-foyer servers list\"\n",
				dash(current.Status.State), wait)
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
