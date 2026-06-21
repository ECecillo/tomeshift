package literary

import (
	"fmt"
	"strings"

	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
)

const systemInstruction = `You are translating English literary prose into French.
Return strict JSON only with this shape:
{"segments":[{"id":"","target_text":""}]}
Preserve every <x id="N"/> placeholder exactly. Preserve every <g id="N"/> glossary token exactly.`

func composeMessages(bible narrative.Bible, terms []glossary.Term, chapterPayload, prevCtx, nextCtx string) []llm.ChatMessage {
	return []llm.ChatMessage{
		{Role: "system", Content: stablePrefix(bible, terms)},
		{Role: "user", Content: volatilePrompt(chapterPayload, prevCtx, nextCtx)},
	}
}

func stablePrefix(bible narrative.Bible, terms []glossary.Term) string {
	var sb strings.Builder
	sb.WriteString(systemInstruction)
	sb.WriteString("\n\nSTYLE_GUIDE:\n")
	sb.WriteString(emptyAsNone(bible.StyleGuide))

	sb.WriteString("\n\nCHARACTER_VOICES:\n")
	if len(bible.Characters) == 0 {
		sb.WriteString("none")
	} else {
		for _, character := range bible.Characters {
			sb.WriteString(fmt.Sprintf("- %s | register: %s | notes: %s", character.Name, character.Register, character.Notes))
			if len(character.SampleFR) > 0 {
				sb.WriteString(" | samples: ")
				sb.WriteString(strings.Join(character.SampleFR, " / "))
			}
			sb.WriteByte('\n')
		}
	}

	sb.WriteString("\nADDRESS_RULES:\n")
	if len(bible.Address) == 0 {
		sb.WriteString("none")
	} else {
		for _, rule := range bible.Address {
			sb.WriteString(fmt.Sprintf("- %s -> %s: %s", rule.From, rule.To, rule.Form))
			if len(rule.History) > 0 {
				last := rule.History[len(rule.History)-1]
				sb.WriteString(fmt.Sprintf(" (latest %s: %s)", last.AtChapter, last.Note))
			}
			sb.WriteByte('\n')
		}
	}

	sb.WriteString("\nSTORY_SO_FAR:\n")
	if len(bible.Summaries) == 0 {
		sb.WriteString("none")
	} else {
		for _, summary := range bible.Summaries {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", summary.ChapterID, summary.Summary))
		}
	}

	sb.WriteString("\nGLOSSARY:\n")
	if len(terms) == 0 {
		sb.WriteString("none")
	} else {
		for _, term := range terms {
			sb.WriteString(fmt.Sprintf("- %s => %s\n", term.EN, term.FR))
		}
	}

	return sb.String()
}

func volatilePrompt(chapterPayload, prevCtx, nextCtx string) string {
	return fmt.Sprintf(`PREVIOUS_CONTEXT:
%s

NEXT_CONTEXT:
%s

CHAPTER_SEGMENTS_JSON:
%s`, emptyAsNone(prevCtx), emptyAsNone(nextCtx), chapterPayload)
}

func emptyAsNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}
