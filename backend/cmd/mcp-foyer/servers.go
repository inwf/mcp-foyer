package main

import (
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"
)

// The shapes below are declared here rather than reused from the api
// package, on purpose. This command is a client of the HTTP contract, and
// naming only the fields it displays means a change to an internal struct
// it does not read cannot break it. It also sidesteps the durations,
// which travel as strings ("30s") and would not decode into
// time.Duration.

type serverListResponse struct {
	Servers []serverRow `json:"servers"`
}

type serverRow struct {
	Name   string `json:"name"`
	Config struct {
		Transport   string `json:"transport"`
		Enabled     bool   `json:"enabled"`
		Description string `json:"description"`
	} `json:"config"`
	Status struct {
		State         string `json:"state"`
		Error         string `json:"error"`
		ToolCount     int    `json:"toolCount"`
		ResourceCount int    `json:"resourceCount"`
		ServerName    string `json:"serverName"`
		ServerVersion string `json:"serverVersion"`
	} `json:"status"`

	// ExposedCount is how many of the server's tools reach the gateway's
	// tools/list. Nothing is exposed unless it is listed, so this is
	// normally well below the tool count — printing the pair is what makes
	// that visible instead of silent.
	ExposedCount int `json:"exposedCount"`
}

func newServersCommand(global *globalOptions, stdout io.Writer) *cobra.Command {
	client := &clientOptions{global: global}

	cmd := &cobra.Command{
		Use:   "servers",
		Short: "Inspect and manage the upstream servers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return usagef("specify a subcommand: %s", availableCommands(cmd))
		},
	}
	client.bind(cmd.PersistentFlags())

	cmd.AddCommand(
		newServersListCommand(client, stdout),
		newServersAddCommand(client, stdout),
	)
	return cmd
}

func newServersListCommand(client *clientOptions, stdout io.Writer) *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the configured servers and what each is doing",
		Long: "List the configured servers as the running instance sees them: whether it\n" +
			"reached each one, and how many tools and resources each is offering.\n\n" +
			"This asks the running instance rather than reading the configuration,\n" +
			"because only the instance knows which servers it actually reached.",
		Args: noPositionalArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listServers(cmd, client, stdout, verbose)
		},
	}
	cmd.Flags().BoolVar(&verbose, "verbose", false,
		"also show what each server reported about itself during the handshake")
	return cmd
}

// exposure renders how much of a server's toolbox reaches clients.
//
// The pair rather than either number alone: the gateway exposes nothing it
// has not been told to expose, so "9" on its own reads as nine tools on
// offer when the true answer may be none. "0/9" says both what the server
// has and how much of it was asked for.
func exposure(server serverRow) string {
	return fmt.Sprintf("%d/%d", server.ExposedCount, server.Status.ToolCount)
}

func listServers(cmd *cobra.Command, client *clientOptions, stdout io.Writer, verbose bool) error {
	gateway, err := client.connect()
	if err != nil {
		return err
	}

	var response serverListResponse
	if err := gateway.get(cmd.Context(), "/servers", &response); err != nil {
		return err
	}

	if len(response.Servers) == 0 {
		fmt.Fprintln(stdout, "no servers are configured")
		return nil
	}

	headings := []string{"NAME", "TRANSPORT", "ENABLED", "STATE", "EXPOSED/TOOLS", "RESOURCES"}
	if verbose {
		headings = append(headings, "SERVER", "VERSION")
	}
	rows := newTable(stdout, headings...)

	// The failures are collected and printed under the table rather than
	// in a column: an error message is a sentence, and a sentence in a
	// column either gets truncated or destroys the alignment of every
	// other row.
	var failures []serverRow

	for _, server := range response.Servers {
		cells := []string{
			server.Name,
			server.Config.Transport,
			yesNo(server.Config.Enabled),
			dash(server.Status.State),
			exposure(server),
			strconv.Itoa(server.Status.ResourceCount),
		}
		if verbose {
			cells = append(cells,
				dash(server.Status.ServerName),
				dash(server.Status.ServerVersion))
		}
		rows.row(cells...)

		if server.Status.Error != "" {
			failures = append(failures, server)
		}
	}
	rows.flush()

	for _, server := range failures {
		fmt.Fprintf(stdout, "\n%s: %s\n", server.Name, server.Status.Error)
	}
	return nil
}
