package providerhttp_test

import (
	"errors"
	"math"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestCompletedUsageMustBeVerifiableBeforeRejectedResponseReceipt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage *model.Usage
		valid bool
	}{
		{name: "missing"},
		{name: "known_zero", usage: &model.Usage{}, valid: true},
		{name: "known_total", usage: &model.Usage{InputTokens: 17, OutputTokens: 5, CacheReadTokens: 2, ReasoningTokens: 1}, valid: true},
		{name: "negative_input", usage: &model.Usage{InputTokens: -1, OutputTokens: 5}},
		{name: "negative_output", usage: &model.Usage{InputTokens: 17, OutputTokens: -1}},
		{name: "negative_cache", usage: &model.Usage{InputTokens: 17, OutputTokens: 5, CacheWriteTokens: -1}},
		{name: "negative_cache_5m", usage: &model.Usage{CacheWrite5mTokens: -1}},
		{name: "negative_cache_1h", usage: &model.Usage{CacheWrite1hTokens: -1}},
		{name: "negative_reasoning", usage: &model.Usage{ReasoningTokens: -1}},
		{name: "overflow_total", usage: &model.Usage{InputTokens: math.MaxInt64, OutputTokens: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if actual := providerhttp.ValidCompletedUsage(tc.usage); actual != tc.valid {
				t.Fatalf("completed Usage proof=%t want=%t: %+v", actual, tc.valid, tc.usage)
			}
		})
	}
}

func TestRejectCompletedResponseNeverExposesInvalidProviderOutput(t *testing.T) {
	cause := errors.New("invalid Tool arguments")
	input := model.Response{
		ResponseID: "paid-1", Usage: &model.Usage{InputTokens: 17, OutputTokens: 5},
		Content: "untrusted data", ToolCalls: []tools.Call{{ID: "unsafe", Name: "lookup"}},
		ProviderReplay: &model.ProviderReplay{Provider: "openai", Format: "unsafe"},
	}
	result, err := providerhttp.RejectCompletedResponse(input, cause)
	if !errors.Is(err, cause) || !model.IsResponseRejectedError(err) ||
		model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
		result.ResponseID != "paid-1" || result.Usage == nil || result.Usage.InputTokens != 17 ||
		result.Usage.OutputTokens != 5 || result.Content != "" || len(result.ToolCalls) != 0 ||
		result.ProviderReplay != nil {
		t.Fatalf("not a minimal rejected receipt: response=%+v err=%v", result, err)
	}
	input.Usage.InputTokens = 99
	if result.Usage.InputTokens != 17 {
		t.Fatal("paid receipt shares untrusted mutable Usage")
	}
	for _, tc := range []model.Response{
		{ResponseID: "", Usage: &model.Usage{}},
		{ResponseID: "paid", Usage: nil},
		{ResponseID: "paid", Usage: &model.Usage{InputTokens: -1}},
		{ResponseID: "paid", Usage: &model.Usage{InputTokens: math.MaxInt64, OutputTokens: 1}},
	} {
		invalid, rejectErr := providerhttp.RejectCompletedResponse(tc, cause)
		if invalid.ResponseID != "" || invalid.Usage != nil ||
			model.IsResponseRejectedError(rejectErr) || !errors.Is(rejectErr, cause) {
			t.Fatalf("invented an uncertified completion: response=%+v err=%v", invalid, rejectErr)
		}
	}
}
