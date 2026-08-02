package epub

import (
	"context"
	"encoding/xml"
	"fmt"
	"path"
	"strings"
)

type Parser interface {
	Parse(ctx context.Context, raw *RawEPUB) (*Book, error)
}

type OPFParser struct{}

type containerDocument struct {
	RootFiles struct {
		RootFiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfile"`
	} `xml:"rootfiles"`
}

type packageDocument struct {
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`

	Spine struct {
		Itemrefs []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

func (p OPFParser) Parse(ctx context.Context, rawEPUB *RawEPUB) (*Book, error) {
	// 1. decode container
	var container containerDocument
	if err := xml.Unmarshal(rawEPUB.Container, &container); err != nil {
		return nil, fmt.Errorf("unable to unmarshal xml container document, err: %w", err)
	}
	if len(container.RootFiles.RootFiles) == 0 {
		return nil, ErrMissingOPF
	}

	// 2. extract opf path
	opfPath := container.RootFiles.RootFiles[0].FullPath
	opfContent, ok := rawEPUB.Files[opfPath]
	if !ok {
		return nil, ErrMissingOPF
	}

	// 3. look up opf bytes in raw.Files
	var pkg packageDocument
	if err := xml.Unmarshal(opfContent, &pkg); err != nil {
		return nil, fmt.Errorf("unable to unmarshal opf content, err: %w", err)
	}

	// 5. map manifest
	manifestItems := make(map[string]ManifestItem, len(pkg.Manifest.Items))
	for _, item := range pkg.Manifest.Items {
		properties := []string{}
		if item.Properties != "" {
			properties = strings.Fields(item.Properties)
		}

		manifestItems[item.ID] = ManifestItem{
			ID:         item.ID,
			Href:       item.Href,
			MediaType:  item.MediaType,
			Properties: properties,
		}
	}

	// 6. map spine
	spine := make([]SpineItem, 0, len(pkg.Spine.Itemrefs))
	for _, itemref := range pkg.Spine.Itemrefs {
		spine = append(spine, SpineItem{IDRef: itemref.IDRef})
	}

	// 7. parse XHTML documents for spine items
	opfDir := path.Dir(opfPath)
	contents := make(map[string]*XHTMLDocument, len(spine))

	for _, item := range spine {
		manifestItem, ok := manifestItems[item.IDRef]
		if !ok {
			continue
		}

		// nav documents are structural — not translated
		if manifestItem.IsNav() {
			continue
		}

		fullPath := path.Join(opfDir, manifestItem.Href)
		raw, ok := rawEPUB.Files[fullPath]
		if !ok {
			return nil, fmt.Errorf("unable to find spine document %q, err: %w", fullPath, ErrMissingSpineDocument)
		}

		xdoc, err := parseXHTMLDocument(raw)
		if err != nil {
			return nil, fmt.Errorf("unable to parse xhtml document %q, err: %w", fullPath, err)
		}

		contents[manifestItem.Href] = xdoc
	}

	// 8. return Book
	return &Book{
		Path: opfPath,
		Manifest: Manifest{
			Items: manifestItems,
		},
		Spine:    spine,
		Contents: contents,
	}, nil
}

var _ Parser = new(OPFParser)
