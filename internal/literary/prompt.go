package literary

import (
	"fmt"
	"strings"

	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
)

const systemInstruction = `Tu es un traducteur littéraire expert de l'anglais vers le français.
RÈGLES ABSOLUES :
1. TRADUIS TOUT LE TEXTE EN FRANÇAIS. Ne laisse aucune phrase ni aucun mot courant en anglais, à la seule exception des balises <x id="N"/> et <g id="N"/> et des noms propres, qui restent inchangés.
2. Adopte un style littéraire fluide, naturel et idiomatique en français. Ne traduis pas mot à mot.
3. Renvoie UNIQUEMENT un objet JSON strict, sans aucun texte avant ou après.
4. Préserve exactement chaque balise <x id="N"/> et <g id="N"/> à sa place d'origine dans la phrase.

FORMAT ATTENDU :
{"segments":[{"id":"ID_DU_SEGMENT","target_text":"texte traduit en français parfait"}]}`

func composeMessages(bible narrative.Bible, terms []glossary.Term, chapterPayload, prevCtx, nextCtx string) []llm.ChatMessage {
	return []llm.ChatMessage{
		{Role: "system", Content: stablePrefix(bible, terms)},
		{Role: "user", Content: volatilePrompt(chapterPayload, prevCtx, nextCtx)},
	}
}

func stablePrefix(bible narrative.Bible, terms []glossary.Term) string {
	var sb strings.Builder
	sb.WriteString(systemInstruction)
	sb.WriteString("\n\nGUIDE_DE_STYLE:\n")
	sb.WriteString(emptyAsNone(bible.StyleGuide))

	sb.WriteString("\n\nVOIX_DES_PERSONNAGES:\n")
	if len(bible.Characters) == 0 {
		sb.WriteString("aucun")
	} else {
		for _, character := range bible.Characters {
			sb.WriteString(fmt.Sprintf("- %s | registre: %s | notes: %s", character.Name, character.Register, character.Notes))
			if len(character.SampleFR) > 0 {
				sb.WriteString(" | exemples: ")
				sb.WriteString(strings.Join(character.SampleFR, " / "))
			}
			sb.WriteByte('\n')
		}
	}

	sb.WriteString("\nREGLES_DE_VOUVOIEMENT_TUTOIEMENT:\n")
	if len(bible.Address) == 0 {
		sb.WriteString("aucun")
	} else {
		for _, rule := range bible.Address {
			sb.WriteString(fmt.Sprintf("- %s -> %s: %s", rule.From, rule.To, rule.Form))
			if len(rule.History) > 0 {
				last := rule.History[len(rule.History)-1]
				sb.WriteString(fmt.Sprintf(" (dernier %s: %s)", last.AtChapter, last.Note))
			}
			sb.WriteByte('\n')
		}
	}

	sb.WriteString("\nRESUME_DE_L_HISTOIRE:\n")
	if len(bible.Summaries) == 0 {
		sb.WriteString("aucun")
	} else {
		for _, summary := range bible.Summaries {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", summary.ChapterID, summary.Summary))
		}
	}

	sb.WriteString("\nGLOSSAIRE:\n")
	if len(terms) == 0 {
		sb.WriteString("aucun")
	} else {
		for _, term := range terms {
			sb.WriteString(fmt.Sprintf("- %s => %s\n", term.EN, term.FR))
		}
	}

	return sb.String()
}

func volatilePrompt(chapterPayload, prevCtx, nextCtx string) string {
	return fmt.Sprintf(`CONTEXTE_PRECEDENT:
%s

CONTEXTE_SUIVANT:
%s

SEGMENTS_DU_CHAPITRE_JSON:
%s`, emptyAsNone(prevCtx), emptyAsNone(nextCtx), chapterPayload)
}

func emptyAsNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "aucun"
	}
	return s
}
