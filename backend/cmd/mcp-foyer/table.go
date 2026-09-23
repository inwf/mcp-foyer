package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// table prints aligned columns.
//
// The output is meant to be read by a person, but a person is not the
// only reader: a single space-padded layout is also what makes `mcp-foyer
// servers list | awk '{print $1}'` work, so the padding is spaces and
// there are no box-drawing characters.
type table struct {
	writer *tabwriter.Writer
}

func newTable(w io.Writer, headings ...string) *table {
	t := &table{writer: tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)}
	if len(headings) > 0 {
		t.row(headings...)
	}
	return t
}

func (t *table) row(cells ...string) {
	fmt.Fprintln(t.writer, strings.Join(cells, "\t"))
}

func (t *table) flush() { _ = t.writer.Flush() }

// yesNo renders a boolean as a word rather than as "true", because the
// column reads as a question.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// dash stands in for a value that is absent, so that an empty cell is
// never mistaken for a missing column.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
