// Command nmt-devserver is a self-hosted, offline NMT endpoint for local
// development. It speaks the same JSON wire contract as the production HTTP NMT
// adapter (internal/translate.HTTPTranslator), so the full pipeline can run
// end-to-end with no external dependency.
//
// It is NOT a production translation engine: it performs dictionary-based,
// word-level EN→FR substitution and passes unknown words and placeholder tokens
// through unchanged. In production the same EPUB_TRANSLATOR_NMT_ENDPOINT should
// point at a real engine (e.g. LibreTranslate / OPUS-MT) honoring this contract.
package main
