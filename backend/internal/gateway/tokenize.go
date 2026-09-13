package gateway

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenize splits text into the terms the search matches on.
//
// Tool names are written every way there is: read_file, readFile,
// read-file, ReadFile. A caller does not know which, and should not have
// to. So a name is split at underscores, dashes, spaces and camel-case
// boundaries, lowered, and reduced to a common plural-free form, so that
// "issues" finds list_issues and "readFile" finds read_file.
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
// and appends each piece, lowered and folded.
func appendWordTerms(out []string, word string) []string {
	start := 0
	var prev rune
	for i, r := range word {
		if i > 0 && isCamelBoundary(prev, r, word[i+utf8.RuneLen(r):]) {
			out = append(out, foldTerm(word[start:i]))
			start = i
		}
		prev = r
	}
	return append(out, foldTerm(word[start:]))
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
	return foldPlural(strings.ToLower(word))
}

// foldPlural strips a regular English plural so that a query in one
// number finds a name in the other. It is deliberately not a stemmer:
// only the plural suffixes, only on words long enough that the ending is
// a suffix rather than the word ("is", "has"). Applied to both sides,
// an over-eager fold still matches; it just matches the same wrong form.
func foldPlural(term string) string {
	if len(term) <= 3 || !isASCIILetters(term) || !strings.HasSuffix(term, "s") {
		return term
	}
	switch {
	case strings.HasSuffix(term, "ss"):
		return term
	case strings.HasSuffix(term, "ies"):
		return term[:len(term)-3] + "y"
	case strings.HasSuffix(term, "sses"), strings.HasSuffix(term, "xes"),
		strings.HasSuffix(term, "zes"), strings.HasSuffix(term, "ches"), strings.HasSuffix(term, "shes"):
		return term[:len(term)-2]
	default:
		return term[:len(term)-1]
	}
}

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
