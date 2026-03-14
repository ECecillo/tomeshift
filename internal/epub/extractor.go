package epub

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
)

type Extractor interface {
	Extract(ctx context.Context, path string) (*RawEPUB, error)
}

type ZipExtractor struct{}

func (z ZipExtractor) Extract(_ context.Context, path string) (*RawEPUB, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("unable to open epub archive %q, err: %w", path, ErrInvalidZipFile)
	}
	defer r.Close()

	rawEPUB := &RawEPUB{
		Files: make(map[string][]byte, len(r.File)),
	}

	for _, file := range r.File {
		rc, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("unable to open archived file %q, err: %w", file.Name, err)
		}

		content, err := io.ReadAll(rc)
		closeErr := rc.Close()
		if err != nil {
			return nil, fmt.Errorf("unable to read archived file %q, err: %w", file.Name, err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("unable to close archived file %q, err: %w", file.Name, closeErr)
		}

		rawEPUB.Files[file.Name] = content
	}

	mimetype, ok := rawEPUB.Files["mimetype"]
	if !ok {
		return nil, fmt.Errorf("unable to validate epub structure, err: %w", ErrMissingMimetype)
	}

	container, ok := rawEPUB.Files["META-INF/container.xml"]
	if !ok {
		return nil, fmt.Errorf("unable to validate epub structure, err: %w", ErrMissingContainer)
	}

	rawEPUB.Mimetype = mimetype
	rawEPUB.Container = container

	return rawEPUB, nil
}

var _ Extractor = new(ZipExtractor)
