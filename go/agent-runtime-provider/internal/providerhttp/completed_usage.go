package providerhttp

import (
	"math"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// ValidCompletedUsage is the narrow, provider-neutral proof required before
// reporting an already completed but semantically rejected Model response.
// A missing or invalid cumulative receipt is not proof of known completion:
// the Agent must not account or acknowledge an invented terminal result.
// Mirror Agent's persisted ModelInvocation usage validity predicate.
func ValidCompletedUsage(usage *model.Usage) bool {
	if usage == nil ||
		usage.InputTokens < 0 || usage.OutputTokens < 0 ||
		usage.CacheReadTokens < 0 || usage.CacheWriteTokens < 0 ||
		usage.CacheWrite5mTokens < 0 || usage.CacheWrite1hTokens < 0 ||
		usage.ReasoningTokens < 0 {
		return false
	}
	return usage.InputTokens <= math.MaxInt64-usage.OutputTokens
}

// RejectCompletedResponse consumes a known completed but semantically invalid
// provider response without authorizing its content, Tool calls or replay.
// Invalid/missing cumulative Usage cannot certify an already-billed receipt.
// Only call this after validating the provider's outer completion status.
func RejectCompletedResponse(response model.Response, cause error) (model.Response, error) {
	if strings.TrimSpace(response.ResponseID) == "" || !ValidCompletedUsage(response.Usage) {
		return model.Response{}, cause
	}
	return model.Response{
		ResponseID: response.ResponseID,
		Usage:      model.CloneUsage(response.Usage),
	}, model.NewResponseRejectedError(model.NewClassifiedError(cause, "model.post_generation_failed"))
}
