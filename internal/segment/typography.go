package segment

import (
	"regexp"
	"strings"
	"unicode"
)

// frenchToken matches an inline placeholder (<x id="N"/>) or a glossary token
// (<g id="N"/>), including the paired form some providers normalize them to
// (<x id="N"></x>). The matched substring is preserved byte-for-byte and the
// token is treated as zero-width when FrenchTypography decides context.
var frenchToken = regexp.MustCompile(`<[xg] id="\d+"\s*/?>(?:\s*</[xg]>)?`)

// tokenSentinelBase is the first Private-Use-Area code point used to stand in
// for an extracted token while the text is being rewritten. Real book text does
// not contain these code points, so they never collide with content.
const tokenSentinelBase = 0xE000

// FrenchTypography rewrites a French target string into the project's single,
// deterministic typography, aligned on the English source conventions (Witch
// Cult Translations style). It is pure and idempotent: f(f(x)) == f(x).
//
// Rules:
//  1. A run of 3+ ASCII dots ("...") becomes one ellipsis ("…"). Existing "……"
//     is left untouched.
//  2. Exactly one U+00A0 sits after every « and before every ».
//  3. Exactly one U+00A0 sits before the first mark of a run of ": ; ! ?" when
//     the previous visible character is a letter, digit, …, », ), ], ” or an
//     apostrophe; a space already present before the mark becomes U+00A0
//     whatever precedes it. In-word colons (Re:Zero, 10:30) and URL colons
//     (://) are left alone.
//  4. The em dash (—) is glued to the following word or to a following . or ,
//     (« —Mot », mot—mot, « —. »); a single space is kept before it after
//     sentence punctuation (. ! ? …); "——" runs are treated as one dash; a dash
//     at the end or before » is left untouched apart from rule 2.
//
// Placeholder tokens (<x id="N"/>) and glossary tokens (<g id="N"/>) are
// preserved exactly and are zero-width for every context decision above.
func FrenchTypography(text string) string {
	if text == "" {
		return text
	}
	runes, tokens := tokenizeFrench(text)
	runes = fixEllipsis(runes)
	runes = fixGuillemets(runes)
	runes = fixHighPunct(runes, tokens)
	runes = fixEmDash(runes, tokens)
	return renderFrench(runes, tokens)
}

// tokenizeFrench replaces every token with a zero-width Private-Use sentinel
// rune and returns the rune slice plus the original token strings, indexed by
// the sentinel's offset from tokenSentinelBase.
func tokenizeFrench(text string) ([]rune, []string) {
	locs := frenchToken.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return []rune(text), nil
	}
	var (
		tokens []string
		out    []rune
	)
	last := 0
	for _, loc := range locs {
		out = append(out, []rune(text[last:loc[0]])...)
		out = append(out, rune(tokenSentinelBase+len(tokens)))
		tokens = append(tokens, text[loc[0]:loc[1]])
		last = loc[1]
	}
	out = append(out, []rune(text[last:])...)
	return out, tokens
}

// renderFrench rebuilds the string, expanding each sentinel back to its token.
func renderFrench(runes []rune, tokens []string) string {
	var b strings.Builder
	for _, r := range runes {
		if i := int(r) - tokenSentinelBase; i >= 0 && i < len(tokens) {
			b.WriteString(tokens[i])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isTokenSentinel(r rune, tokens []string) bool {
	i := int(r) - tokenSentinelBase
	return i >= 0 && i < len(tokens)
}

// isBreakingSpace reports the whitespace this normalizer may rewrite: the plain
// space, the no-break space and the narrow no-break space.
func isBreakingSpace(r rune) bool {
	return r == ' ' || r == '\u00a0' || r == '\u202f'
}

// fixEllipsis collapses every run of 3+ ASCII dots to a single U+2026.
func fixEllipsis(runes []rune) []rune {
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); {
		if runes[i] != '.' {
			out = append(out, runes[i])
			i++
			continue
		}
		j := i
		for j < len(runes) && runes[j] == '.' {
			j++
		}
		if j-i >= 3 {
			out = append(out, '…')
		} else {
			out = append(out, runes[i:j]...)
		}
		i = j
	}
	return out
}

// fixGuillemets forces exactly one U+00A0 after every « and before every ».
func fixGuillemets(runes []rune) []rune {
	out := make([]rune, 0, len(runes)+4)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '«':
			out = append(out, '«')
			j := i + 1
			for j < len(runes) && isBreakingSpace(runes[j]) {
				j++
			}
			out = append(out, '\u00a0')
			i = j - 1
		case '»':
			for len(out) > 0 && isBreakingSpace(out[len(out)-1]) {
				out = out[:len(out)-1]
			}
			out = append(out, '\u00a0', '»')
		default:
			out = append(out, runes[i])
		}
	}
	return out
}

func isHighPunct(r rune) bool {
	return highPunctClass(r) != 0
}

// highPunctClass groups high punctuation so a run stays within one class: the
// terminal marks (! ?) and the clitic marks (: ;) are separate runs. This keeps
// a speaker label's colon ("???:") distinct from the exclamation run before it,
// so each gets its own U+00A0 decision.
func highPunctClass(r rune) int {
	switch r {
	case '!', '?':
		return 1
	case ':', ';':
		return 2
	}
	return 0
}

// allowedBeforeHigh reports whether a U+00A0 may be forced before a high
// punctuation mark that follows r. Terminal marks (! ?) are included so a
// speaker label's colon after "???" gets its space.
func allowedBeforeHigh(r rune) bool {
	switch r {
	case '…', '»', ')', ']', '”', '\'', '’', '!', '?':
		return true
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// fixHighPunct forces one U+00A0 before the first mark of each run of ": ; ! ?".
func fixHighPunct(runes []rune, tokens []string) []rune {
	out := make([]rune, 0, len(runes)+4)
	for i := range runes {
		r := runes[i]
		if !isHighPunct(r) {
			out = append(out, r)
			continue
		}
		if i > 0 && highPunctClass(runes[i-1]) == highPunctClass(r) { // mid-run
			out = append(out, r)
			continue
		}
		if !spaceBeforeHigh(r, prevVisible(out, tokens), runes, i, tokens) {
			// A space the text already has before the mark becomes the no-break
			// space, whatever precedes it (■■■■ :, “Subaru“ :).
			if len(out) > 0 && isBreakingSpace(out[len(out)-1]) && prevVisible(out, tokens) != 0 {
				for len(out) > 0 && isBreakingSpace(out[len(out)-1]) {
					out = out[:len(out)-1]
				}
				out = append(out, '\u00a0')
			}
			out = append(out, r)
			continue
		}
		for len(out) > 0 && isBreakingSpace(out[len(out)-1]) {
			out = out[:len(out)-1]
		}
		out = append(out, '\u00a0', r)
	}
	return out
}

func spaceBeforeHigh(mark, prev rune, runes []rune, i int, tokens []string) bool {
	if !allowedBeforeHigh(prev) {
		return false
	}
	if mark == ':' {
		if isInWordColon(runes, i) {
			return false // Re:Zero, 10:30
		}
		if isURLColon(runes, i, tokens) {
			return false // scheme://
		}
	}
	return true
}

// isInWordColon reports whether the colon at index i is glued on both sides to
// a letter or digit, as in a title (Re:Zero) or a time (10:30): it is part of
// the word, not punctuation.
func isInWordColon(runes []rune, i int) bool {
	if i == 0 || i+1 >= len(runes) {
		return false
	}
	isWordRune := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	return isWordRune(runes[i-1]) && isWordRune(runes[i+1])
}

// isURLColon reports whether the colon at index i is immediately followed by
// "//" (ignoring zero-width tokens), i.e. a URL scheme separator.
func isURLColon(runes []rune, i int, tokens []string) bool {
	seen := 0
	for j := i + 1; j < len(runes) && seen < 2; j++ {
		if isTokenSentinel(runes[j], tokens) {
			continue
		}
		if runes[j] != '/' {
			return false
		}
		seen++
	}
	return seen == 2
}

// fixEmDash glues the em dash to the following word, keeping a single space
// before it only after sentence punctuation. A "——" run is handled as one dash.
// A dash followed by . or , is glued to it (the source's "—." silence); a dash
// at the end or before » is left untouched (rule 2 still applies).
func fixEmDash(runes []rune, tokens []string) []rune {
	out := make([]rune, 0, len(runes)+2)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '—' {
			out = append(out, runes[i])
			continue
		}
		j := i
		for j < len(runes) && runes[j] == '—' {
			j++
		}
		prev := prevVisible(out, tokens)
		next := nextVisible(runes, j, tokens)

		if next == 0 || next == '»' {
			out = append(out, runes[i:j]...) // trailing dash: leave as-is
			i = j - 1
			continue
		}

		switch {
		case prev == '.' || prev == '!' || prev == '?' || prev == '…':
			for len(out) > 0 && isBreakingSpace(out[len(out)-1]) {
				out = out[:len(out)-1]
			}
			out = append(out, ' ')
		case unicode.IsLetter(prev) || unicode.IsDigit(prev):
			for len(out) > 0 && isBreakingSpace(out[len(out)-1]) {
				out = out[:len(out)-1]
			}
		default:
			// Opening context (« ( start) or other: leave the left side alone.
		}

		out = append(out, runes[i:j]...)

		k := j
		for k < len(runes) && isBreakingSpace(runes[k]) {
			k++
		}
		i = k - 1
	}
	return out
}

// prevVisible returns the last emitted rune that is neither whitespace nor a
// token sentinel, or 0 when none precedes.
func prevVisible(out []rune, tokens []string) rune {
	for i := len(out) - 1; i >= 0; i-- {
		if isBreakingSpace(out[i]) || isTokenSentinel(out[i], tokens) {
			continue
		}
		return out[i]
	}
	return 0
}

// nextVisible returns the first rune at or after from that is neither whitespace
// nor a token sentinel, or 0 when none follows.
func nextVisible(runes []rune, from int, tokens []string) rune {
	for i := from; i < len(runes); i++ {
		if isBreakingSpace(runes[i]) || isTokenSentinel(runes[i], tokens) {
			continue
		}
		return runes[i]
	}
	return 0
}
