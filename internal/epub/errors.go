package epub

import "errors"

var (
	ErrMissingMimetype  = errors.New("epub missing mimetype")
	ErrMissingContainer = errors.New("epub missing META-INF/container.xml")
	ErrInvalidZipFile   = errors.New("invalid zip file")
)
