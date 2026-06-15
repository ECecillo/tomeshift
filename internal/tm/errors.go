package tm

import "errors"

var (
	ErrEmptyHash   = errors.New("tm: entry source hash is empty")
	ErrNilDatabase = errors.New("tm: store has no open database")
)
