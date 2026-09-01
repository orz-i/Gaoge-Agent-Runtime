package budget_test

import (
	"errors"
	"math"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestChargeModelCallAccumulatesOnlyObservedUsage(t *testing.T) {
	t.Parallel()
	usage, err := budget.ChargeModelCall(budget.Usage{}, &budget.TokenUsage{
		InputTokens: 7, OutputTokens: 3, CacheReadTokens: 2, CacheWriteTokens: 1, ReasoningTokens: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.LLMCalls != 1 || usage.InputTokens != 7 || usage.OutputTokens != 3 || usage.TotalTokens != 10 ||
		usage.CacheReadTokens != 2 || usage.CacheWriteTokens != 1 || usage.ReasoningTokens != 4 {
		t.Fatalf("usage = %#v", usage)
	}
	usage, err = budget.ChargeModelCall(usage, nil)
	if err != nil || usage.LLMCalls != 2 || usage.TotalTokens != 10 {
		t.Fatalf("nil observation usage = %#v, err=%v", usage, err)
	}
}

func TestChargeModelCallRejectsInvalidOrOverflowingUsage(t *testing.T) {
	t.Parallel()
	if _, err := budget.ChargeModelCall(budget.Usage{}, &budget.TokenUsage{InputTokens: -1}); !errors.Is(err, budget.ErrInvalidUsage) {
		t.Fatalf("negative usage err = %v", err)
	}
	if _, err := budget.ChargeModelCall(
		budget.Usage{InputTokens: math.MaxInt64}, &budget.TokenUsage{InputTokens: 1},
	); !errors.Is(err, budget.ErrInvalidUsage) {
		t.Fatalf("overflow usage err = %v", err)
	}
}

func TestExceededUsesOptionalCommonCeilings(t *testing.T) {
	t.Parallel()
	limits := budget.Limits{
		MaxLLMCalls: 2, MaxToolCalls: 3, MaxTotalTokens: 10, MaxOutputBytes: 64, MaxStateBytes: 128,
	}
	if dimension := budget.Exceeded(limits, budget.Usage{
		LLMCalls: 2, ToolCalls: 3, TotalTokens: 10, OutputBytes: 64, StateBytes: 128,
	}); dimension != "" {
		t.Fatalf("equal usage exceeded %q", dimension)
	}
	if dimension := budget.Exceeded(limits, budget.Usage{LLMCalls: 2, TotalTokens: 11}); dimension != budget.DimensionTotalTokens {
		t.Fatalf("dimension = %q", dimension)
	}
	if dimension := budget.Exceeded(limits, budget.Usage{OutputBytes: 65}); dimension != budget.DimensionOutputBytes {
		t.Fatalf("output dimension = %q", dimension)
	}
}

func TestValidLimitsAndTokenRequirement(t *testing.T) {
	t.Parallel()
	if !budget.ValidLimits(budget.Limits{}) || budget.ValidLimits(budget.Limits{MaxCostUnits: -1}) {
		t.Fatal("invalid limit validation")
	}
	if !budget.ValidUsage(budget.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}) ||
		budget.ValidUsage(budget.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 4}) {
		t.Fatal("invalid usage validation")
	}
	if budget.HasTokenLimits(budget.Limits{}) || !budget.HasTokenLimits(budget.Limits{MaxOutputTokens: 1}) {
		t.Fatal("token limit detection failed")
	}
}

func TestResolveLimitsUsesDefaultsOnlyForZeroDimensions(t *testing.T) {
	t.Parallel()
	resolved, err := budget.ResolveLimits(
		budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 16, MaxTotalTokens: 100, MaxOutputBytes: 1024},
		budget.Limits{MaxLLMCalls: 2, MaxOutputTokens: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MaxLLMCalls != 2 || resolved.MaxToolCalls != 16 || resolved.MaxOutputTokens != 20 ||
		resolved.MaxTotalTokens != 100 || resolved.MaxOutputBytes != 1024 {
		t.Fatalf("resolved = %#v", resolved)
	}
	if _, err = budget.ResolveLimits(budget.Limits{}, budget.Limits{MaxInputTokens: -1}); !errors.Is(err, budget.ErrInvalidUsage) {
		t.Fatalf("negative limits err = %v", err)
	}
}
