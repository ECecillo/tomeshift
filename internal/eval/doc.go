// Package eval provides reference-based translation quality metrics.
//
// When a human reference translation is available (e.g. a published French
// edition), chrF compares the pipeline's output against it. chrF is a
// character n-gram F-score: robust for literary text and morphologically rich
// languages, and computable in pure Go without a model. Use it for *relative*
// comparison of strategies against the same reference — absolute values are
// modest against free literary translations, where neural metrics (COMET) are
// better and can be added later as an HTTP service.
package eval
