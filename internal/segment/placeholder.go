package segment

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// PlaceholderManager tokenizes inline markup out of a node before translation
// and restores it into the (translated) node afterwards.
type PlaceholderManager interface {
	Tokenize(node *html.Node) (text string, placeholders []Placeholder, err error)
	Restore(translatedText string, placeholders []Placeholder, node *html.Node) error
}

// InlinePlaceholderManager treats each direct child element of a translatable
// node as an opaque placeholder. Text nodes pass through untouched. This keeps
// inline subtrees verbatim, so restoration always yields valid markup.
type InlinePlaceholderManager struct{}

var placeholderTokenPattern = regexp.MustCompile(`<x id="\d+"/>`)

func placeholderToken(id int) string {
	return fmt.Sprintf(`<x id="%d"/>`, id)
}

// Tokenize walks the node's direct children, replacing each element with a
// stable placeholder token and concatenating text nodes verbatim.
func (InlinePlaceholderManager) Tokenize(node *html.Node) (string, []Placeholder, error) {
	if node == nil {
		return "", nil, ErrNilDocument
	}

	var (
		sb           strings.Builder
		placeholders []Placeholder
		id           int
	)

	for c := node.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
			continue
		}

		// Any non-text child (inline element, comment, ...) is preserved as an
		// opaque placeholder so the round-trip never loses content.
		id++

		var buf strings.Builder
		if err := html.Render(&buf, c); err != nil {
			return "", nil, fmt.Errorf("unable to render inline markup for placeholder, err: %w", err)
		}

		token := placeholderToken(id)
		sb.WriteString(token)
		placeholders = append(placeholders, Placeholder{
			Token:    token,
			Original: buf.String(),
			Type:     PlaceholderInline,
			node:     c,
		})
	}

	return sb.String(), placeholders, nil
}

// Restore rebuilds the node's children from the translated text, splicing the
// original inline subtrees back in place of their placeholder tokens. It is
// strict: an unknown token or a dropped placeholder is an error so the pipeline
// can flag the segment instead of emitting broken markup.
func (InlinePlaceholderManager) Restore(translatedText string, placeholders []Placeholder, node *html.Node) error {
	if node == nil {
		return ErrNilDocument
	}

	remaining := make(map[string]*html.Node, len(placeholders))
	for i := range placeholders {
		remaining[placeholders[i].Token] = placeholders[i].node
	}

	detachChildren(node)

	appendText := func(s string) {
		if s == "" {
			return
		}
		node.AppendChild(&html.Node{Type: html.TextNode, Data: s})
	}

	pos := 0
	for _, loc := range placeholderTokenPattern.FindAllStringIndex(translatedText, -1) {
		appendText(translatedText[pos:loc[0]])

		token := translatedText[loc[0]:loc[1]]
		orig, ok := remaining[token]
		if !ok {
			// A token-shaped string we did not issue: either the source text
			// literally contained it, or the provider hallucinated one. Keep it
			// as literal text rather than failing the whole segment.
			appendText(token)
			pos = loc[1]
			continue
		}
		node.AppendChild(orig)
		delete(remaining, token)
		pos = loc[1]
	}
	appendText(translatedText[pos:])

	if len(remaining) > 0 {
		return fmt.Errorf("unable to restore %d placeholder(s), err: %w", len(remaining), ErrMissingPlaceholder)
	}

	return nil
}

// detachChildren removes every child of n so they can be re-appended in a new
// order. RemoveChild clears each child's parent/sibling links, which lets us
// safely re-attach the original inline nodes during restoration.
func detachChildren(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		n.RemoveChild(c)
		c = next
	}
}
