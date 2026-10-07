package gateway

// commonQueryWords are the words a query spends without asking for
// anything: articles, pronouns, prepositions, auxiliaries, question
// words and the verbs of asking that every request a model writes is
// made of ("what does the ... do").
//
// They are cut from the query before it is scored. A word sitting in
// most descriptions cannot tell one tool from another, and letting it
// match is how a query for something the directory does not serve comes
// back looking answered: "what does the acme toolkit do" matched what,
// the and do across seventeen tools, and the caller had to read every
// hit to learn that none of them was about acme. With the words cut, the
// same query returns nothing and names acme and toolkit in unmatched,
// which is the truth the caller can act on.
//
// The list holds only function words. A frequency threshold was the
// alternative and was rejected: it would decide differently on a
// directory of ten tools than on one of ten thousand, and in a directory
// of file tools it would drop "file", which is exactly the word the
// caller meant. A wrong entry here means ignoring a word somebody
// needed, so only words that carry no capability in any directory are on
// it, and a word that names something is never treated as common (see
// searchIndex.namesAWord). "List", "get", "read" and "search" are
// deliberately absent: they are verbs of asking, but they are also what
// tools are called, and a directory that names any tool with them keeps
// them through the naming exemption anyway.
//
// The list is English. Descriptions and queries in this ecosystem are
// overwhelmingly English; CJK text is cut into pairs, which are too
// specific to be function words, so it needs no list.
var commonQueryWords = map[string]bool{
	// Articles, conjunctions, prepositions.
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true,
	"nor": true, "if": true, "then": true, "than": true, "so": true, "as": true,
	"at": true, "by": true, "for": true, "from": true, "in": true, "into": true,
	"of": true, "off": true, "on": true, "onto": true, "to": true, "with": true,
	"without": true, "over": true, "under": true, "about": true, "above": true,
	"below": true, "between": true, "during": true, "after": true, "before": true,
	"up": true, "down": true, "out": true, "again": true, "once": true,
	"here": true, "there": true, "while": true, "because": true, "though": true,
	"although": true, "unless": true, "until": true, "since": true, "per": true,
	"via": true, "within": true, "across": true, "along": true, "around": true,
	"through": true, "against": true, "among": true, "upon": true, "whether": true,

	// Pronouns and determiners.
	"i": true, "me": true, "my": true, "mine": true, "we": true, "us": true,
	"our": true, "you": true, "your": true, "he": true, "him": true, "his": true,
	"she": true, "her": true, "it": true, "its": true, "they": true, "them": true,
	"their": true, "this": true, "that": true, "these": true, "those": true,
	"some": true, "any": true, "all": true, "both": true, "each": true, "few": true,
	"more": true, "most": true, "other": true, "others": true, "such": true,
	"no": true, "none": true, "same": true, "own": true,

	// Auxiliaries.
	"am": true, "is": true, "are": true, "was": true, "were": true, "be": true,
	"been": true, "being": true, "have": true, "has": true, "had": true,
	"having": true, "do": true, "does": true, "did": true, "doing": true,
	"done": true, "can": true, "could": true, "will": true, "would": true,
	"shall": true, "should": true, "may": true, "might": true, "must": true,

	// Question words and the boilerplate of asking.
	"what": true, "which": true, "who": true, "whom": true, "whose": true,
	"why": true, "how": true, "when": true, "where": true, "please": true,
	"thanks": true, "thank": true, "hello": true, "hi": true, "want": true,
	"need": true, "help": true, "find": true, "show": true, "give": true,
	"tell": true, "get": true, "looking": true, "let": true,
}

// distinctiveWords returns the query's words minus the ones that cannot
// pick anything out, keeping the order so that a query spelled in several
// words still meets a name joined from them.
func (idx *searchIndex) distinctiveWords(words []string) []string {
	out := make([]string, 0, len(words))
	for _, word := range words {
		if idx.commonWord(word) && !idx.namesAWord(word) {
			continue
		}
		out = append(out, word)
	}
	return out
}

// commonWord reports whether the word is one of [commonQueryWords]. Only
// a word that splits into exactly one piece is compared, so "how-to" and
// a CJK term are never judged by a list of single English words.
func (idx *searchIndex) commonWord(word string) bool {
	pieces := tokenizeVerbatim(word)
	return len(pieces) == 1 && commonQueryWords[pieces[0]]
}

// namesAWord reports whether any of the word's terms occurs in a tool's
// name or a server's name. A common word that names something is not
// common here: a server called "in" or a tool called "do" makes those
// words worth matching, and the caller that typed them meant that tool.
//
// Descriptions, handshake titles and parameter text do not count,
// however much they look like names. They are prose, and they are full
// of function words: a parameter documented as "the maximum number of
// results" put "the" in every tool's argument field, and an exemption
// read from there kept the word matching everything — the defect this
// whole rule exists to remove.
func (idx *searchIndex) namesAWord(word string) bool {
	for _, term := range wordTerms(word) {
		entry := idx.terms[term]
		if entry == nil {
			continue
		}
		for _, p := range entry.postings {
			if p.field == fieldName || p.field == fieldServer {
				return true
			}
		}
	}
	return false
}
