package gateway_test

import (
	"fmt"
	"testing"

	"mcp-foyer/internal/gateway"
)

// A word that most of a directory contains is worth a small fraction of
// a point to every tool. The tools that carry it in their name still say
// it louder than the ones that only mention it in a server's blurb, and
// the order has to show that, even though the scores a caller reads may
// all round to the same number. Ordering on the rounded number put
// whichever server sorts first alphabetically at the top.
func TestACommonWordStillRanksTheToolsNamedForItFirst(t *testing.T) {
	var candidates []gateway.Searchable
	// Two hundred servers whose description mentions search, each with a
	// tool that has nothing to do with it. Their names sort before the
	// one that matters.
	for i := range 200 {
		candidates = append(candidates, gateway.Searchable{
			Server:            fmt.Sprintf("aaa-%03d", i),
			ServerDescription: "Search, read and manage records for the team.",
			Tool:              "create_record",
			Description:       "Create a new record.",
		})
	}
	candidates = append(candidates, gateway.Searchable{
		Server:      "zzz",
		Tool:        "search_records",
		Description: "Search records by keyword.",
	})

	hits := gateway.SearchTools("search", candidates, 1)

	if len(hits) != 1 || hits[0].Tool != "search_records" {
		t.Fatalf("SearchTools(search) ranked %v first, want zzz/search_records", hits)
	}
}
