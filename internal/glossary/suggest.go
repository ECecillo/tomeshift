package glossary

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Candidate is a suggested glossary term with its observed frequency.
type Candidate struct {
	Term  string `json:"term"`
	Count int    `json:"count"`
}

// Suggest scans text for proper-noun candidates: words capitalized mid-sentence
// (i.e. not merely sentence-initial) and runs of consecutive capitalized words
// (e.g. "New York"). Candidates occurring at least minCount times are returned,
// ranked by frequency then alphabetically. It is a heuristic to bootstrap a
// series glossary — the output is meant to be reviewed and edited.
func Suggest(text string, minCount int) []Candidate {
	if minCount < 1 {
		minCount = 1
	}

	words, capitalized := tokenize(text)

	single := map[string]int{}
	for i, w := range words {
		// Count a capitalized word only when it is NOT sentence-initial, since
		// proper nouns are capitalized regardless of position.
		if capitalized[i] && !sentenceInitial(words, i) {
			single[w]++
		}
	}

	phrases := map[string]int{}
	for i := 0; i < len(words); i++ {
		if !capitalized[i] {
			continue
		}
		j := i
		for j+1 < len(words) && capitalized[j+1] {
			j++
		}
		if j > i { // a run of 2+ capitalized words
			phrases[strings.Join(words[i:j+1], " ")]++
		}
		i = j
	}

	seen := map[string]bool{}
	var out []Candidate
	add := func(term string, count int) {
		if count >= minCount && !seen[term] {
			seen[term] = true
			out = append(out, Candidate{Term: term, Count: count})
		}
	}
	for term, c := range phrases {
		add(term, c)
	}
	for term, c := range single {
		add(term, c)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Term < out[j].Term
	})
	return out
}

// SuggestTerms converts candidates into a locked-glossary skeleton with the
// target term defaulting to the source term (to be edited by hand).
func SuggestTerms(candidates []Candidate) []Term {
	terms := make([]Term, 0, len(candidates))
	for _, c := range candidates {
		terms = append(terms, Term{EN: c.Term, FR: c.Term, Locked: true})
	}
	return terms
}

// tokenize splits text into letter-runs, recording whether each starts with an
// uppercase letter.
func tokenize(text string) (words []string, capitalized []bool) {
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		w := b.String()
		b.Reset()
		r, _ := utf8.DecodeRuneInString(w)
		words = append(words, w)
		capitalized = append(capitalized, unicode.IsUpper(r))
	}

	for _, r := range text {
		if unicode.IsLetter(r) {
			b.WriteRune(r)
			continue
		}
		flush()
		// A sentence/paragraph boundary marker resets capitalization context;
		// we encode it as an empty sentinel word so sentenceInitial can detect it.
		if isBoundary(r) {
			words = append(words, "")
			capitalized = append(capitalized, false)
		}
	}
	flush()
	return words, capitalized
}

func isBoundary(r rune) bool {
	switch r {
	case '.', '!', '?', ':', ';', '\n', '"', '«', '»', '“', '”', '—':
		return true
	}
	return false
}

// sentenceInitial reports whether the word at i is the first real word of a
// sentence: at the start of text, or immediately after a boundary sentinel.
func sentenceInitial(words []string, i int) bool {
	if i == 0 {
		return true
	}
	return words[i-1] == ""
}
