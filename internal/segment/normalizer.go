package segment

import "strings"

// typographicReplacer maps common typographic characters to ASCII equivalents
// so that visually-identical source text hashes consistently and maximizes TM
// reuse across chapters.
var typographicReplacer = strings.NewReplacer(
	" ", " ", // non-breaking space
	"‘", "'", // left single quote
	"’", "'", // right single quote / apostrophe
	"“", `"`, // left double quote
	"”", `"`, // right double quote
	"–", "-", // en dash
	"—", "-", // em dash
	"…", "...", // horizontal ellipsis
)

// Normalize collapses whitespace runs to single spaces, trims the result, and
// folds typographic punctuation to ASCII. It is deterministic and is the basis
// for stable segment hashing.
func Normalize(text string) string {
	text = typographicReplacer.Replace(text)
	return strings.Join(strings.Fields(text), " ")
}
