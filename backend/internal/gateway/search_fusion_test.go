package gateway

import (
	"fmt"
	"slices"
	"testing"
)

// These are the fusion rules themselves, tested where they live: what the
// two rankings are added up from, and what a signal of nothing means.

func fusionCandidates() []Searchable {
	return []Searchable{
		{Server: "files", Tool: "read", Description: "read a file from disk"},
		{Server: "tracker", Tool: "list_issues", Description: "list the issues of a project"},
		{Server: "tracker", Tool: "create_issue", Description: "create an issue"},
	}
}

// The value of the semantic half: a tool whose words do not answer the
// query at all still reaches the caller when its meaning does.
func TestASemanticOnlyHitJoinsTheResult(t *testing.T) {
	similarities := []float32{0, 0.8, 0}

	hits, unmatched := searchCandidatesWith("bug tracking", fusionCandidates(), 0, similarities)

	if len(hits) != 1 || hits[0].Tool != "list_issues" {
		t.Fatalf("hits = %+v, want the one the meaning found", hits)
	}
	if hits[0].Matched != 0 {
		t.Errorf("matched = %d, want 0: no query word matched", hits[0].Matched)
	}
	// Saying which words matched nothing is what lets a caller tell a hit
	// found by meaning from one found by words.
	if len(unmatched) != 2 {
		t.Errorf("unmatched = %v, want both words named", unmatched)
	}
}

// And its limit: nothing close enough means no hits, so a query about
// something the directory does not have still comes back empty.
func TestNoSemanticSignalLeavesAnEmptyResultEmpty(t *testing.T) {
	hits, _ := searchCandidatesWith("bug tracking", fusionCandidates(), 0, make([]float32, 3))

	if len(hits) != 0 {
		t.Errorf("hits = %+v, want none: nothing matched and nothing was close", hits)
	}
}

// A deployment without an embedding service passes nil, and gets exactly
// the ranking it had before any of this existed.
func TestWithoutASemanticSignalTheRankingIsUntouched(t *testing.T) {
	candidates := fusionCandidates()

	lexical, lexicalUnmatched := searchCandidates("file issue", candidates, 0)
	untouched, untouchedUnmatched := searchCandidatesWith("file issue", candidates, 0, nil)

	if len(lexical) != len(untouched) {
		t.Fatalf("hits = %+v with a nil signal, want %+v", untouched, lexical)
	}
	for i := range lexical {
		if lexical[i] != untouched[i] {
			t.Errorf("hit %d = %+v with a nil signal, want %+v", i, untouched[i], lexical[i])
		}
	}
	if !slices.Equal(lexicalUnmatched, untouchedUnmatched) {
		t.Errorf("unmatched = %v with a nil signal, want %v", untouchedUnmatched, lexicalUnmatched)
	}
}

// A blank query is browsing — paging through a directory — and there is
// nothing to embed, so a signal that would reorder it is ignored.
func TestABlankQueryKeepsThePlainOrder(t *testing.T) {
	candidates := fusionCandidates()
	reordering := []float32{0, 0, 0.9}

	hits, _ := searchCandidatesWith("", candidates, 0, reordering)
	plain := SearchTools("", candidates, 0)

	if len(hits) != len(plain) {
		t.Fatalf("hits = %+v, want the plain order %+v", hits, plain)
	}
	for i := range plain {
		if hits[i] != plain[i] {
			t.Errorf("hit %d = %+v, want %+v", i, hits[i], plain[i])
		}
	}
}

func TestReciprocalRankFusionRewardsAgreement(t *testing.T) {
	candidates := []Searchable{
		{Server: "a", Tool: "x"}, {Server: "a", Tool: "y"}, {Server: "a", Tool: "z"},
	}
	// The words order x, y, z; the meaning knows only y — and knows it
	// well. Two weak agreements beat one strong one, which is what keeps
	// a single ranking from deciding on its own.
	reported := []int{300, 200, 100}
	similarities := []float32{0, 0.9, 0}

	fused := reciprocalRankFusion(reported, similarities, candidates)

	if fused[1] <= fused[0] {
		t.Errorf("fused = %v, want the tool both rankings like above the one only the words like", fused)
	}
	if fused[0] <= fused[2] {
		t.Errorf("fused = %v, want the lexical order kept among the rest", fused)
	}
}

func TestReciprocalRankFusionWithoutASignalIsTheLexicalOrder(t *testing.T) {
	candidates := []Searchable{{Server: "a", Tool: "x"}, {Server: "a", Tool: "y"}}
	reported := []int{100, 300}

	fused := reciprocalRankFusion(reported, make([]float32, 2), candidates)

	if fused[1] <= fused[0] {
		t.Errorf("fused = %v, want the higher lexical score first", fused)
	}
}

// A tool no word matched is not at the bottom of the lexical order; it is
// not in it. Giving it a tail rank would hand it a share of the fused
// score it never earned.
func TestAToolTheWordsMissedHasNoLexicalRank(t *testing.T) {
	candidates := []Searchable{{Server: "a", Tool: "x"}, {Server: "a", Tool: "y"}}
	reported := []int{100, 0}
	similarities := []float32{0.5, 0.5}

	fused := reciprocalRankFusion(reported, similarities, candidates)

	// Equal semantic ranks; the one the words also found comes first.
	if fused[0] <= fused[1] {
		t.Errorf("fused = %v, want the tool the words found above the one they did not", fused)
	}
}

// The fusion's own cost at directory scale: one more ordering of the
// candidates plus the arithmetic that combines two ranks. The semantic
// half is a table here — the network call is not what these measure.
func benchmarkCandidates(count int) []Searchable {
	kinds := []string{"docs", "code", "issues", "notes", "files", "chat", "mail", "data"}
	nouns := []string{"documents", "files", "records", "issues", "pages", "users", "channels", "tasks"}

	out := make([]Searchable, 0, count)
	for i := 0; i < count; i++ {
		kind := kinds[i%len(kinds)]
		noun := nouns[(i/len(kinds))%len(nouns)]
		out = append(out, Searchable{
			Server:      fmt.Sprintf("%s-%d", kind, i/len(kinds)),
			Tool:        "read_" + noun,
			Description: fmt.Sprintf("read the %s of the %s store", noun, kind),
			Arguments:   "id limit offset",
		})
	}
	return out
}

func BenchmarkSearchLexicalOverThirtyThousand(b *testing.B) {
	candidates := benchmarkCandidates(30_000)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = searchCandidatesWith("read a file 读取文件", candidates, 5, nil)
	}
}

func BenchmarkSearchFusedOverThirtyThousand(b *testing.B) {
	candidates := benchmarkCandidates(30_000)
	// Every candidate above the floor is the worst case for the fusion:
	// both orders are full lists.
	similarities := make([]float32, len(candidates))
	for i := range similarities {
		similarities[i] = 0.5
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = searchCandidatesWith("read a file 读取文件", candidates, 5, similarities)
	}
}
