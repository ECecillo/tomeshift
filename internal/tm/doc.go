// Package tm implements the V1 translation memory: an exact-match cache of
// previously translated segments backed by SQLite.
//
// V1 supports exact hash lookups only (fuzzy matching is deferred to a later
// phase). Each stored entry is keyed by the sha256 hash of a segment's
// normalized source text so identical source paragraphs reuse prior output.
package tm
