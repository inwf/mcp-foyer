package main

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"mcp-foyer/internal/gateway"
)

// The shapes below name only the fields this command displays, for the
// reason given in servers.go: it is a client of the HTTP contract rather
// than of the api package's structs.

type healthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
}

type gatewayStatusResponse struct {
	Servers struct {
		Configured int `json:"configured"`
		Connected  int `json:"connected"`
		Failed     int `json:"failed"`
	} `json:"servers"`
	Tools       int    `json:"tools"`
	Sessions    int    `json:"sessions"`
	SessionMode string `json:"sessionMode"`
}

type sessionListResponse struct {
	Sessions []sessionRow `json:"sessions"`
}

type sessionRow struct {
	ID              string `json:"id"`
	ClientName      string `json:"clientName"`
	ClientVersion   string `json:"clientVersion"`
	ProtocolVersion string `json:"protocolVersion"`
}

// newStatusCommand reports on the instance that is running.
//
// There is deliberately no process id in the output. mcp-foyer writes no pid
// file, so the only pid this command could print is one it guessed — and
// daemon management is not something it does. A number that looks like a
// pid but might belong to another process is worse than no number at all:
// the first thing anyone does with one is send it a signal.
func newStatusCommand(global *globalOptions, stdout io.Writer) *cobra.Command {
	client := &clientOptions{global: global}

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report what the running instance is doing",
		Long: "Report on the running instance: which build it is, how long it has been\n" +
			"up, how many servers it reached, how much it is offering clients, and\n" +
			"which clients are connected.\n\n" +
			"Because it asks the instance rather than reading the configuration, it\n" +
			"also answers whether one is running at all.",
		Args: noPositionalArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hub, err := client.connect()
			if err != nil {
				return err
			}
			return reportStatus(cmd, hub, stdout)
		},
	}
	client.bind(cmd.Flags())
	return cmd
}

func reportStatus(cmd *cobra.Command, hub *gatewayClient, stdout io.Writer) error {
	ctx := cmd.Context()

	var health healthResponse
	if err := hub.get(ctx, "/health", &health); err != nil {
		return err
	}

	var status gatewayStatusResponse
	if err := hub.get(ctx, "/gateway/status", &status); err != nil {
		return err
	}

	var sessions sessionListResponse
	if err := hub.get(ctx, "/gateway/sessions", &sessions); err != nil {
		return err
	}

	// The tools that exist but are not on offer come from the server list,
	// which is the only place holding both numbers.
	unexposed, err := fetchUnexposedCounts(ctx, hub)
	if err != nil {
		return err
	}
	hidden := 0
	for _, count := range unexposed {
		hidden += count
	}

	fmt.Fprintf(stdout, "mcp-foyer %s at %s\n\n", dash(health.Version), hub.base)

	fmt.Fprintf(stdout, "uptime:     %s\n", uptime(health.UptimeSeconds))
	fmt.Fprintf(stdout, "servers:    %d configured, %d connected, %d failed\n",
		status.Servers.Configured, status.Servers.Connected, status.Servers.Failed)
	fmt.Fprintf(stdout, "tools:      %s\n", offering(status.Tools, hidden))
	fmt.Fprintf(stdout, "sessions:   %s\n", connectedClients(status.Sessions, status.SessionMode))

	// A failed server is the one thing here that needs acting on, and this
	// command does not print the reason: an error message is a sentence,
	// and `servers list` is where the sentences are.
	if status.Servers.Failed > 0 {
		fmt.Fprintf(stdout, "\n%d %s not connect; run \"mcp-foyer servers list\" to see why\n",
			status.Servers.Failed, plural(status.Servers.Failed, "server did", "servers did"))
	}

	printSessions(stdout, sessions.Sessions)
	return nil
}

// uptime renders the figure the gateway reported rather than a difference
// of two clocks: the instance may be on a machine whose clock disagrees
// with this one, and the point of the line is how long it has been up.
func uptime(seconds int64) string {
	if seconds <= 0 {
		return "less than a second"
	}
	return (time.Duration(seconds) * time.Second).String()
}

// offering says both numbers, because either alone misleads.
//
// The count on its own reads as everything available, when on an ordinary
// installation it is the four gateway tools and nothing else — nothing is
// exposed unless the configuration asks for it. Saying what was left out,
// and that it is still reachable, is what stops that reading as breakage.
func offering(published, hidden int) string {
	if hidden == 0 {
		return fmt.Sprintf("%d offered to clients", published)
	}
	return fmt.Sprintf("%d offered to clients, %d upstream %s not exposed (reach them with %s)",
		published, hidden, plural(hidden, "tool is", "tools are"), gateway.ToolCallTool)
}

func connectedClients(count int, mode string) string {
	if count == 0 {
		return fmt.Sprintf("none connected, %s for the next one", dash(mode))
	}
	return fmt.Sprintf("%d connected, %s unless a client asks otherwise",
		count, dash(mode))
}

func printSessions(stdout io.Writer, sessions []sessionRow) {
	if len(sessions) == 0 {
		return
	}

	fmt.Fprintf(stdout, "\nSESSIONS (%d)\n", len(sessions))
	rows := newTable(stdout, "ID", "CLIENT", "VERSION", "PROTOCOL")
	for _, session := range sessions {
		rows.row(
			session.ID,
			dash(session.ClientName),
			dash(session.ClientVersion),
			dash(session.ProtocolVersion),
		)
	}
	rows.flush()
}
