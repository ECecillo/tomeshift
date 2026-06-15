// Package qa implements the V1 quality gate: placeholder integrity, spine
// structural integrity, untranslated-coverage checks, and an epubcheck wrapper.
//
// The static checks operate on the in-memory segments and book; the epubcheck
// runner shells out to the external validator when it is available and reports
// a skip otherwise.
package qa
