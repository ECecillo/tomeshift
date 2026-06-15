package translate

import "context"

// Request is a single translation request for one segment's text.
type Request struct {
	Text       string
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
	// Engine identifies the provider for TM accounting and reporting.
	Engine() string
}
