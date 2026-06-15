package main

import (
	"regexp"
	"strings"
)

// dictionary holds a small EN→FR word map for the dev server. It is
// intentionally tiny: enough to demonstrate genuine word-level translation
// while keeping the binary self-contained. Unknown words pass through.
var dictionary = map[string]string{
	"the": "le", "a": "un", "an": "un", "and": "et", "or": "ou", "but": "mais",
	"hello": "bonjour", "world": "monde", "chapter": "chapitre", "one": "un",
	"two": "deux", "three": "trois", "book": "livre", "story": "histoire",
	"the end": "la fin", "title": "titre", "first": "premier", "second": "second",
	"body": "corps", "claim": "affirmation", "plain": "simple", "bold": "gras",
	"house": "maison", "went": "alla", "home": "maison", "he": "il", "she": "elle",
	"said": "dit", "was": "était", "is": "est", "of": "de", "to": "à", "in": "dans",
	"with": "avec", "for": "pour", "on": "sur", "day": "jour", "night": "nuit",
	"good": "bon", "morning": "matin", "left": "quitta",
}

// wordPattern captures word runs while leaving punctuation and placeholder
// tokens (e.g. <x id="1"/>) untouched.
var wordPattern = regexp.MustCompile(`[A-Za-z]+`)

// translate performs case-aware, word-level EN→FR substitution. Placeholder
// tokens and unknown words are preserved verbatim so downstream restoration and
// QA stay intact.
func translate(text string) string {
	return wordPattern.ReplaceAllStringFunc(text, func(word string) string {
		fr, ok := dictionary[strings.ToLower(word)]
		if !ok {
			return word
		}
		return matchCase(word, fr)
	})
}

// matchCase mirrors the capitalization of the source word onto the target.
func matchCase(src, dst string) string {
	if src == "" || dst == "" {
		return dst
	}
	if src == strings.ToUpper(src) && len(src) > 1 {
		return strings.ToUpper(dst)
	}
	if src[0] >= 'A' && src[0] <= 'Z' {
		return strings.ToUpper(dst[:1]) + dst[1:]
	}
	return dst
}
