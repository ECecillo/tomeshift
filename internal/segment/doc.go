// Package segment provides paragraph-level segmentation, text normalization,
// content hashing, and inline-placeholder handling for translation.
//
// It turns XHTML content into stable translatable units while preserving inline
// markup so translated content can be safely restored into the source document.
// It also owns the serialisable projection of a segment (Unit, schema §16.1)
// that crosses process boundaries, and the re-application of translated targets
// back into a live document tree (ApplyTranslations), which re-segments the
// source deterministically to recover the placeholder subtrees Unit cannot
// carry.
package segment
