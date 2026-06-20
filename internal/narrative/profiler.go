package narrative

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ECecillo/ebooks-translator/internal/llm"
)

// defaultProfilerMaxTokens must fit a full series bible (style guide + many
// characters + address rules + summaries) from a large sample set; 2000 was too
// small and truncated the JSON.
const defaultProfilerMaxTokens = 16000

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
	if err := validateBible(bible); err != nil {
		return Bible{}, err
	}
	return bible, nil
}

// Update asks the chat model to return the full updated bible after a chapter
// has source and target prose available.
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
	if updated.SeriesID == "" {
		updated.SeriesID = bible.SeriesID
	}
	if err := validateBible(updated); err != nil {
		return Bible{}, err
	}
	return updated, nil
}

func (p Profiler) maxTokens() int {
	if p.MaxTokens > 0 {
		return p.MaxTokens
	}
	return defaultProfilerMaxTokens
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

func validateBible(bible Bible) error {
	if bible.StyleGuide == "" && len(bible.Characters) == 0 && len(bible.Address) == 0 && len(bible.Summaries) == 0 {
		return ErrInvalidBible
	}
	return nil
}

func nonEmpty(samples []string) []string {
	out := samples[:0]
	for _, sample := range samples {
		if strings.TrimSpace(sample) != "" {
			out = append(out, sample)
		}
	}
	return out
}

const seedSystemPrompt = `You build a French literary translation series bible.
Return strict JSON only, matching this shape:
{"series_id":"","style_guide":"","characters":[{"name":"","register":"","notes":"","sample_fr":[]}],"address":[{"from":"","to":"","form":"tu","history":[{"at_chapter":"","note":""}]}],"summaries":[{"chapter_id":"","summary":""}],"version":0}
Do not include markdown or commentary.`

func seedUserPrompt(samples []string) string {
	var sb strings.Builder
	sb.WriteString("Infer a concise style guide, recurring character voices, and address rules from these samples.\n")
	for i, sample := range samples {
		if strings.TrimSpace(sample) == "" {
			continue
		}
		sb.WriteString("\nSAMPLE ")
		sb.WriteString(fmt.Sprint(i + 1))
		sb.WriteString(":\n")
		sb.WriteString(sample)
		sb.WriteByte('\n')
	}
	return sb.String()
}

const updateSystemPrompt = `You update an existing French literary translation series bible.
Return the complete updated bible as strict JSON only, matching this shape:
{"series_id":"","style_guide":"","characters":[{"name":"","register":"","notes":"","sample_fr":[]}],"address":[{"from":"","to":"","form":"tu","history":[{"at_chapter":"","note":""}]}],"summaries":[{"chapter_id":"","summary":""}],"version":0}
Preserve existing facts unless the chapter clearly changes them. Do not include markdown or commentary.`

func updateUserPrompt(bibleJSON, chapterID, sourceText, targetText string) string {
	return fmt.Sprintf(`CURRENT_BIBLE_JSON:
%s

CHAPTER_ID:
%s

SOURCE_TEXT:
%s

TARGET_TEXT:
%s

Append one summary for the chapter and record any new voice notes or tu/vous address changes.`, bibleJSON, chapterID, sourceText, targetText)
}
