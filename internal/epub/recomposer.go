package epub

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"

	"golang.org/x/net/html"
)

// golang.org/x/net/html parses an XML declaration as a comment, so html.Render
// emits it as a bogus `<!--?xml ...-->` comment. These patterns let us restore
// the original `<?xml ...?>` declaration, which XHTML readers/epubcheck expect.
var (
	sourceXMLDecl      = regexp.MustCompile(`(?s)^\s*<\?xml[^>]*\?>`)
	renderedXMLComment = regexp.MustCompile(`^<!--\?xml[^>]*-->`)
)

// Recomposer rebuilds a valid EPUB archive from a (possibly translated) book.
type Recomposer interface {
	Recompose(ctx context.Context, raw *RawEPUB, book *Book, outputPath string) error
}

// EPUBRecomposer re-renders mutated XHTML content back into the original file
// set and writes a spec-compliant EPUB archive (mimetype first and stored).
type EPUBRecomposer struct{}

// Recompose renders each content document's live node tree, overlays the result
// onto the original archive's files, and writes the EPUB to outputPath.
func (EPUBRecomposer) Recompose(_ context.Context, raw *RawEPUB, book *Book, outputPath string) error {
	if raw == nil || book == nil {
		return ErrNilRecomposeInput
	}

	files := maps.Clone(raw.Files)
	if files == nil {
		files = make(map[string][]byte)
	}

	opfDir := path.Dir(book.Path)
	for href, doc := range book.Contents {
		if doc == nil || doc.Root == nil {
			continue
		}

		rendered, err := renderDocument(doc)
		if err != nil {
			return fmt.Errorf("unable to render translated document %q, err: %w", href, err)
		}
		files[path.Join(opfDir, href)] = rendered
	}

	return writeEPUBArchive(outputPath, files)
}

// renderDocument serializes a document's node tree and restores the XML
// declaration that html.Render would otherwise emit as a bogus comment.
func renderDocument(doc *XHTMLDocument) ([]byte, error) {
	var buf bytes.Buffer
	if err := html.Render(&buf, doc.Root); err != nil {
		return nil, err
	}
	out := buf.Bytes()

	decl := bytes.TrimSpace(sourceXMLDecl.Find(doc.Raw))
	if len(decl) == 0 {
		return out, nil
	}

	if renderedXMLComment.Match(out) {
		return renderedXMLComment.ReplaceAll(out, decl), nil
	}
	// Original had a declaration but rendering dropped it: prepend it.
	return append(append(append([]byte{}, decl...), '\n'), out...), nil
}

// writeEPUBArchive writes files to a ZIP at outputPath. Per the EPUB spec the
// mimetype entry must come first and be stored uncompressed; remaining entries
// are written in deterministic (sorted) order.
func writeEPUBArchive(outputPath string, files map[string][]byte) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("unable to create output directory for %q, err: %w", outputPath, err)
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("unable to create output epub %q, err: %w", outputPath, err)
	}
	defer func() { _ = out.Close() }()

	zw := zip.NewWriter(out)

	if mimetype, ok := files["mimetype"]; ok {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
		if err != nil {
			return fmt.Errorf("unable to write mimetype entry, err: %w", err)
		}
		if _, err := w.Write(mimetype); err != nil {
			return fmt.Errorf("unable to write mimetype content, err: %w", err)
		}
	}

	names := make([]string, 0, len(files))
	for name := range files {
		if name == "mimetype" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			return fmt.Errorf("unable to write archive entry %q, err: %w", name, err)
		}
		if _, err := w.Write(files[name]); err != nil {
			return fmt.Errorf("unable to write archive content %q, err: %w", name, err)
		}
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("unable to finalize epub archive, err: %w", err)
	}

	return nil
}

var _ Recomposer = EPUBRecomposer{}
