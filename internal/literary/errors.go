package literary

import "errors"

var (
	ErrNilChatClient = errors.New("literary: chat client is nil")
	ErrEmptyResponse = errors.New("literary: provider returned empty response")
	// ErrProviderRefusal is returned when the model declined to answer. A
	// smaller batch will not fix it, so it fails the batch outright.
	ErrProviderRefusal = errors.New("literary: provider refused to answer")
	// ErrIncompleteResponse is returned when a single-segment batch came back
	// truncated — the reply hit max_tokens or another non-terminal stop reason
	// and there is nothing left to split. The segment keeps its draft.
	ErrIncompleteResponse = errors.New("literary: provider stopped before completing the response")
)
