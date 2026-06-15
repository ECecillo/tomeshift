package llm

import "context"

// PostEditRequest carries an NMT draft plus its source for LLM refinement.
type PostEditRequest struct {
	SourceText string
	NMTText    string
	SourceLang string
	TargetLang string
}

// PostEditResponse is the strict JSON contract returned by the provider.
type PostEditResponse struct {
	TranslatedText string   `json:"translated_text"`
	Notes          string   `json:"notes"`
	ChangedTerms   []string `json:"changed_terms"`
}

// PostEditor refines an NMT draft. Implementations must preserve placeholder
// tokens; the caller treats a placeholder-altering response as a failure and
// falls back to the NMT draft.
type PostEditor interface {
	PostEdit(ctx context.Context, req PostEditRequest) (PostEditResponse, error)
}
