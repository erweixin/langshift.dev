package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/langshift/lites/internal/llmgateway/provider"
)

var ErrTokenEstimate = errors.New("provider token estimate is invalid")

// ConservativeTokenEstimator converts a provider-neutral request into a hard
// upper bound. Six tokens per encoded byte covers JSON escape expansion and
// byte-fallback tokenizers; FixedOverhead covers provider framing and special
// tokens. It deliberately prefers rejecting an oversized request to silently
// under-reserving credits.
type ConservativeTokenEstimator struct {
	FixedOverhead uint64
	MaximumTokens uint64
}

func (estimator ConservativeTokenEstimator) Estimate(ctx context.Context, request provider.Request) (uint64, error) {
	if ctx == nil || ctx.Err() != nil || request.System == "" || len(request.Messages) == 0 || request.MaxOutputTokens == 0 {
		return 0, ErrTokenEstimate
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) == 0 {
		return 0, ErrTokenEstimate
	}
	overhead := estimator.FixedOverhead
	if overhead == 0 {
		overhead = 8192
	}
	if uint64(len(encoded)) > (math.MaxUint64-overhead)/6 {
		return 0, ErrTokenEstimate
	}
	estimate := uint64(len(encoded))*6 + overhead
	maximum := estimator.MaximumTokens
	if maximum == 0 {
		maximum = 8_000_000
	}
	if estimate > maximum {
		return 0, ErrTokenEstimate
	}
	return estimate, nil
}
