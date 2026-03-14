package epub

import (
	"archive/zip"
	"io"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var validEPUBEntries = map[string]string{
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
    <item id="chap1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="chap1"/>
  </spine>
</package>`,
	"OEBPS/chapter1.xhtml": `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <body>
    <p>Hello world</p>
  </body>
</html>`,
}

func TestEpubExtraction(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		desc     string
		input    string
		expected struct {
			err     error
			content *RawEPUB
		}
	}{
		{
			desc:  "valid epub file",
			input: writeTestEPUB(t, cloneEntries(validEPUBEntries)),
			expected: struct {
				err     error
				content *RawEPUB
			}{
				content: &RawEPUB{},
			},
		},
		{
			desc: "missing required mimetype",
			input: writeTestEPUB(t, func() map[string]string {
				entries := cloneEntries(validEPUBEntries)
				delete(entries, "mimetype")

				return entries
			}()),
			expected: struct {
				err     error
				content *RawEPUB
			}{
				err: ErrMissingMimetype,
			},
		},
		{
			desc: "missing required META-INF/container.xml",
			input: writeTestEPUB(t, func() map[string]string {
				entries := cloneEntries(validEPUBEntries)
				delete(entries, "META-INF/container.xml")

				return entries
			}()),
			expected: struct {
				err     error
				content *RawEPUB
			}{
				err: ErrMissingContainer,
			},
		},
		{
			desc:  "invalid zip file",
			input: writeInvalidZipFile(t),
			expected: struct {
				err     error
				content *RawEPUB
			}{
				err: ErrInvalidZipFile,
			},
		},
	}

	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			extractor := ZipExtractor{}

			content, err := extractor.Extract(ctx, tC.input)

			if tC.expected.err != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tC.expected.err, "returned error of the extractor does not match")
				require.Nil(t, content, "content should be nil on error")

				return
			}

			require.NoError(t, err)
			require.NotNil(t, content, "content should not be nil for a valid epub")
			require.Equal(t, string(content.Mimetype), validEPUBEntries["mimetype"], "mimetype does not match expected output")
			require.Equal(t, []byte(validEPUBEntries["META-INF/container.xml"]), content.Container, "container.xml bytes do not match expected output")
			require.Contains(t, content.Files, "META-INF/container.xml", "container.xml should be present in extracted files")
			require.Contains(t, content.Files, "OEBPS/content.opf", "content.opf should be present in extracted files")
			require.Equal(t, []byte(validEPUBEntries["META-INF/container.xml"]), content.Files["META-INF/container.xml"], "container.xml file bytes do not match expected output")
			require.Equal(t, []byte(validEPUBEntries["OEBPS/content.opf"]), content.Files["OEBPS/content.opf"], "content.opf file bytes do not match expected output")
		})
	}
}

func cloneEntries(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

func writeTestEPUB(t *testing.T, entries map[string]string) string {
	t.Helper()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.epub")

	file, err := os.Create(path)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(file)

	mimetype, ok := entries["mimetype"]
	if ok {
		header := &zip.FileHeader{
			Name:   "mimetype",
			Method: zip.Store,
		}
		writer, createErr := zipWriter.CreateHeader(header)
		require.NoError(t, createErr)

		_, writeErr := writer.Write([]byte(mimetype))
		require.NoError(t, writeErr)
	}

	for name, content := range entries {
		if name == "mimetype" {
			continue
		}

		var writer io.Writer

		writer, err = zipWriter.Create(name)
		require.NoError(t, err)

		_, err = writer.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, zipWriter.Close())
	require.NoError(t, file.Close())

	return path
}

func writeInvalidZipFile(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalid.epub")

	err := os.WriteFile(path, []byte("not a zip"), 0o600)
	require.NoError(t, err)

	return path
}
