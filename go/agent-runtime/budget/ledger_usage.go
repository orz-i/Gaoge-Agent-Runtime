package budget

import "math"

// AddUsage rejects overflow in all dimensions instead of wrapping the ledger.
func AddUsage(left, right Usage) (Usage, error) {
	if !ValidUsage(left) || !ValidUsage(right) {
		return Usage{}, ErrInvalidUsage
	}
	result := Usage{}
	for _, pair := range []struct {
		a, b   int
		target *int
	}{
		{left.LLMCalls, right.LLMCalls, &result.LLMCalls},
		{left.ToolCalls, right.ToolCalls, &result.ToolCalls},
		{left.ChildRuns, right.ChildRuns, &result.ChildRuns},
		{left.OutputBytes, right.OutputBytes, &result.OutputBytes},
		{left.StateBytes, right.StateBytes, &result.StateBytes},
	} {
		if pair.a > math.MaxInt-pair.b {
			return Usage{}, ErrInvalidUsage
		}
		*pair.target = pair.a + pair.b
	}
	for _, pair := range []struct {
		a, b   int64
		target *int64
	}{
		{left.InputTokens, right.InputTokens, &result.InputTokens},
		{left.OutputTokens, right.OutputTokens, &result.OutputTokens},
		{left.TotalTokens, right.TotalTokens, &result.TotalTokens},
		{left.CacheReadTokens, right.CacheReadTokens, &result.CacheReadTokens},
		{left.CacheWriteTokens, right.CacheWriteTokens, &result.CacheWriteTokens},
		{left.ReasoningTokens, right.ReasoningTokens, &result.ReasoningTokens},
		{left.CostUnits, right.CostUnits, &result.CostUnits},
	} {
		value, err := checkedAdd(pair.a, pair.b)
		if err != nil {
			return Usage{}, err
		}
		*pair.target = value
	}
	return result, nil
}

func remainingUsage(limits Limits, used, reserved Usage) Usage {
	return Usage{
		LLMCalls:     max(0, limits.MaxLLMCalls-used.LLMCalls-reserved.LLMCalls),
		ToolCalls:    max(0, limits.MaxToolCalls-used.ToolCalls-reserved.ToolCalls),
		ChildRuns:    max(0, limits.MaxChildRuns-used.ChildRuns-reserved.ChildRuns),
		InputTokens:  max(0, limits.MaxInputTokens-used.InputTokens-reserved.InputTokens),
		OutputTokens: max(0, limits.MaxOutputTokens-used.OutputTokens-reserved.OutputTokens),
		TotalTokens:  max(0, limits.MaxTotalTokens-used.TotalTokens-reserved.TotalTokens),
	}
}
