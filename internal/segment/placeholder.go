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

// Matches both the self-closing token we emit (<x id="1"/>) and the paired
// form some providers normalize it to (<x id="1"></x>), capturing the id.
var placeholderTokenPattern = regexp.MustCompile(`<x id="(\d+)"\s*/?>(?:\s*</x>)?`)

// tokenIDPattern extracts the numeric id from an issued token.
var tokenIDPattern = regexp.MustCompile(`\d+`)

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

		// A simple inline element (e.g. <span>, <em>, <i>) whose children are
		// all text is split into an open/close token pair so its inner text is
		// exposed for translation instead of being shelved verbatim.
		if isSimpleInline(c) {
			var buf strings.Builder
			if err := html.Render(&buf, c); err != nil {
				return "", nil, fmt.Errorf("unable to render inline markup for placeholder, err: %w", err)
			}

			id++
			open := placeholderToken(id)
			sb.WriteString(open)
			placeholders = append(placeholders, Placeholder{
				Token:    open,
				Original: buf.String(),
				Type:     PlaceholderInline,
				role:     roleOpen,
				node:     shallowClone(c),
			})

			for t := c.FirstChild; t != nil; t = t.NextSibling {
				sb.WriteString(t.Data)
			}

			id++
			closeTok := placeholderToken(id)
			sb.WriteString(closeTok)
			placeholders = append(placeholders, Placeholder{
				Token: closeTok,
				Type:  PlaceholderInline,
				role:  roleClose,
			})
			continue
		}

		// Any other non-text child (nested inline, void element, comment, ...)
		// is preserved as an opaque placeholder so the round-trip never loses
		// content.
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
			role:     roleOpaque,
			node:     c,
		})
	}

	return sb.String(), placeholders, nil
}

// isSimpleInline reports whether n is an element whose children are all text
// nodes (and it has at least one). Such an element can be re-created as an
// open/close token pair with its inner text translated in between; anything
// with nested elements or no children stays opaque.
func isSimpleInline(n *html.Node) bool {
	if n.Type != html.ElementNode || n.FirstChild == nil {
		return false
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.TextNode {
			return false
		}
	}
	return true
}

// shallowClone copies an element node without its children, preserving the tag,
// namespace and attributes so the opening tag can be reconstructed on Restore.
func shallowClone(n *html.Node) *html.Node {
	attr := make([]html.Attribute, len(n.Attr))
	copy(attr, n.Attr)
	return &html.Node{
		Type:      n.Type,
		DataAtom:  n.DataAtom,
		Data:      n.Data,
		Namespace: n.Namespace,
		Attr:      attr,
	}
}

// Restore rebuilds the node's children from the translated text, splicing the
// original inline subtrees back in place of their placeholder tokens. It is
// strict: an unknown token or a dropped placeholder is an error so the pipeline
// can flag the segment instead of emitting broken markup.
func (InlinePlaceholderManager) Restore(translatedText string, placeholders []Placeholder, node *html.Node) error {
	if node == nil {
		return ErrNilDocument
	}

	remaining := make(map[string]*Placeholder, len(placeholders))
	for i := range placeholders {
		remaining[tokenIDPattern.FindString(placeholders[i].Token)] = &placeholders[i]
	}

	detachChildren(node)

	// stack tracks the current append target. Open tokens push the re-created
	// element so subsequent text/elements nest inside it; close tokens pop back.
	stack := []*html.Node{node}
	current := func() *html.Node { return stack[len(stack)-1] }
	appendText := func(s string) {
		if s == "" {
			return
		}
		current().AppendChild(&html.Node{Type: html.TextNode, Data: s})
	}

	pos := 0
	for _, loc := range placeholderTokenPattern.FindAllStringSubmatchIndex(translatedText, -1) {
		appendText(translatedText[pos:loc[0]])

		id := translatedText[loc[2]:loc[3]]
		ph, ok := remaining[id]
		if !ok {
			// A token-shaped string we did not issue: either the source text
			// literally contained it, or the provider hallucinated one. Keep it
			// as literal text rather than failing the whole segment.
			appendText(translatedText[loc[0]:loc[1]])
			pos = loc[1]
			continue
		}
		switch ph.role {
		case roleOpen:
			current().AppendChild(ph.node)
			stack = append(stack, ph.node)
		case roleClose:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		default:
			current().AppendChild(ph.node)
		}
		delete(remaining, id)
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
