package context

import (
	"errors"
	"fmt"
)

// ErrArtifactPayloadTooLarge identifies the durable archive ceiling, independently
// of the routed model's input token budget.
var ErrArtifactPayloadTooLarge = errors.New("context artifact payload exceeds durable limit")

// ArtifactPayloadLimitError reports content-free archive size facts. It also
// unwraps to ErrInvalidInput for callers written before the specific limit error.
type ArtifactPayloadLimitError struct {
	PayloadBytes int64
	LimitBytes   int64
}

func (err *ArtifactPayloadLimitError) Error() string {
	if err == nil {
		return ErrArtifactPayloadTooLarge.Error()
	}
	return fmt.Sprintf("%s: bytes=%d limit=%d; shorten the input or start a new conversation",
		ErrArtifactPayloadTooLarge, err.PayloadBytes, err.LimitBytes)
}

func (*ArtifactPayloadLimitError) Unwrap() []error {
	return []error{ErrArtifactPayloadTooLarge, ErrInvalidInput}
}
