package gateway_test

import (
	"slices"
	"testing"

	"mcp-foyer/internal/gateway"
)

// A query that only asks — "what does the acme toolkit do" — must come
// back empty when the directory has never heard of acme. Before the
// common words were cut, it returned a page of hits that had matched
// "what", "the" and "do", and the caller had to read every one to learn
// that none of them was about acme.
func TestAQueryOfNothingButCommonWordsComesBackEmpty(t *testing.T) {
	const query = "what does the acme toolkit do"

	if hits := gateway.SearchTools(query, candidates(), 0); len(hits) != 0 {
		t.Errorf("SearchTools(%q) = %v, want nothing: the directory has no such capability", query, hitNames(hits))
	}

	// The empty result still names the words that would have picked
	// something out, which is what the caller retries with.
	unmatched := gateway.UnmatchedTerms(query, candidates())
	for _, want := range []string{"acme", "toolkit"} {
		if !slices.Contains(unmatched, want) {
			t.Errorf("unmatched = %v, want it to name %q", unmatched, want)
		}
	}
}

// With no hits and no word to blame, the empty result has to blame the
// words it dropped: "the" is in the corpus but picks nothing out, and an
// unexplained empty answer reads as "this endpoint cannot do that".
func TestAQueryOfNothingButCommonWordsNamesThem(t *testing.T) {
	if hits := gateway.SearchTools("the", candidates(), 0); len(hits) != 0 {
		t.Errorf("SearchTools(\"the\") = %v, want nothing", hitNames(hits))
	}
	if got, want := gateway.UnmatchedTerms("the", candidates()), []string{"the"}; !slices.Equal(got, want) {
		t.Errorf("UnmatchedTerms = %v, want %v", got, want)
	}
}

// Cutting the common words must not touch the words that carry the
// question: the hit that the meaningful word finds is the whole answer.
func TestCommonWordsDoNotDiluteTheWordsThatMatter(t *testing.T) {
	directory := []gateway.Searchable{
		{Server: "notes", Tool: "list", Description: "List the notes the user wrote"},
		{Server: "files", Tool: "upload", Description: "Upload a file to the server"},
	}

	hits := gateway.SearchTools("what are the notes please", directory, 0)
	if len(hits) != 1 || hits[0].Tool != "list" {
		t.Errorf("SearchTools = %v, want only notes/list", hitNames(hits))
	}
}

// A common word that names something is not common: a caller that typed
// "do" meant the tool called do, and the same word with nothing named
// after it stays cut.
func TestACommonWordThatNamesSomethingIsNotIgnored(t *testing.T) {
	named := []gateway.Searchable{
		{Server: "tasks", Tool: "do", Description: "Run a task now"},
		{Server: "notes", Tool: "list", Description: "List notes"},
	}
	if hits := gateway.SearchTools("do", named, 0); len(hits) != 1 || hits[0].Tool != "do" {
		t.Errorf("SearchTools(\"do\") = %v, want only tasks/do", hitNames(hits))
	}

	// The same word where no name carries it, only prose: cut, or every
	// description that says "do" would match.
	plain := []gateway.Searchable{
		{Server: "notes", Tool: "list", Description: "Do the thing the user asked"},
		{Server: "files", Tool: "upload", Description: "Upload a file to the server"},
	}
	if hits := gateway.SearchTools("do", plain, 0); len(hits) != 0 {
		t.Errorf("SearchTools(\"do\") = %v, want nothing when no name carries it", hitNames(hits))
	}
}

// Parameter text is prose, not a name. The live corpus caught this the
// first time the rule ran against it: a parameter documented as "the
// maximum number of results to return" put "the" in a tool's argument
// field, the exemption was read from there, and every function word of
// every query stayed matched.
func TestACommonWordInParameterProseDoesNotNameAnything(t *testing.T) {
	directory := []gateway.Searchable{
		{Server: "notes", Tool: "list", Description: "List notes",
			Arguments: "limit the maximum number of results to return"},
		{Server: "files", Tool: "upload", Description: "Upload a file",
			Arguments: "path the file to upload"},
	}

	if hits := gateway.SearchTools("the", directory, 0); len(hits) != 0 {
		t.Errorf("SearchTools(\"the\") = %v, want nothing: parameter prose is not a name", hitNames(hits))
	}
	if hits := gateway.SearchTools("the notes", directory, 0); len(hits) != 1 || hits[0].Tool != "list" {
		t.Errorf("SearchTools(\"the notes\") = %v, want only notes/list", hitNames(hits))
	}
}
