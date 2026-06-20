package literary_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/literary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/segment"
)

type stubChat struct {
	reply string
	req   llm.ChatRequest
}

func (s *stubChat) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	s.req = req
	return llm.ChatResponse{Text: s.reply}, nil
}

func TestTranslator_ComposesPromptWithBibleAndGlossary(t *testing.T) {
	t.Parallel()

	gloss, err := glossary.New([]glossary.Term{{EN: "Bilbo", FR: "Bilbon", Locked: true}})
	require.NoError(t, err)

	chat := &stubChat{reply: `{"segments":[{"id":"s1","target_text":"Bonjour."}]}`}
	tr := literary.Translator{Chat: chat, Model: "model-a", Glossary: gloss}

	_, err = tr.TranslateChapter(t.Context(), bibleFixture(), "ch_0001", []segment.Segment{{
		ID:         "s1",
		SourceText: "Bilbo arrived.",
		TargetText: "Bilbon arriva.",
	}}, "previous", "next")
	require.NoError(t, err)

	require.Len(t, chat.req.Messages, 2)
	stable := chat.req.Messages[0].Content
	volatile := chat.req.Messages[1].Content
	require.Contains(t, stable, "formal, precise literary French")
	require.Contains(t, stable, "Alice")
	require.Contains(t, stable, "Alice -> Bruno: vous")
	require.Contains(t, stable, "Bilbo => Bilbon")
	require.Contains(t, volatile, "CHAPTER_SEGMENTS_JSON")
	require.Contains(t, volatile, "previous")
	require.Contains(t, volatile, "next")

	combined := stable + volatile
	require.Less(t,
		strings.Index(combined, "STYLE_GUIDE"),
		strings.Index(combined, "CHAPTER_SEGMENTS_JSON"),
		"stable prefix must precede volatile chapter content")
	require.Equal(t, "model-a", chat.req.Model)
}

func TestTranslator_RoundTripPreservesPlaceholders(t *testing.T) {
	t.Parallel()

	seg := tokenizedSegment(t, "s1", `<p>A <em>bold</em> claim.</p>`)
	seg.TargetText = "Une affirmation <x id=\"1\"/>."

	chat := &stubChat{reply: `{"segments":[{"id":"s1","target_text":"Une affirmation <x id=\"1\"/>."}]}`}
	tr := literary.Translator{Chat: chat}

	out, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", []segment.Segment{seg}, "", "")
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, segment.StatusPostEdited, out[0].Status)

	require.NoError(t, segment.InlinePlaceholderManager{}.Restore(out[0].TargetText, out[0].Placeholders, out[0].Node))
	rendered := renderNode(t, out[0].Node)
	require.Contains(t, rendered, "<em>bold</em>")
	require.Contains(t, rendered, "Une affirmation")
}

func TestTranslator_FallsBackWhenPlaceholderDropped(t *testing.T) {
	t.Parallel()

	seg := segment.Segment{
		ID:         "s1",
		SourceText: `A <x id="1"/> claim.`,
		TargetText: `Brouillon <x id="1"/> sûr.`,
	}
	chat := &stubChat{reply: `{"segments":[{"id":"s1","target_text":"Texte sans jeton."}]}`}
	tr := literary.Translator{Chat: chat}

	out, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", []segment.Segment{seg}, "", "")
	require.NoError(t, err)
	require.Equal(t, `Brouillon <x id="1"/> sûr.`, out[0].TargetText)
	require.Equal(t, segment.StatusTranslated, out[0].Status)
}

func TestTranslator_GlossaryLockedTermEnforced(t *testing.T) {
	t.Parallel()

	gloss, err := glossary.New([]glossary.Term{{EN: "Bilbo", FR: "Bilbon", Locked: true}})
	require.NoError(t, err)

	chat := &stubChat{reply: `{"segments":[{"id":"s1","target_text":"Bonjour <g id=\"1\"/>."}]}`}
	tr := literary.Translator{Chat: chat, Glossary: gloss}

	out, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", []segment.Segment{{
		ID:         "s1",
		SourceText: "Bilbo waved.",
		TargetText: "Bilbon salua.",
	}}, "", "")
	require.NoError(t, err)
	require.Equal(t, "Bonjour Bilbon.", out[0].TargetText)
	require.Contains(t, chat.req.Messages[1].Content, `<g id=\"1\"/>`)
}

func TestTranslator_NilChat(t *testing.T) {
	t.Parallel()

	tr := literary.Translator{}
	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{}, "ch_0001", []segment.Segment{{ID: "s1"}}, "", "")
	require.ErrorIs(t, err, literary.ErrNilChatClient)
}

func bibleFixture() narrative.Bible {
	return narrative.Bible{
		StyleGuide: "Use formal, precise literary French.",
		Characters: []narrative.CharacterVoice{{
			Name:     "Alice",
			Register: "measured",
			Notes:    "Avoid slang.",
		}},
		Address: []narrative.AddressRule{{
			From: "Alice",
			To:   "Bruno",
			Form: "vous",
			History: []narrative.AddressChange{{
				AtChapter: "ch_0001",
				Note:      "Formal distance.",
			}},
		}},
	}
}

func tokenizedSegment(t *testing.T, id, raw string) segment.Segment {
	t.Helper()

	root, err := html.Parse(strings.NewReader(raw))
	require.NoError(t, err)

	var p *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "p" {
			p = n
			return
		}
		for c := n.FirstChild; c != nil && p == nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	require.NotNil(t, p)

	text, placeholders, err := segment.InlinePlaceholderManager{}.Tokenize(p)
	require.NoError(t, err)
	return segment.Segment{
		ID:           id,
		ChapterID:    "ch_0001",
		SourceText:   text,
		Placeholders: placeholders,
		Node:         p,
	}
}

func renderNode(t *testing.T, n *html.Node) string {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, html.Render(&buf, n))
	return buf.String()
}
