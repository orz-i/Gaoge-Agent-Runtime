package providerhttp

import (
	"math"

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
