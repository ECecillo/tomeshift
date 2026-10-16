package narrative

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ECecillo/tomeshift/internal/llm"
)

// defaultProfilerMaxTokens must fit a full series bible (style guide + many
// characters + address rules + summaries) from a large sample set; 2000 was too
// small and truncated the JSON.
const defaultProfilerMaxTokens = 16000

// defaultSummaryMaxTokens bounds one chapter summary or one volume synopsis:
// prose, not a bible. ~274 tokens is the measured chapter summary size, ~400
// the synopsis; 800 leaves room without letting either run away.
const defaultSummaryMaxTokens = 800

// Profiler seeds and updates a series bible from book/chapter prose through a
// provider-agnostic chat client.
type Profiler struct {
	Chat      llm.ChatClient
	Model     string
	MaxTokens int
}

// Seed asks the chat model for an initial series bible from representative
// samples. The response must be strict JSON matching Bible.
func (p Profiler) Seed(ctx context.Context, samples []string) (Bible, error) {
	if p.Chat == nil {
		return Bible{}, ErrNilChatClient
	}
	if len(nonEmpty(samples)) == 0 {
		return Bible{}, ErrEmptySamples
	}

	resp, err := p.Chat.Chat(ctx, llm.ChatRequest{
		Model:     p.Model,
		MaxTokens: p.maxTokens(),
		Messages: []llm.ChatMessage{
			{Role: "system", Content: seedSystemPrompt},
			{Role: "user", Content: seedUserPrompt(samples)},
		},
	})
	if err != nil {
		return Bible{}, fmt.Errorf("unable to seed narrative bible, err: %w", err)
	}

	bible, err := decodeBible(resp.Text)
	if err != nil {
		return Bible{}, fmt.Errorf("unable to parse seeded narrative bible, err: %w", err)
	}
	if err := ValidateStyleBible(bible); err != nil {
		return Bible{}, err
	}
	return bible, nil
}

// Update asks the chat model to refine style, voices and address rules after a
// chapter has source and target prose available. Pre-generated volume synopses
// and the caller's storage scope are preserved regardless of model output.
func (p Profiler) Update(ctx context.Context, bible Bible, chapterID, sourceText, targetText string) (Bible, error) {
	if p.Chat == nil {
		return Bible{}, ErrNilChatClient
	}

	bibleJSON, err := json.Marshal(bible)
	if err != nil {
		return Bible{}, fmt.Errorf("unable to encode narrative bible for update, err: %w", err)
	}

	resp, err := p.Chat.Chat(ctx, llm.ChatRequest{
		Model:     p.Model,
		MaxTokens: p.maxTokens(),
		Messages: []llm.ChatMessage{
			{Role: "system", Content: updateSystemPrompt},
			{Role: "user", Content: updateUserPrompt(string(bibleJSON), chapterID, sourceText, targetText)},
		},
	})
	if err != nil {
		return Bible{}, fmt.Errorf("unable to update narrative bible, err: %w", err)
	}

	updated, err := decodeBible(resp.Text)
	if err != nil {
		return Bible{}, fmt.Errorf("unable to parse updated narrative bible, err: %w", err)
	}
	// RFC 0008 makes summaries pre-generated volume synopses. An opted-in
	// in-run update may refine style, voices and address rules, but it must not
	// replace those synopses with per-chapter output or change storage scope.
	updated.SeriesID = bible.SeriesID
	updated.Summaries = append([]Summary(nil), bible.Summaries...)
	if err := ValidateBible(updated); err != nil {
		return Bible{}, err
	}
	return updated, nil
}

// SummariseChapter is the map step of volume synopsis generation: one chapter's
// source in, one short French summary out, at roughly 4:1.
//
// It works from the source alone. Building the bible before any translation
// means the target does not exist yet, and a plot summary is narrative content
// rather than translation commentary — so uniformity across volumes (already
// translated or not) is worth more than what an existing translation would add.
// Nothing here round-trips a Bible; the caller assembles the result.
func (p Profiler) SummariseChapter(ctx context.Context, chapterID, sourceText string) (string, error) {
	if p.Chat == nil {
		return "", ErrNilChatClient
	}
	if strings.TrimSpace(sourceText) == "" {
		return "", ErrEmptySource
	}

	resp, err := p.Chat.Chat(ctx, llm.ChatRequest{
		Model:     p.Model,
		MaxTokens: p.summaryMaxTokens(),
		Messages: []llm.ChatMessage{
			{Role: "system", Content: chapterSummarySystemPrompt},
			{Role: "user", Content: chapterSummaryUserPrompt(chapterID, sourceText)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("unable to summarise chapter %q, err: %w", chapterID, err)
	}

	summary := strings.TrimSpace(resp.Text)
	if summary == "" {
		return "", fmt.Errorf("unable to summarise chapter %q, err: %w", chapterID, ErrEmptySummary)
	}
	return summary, nil
}

// SynthesiseVolume is the reduce step: every chapter summary of one volume in,
// one volume synopsis out, at roughly 47:1.
//
// Reducing over summaries rather than summarising the raw volume in one pass is
// the whole point. A single 298:1 pass reads well at the opening and the ending
// and thins out in the middle, silently; here every chapter has demonstrably
// been read before the reduction starts.
func (p Profiler) SynthesiseVolume(ctx context.Context, volumeID string, chapterSummaries []string) (string, error) {
	if p.Chat == nil {
		return "", ErrNilChatClient
	}
	summaries := nonEmpty(chapterSummaries)
	if len(summaries) == 0 {
		return "", ErrEmptySamples
	}

	resp, err := p.Chat.Chat(ctx, llm.ChatRequest{
		Model:     p.Model,
		MaxTokens: p.summaryMaxTokens(),
		Messages: []llm.ChatMessage{
			{Role: "system", Content: volumeSynopsisSystemPrompt},
			{Role: "user", Content: volumeSynopsisUserPrompt(volumeID, summaries)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("unable to synthesise volume %q, err: %w", volumeID, err)
	}

	synopsis := strings.TrimSpace(resp.Text)
	if synopsis == "" {
		return "", fmt.Errorf("unable to synthesise volume %q, err: %w", volumeID, ErrEmptySummary)
	}
	return synopsis, nil
}

func (p Profiler) maxTokens() int {
	if p.MaxTokens > 0 {
		return p.MaxTokens
	}
	return defaultProfilerMaxTokens
}

// summaryMaxTokens bounds the two synopsis operations. The 16k ceiling above
// exists to fit a whole bible JSON; applied here it would let a chapter summary
// run long enough to defeat the 4:1 compression the map step exists for.
func (p Profiler) summaryMaxTokens() int {
	if p.MaxTokens > 0 && p.MaxTokens < defaultSummaryMaxTokens {
		return p.MaxTokens
	}
	return defaultSummaryMaxTokens
}

func decodeBible(raw string) (Bible, error) {
	first := strings.Index(raw, "{")
	last := strings.LastIndex(raw, "}")
	if first != -1 && last != -1 && last >= first {
		raw = raw[first : last+1]
	}

	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()

	var bible Bible
	if err := dec.Decode(&bible); err != nil {
		return Bible{}, fmt.Errorf("unable to decode bible JSON, err: %w", err)
	}
	if dec.More() {
		return Bible{}, fmt.Errorf("unable to decode bible JSON, err: %w", ErrInvalidBible)
	}
	return bible, nil
}

// ValidateStyleBible checks the output owned by the style pass. A synopsis
// returned in the same JSON cannot stand in for missing style information.
func ValidateStyleBible(bible Bible) error {
	if strings.TrimSpace(bible.StyleGuide) == "" {
		return ErrInvalidBible
	}
	for _, character := range bible.Characters {
		if strings.TrimSpace(character.Name) == "" || strings.TrimSpace(character.Register) == "" {
			return ErrInvalidBible
		}
	}
	for _, rule := range bible.Address {
		if strings.TrimSpace(rule.From) == "" || strings.TrimSpace(rule.To) == "" ||
			(rule.Form != "tu" && rule.Form != "vous") {
			return ErrInvalidBible
		}
	}
	return nil
}

// ValidateBible checks the frozen RFC-0008 input consumed by literary mode.
func ValidateBible(bible Bible) error {
	if bible.SeriesID == "" || len(bible.Summaries) == 0 {
		return ErrInvalidBible
	}
	if err := ValidateStyleBible(bible); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(bible.Summaries))
	for _, summary := range bible.Summaries {
		if summary.VolumeID == "" || summary.ChapterID != "" || strings.TrimSpace(summary.Summary) == "" {
			return ErrInvalidBible
		}
		if _, duplicate := seen[summary.VolumeID]; duplicate {
			return ErrInvalidBible
		}
		seen[summary.VolumeID] = struct{}{}
	}
	return nil
}

// nonEmpty must not filter in place. Writing into samples[:0] compacted the
// caller's backing array while leaving its length untouched, so a single blank
// sample made the surviving ones reappear further down the slice — and every
// caller here reuses its slice after the check.
func nonEmpty(samples []string) []string {
	out := make([]string, 0, len(samples))
	for _, sample := range samples {
		if strings.TrimSpace(sample) != "" {
			out = append(out, sample)
		}
	}
	return out
}

const seedSystemPrompt = `You build a French literary translation series bible.
Return strict JSON only, matching this shape:
{"series_id":"","style_guide":"","characters":[{"name":"","register":"","notes":"","sample_fr":[]}],"address":[{"from":"","to":"","form":"tu","history":[{"at_chapter":"","note":""}]}],"summaries":[],"version":0}
This pass owns style, voices, and address rules only. Keep summaries empty.
Do not include markdown or commentary.`

func seedUserPrompt(samples []string) string {
	var sb strings.Builder
	sb.WriteString("Infer a concise style guide, recurring character voices, and address rules from these samples.\n")
	for i, sample := range samples {
		if strings.TrimSpace(sample) == "" {
			continue
		}
		sb.WriteString("\nSAMPLE ")
		fmt.Fprint(&sb, i+1)
		sb.WriteString(":\n")
		sb.WriteString(sample)
		sb.WriteByte('\n')
	}
	return sb.String()
}

const updateSystemPrompt = `You update an existing French literary translation series bible.
Return the complete updated bible as strict JSON only, matching this shape:
{"series_id":"","style_guide":"","characters":[{"name":"","register":"","notes":"","sample_fr":[]}],"address":[{"from":"","to":"","form":"tu","history":[{"at_chapter":"","note":""}]}],"summaries":[{"volume_id":"","summary":""}],"version":0}
Preserve every volume synopsis exactly. Update only style, character voice notes, and tu/vous address rules when the chapter clearly changes them. Do not add chapter summaries. Do not include markdown or commentary.`

func updateUserPrompt(bibleJSON, chapterID, sourceText, targetText string) string {
	return fmt.Sprintf(`CURRENT_BIBLE_JSON:
%s

CHAPTER_ID:
%s

SOURCE_TEXT:
%s

TARGET_TEXT:
%s

Record any new voice notes or tu/vous address changes. Keep the existing volume synopses unchanged.`, bibleJSON, chapterID, sourceText, targetText)
}

// The two synopsis prompts return prose, not JSON: nothing downstream parses
// them, and asking for JSON would only spend tokens on escaping. Output is
// French because that is what lands in the bible's RESUME_DE_L_HISTOIRE block.
const chapterSummarySystemPrompt = `Tu résumes un chapitre de roman pour la mémoire narrative d'un traducteur.
Écris en français, 3 à 5 phrases, au présent de narration.
Retiens : les personnages présents, ce qu'ils font, les révélations et les changements de situation.
Écarte : le style, les détails décoratifs, tout commentaire sur la traduction.
Ne renvoie que le résumé, sans titre, sans puces, sans préambule.`

func chapterSummaryUserPrompt(chapterID, sourceText string) string {
	return fmt.Sprintf(`CHAPITRE:
%s

TEXTE_SOURCE:
%s`, chapterID, sourceText)
}

const volumeSynopsisSystemPrompt = `Tu écris le synopsis d'un tome de roman à partir des résumés de tous ses chapitres, dans l'ordre.
Écris en français, 8 à 12 phrases, au présent de narration.
Couvre l'arc entier : la situation initiale, les développements du milieu du tome, et le dénouement.
Nomme les personnages récurrents et les enjeux qui se poursuivront dans les tomes suivants.
Ne renvoie que le synopsis, sans titre, sans puces, sans préambule.`

func volumeSynopsisUserPrompt(volumeID string, summaries []string) string {
	var sb strings.Builder
	sb.WriteString("TOME:\n")
	sb.WriteString(volumeID)
	sb.WriteString("\n\nRESUMES_DES_CHAPITRES (dans l'ordre):\n")
	for i, summary := range summaries {
		fmt.Fprintf(&sb, "\n[%d] %s\n", i+1, summary)
	}
	return sb.String()
}
