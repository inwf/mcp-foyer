package gateway

import (
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

	// Matched is how many of the query's words this tool matched. It
	// does not order the results — a rare word answered once says more
	// than three common ones — but it lets a caller tell a tool that
	// answered the whole question from one that answered a word of it.
	Matched int `json:"matched"`

	// Score is relative: it orders one search's hits and means nothing
	// across searches.
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
// chances to contain the term.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
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
// A blank query matches everything, so that a caller can page through
// the full list with the same call. Ties break on the origin rather than
// on the exposed name, which is empty for every tool an installation has
// not exposed — and that is most of them.
func SearchTools(query string, candidates []Searchable, limit int) []SearchHit {
	hits, _ := searchCandidates(query, candidates, limit)
	return hits
}

// UnmatchedTerms returns the query words that appear in no candidate at
// all, spelled as the caller wrote them.
//
// It exists so that an empty or thin result can say why. "No hits" reads
// as "this gateway cannot do that", which is a conclusion a caller should
// not reach because it used a word this installation does not use. Naming
// the words that found nothing turns a dead end into a next attempt.
func UnmatchedTerms(query string, candidates []Searchable) []string {
	_, unmatched := searchCandidates(query, candidates, 0)
	return unmatched
}

// searchCandidates answers both questions from one index, for the caller
// that asks both.
func searchCandidates(query string, candidates []Searchable, limit int) ([]SearchHit, []string) {
	words := strings.Fields(query)
	idx := indexCandidates(candidates)

	scores := idx.scores(queryTerms(words))
	matched := idx.wordsMatched(words)

	hits := make([]SearchHit, 0, len(candidates))
	for i, candidate := range candidates {
		// A query with no words asks for everything; one with words asks
		// for the tools that answered at least one of them.
		if len(words) > 0 && matched[i] == 0 {
			continue
		}
		hits = append(hits, SearchHit{
			Server:      candidate.Server,
			Tool:        candidate.Tool,
			Exposed:     candidate.Exposed,
			Description: candidate.Description,
			Matched:     matched[i],
			Score:       reportedScore(scores[i]),
		})
	}

	slices.SortFunc(hits, func(a, b SearchHit) int {
		if a.Score != b.Score {
			return b.Score - a.Score
		}
		if origin := strings.Compare(a.Server, b.Server); origin != 0 {
			return origin
		}
		return strings.Compare(a.Tool, b.Tool)
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
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
	return hits, unmatched
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
func queryTerms(words []string) []string {
	var terms, joined []string
	for _, word := range words {
		terms = append(terms, wordTerms(word)...)
		joined = append(joined, tokenize(word)...)
	}
	if len(joined) > 1 {
		terms = append(terms, strings.Join(joined, ""))
	}
	slices.Sort(terms)
	return slices.Compact(terms)
}

// wordTerms are one query word's terms plus, when it split, the word
// joined back together: readFile is read, file and readfile, and it is
// the last of these that meets a name indexed as readfile.
func wordTerms(word string) []string {
	return joinedTerms(tokenize(word))
}

// nameTerms are a tool name's terms plus the name joined into one, so
// that readfile finds read_file: the joined form is what a caller writes
// when it does not know how the name is spelled.
func nameTerms(name string) []string {
	return joinedTerms(tokenize(name))
}

func joinedTerms(terms []string) []string {
	if len(terms) > 1 {
		terms = append(terms, strings.Join(terms, ""))
	}
	return terms
}

// argumentTerms are the terms of a tool's argument text, with each
// word's joined form as well, so that a parameter name is matched the
// way a tool name is: a caller writing fullPage or full_page meets
// fullPage exactly, rather than as the two common words it splits into.
func argumentTerms(text string) []string {
	var terms []string
	for _, word := range strings.Fields(text) {
		terms = append(terms, wordTerms(word)...)
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
			shared = [3][]string{tokenize(key.name), tokenize(key.title), tokenize(key.description)}
			serverTerms[key] = shared
		}
		texts := [fieldCount][]string{
			fieldName:              nameTerms(candidate.Tool),
			fieldServer:            shared[0],
			fieldServerTitle:       shared[1],
			fieldDescription:       tokenize(candidate.Description),
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
