package literary

import (
	"fmt"
	"strings"

	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
)

const systemInstruction = `Tu es un traducteur littéraire professionnel de l'anglais vers le français.

REGISTRE (priorité) :
- Écris un français CONTEMPORAIN, courant et fluide — celui d'un roman populaire moderne. Jamais soutenu ni archaïsant.
- INTERDIT : le subjonctif imparfait (fût, dût, s'agît, pût, eût...) et le registre soutenu/archaïsant. Écris « bien qu'il s'agisse » (pas « s'agît »), « il ne devait jamais » (pas « il ne dût »). Le passé simple reste le temps normal du récit : garde-le.
- Pas de tournures administratives ou juridiques (« ledit », « dudit », « sera omis ») : préfère une formule naturelle.

NATUREL :
- Ne calque pas la syntaxe anglaise. Découpe les phrases longues et rétablis un ordre naturel en français.
- Traduis les idiomes par leur équivalent français, jamais mot à mot. Ex. : « bodily movements » -> « ses mouvements » (pas « mouvements corporels »).
- Pour une remarque méta du narrateur (« further explanation is omitted... »), allège : « mais n'entrons pas dans les détails, cela nous éloignerait du sujet. »
- Garde un temps de récit cohérent ; n'introduis pas un futur isolé au milieu d'une narration au passé.

DIALOGUE : oral, vivant, à hauteur du personnage. Préserve les allongements expressifs et onomatopées (reaaally -> vraaaiment). Ponctuation française : guillemets « ... » et tirets cadratins.

CONTRAINTES TECHNIQUES (absolues) :
1. Traduis TOUT en français. Seules exceptions laissées telles quelles : les balises <x id="N"/> et <g id="N"/> et les noms propres.
2. Préserve exactement chaque balise <x id="N"/> et <g id="N"/> à sa place d'origine dans la phrase.
3. Renvoie UNIQUEMENT un objet JSON strict, sans aucun texte avant ou après.

FORMAT ATTENDU :
{"segments":[{"id":"ID_DU_SEGMENT","target_text":"texte traduit en français naturel"}]}`

func composeMessages(bible narrative.Bible, terms []glossary.Term, chapterSource, chapterPayload, prevCtx string) []llm.ChatMessage {
	return []llm.ChatMessage{
		{Role: "system", Content: stablePrefix(bible, terms, chapterSource)},
		{Role: "user", Content: volatilePrompt(chapterPayload, prevCtx)},
	}
}

// stablePrefix builds the cacheable system block: the frozen specification
// (style guide, character voices, address rules, glossary) plus the full source
// of the chapter being corrected.
//
// The chapter source is here rather than in the user turn on purpose. It is
// constant across every batch of a chapter, so the prompt cache absorbs it
// after the first call; putting it in the volatile turn would re-send it on
// each batch at full input price. It replaces the two 1200-rune windows that
// used to bracket each batch: the model now sees the whole chapter it is
// correcting, which is what answers a pronoun referent or a callback.
//
// It does not carry style coherence. That comes from the frozen specification
// above, which is byte-identical in every call of every chapter, and from the
// glossary, enforced mechanically by Protect/Apply rather than entrusted to the
// model.
func stablePrefix(bible narrative.Bible, terms []glossary.Term, chapterSource string) string {
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
			sb.WriteString(fmt.Sprintf("- %s: %s\n", summary.Label(), summary.Summary))
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

	sb.WriteString("\nSOURCE_INTEGRALE_DU_CHAPITRE:\n")
	sb.WriteString(emptyAsNone(chapterSource))

	return sb.String()
}

// volatilePrompt is the per-batch turn. It carries only what changes between
// batches: the segments to correct, and the French already produced for the
// batch before them so register and pronouns carry across the seam. What
// follows the batch is no longer sent — the chapter source in the prefix covers
// it, and cached.
func volatilePrompt(chapterPayload, prevCtx string) string {
	return fmt.Sprintf(`CONTEXTE_PRECEDENT:
%s

SEGMENTS_DU_CHAPITRE_JSON:
%s`, emptyAsNone(prevCtx), chapterPayload)
}

func emptyAsNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "aucun"
	}
	return s
}
