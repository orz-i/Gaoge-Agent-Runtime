package providerhttp_test

import (
	"math"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
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
