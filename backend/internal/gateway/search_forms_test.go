package gateway_test

import (
	"testing"

	"mcp-foyer/internal/gateway"
)

// Models write queries in whatever form the sentence in their head had:
// "searching files", "deleting relations", "committed changes". A word
// in another form than the directory's is still that word, and reporting
// it unmatched tells the caller the endpoint cannot do something it can.
//
// The want lists every tool of the server where the query does not choose
// between them: 诗 is in all three poetry tools.
func TestAWordInAnotherFormFindsTheTool(t *testing.T) {
	candidates := corpusCandidates(t)
	for _, tc := range []struct {
		query string
		want  string
	}{
		{"searching files", "files/search_files"},
		{"deleting relations", "memory/delete_relations"},
		{"committed changes", "git/git_commit"},
		{"listed issues", "github/list_issues"},
		// A single character is a word in Chinese; the directory writes
		// it inside a longer one (古诗词).
		{"诗", "poetry/search_poem"},
	} {
		hits := gateway.SearchTools(tc.query, candidates, 3)
		if !ranksAmongTopThree(hits, []string{tc.want}) {
			t.Errorf("SearchTools(%q) ranked %v in the top three, want %s",
				tc.query, originNames(hits), tc.want)
		}
		if unmatched := gateway.UnmatchedTerms(tc.query, candidates); len(unmatched) != 0 {
			t.Errorf("SearchTools(%q) reported %v as matching nothing", tc.query, unmatched)
		}
	}
}
