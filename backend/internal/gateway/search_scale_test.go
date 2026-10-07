package gateway_test

import (
	"fmt"
	"slices"
	"testing"

	"mcp-foyer/internal/gateway"
)

// The ranking test in the other file judges a clean, hand-built corpus.
// This one asks the question that decides whether a hundred-server
// installation is usable: do the same rows still come up when thousands
// of plausible neighbours compete for the same words?
//
// The competition is generated rather than random: every name is a
// function of its index, so a given size builds the same directory on
// every machine and the numbers mean the same thing from run to run.
// The names and descriptions are modelled on real servers - search and
// list and read and fetch over documents, records and users - because a
// numbered directory would measure cost, not judgement.
//
// A row that misses at this scale documents a ranking defect the way a
// knownFailing mark does in the table the other test runs.
func TestSearchAtScaleKeepsTheIntendedToolInTheTopThree(t *testing.T) {
	for _, size := range []struct {
		label          string
		servers, tools int
	}{
		{"20x50", 20, 50},
		{"100x50", 100, 50},
		{"100x100", 100, 100},
		{"1000x30", 1000, 30},
	} {
		t.Run(size.label, func(t *testing.T) {
			candidates := slices.Concat(corpusCandidates(t), distractorCandidates(size.servers, size.tools))

			asserted, found, missed := 0, 0, 0
			for _, tc := range rankingQueries {
				if tc.knownFailing != "" {
					continue
				}
				asserted++
				hits := gateway.SearchTools(tc.query, candidates, 3)
				among := ranksAmongTopThree(hits, tc.want)

				mark, marked := scaleMisses[tc.query]
				if marked && slices.Contains(mark.sizes, size.label) {
					missed++
					if among {
						t.Errorf("SearchTools(%q) is marked as missing at %s (%s) but found its tool; remove the mark",
							tc.query, size.label, mark.reason)
					} else {
						t.Logf("known miss at %s: %s", size.label, mark.reason)
					}
					continue
				}

				if among {
					found++
					continue
				}
				t.Errorf("SearchTools(%q) ranked %v in the top three, want one of %v",
					tc.query, originNames(hits), tc.want)
			}
			t.Logf("%d candidates, %d rows (%d known misses): %d rows found their tool (%.1f%%)",
				len(candidates), asserted, missed, found, 100*float64(found)/float64(asserted))
		})
	}
}

// scaleMisses records the rows that miss the top three at some size, and
// what each miss shows. Like knownFailing in the table the other test
// runs, the record is two-way: an unrecorded miss fails the test, and a
// recorded miss that stops missing fails too, so a mark cannot outlive
// the defect it documents. P20 works this list down.
//
// The twins named below are the test's own generated tools: a server
// like storage-mcp carrying read_files next to the corpus's
// files/read_file, which is exactly the neighbour a hundred-server
// directory has.
var scaleMisses = map[string]struct {
	sizes  []string
	reason string
}{
	"listChannels": {
		sizes:  []string{"20x50", "100x50", "100x100", "1000x30"},
		reason: "the generated twins spell list_channels exactly while the corpus tool's server prefix keeps it off the joined form; at a thousand servers several twins clear it at once",
	},
	"list issues": {
		sizes:  []string{"100x100"},
		reason: "the generated twins spell list_issues exactly, so no join separates them; a length gap decides the page",
	},
	"knowledge graph search": {
		sizes:  []string{"20x50", "100x50", "100x100", "1000x30"},
		reason: "memory's other tools concentrate knowledge and graph in short descriptions; search carries almost no weight once thousands of tools match it",
	},
	"dryRun": {
		sizes:  []string{"20x50"},
		reason: "the camel split matches dry and run; run lands in generated descriptions while the exact dryRun sits in a long argument field at the lowest weight",
	},
}

// ranksAmongTopThree reports whether one of the wanted origins is among
// the hits, which the caller has already cut to the page it would hand a
// model.
func ranksAmongTopThree(hits []gateway.SearchHit, want []string) bool {
	for _, hit := range hits {
		if slices.Contains(want, hit.Server+"/"+hit.Tool) {
			return true
		}
	}
	return false
}

// The raw material of the generated competition. One server per kind,
// cycled with a suffix so that a hundred servers all have distinct
// names; its tools are the ten shapes every real directory has, over
// nouns the corpus also uses ("read", "files", "users"), which is where
// the pressure on the ranking comes from.
var (
	distractorKinds = []string{
		"docs", "code", "cloud", "data", "mail", "chat", "issues", "hosting",
		"billing", "media", "support", "analytics", "deploy", "storage",
		"calendar", "notes", "wiki", "crm", "orders", "inventory",
	}
	distractorSuffixes = []string{"mcp", "server", "api", "tools", "hub"}
	distractorNouns    = []string{
		"documents", "files", "records", "items", "users", "messages",
		"channels", "pages", "comments", "events", "tasks", "issues",
		"orders", "invoices", "metrics", "logs", "reports", "tickets",
		"contacts", "assets",
	}
	distractorTemplates = []struct{ name, description, arguments string }{
		{"search_%s", "Search %s by keyword, with filters for tags and dates.", "query tags limit offset"},
		{"list_%s", "List the %s visible to the current user, newest first.", "page perPage"},
		{"get_%s", "Get one entry from %s by identifier, with metadata and related items.", "id identifier"},
		{"read_%s", "Read the full contents of the given %s, with pagination for long bodies.", "id path"},
		{"create_%s", "Create a new entry in %s and return its identifier.", "name body"},
		{"update_%s", "Update the fields of an existing entry in %s.", "id name body"},
		{"delete_%s", "Delete an entry from %s permanently. This cannot be undone.", "id"},
		{"fetch_%s", "Fetch %s from a remote source and keep them locally.", "url id"},
		{"query_%s", "Run a structured query over %s and return aggregate results.", "filter groupBy orderBy"},
		{"export_%s", "Export %s to CSV or JSON.", "format ids"},
	}
)

// distractorCandidates builds servers×tools generated tools. A server's
// tools are distinct by construction: the template cycles with the tool
// index, the noun with every tenth tool, so no name repeats within one
// server however many tools it is asked for.
func distractorCandidates(servers, tools int) []gateway.Searchable {
	out := make([]gateway.Searchable, 0, servers*tools)
	for i := range servers {
		kind := distractorKinds[i%len(distractorKinds)]
		suffix := distractorSuffixes[(i/len(distractorKinds))%len(distractorSuffixes)]
		name := kind + "-" + suffix
		// Server names are unique keys in a real installation, so the
		// hundred combinations above cycle with a round number instead of
		// repeating: a thousand servers must not contain ten servers
		// called docs-mcp, which would collide origins in a way no
		// directory can.
		if round := i / (len(distractorKinds) * len(distractorSuffixes)); round > 0 {
			name = fmt.Sprintf("%s-%d", name, round)
		}
		for j := range tools {
			template := distractorTemplates[j%len(distractorTemplates)]
			noun := distractorNouns[(j/len(distractorTemplates)+i)%len(distractorNouns)]
			out = append(out, gateway.Searchable{
				Server:            name,
				ServerTitle:       "Acme " + kind + " MCP",
				ServerDescription: "Search, read and manage " + kind + " for the team.",
				Tool:              fmt.Sprintf(template.name, noun),
				Description:       fmt.Sprintf(template.description, noun),
				Arguments:         template.arguments,
			})
		}
	}
	return out
}
