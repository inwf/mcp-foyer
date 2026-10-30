package gateway

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/kljensen/snowball/english"
)

// tokenize splits text into the terms the search matches on.
//
// Tool names are written every way there is: read_file, readFile,
// read-file, ReadFile. A caller does not know which, and should not have
// to. So a name is split at underscores, dashes, spaces and camel-case
// boundaries, lowered, and reduced to its English stem, so that "issues"
// finds list_issues, "searching" finds search_files and "readFile" finds
// read_file.
//
// Chinese, Japanese and Korean have no word boundaries to split at, and
// a dictionary would be a heavy dependency for a gateway. Runs of those
// scripts are cut into overlapping pairs of characters instead — 读取文件
// becomes 读取, 取文, 文件 — which is how Lucene handles them, and works
// well on text this short: a query shares most of its pairs with the
// description that answers it, and the meaningless pairs are rare enough
// not to matter. A lone character is kept as itself.
//
// Scripts are split from one another, so 读取file is two terms.
//
// Terms are substrings of the input wherever possible. The whole
// directory is tokenized on every search, so what this allocates is what
// a search costs.
func tokenize(s string) []string {
	terms := splitPieces(s)
	for i, term := range terms {
		terms[i] = foldTerm(term)
	}
	return terms
}

// tokenizeVerbatim splits like tokenize but keeps every word in the
// spelling it was written in. The search joins a name into one term in
// both spellings, which is what keeps read_file and read_files apart.
func tokenizeVerbatim(s string) []string {
	return splitPieces(s)
}

// foldedPieces returns the pieces reduced to their stems, a copy.
func foldedPieces(pieces []string) []string {
	folded := make([]string, len(pieces))
	for i, piece := range pieces {
		folded[i] = foldTerm(piece)
	}
	return folded
}

// splitPieces cuts text into lowercased words and CJK pairs, in the
// order they appear, before any stemming. It is the one walk of the
// text; stemming is a second pass over what it returns, so a caller that
// needs both spellings splits only once.
func splitPieces(s string) []string {
	if s == "" {
		return nil
	}
	// Roughly one term per five bytes, for prose and for pairs alike;
	// growing the slice would be most of what this function allocates.
	out := make([]string, 0, len(s)/5+1)
	wordStart, runStart := -1, -1

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case isPairScript(r):
			if wordStart >= 0 {
				out = appendWordTerms(out, s[wordStart:i])
				wordStart = -1
			}
			if runStart < 0 {
				runStart = i
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if runStart >= 0 {
				out = appendPairTerms(out, s[runStart:i])
				runStart = -1
			}
			if wordStart < 0 {
				wordStart = i
			}
		default:
			if wordStart >= 0 {
				out = appendWordTerms(out, s[wordStart:i])
				wordStart = -1
			}
			if runStart >= 0 {
				out = appendPairTerms(out, s[runStart:i])
				runStart = -1
			}
		}
		i += size
	}
	if wordStart >= 0 {
		out = appendWordTerms(out, s[wordStart:])
	}
	if runStart >= 0 {
		out = appendPairTerms(out, s[runStart:])
	}
	return out
}

// appendWordTerms splits one alphanumeric run at camel-case boundaries
// and appends each piece, lowered.
func appendWordTerms(out []string, word string) []string {
	start := 0
	var prev rune
	for i, r := range word {
		if i > 0 && isCamelBoundary(prev, r, word[i+utf8.RuneLen(r):]) {
			out = append(out, strings.ToLower(word[start:i]))
			start = i
		}
		prev = r
	}
	return append(out, strings.ToLower(word[start:]))
}

// isCamelBoundary reports whether a new word starts at r: an upper-case
// letter after a lower-case one (readFile), or the last capital of an
// acronym before a lower-case letter (HTTPServer → HTTP, Server).
func isCamelBoundary(prev, r rune, rest string) bool {
	if !unicode.IsUpper(r) {
		return false
	}
	if unicode.IsLower(prev) || unicode.IsDigit(prev) {
		return true
	}
	if !unicode.IsUpper(prev) {
		return false
	}
	next, _ := utf8.DecodeRuneInString(rest)
	return unicode.IsLower(next)
}

func foldTerm(word string) string {
	// ToLower returns its argument unchanged, without copying, when
	// there is nothing to lower — which is nearly always.
	return stemTerm(strings.ToLower(word))
}

// stemTerm reduces an English word to its Snowball stem, so that a query
// in one form finds a name or a description in another: searching finds
// search_files, deleting finds delete_relations, committed finds
// git_commit. Applied to both sides, a stem that is not a word ("issu")
// still matches; it just matches the same form on each side.
//
// Only lower-case ASCII words are stemmed. A CJK pair is not English, and
// a word with digits in it ("v2", "s3") is an identifier, which a stemmer
// would only damage.
func stemTerm(term string) string {
	if !isASCIILetters(term) {
		return term
	}
	if stem, ok := stems.Load(term); ok {
		return stem.(string)
	}
	// Function words are stemmed too: they are cut from the query by
	// their written spelling before this runs, and a stemmer that left
	// some words alone would only add a second rule to reason about.
	stem := english.Stem(term, true)
	// The key is cloned: term is usually a substring of a description,
	// and keeping it as a key would keep the whole description alive.
	stems.Store(strings.Clone(term), stem)
	return stem
}

// stems remembers every word's stem. The whole directory is tokenized on
// every search, and stemming is most of what that costs; a directory's
// vocabulary is a few thousand words that every search repeats, so the
// table stops growing once the directory has been searched once.
var stems sync.Map

func isASCIILetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

// appendPairTerms appends the overlapping character pairs of a run, or
// the run itself when it is a single character.
func appendPairTerms(out []string, run string) []string {
	_, first := utf8.DecodeRuneInString(run)
	if first == len(run) {
		return append(out, run)
	}
	start := 0
	for i := first; i < len(run); {
		_, size := utf8.DecodeRuneInString(run[i:])
		out = append(out, run[start:i+size])
		start = i
		i += size
	}
	return out
}

// appendCharacterTerms appends every character of the text's CJK runs as
// a term of its own. It is for the text being indexed, not for queries.
//
// A caller writing a single character (诗) means a word the directory
// writes inside longer ones (古诗词), and pairs alone cannot meet it. A
// character is far more common than a pair, so its weight stays below
// that of the pair the caller would have written. Queries are not cut
// this way: a query of several characters keeps to its pairs, and a
// query for something the directory does not serve (火车票) does not come
// back answered by tools that happen to contain 车.
func appendCharacterTerms(out []string, s string) []string {
	for i, r := range s {
		if isPairScript(r) {
			out = append(out, s[i:i+utf8.RuneLen(r)])
		}
	}
	return out
}

// isPairScript reports whether a rune belongs to a script that is
// tokenized in pairs rather than at word boundaries.
func isPairScript(r rune) bool {
	// The prolonged sound mark (タワー) is script-neutral in Unicode but
	// only ever appears inside a Japanese word.
	return r == 'ー' || unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}
