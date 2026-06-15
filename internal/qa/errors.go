package qa

import "errors"

var ErrEPUBCheckFailed = errors.New("qa: epubcheck reported validation errors")
