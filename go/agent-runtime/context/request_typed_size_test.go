package context_test

import (
	"encoding/json"
	"strings"
	"testing"

	runtimectx "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestAssessModelRequestBoundsTypedNumbersWithoutExactCounter(t *testing.T) {
	t.Parallel()
	request := model.Request{RunID: "typed-size-run", Model: "model", InvocationID: "modelinv_" + strings.Repeat("a", 32),
		Messages:     []model.Message{{Role: model.RoleUser, Content: model.TextParts("count the actual request")}},
		ModelOptions: json.RawMessage(`{"temperature":0.1` + strings.Repeat("0", 300) + `}`),
	}
	typed, err := model.MarshalRequestForInputCount(request)
	requireNoError(t, err)
	for _, mode := range []string{"absent", "unrouted", "unavailable", "exact"} {
		t.Run(mode, func(t *testing.T) {
			var counted runtimectx.TokenCountRequest
			dependencies := runtimectx.Dependencies{}
			window := runtimectx.ModelWindow{ContextTokens: 100000}
			if mode != "absent" {
				count := int64(0)
				if mode == "exact" {
					count = 17
				}
				dependencies.Counter = exactCounter{tokens: count, seen: &counted}
			}
			if mode == "unavailable" || mode == "exact" {
				window.TokenCountContext = runtimectx.TokenCountContext{Protocol: "fixture", Model: "model"}
			}
			manager := runtimectx.NewManager(dependencies)
			policy := runtimectx.Policy{MaxSerializedBytes: int64(len(typed) - 1)}
			assessment, err := manager.AssessModelRequest(t.Context(), request, window, policy)
			requireNoError(t, err)
			if assessment.SerializedBytes < int64(len(typed)) || runtimectx.WithinHardBudget(assessment, policy) {
				t.Fatalf("raw numeric spelling escaped the byte ceiling: mode=%s actual=%d assessment=%+v", mode, len(typed), assessment)
			}
			if mode == "unrouted" && len(counted.Payload) != 0 {
				t.Fatal("an unbound exact counter was invoked")
			}
			if mode == "exact" {
				if assessment.TokenCountSource != runtimectx.CountExact || assessment.HardTokenEstimate != 17 {
					t.Fatal("typed byte accounting replaced a valid exact token count")
				}
				return
			}
			policy.MaxSerializedBytes, policy.MaxInputTokens = int64(len(typed)*2), int64(len(typed)-1)
			assessment, err = manager.AssessModelRequest(t.Context(), request, window, policy)
			requireNoError(t, err)
			if assessment.TokenCountSource != runtimectx.CountEstimated || assessment.HardTokenEstimate < int64(len(typed)) || runtimectx.WithinHardBudget(assessment, policy) {
				t.Fatalf("raw numeric spelling escaped the fallback token ceiling: actual=%d assessment=%+v", len(typed), assessment)
			}
		})
	}
}

func TestAssessModelRequestBoundsNumbersOutsideFloat64(t *testing.T) {
	t.Parallel()
	request := model.Request{RunID: "budget", Model: "model", ModelOptions: json.RawMessage(`{"limit":1e1000}`),
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("count the actual request")}},
	}
	typed, err := model.MarshalRequestForInputCount(request)
	requireNoError(t, err)
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	policy := runtimectx.Policy{MaxSerializedBytes: int64(len(typed) - 1)}
	assessment, err := manager.AssessModelRequest(t.Context(), request, runtimectx.ModelWindow{ContextTokens: 100000}, policy)
	requireNoError(t, err)
	if assessment.TokenCountSource != runtimectx.CountEstimated || assessment.SerializedBytes != int64(len(typed)) ||
		assessment.HardTokenEstimate < int64(len(typed)) || runtimectx.WithinHardBudget(assessment, policy) {
		t.Fatalf("overflowing JSON number escaped the fallback budget: actual=%d assessment=%+v", len(typed), assessment)
	}
}
