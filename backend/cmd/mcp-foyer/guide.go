package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"mcp-foyer/internal/guide"
)

func newGuideCommand(stdout io.Writer) *cobra.Command {
	var topic string

	cmd := &cobra.Command{
		Use:   "guide",
		Short: "Print the usage guide",
		Long: "Print the usage guide as Markdown.\n\n" +
			"It is written to standard output unrendered, so it can be piped to a\n" +
			"pager, saved to a file, or handed to an assistant that reads Markdown.\n\n" +
			"mcp-foyer also serves the same document as an MCP resource, so a model\n" +
			"connected to it can read this without being handed the text.",
		Args: noPositionalArgs,
		RunE: func(*cobra.Command, []string) error {
			text := guide.Text()
			if topic != "" {
				section, ok := guide.Section(topic)
				if !ok {
					return usagef("no section matches %q; the sections are: %s",
						topic, quotedHeadings())
				}
				text = section
			}
			_, err := io.WriteString(stdout, text)
			return err
		},
	}

	cmd.Flags().StringVar(&topic, "section", "",
		"print only the section whose heading contains this text")
	return cmd
}

// quotedHeadings lists the sections for a message that has to name them.
// They are quoted because the headings are prose and contain spaces, so an
// unquoted list would not show where one ends and the next begins.
func quotedHeadings() string {
	headings := guide.Headings()
	quoted := make([]string, len(headings))
	for i, heading := range headings {
		quoted[i] = fmt.Sprintf("%q", heading)
	}
	return strings.Join(quoted, ", ")
}
