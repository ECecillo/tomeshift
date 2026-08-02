package translate

import "context"

// Request is a single translation request for one segment's text.
type Request struct {
	Text       string
	SourceLang string
	TargetLang string
}

// BatchRequest translates several texts sharing one language pair in as few
// round-trips as the provider allows. The pipeline builds one per chapter, from
// that chapter's TM misses.
type BatchRequest struct {
	Texts      []string
	SourceLang string
	TargetLang string
}

// Response is the provider's translation result.
type Response struct {
	Text   string
	Engine string
}

// Translator is the provider contract used by the pipeline's translation stage.
type Translator interface {
	Translate(ctx context.Context, req Request) (Response, error)
	// TranslateBatch translates every text in req and returns results aligned
	// with req.Texts by index. Providers without a batch endpoint implement it
	// as a loop over Translate; implementations must never return a slice of a
	// different length than req.Texts.
	TranslateBatch(ctx context.Context, req BatchRequest) ([]Response, error)
	// Engine identifies the provider for TM accounting and reporting.
	Engine() string
}

// translateEach is the batch fallback for providers whose wire contract carries
// a single text per call.
func translateEach(ctx context.Context, t Translator, req BatchRequest) ([]Response, error) {
	out := make([]Response, 0, len(req.Texts))
	for _, text := range req.Texts {
		resp, err := t.Translate(ctx, Request{
			Text:       text,
			SourceLang: req.SourceLang,
			TargetLang: req.TargetLang,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, resp)
	}
	return out, nil
}
