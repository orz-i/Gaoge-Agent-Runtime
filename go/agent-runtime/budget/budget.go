// Package budget defines a small feature-neutral vocabulary for runtime limits
// and durable usage. Features decide which dimensions they can enforce.
package budget

import (
	"errors"
	"math"
)

var ErrInvalidUsage = errors.New("invalid runtime budget usage")

// Dimension identifies one common runtime budget dimension.
type Dimension string

const (
	DimensionLLMCalls       Dimension = "llm_calls"
	DimensionToolCalls      Dimension = "tool_calls"
	DimensionInputTokens    Dimension = "input_tokens"
	DimensionOutputTokens   Dimension = "output_tokens"
	DimensionTotalTokens    Dimension = "total_tokens"
	DimensionOutputBytes    Dimension = "output_bytes"
	DimensionStateBytes     Dimension = "state_bytes"
	DimensionChildRuns      Dimension = "child_runs"
	DimensionCostUnits      Dimension = "cost_units"
	DimensionConcurrentRuns Dimension = "concurrent_runs"
)

// Limits are optional ceilings shared only where features have identical
// semantics. Zero means that dimension is not limited by this common layer.
// Reasoning tokens are intentionally observation-only because providers do not
// report them consistently enough for a portable hard limit.
type Limits struct {
	MaxLLMCalls       int   `json:"maxLLMCalls,omitempty"`
	MaxToolCalls      int   `json:"maxToolCalls,omitempty"`
	MaxInputTokens    int64 `json:"maxInputTokens,omitempty"`
	MaxOutputTokens   int64 `json:"maxOutputTokens,omitempty"`
	MaxTotalTokens    int64 `json:"maxTotalTokens,omitempty"`
	MaxOutputBytes    int   `json:"maxOutputBytes,omitempty"`
	MaxStateBytes     int   `json:"maxStateBytes,omitempty"`
	MaxChildRuns      int   `json:"maxChildRuns,omitempty"`
	MaxCostUnits      int64 `json:"maxCostUnits,omitempty"`
	MaxConcurrentRuns int   `json:"maxConcurrentRuns,omitempty"`
}

// ValidUsage validates a durable ledger, including the invariant that total
// tokens are exactly the sum of observed input and output tokens.
func ValidUsage(value Usage) bool {
	if value.LLMCalls < 0 || value.ToolCalls < 0 || value.InputTokens < 0 || value.OutputTokens < 0 ||
		value.TotalTokens < 0 || value.CacheReadTokens < 0 || value.CacheWriteTokens < 0 ||
		value.ReasoningTokens < 0 || value.OutputBytes < 0 || value.StateBytes < 0 ||
		value.ChildRuns < 0 || value.CostUnits < 0 {
		return false
	}
	if value.InputTokens > math.MaxInt64-value.OutputTokens {
		return false
	}
	return value.TotalTokens == value.InputTokens+value.OutputTokens
}

// ResolveLimits fills zero-valued request dimensions from defaults. Negative
// values are invalid in either input; zero remains unlimited when the default
// for that dimension is also zero.
func ResolveLimits(defaults Limits, requested Limits) (Limits, error) {
	if !ValidLimits(defaults) || !ValidLimits(requested) {
		return Limits{}, ErrInvalidUsage
	}
	resolved := requested
	if resolved.MaxLLMCalls == 0 {
		resolved.MaxLLMCalls = defaults.MaxLLMCalls
	}
	if resolved.MaxToolCalls == 0 {
		resolved.MaxToolCalls = defaults.MaxToolCalls
	}
	if resolved.MaxInputTokens == 0 {
		resolved.MaxInputTokens = defaults.MaxInputTokens
	}
	if resolved.MaxOutputTokens == 0 {
		resolved.MaxOutputTokens = defaults.MaxOutputTokens
	}
	if resolved.MaxTotalTokens == 0 {
		resolved.MaxTotalTokens = defaults.MaxTotalTokens
	}
	if resolved.MaxOutputBytes == 0 {
		resolved.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if resolved.MaxStateBytes == 0 {
		resolved.MaxStateBytes = defaults.MaxStateBytes
	}
	if resolved.MaxChildRuns == 0 {
		resolved.MaxChildRuns = defaults.MaxChildRuns
	}
	if resolved.MaxCostUnits == 0 {
		resolved.MaxCostUnits = defaults.MaxCostUnits
	}
	if resolved.MaxConcurrentRuns == 0 {
		resolved.MaxConcurrentRuns = defaults.MaxConcurrentRuns
	}
	return resolved, nil
}

// Usage is one durable, logically consumed usage ledger. Token cache and
// reasoning observations are retained for telemetry but are not independently
// limited by this common vocabulary.
type Usage struct {
	LLMCalls         int   `json:"llmCalls,omitempty"`
	ToolCalls        int   `json:"toolCalls,omitempty"`
	InputTokens      int64 `json:"inputTokens,omitempty"`
	OutputTokens     int64 `json:"outputTokens,omitempty"`
	TotalTokens      int64 `json:"totalTokens,omitempty"`
	CacheReadTokens  int64 `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int64 `json:"cacheWriteTokens,omitempty"`
	ReasoningTokens  int64 `json:"reasoningTokens,omitempty"`
	OutputBytes      int   `json:"outputBytes,omitempty"`
	StateBytes       int   `json:"stateBytes,omitempty"`
	ChildRuns        int   `json:"childRuns,omitempty"`
	CostUnits        int64 `json:"costUnits,omitempty"`
}

// Snapshot pairs the frozen limits with the usage ledger observed so far.
type Snapshot struct {
	Limits Limits `json:"limits"`
	Usage  Usage  `json:"usage"`
}

// TokenUsage is one provider-neutral model usage observation.
type TokenUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
}

// ValidLimits rejects negative ceilings while preserving zero as unlimited.
func ValidLimits(value Limits) bool {
	return value.MaxLLMCalls >= 0 && value.MaxToolCalls >= 0 &&
		value.MaxInputTokens >= 0 && value.MaxOutputTokens >= 0 && value.MaxTotalTokens >= 0 &&
		value.MaxOutputBytes >= 0 && value.MaxStateBytes >= 0 && value.MaxChildRuns >= 0 && value.MaxCostUnits >= 0 && value.MaxConcurrentRuns >= 0
}

// HasTokenLimits reports whether exact token observations are required to
// enforce the configured common budget.
func HasTokenLimits(value Limits) bool {
	return value.MaxInputTokens > 0 || value.MaxOutputTokens > 0 || value.MaxTotalTokens > 0
}

// ChargeModelCall advances the logically consumed model and token usage once.
func ChargeModelCall(current Usage, tokens *TokenUsage) (Usage, error) {
	current.LLMCalls++
	if tokens == nil {
		return current, nil
	}
	values := []int64{
		tokens.InputTokens, tokens.OutputTokens, tokens.CacheReadTokens,
		tokens.CacheWriteTokens, tokens.ReasoningTokens,
	}
	for _, value := range values {
		if value < 0 {
			return Usage{}, ErrInvalidUsage
		}
	}
	var err error
	if current.InputTokens, err = checkedAdd(current.InputTokens, tokens.InputTokens); err != nil {
		return Usage{}, err
	}
	if current.OutputTokens, err = checkedAdd(current.OutputTokens, tokens.OutputTokens); err != nil {
		return Usage{}, err
	}
	callTotal, err := checkedAdd(tokens.InputTokens, tokens.OutputTokens)
	if err != nil {
		return Usage{}, err
	}
	if current.TotalTokens, err = checkedAdd(current.TotalTokens, callTotal); err != nil {
		return Usage{}, err
	}
	if current.CacheReadTokens, err = checkedAdd(current.CacheReadTokens, tokens.CacheReadTokens); err != nil {
		return Usage{}, err
	}
	if current.CacheWriteTokens, err = checkedAdd(current.CacheWriteTokens, tokens.CacheWriteTokens); err != nil {
		return Usage{}, err
	}
	if current.ReasoningTokens, err = checkedAdd(current.ReasoningTokens, tokens.ReasoningTokens); err != nil {
		return Usage{}, err
	}
	return current, nil
}

// Exceeded returns the first common dimension whose configured ceiling was
// crossed. Equality is within budget.
func Exceeded(limits Limits, usage Usage) Dimension {
	switch {
	case limits.MaxLLMCalls > 0 && usage.LLMCalls > limits.MaxLLMCalls:
		return DimensionLLMCalls
	case limits.MaxToolCalls > 0 && usage.ToolCalls > limits.MaxToolCalls:
		return DimensionToolCalls
	case limits.MaxInputTokens > 0 && usage.InputTokens > limits.MaxInputTokens:
		return DimensionInputTokens
	case limits.MaxOutputTokens > 0 && usage.OutputTokens > limits.MaxOutputTokens:
		return DimensionOutputTokens
	case limits.MaxTotalTokens > 0 && usage.TotalTokens > limits.MaxTotalTokens:
		return DimensionTotalTokens
	case limits.MaxOutputBytes > 0 && usage.OutputBytes > limits.MaxOutputBytes:
		return DimensionOutputBytes
	case limits.MaxStateBytes > 0 && usage.StateBytes > limits.MaxStateBytes:
		return DimensionStateBytes
	case limits.MaxChildRuns > 0 && usage.ChildRuns > limits.MaxChildRuns:
		return DimensionChildRuns
	case limits.MaxCostUnits > 0 && usage.CostUnits > limits.MaxCostUnits:
		return DimensionCostUnits
	default:
		return ""
	}
}

func checkedAdd(left int64, right int64) (int64, error) {
	if left < 0 || right < 0 || (right > 0 && left > math.MaxInt64-right) {
		return 0, ErrInvalidUsage
	}
	return left + right, nil
}
