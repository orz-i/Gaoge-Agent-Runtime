package model

import (
	"math"
	"strings"
)

// ValidResponse reports whether a completed response has a consumable SDK shape.
func ValidResponse(response Response) bool {
	return ValidUsage(response.Usage) &&
		(!response.Continue || len(response.ToolCalls) == 0) &&
		(response.Continue || len(response.ToolCalls) > 0 || strings.TrimSpace(response.Content) != "" || len(response.HostedToolCalls) > 0 || len(response.Artifacts) > 0)
}

func ValidUsage(usage *Usage) bool {
	if usage == nil {
		return true
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheReadTokens < 0 ||
		usage.CacheWriteTokens < 0 || usage.CacheWrite5mTokens < 0 || usage.CacheWrite1hTokens < 0 ||
		usage.ReasoningTokens < 0 {
		return false
	}
	return usage.InputTokens <= math.MaxInt64-usage.OutputTokens
}

// BillingOnlyResponse keeps identity and usage but never executable content
// from a rejected completion.
func BillingOnlyResponse(response Response) Response {
	return Response{ResponseID: response.ResponseID, Usage: CloneUsage(response.Usage)}
}
