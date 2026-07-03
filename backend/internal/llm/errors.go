package llm

import "errors"

var (
	ErrInvalidRequest = errors.New("invalid llm request")
	ErrAttemptExists  = errors.New("llm attempt key already exists")
)
