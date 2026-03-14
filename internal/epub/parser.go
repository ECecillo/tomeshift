package epub

import "context"

type Parser interface {
	Parse(ctx context.Context, raw *RawEPUB) (*Book, error)
}

type OPFParser struct{}

func (p OPFParser) Parse(_ context.Context, _ *RawEPUB) (*Book, error) {
	return nil, nil
}

var _ Parser = new(OPFParser)
