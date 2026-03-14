package epub

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOPFParser_Parse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *RawEPUB
		expected struct {
			err   error
			book  *Book
			spine []SpineItem
		}
	}{
		{
			name:  "valid raw epub parses manifest items and spine order",
			input: newRawEPUBFixture(t, validEPUBEntries),
			expected: struct {
				err   error
				book  *Book
				spine []SpineItem
			}{
				book: &Book{
					Path: "OEBPS/content.opf",
					Manifest: Manifest{
						Items: map[string]ManifestItem{
							"chap1": {
								ID:        "chap1",
								Href:      "chapter1.xhtml",
								MediaType: "application/xhtml+xml",
							},
						},
					},
					Spine: []SpineItem{
						{IDRef: "chap1"},
					},
				},
				spine: []SpineItem{
					{IDRef: "chap1"},
				},
			},
		},
		{
			name: "container points to missing opf",
			input: newRawEPUBFixture(t, map[string]string{
				"mimetype": "application/epub+zip",
				"META-INF/container.xml": `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/missing.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
			}),
			expected: struct {
				err   error
				book  *Book
				spine []SpineItem
			}{
				err: ErrMissingOPF,
			},
		},
		{
			name: "manifest item properties are parsed",
			input: newRawEPUBFixture(t, map[string]string{
				"mimetype": "application/epub+zip",
				"META-INF/container.xml": `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
				"OEBPS/content.opf": `<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" xmlns="http://www.idpf.org/2007/opf">
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav scripted"/>
  </manifest>
  <spine>
    <itemref idref="nav"/>
  </spine>
</package>`,
			}),
			expected: struct {
				err   error
				book  *Book
				spine []SpineItem
			}{
				book: &Book{
					Path: "OEBPS/content.opf",
					Manifest: Manifest{
						Items: map[string]ManifestItem{
							"nav": {
								ID:         "nav",
								Href:       "nav.xhtml",
								MediaType:  "application/xhtml+xml",
								Properties: []string{"nav", "scripted"},
							},
						},
					},
					Spine: []SpineItem{
						{IDRef: "nav"},
					},
				},
				spine: []SpineItem{
					{IDRef: "nav"},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parser := OPFParser{}

			book, err := parser.Parse(t.Context(), tt.input)

			if tt.expected.err != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.expected.err)
				require.Nil(t, book)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, book)
			require.Equal(t, tt.expected.book.Path, book.Path)
			require.Equal(t, tt.expected.book.Manifest.Items, book.Manifest.Items)
			require.Equal(t, tt.expected.spine, book.Spine)
		})
	}
}

func newRawEPUBFixture(t *testing.T, entries map[string]string) *RawEPUB {
	t.Helper()

	raw := &RawEPUB{
		Files: make(map[string][]byte, len(entries)),
	}

	for name, content := range entries {
		raw.Files[name] = []byte(content)
	}

	raw.Mimetype = raw.Files["mimetype"]
	raw.Container = raw.Files["META-INF/container.xml"]

	return raw
}
