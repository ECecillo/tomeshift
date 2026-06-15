package epub

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

var translatableTags = map[string]bool{
	"p": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true,
}

var skipTags = map[string]bool{
	"head": true, "script": true, "style": true,
	"code": true, "pre": true,
}

func parseXHTMLDocument(raw []byte) (*XHTMLDocument, error) {
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("unable to parse xhtml document, err: %w", err)
	}

	var nodes []*ContentNode
	walkXHTMLNode(doc, "", &nodes)

	return &XHTMLDocument{Raw: raw, Root: doc, Nodes: nodes}, nil
}

// walkXHTMLNode traverses the HTML tree depth-first and collects translatable nodes.
// Each translatable element (p, h1-h6) is added with a stable 1-based XPath-like path.
// Non-translatable containers are recursed into; skip tags are ignored entirely.
func walkXHTMLNode(n *html.Node, parentPath string, nodes *[]*ContentNode) {
	tagCount := map[string]int{}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			tagCount[c.Data]++
		}
	}

	tagSeen := map[string]int{}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}

		if skipTags[c.Data] {
			continue
		}

		tagSeen[c.Data]++
		nodePath := parentPath + "/" + c.Data + "[" + strconv.Itoa(tagSeen[c.Data]) + "]"

		if translatableTags[c.Data] {
			text := strings.TrimSpace(innerText(c))
			if text != "" {
				*nodes = append(*nodes, &ContentNode{
					NodePath: nodePath,
					Text:     text,
					Node:     c,
				})
			}
		} else {
			walkXHTMLNode(c, nodePath, nodes)
		}
	}
}

func innerText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}
