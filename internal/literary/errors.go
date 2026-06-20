package literary

import "errors"

var (
	ErrNilChatClient = errors.New("literary: chat client is nil")
	ErrEmptyResponse = errors.New("literary: provider returned empty response")
)
