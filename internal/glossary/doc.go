// Package glossary enforces canonical translations for locked terms (typically
// proper nouns) in the V1 pipeline.
//
// Enforcement is deterministic: before a segment is sent to the translator each
// locked source term is replaced with an opaque token, and after translation
// the token is substituted with the canonical target term. This guarantees
// 100% locked-term compliance regardless of provider behavior.
package glossary
