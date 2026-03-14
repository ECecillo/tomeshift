package epub

import "context"

type Extractor interface {
	Extract(ctx context.Context, path string) (*RawEPUB, error)
}
