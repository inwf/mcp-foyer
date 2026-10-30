package gateway

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
)

// SearchHit is one tool matching a query.
type SearchHit struct {
	Server      string `json:"server"`
	Tool        string `json:"tool"`
	Exposed     string `json:"exposed"`
	Description string `json:"description,omitempty"`

	// Matched is how many of the query's words this tool matched, not
	// counting the words too common to tell tools apart. It does not
	// order the results — a rare word answered once says more than three
	// common ones — but it lets a caller tell a tool that answered the
	// whole question from one that answered a word of it.
	Matched int `json:"matched"`

	// Score is relative: it compares one search's hits and means nothing
	// across searches. It is rounded for reading, and the order is decided
	// before the rounding, so two hits can show the same score and still
	// be in the order the search meant.
	Score int `json:"score"`
}

// Searchable is one candidate offered to [SearchTools].
type Searchable struct {
	Server            string
	ServerTitle       string
	ServerDescription string
	Tool              string
	Exposed           string
	Description       string

	// Arguments is the text of the tool's parameters — their names and
	// descriptions — as [ArgumentText] extracts it from the input schema.
	// A caller that knows what it wants to pass ("branch", "dryRun")
	// often knows that better than what the tool is called.
	Arguments string
}

// The fields of a candidate a term can be found in, each with a weight.
// A term in the tool's name says far more about relevance than the same
// term in prose, and the server's name says where the tool is, which is
// often half of what the caller typed. Argument text is the weakest
// signal: parameters named path and query are everywhere.
//
// The exposed name is not a field of its own: its terms are the server's
// and the tool's, and counting them again would rank a tool above an
// otherwise identical one merely because the operator exposed it.
const (
	fieldName = iota
	fieldServer
	fieldServerTitle
	fieldDescription
	fieldServerDescription
	fieldArguments
	fieldCount
)

var fieldWeights = [fieldCount]float64{
	fieldName:              4,
	fieldServer:            2,
	fieldServerTitle:       1,
	fieldDescription:       1,
	fieldServerDescription: 1,
	fieldArguments:         0.5,
}

// BM25 parameters. k1 bounds how much repeating a term can add, so that
// a description saying "file" five times does not outrank a name saying
// it once. b is how much a long field is discounted for having had more
// chances to contain the term; it sits below the usual 0.75 because
// real tool descriptions run from one terse line to several paragraphs,
// and at directory scale the standard discount let the shortest
// plausible neighbour outrank the tool the caller meant.
const (
	bm25K1 = 1.2
	bm25B  = 0.6
)

// SearchTools ranks candidates against a query.
//
// The query and every field of every candidate are cut into terms (see
// [tokenize]) and scored with fielded BM25: each term found contributes
// in proportion to how rare it is across the candidates and how
// significant the field it was found in, discounted for the field's
// length. A tool matching a rare word outranks one matching two common
// ones, which is what makes "github issue list" find list_issues rather
// than whichever tool's description happens to use "list" and "github".
//
// A term that matches nothing does not erase the terms that did: someone
// describing what they want in several words ("horoscope zodiac
// astrology") is naming one thing several ways, not narrowing a filter,
// and requiring all of them turns a good query into no answer at all.
// Which terms found nothing is worth knowing separately — see
// [UnmatchedTerms].
//
// Words that carry no capability — articles, pronouns, auxiliaries, the
// verbs of asking (see [commonQueryWords]) — are cut before anything is
// scored, unless they name a tool, a server or a parameter. They sit in
// most descriptions, so matching them says nothing about which tool the
// caller meant, and a query made only of them comes back empty instead
// of ranked.
//
// A blank query matches everything, so that a caller can page through
// the full list with the same call. Ties break on the origin rather than
// on the exposed name, which is empty for every tool an installation has
// not exposed — and that is most of them.
func SearchTools(query string, candidates []Searchable, limit int) []SearchHit {
	hits, _ := searchCandidates(query, candidates, limit)
	return hits
}

// RankTools ranks candidates the way search_tools does: by the words they
// contain, with the semantic ranking fused in when sem has something to
// say. It is what the settings page's preview shows beside the lexical
// order, and what the system tool itself calls, so the two cannot
// disagree about what a caller would get.
func RankTools(ctx context.Context, query string, candidates []Searchable, limit int, sem Semantics) ([]SearchHit, []string) {
	// Only a query with words has anything to embed, and browsing keeps
	// the lexical order whatever the service has to say. The semantic side
	// never fails the search: a service that is down comes back as no
	// signal at all, and the ranking is the one it always was.
	var similarities []float32
	if query != "" && sem != nil {
		similarities = sem.Similarities(ctx, query, candidates)
	}
	return searchCandidatesWith(query, candidates, limit, similarities)
}

// UnmatchedTerms returns the query words that appear in no candidate at
// all, spelled as the caller wrote them. A query of nothing but common
// words returns nothing and reports those words here, so that an empty
// result always carries a reason.
//
// It exists so that an empty or thin result can say why. "No hits" reads
// as "this gateway cannot do that", which is a conclusion a caller should
// not reach because it used a word this installation does not use. Naming
// the words that found nothing turns a dead end into a next attempt.
func UnmatchedTerms(query string, candidates []Searchable) []string {
	_, unmatched := searchCandidates(query, candidates, 0)
	return unmatched
}

// Semantics ranks candidates by meaning, alongside the words they
// contain. It is optional: nil means this deployment has no embedding
// service, and search is exactly the lexical search it always was.
type Semantics interface {
	// Similarities reports how close each candidate is to the query, one
	// value per candidate in order, zero meaning nothing to say about it —
	// it is below the configured floor, has no vector yet, or the service
	// did not answer. A nil result says it about every candidate at once.
	//
	// It may take as long as a call to the embedding service; the caller's
	// context bounds that. Not answering is not an error the caller has to
	// handle: search has to work without it, so a failure arrives as no
	// answer.
	Similarities(ctx context.Context, query string, candidates []Searchable) []float32
}

// searchCandidates answers both questions from one index, for the caller
// that asks both.
func searchCandidates(query string, candidates []Searchable, limit int) ([]SearchHit, []string) {
	return searchCandidatesWith(query, candidates, limit, nil)
}

// searchCandidatesWith is [searchCandidates] plus a semantic signal:
// similarities[i] is how close candidate i is to the query, zero meaning
// nothing to say about it, and nil meaning nothing to say about any of
// them — which is what a deployment with no embedding service passes, and
// what leaves the ranking exactly as it was.
//
// With a signal the two orders are fused by rank: a tool the words missed
// but the meaning found is shown beside the tools the words found, and a
// tool both agreed on comes first.
func searchCandidatesWith(query string, candidates []Searchable, limit int, similarities []float32) ([]SearchHit, []string) {
	hasSemantics := len(similarities) == len(candidates)
	if !hasSemantics {
		similarities = make([]float32, len(candidates))
	}

	words := strings.Fields(query)
	idx := indexCandidates(candidates)

	// The words that cannot tell one tool from another are cut before
	// scoring, so the hits, the scores and the matched counts all
	// describe what the words that can did find.
	distinctive := idx.distinctiveWords(words)
	scores := idx.scores(queryTerms(distinctive))
	matched := idx.wordsMatched(distinctive)

	// A blank query is browsing: there is nothing to embed, and the plain
	// order is the one paging through a directory uses.
	ranking, report := scores, reportedScore
	if len(words) > 0 && hasSemantics {
		ranking, report = reciprocalRankFusion(scores, similarities, candidates), reportedFusion
	}

	// A query with no words asks for everything; one with words asks for
	// the tools that answered at least one of them, or that the semantic
	// side could speak for. A query of nothing but common words asks for
	// nothing, and gets it.
	kept := make([]int, 0, len(candidates))
	for i := range candidates {
		if len(words) > 0 && matched[i] == 0 && similarities[i] <= 0 {
			continue
		}
		kept = append(kept, i)
	}

	// The order is decided on the scores themselves, never on the integers
	// clients see: a word found in most of the directory scores a small
	// fraction for every tool, and once rounded those fractions are all the
	// same number, which would leave the order to the alphabet.
	slices.SortFunc(kept, func(a, b int) int {
		if ranking[a] != ranking[b] {
			return cmp.Compare(ranking[b], ranking[a])
		}
		return compareOrigins(candidates[a].Server, candidates[a].Tool,
			candidates[b].Server, candidates[b].Tool)
	})
	if limit > 0 && len(kept) > limit {
		kept = kept[:limit]
	}

	hits := make([]SearchHit, len(kept))
	for n, i := range kept {
		candidate := candidates[i]
		hits[n] = SearchHit{
			Server:      candidate.Server,
			Tool:        candidate.Tool,
			Exposed:     candidate.Exposed,
			Description: candidate.Description,
			Matched:     matched[i],
			Score:       report(ranking[i]),
		}
	}

	unmatched := make([]string, 0)
	for _, word := range words {
		// A word is matched when any of its terms is, so that the
		// meaningless pairs a CJK word is cut into (取文 in 读取文件)
		// cannot make the word look unmatched when the word landed.
		if !slices.ContainsFunc(wordTerms(word), idx.has) {
			unmatched = append(unmatched, word)
		}
	}
	// Nothing came back and no word explains it: every word was common.
	// Name them, or an empty result would read as "this endpoint cannot
	// do that" with no reason given.
	if len(hits) == 0 && len(unmatched) == 0 && len(words) > 0 {
		unmatched = append(unmatched, words...)
	}
	return hits, unmatched
}

// rrfK is the constant of reciprocal rank fusion: ranks are discounted by
// it so that the top of one list is never worth more than a good showing
// in the other. 60 is the value the method was published with.
const rrfK = 60

// reciprocalRankFusion combines a lexical order and a semantic order into
// one score per candidate: each order contributes 1/(rrfK+rank), and a
// candidate missing from an order — below the similarity floor, or
// without a vector yet — simply gets nothing from it.
//
// Ranks rather than scores, because a BM25 score and a cosine have no
// common scale: one is relative to the other candidates, the other to the
// model that produced it, and adding them would mean inventing a
// calibration neither has.
func reciprocalRankFusion(scores []float64, similarities []float32, candidates []Searchable) []float64 {
	fused := make([]float64, len(candidates))

	// A candidate that matched no word is not at the bottom of the
	// lexical order; it is not in it.
	lexical := make([]int, 0, len(candidates))
	for i, score := range scores {
		if score > 0 {
			lexical = append(lexical, i)
		}
	}
	slices.SortFunc(lexical, func(a, b int) int {
		if scores[a] != scores[b] {
			return cmp.Compare(scores[b], scores[a])
		}
		return compareOrigins(candidates[a].Server, candidates[a].Tool,
			candidates[b].Server, candidates[b].Tool)
	})
	for rank, at := range lexical {
		fused[at] += 1 / (rrfK + float64(rank))
	}

	semantic := make([]int, 0, len(candidates))
	for i, similarity := range similarities {
		if similarity > 0 {
			semantic = append(semantic, i)
		}
	}
	slices.SortFunc(semantic, func(a, b int) int {
		if similarities[a] != similarities[b] {
			return cmp.Compare(similarities[b], similarities[a])
		}
		return compareOrigins(candidates[a].Server, candidates[a].Tool,
			candidates[b].Server, candidates[b].Tool)
	})
	for rank, at := range semantic {
		fused[at] += 1 / (rrfK + float64(rank))
	}

	return fused
}

// reportedFusion turns a fused score into the integer clients see, the
// way [reportedScore] does for a lexical one. A fused score is far
// smaller — a top rank is 1/60 — so it is scaled to keep the digits a
// caller reads apart.
func reportedFusion(score float64) int {
	if score <= 0 {
		return 0
	}
	return max(1, int(math.Round(score*1_000_000)))
}

// compareOrigins orders two tools by where they come from, which is what
// breaks a tie: the exposed name is empty for most tools, so it cannot.
func compareOrigins(aServer, aTool, bServer, bTool string) int {
	if origin := strings.Compare(aServer, bServer); origin != 0 {
		return origin
	}
	return strings.Compare(aTool, bTool)
}

// lowerTerms are the query's words, lowered, for the search cursor to
// recognise the same query again.
func lowerTerms(query string) []string {
	words := strings.Fields(query)
	terms := make([]string, len(words))
	for i, word := range words {
		terms[i] = strings.ToLower(word)
	}
	return terms
}

// queryTerms are the distinct terms of a query: each word's terms (see
// [wordTerms]), plus the whole query joined into one — the form under
// which every tool's name is indexed — so that a query spelling a
// complete name in several words ("read file") meets read_file exactly.
// The join is carried in both spellings, since only the caller's words
// know which number it meant.
func queryTerms(words []string) []string {
	var terms, pieces []string
	for _, word := range words {
		terms = append(terms, wordTerms(word)...)
		pieces = append(pieces, tokenizeVerbatim(word)...)
	}
	written, folded := joinedSpellings(pieces, foldedPieces(pieces))
	terms = appendNewTerms(terms, written, folded)
	slices.Sort(terms)
	return slices.Compact(terms)
}

// wordTerms are one query word's terms plus, when it split, the word
// joined back together: readFile is read, file and readfile, and it is
// the last of these that meets a name indexed as readfile. The written
// spelling rides along too, so that a caller who writes listissues
// still meets list_issues.
func wordTerms(word string) []string {
	pieces := tokenizeVerbatim(word)
	terms := appendNewTerms(foldedPieces(pieces), pieces...)
	written, folded := joinedSpellings(pieces, foldedPieces(pieces))
	return appendNewTerms(terms, written, folded)
}

// nameTerms are a tool name's terms plus the name joined as written, so
// that readfile finds read_file: the joined form is what a caller writes
// when it does not know how the name is spelled. The folded spelling is
// deliberately not indexed here, because read_file and read_files would
// join to the same term and a caller spelling either exactly would land
// on both. The folded join lives on the query side instead.
//
// Many servers prefix every tool with their own name: slack_list_channels,
// browser_take_screenshot, git_diff. A caller asking for listChannels has
// already left the server out, so the name is also joined without the
// prefix when it begins with the server's pieces — listchannels meets
// slack_list_channels exactly, the way it meets a twin called
// list_channels on another server.
func nameTerms(server, name string) []string {
	pieces := tokenizeVerbatim(name)
	written, _ := joinedSpellings(pieces, nil)
	var unprefixed string
	if prefix := tokenizeVerbatim(server); len(prefix) > 0 &&
		len(pieces) > len(prefix) && slices.Equal(pieces[:len(prefix)], prefix) {
		unprefixed, _ = joinedSpellings(pieces[len(prefix):], nil)
	}
	for i, piece := range pieces {
		pieces[i] = foldTerm(piece)
	}
	return appendNewTerms(pieces, written, unprefixed)
}

// joinedSpellings are the ways a piece sequence is joined into one term:
// as written, and, when stemming changes it, the stemmed spelling
// too. A sequence of fewer than two pieces has no joined form and
// reports none.
func joinedSpellings(written, folded []string) (string, string) {
	if len(written) < 2 {
		return "", ""
	}
	joined := strings.Join(written, "")
	if len(folded) < 2 {
		return joined, ""
	}
	if f := strings.Join(folded, ""); f != joined {
		return joined, f
	}
	return joined, ""
}

// appendNewTerms appends the terms not already in the list, skipping the
// empty string, which the join helpers use to mean "no joined form". A
// repeat would otherwise double a term's count in the index.
func appendNewTerms(terms []string, more ...string) []string {
	for _, term := range more {
		if term != "" && !slices.Contains(terms, term) {
			terms = append(terms, term)
		}
	}
	return terms
}

// argumentTerms are the terms of a tool's argument text, with each
// word's joined form as well, so that a parameter name is matched the
// way a tool name is: a caller writing fullPage or full_page meets
// fullPage exactly, rather than as the two common words it splits into.
// The join keeps the written spelling, as it does for names.
func argumentTerms(text string) []string {
	var terms []string
	for _, word := range strings.Fields(text) {
		pieces := tokenizeVerbatim(word)
		written, _ := joinedSpellings(pieces, nil)
		for i, piece := range pieces {
			pieces[i] = foldTerm(piece)
		}
		terms = append(terms, pieces...)
		terms = appendNewTerms(terms, written)
	}
	return terms
}

// searchIndex is an inverted index over one set of candidates: for each
// term, where it occurs. It is built per search: the candidates are
// whatever the caller has cached, and a few thousand short texts index in
// a small fraction of the time an upstream call takes. What it costs is
// the allocations, so the index is a handful of flat slices and one map
// rather than a map per document.
type searchIndex struct {
	terms   map[string]*termEntry
	lengths [][fieldCount]int
	avgLen  [fieldCount]float64
}

type termEntry struct {
	// df is how many documents contain the term in any field.
	df int
	// postings are in document order, and a document's postings are
	// contiguous — one per field the term occurs in.
	postings []posting
}

type posting struct {
	doc   int32
	field uint8
	count uint16
}

func indexCandidates(candidates []Searchable) *searchIndex {
	idx := &searchIndex{
		terms:   make(map[string]*termEntry),
		lengths: make([][fieldCount]int, len(candidates)),
	}
	// The server's fields are the same for every one of its tools, and a
	// server has many tools; tokenizing them once per server rather than
	// once per tool removes most of the tokenizing.
	type serverFields struct{ name, title, description string }
	serverTerms := make(map[serverFields][3][]string)

	var total [fieldCount]int
	for i, candidate := range candidates {
		key := serverFields{candidate.Server, candidate.ServerTitle, candidate.ServerDescription}
		shared, ok := serverTerms[key]
		if !ok {
			shared = [3][]string{
				tokenize(key.name),
				appendCharacterTerms(tokenize(key.title), key.title),
				appendCharacterTerms(tokenize(key.description), key.description),
			}
			serverTerms[key] = shared
		}
		texts := [fieldCount][]string{
			fieldName:              nameTerms(candidate.Server, candidate.Tool),
			fieldServer:            shared[0],
			fieldServerTitle:       shared[1],
			fieldDescription:       appendCharacterTerms(tokenize(candidate.Description), candidate.Description),
			fieldServerDescription: shared[2],
			fieldArguments:         argumentTerms(candidate.Arguments),
		}
		for f, terms := range texts {
			idx.lengths[i][f] = len(terms)
			total[f] += len(terms)
			for _, term := range terms {
				idx.add(term, int32(i), uint8(f))
			}
		}
	}
	for f := range fieldCount {
		if len(candidates) > 0 {
			idx.avgLen[f] = float64(total[f]) / float64(len(candidates))
		}
	}
	return idx
}

// add records one occurrence. Documents and their fields are indexed in
// order, so an occurrence belongs to the last posting or starts a new one.
func (idx *searchIndex) add(term string, doc int32, field uint8) {
	entry := idx.terms[term]
	if entry == nil {
		entry = &termEntry{}
		idx.terms[term] = entry
	}
	if n := len(entry.postings); n > 0 {
		last := &entry.postings[n-1]
		if last.doc == doc {
			if last.field == field {
				last.count++
				return
			}
			entry.postings = append(entry.postings, posting{doc: doc, field: field, count: 1})
			return
		}
	}
	entry.df++
	entry.postings = append(entry.postings, posting{doc: doc, field: field, count: 1})
}

func (idx *searchIndex) has(term string) bool {
	return idx.terms[term] != nil
}

// scores is the fielded BM25 score of every document for the terms.
func (idx *searchIndex) scores(terms []string) []float64 {
	n := float64(len(idx.lengths))
	scores := make([]float64, len(idx.lengths))
	for _, term := range terms {
		entry := idx.terms[term]
		if entry == nil {
			continue
		}
		idf := math.Log(1 + (n-float64(entry.df)+0.5)/(float64(entry.df)+0.5))

		postings := entry.postings
		for j := 0; j < len(postings); {
			doc := postings[j].doc
			// Weighted frequency across the document's fields, each
			// discounted for its length relative to the average.
			tf := 0.0
			for ; j < len(postings) && postings[j].doc == doc; j++ {
				p := postings[j]
				norm := 1 - bm25B + bm25B*float64(idx.lengths[doc][p.field])/idx.avgLen[p.field]
				tf += fieldWeights[p.field] * float64(p.count) / norm
			}
			scores[doc] += idf * tf * (bm25K1 + 1) / (tf + bm25K1)
		}
	}
	return scores
}

// wordsMatched counts, for every document, the query words of which at
// least one term is in it.
func (idx *searchIndex) wordsMatched(words []string) []int {
	matched := make([]int, len(idx.lengths))
	// lastWord is the word most recently counted for a document, so that
	// a word occurring in several of its fields is counted once.
	lastWord := make([]int, len(idx.lengths))
	for i := range lastWord {
		lastWord[i] = -1
	}
	for w, word := range words {
		for _, term := range wordTerms(word) {
			entry := idx.terms[term]
			if entry == nil {
				continue
			}
			for _, p := range entry.postings {
				if lastWord[p.doc] != w {
					lastWord[p.doc] = w
					matched[p.doc]++
				}
			}
		}
	}
	return matched
}

// reportedScore turns a score into the integer clients see. A hit that
// matched anything is never reported as zero, which is what a caller
// would read as "did not match".
func reportedScore(score float64) int {
	if score <= 0 {
		return 0
	}
	return max(1, int(math.Round(score*100)))
}
