package context_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	runtimectx "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestCanonicalAccountingProjectionKeepsOriginalRequestAndCountingIdentity(t *testing.T) {
	request := model.Request{RunID: "run", InvocationID: "inv", BindingRef: "route", Model: "model",
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("protected input")}}}
	window := runtimectx.ModelWindow{ContextTokens: 4096, ReservedOutputTokens: 256,
		TokenCountContext: runtimectx.TokenCountContext{Protocol: "fixture", Model: "provider"}}
	policy := runtimectx.Policy{MaxInputTokens: 900, EstimateSafetyPercent: 25}
	original, err := model.MarshalRequestForInputCount(request)
	requireNoError(t, err)
	project := func(_ context.Context, r model.Request, w runtimectx.ModelWindow) (model.Request, error) {
		r.ModelOptions = json.RawMessage("{\"provider_default\":true}")
		r.MaxOutputTokens = w.ReservedOutputTokens
		r.Messages = append([]model.Message{{Role: model.RoleSystem, Content: model.TextParts(strings.Repeat("s", 1200))}}, r.Messages...)
		return r, nil
	}
	canonical, err := project(t.Context(), model.CloneRequest(request), window)
	requireNoError(t, err)
	accountingBytes, err := model.MarshalRequestForInputCount(canonical)
	requireNoError(t, err)
	for _, mode := range []string{"estimated", "exact"} {
		t.Run(mode, func(t *testing.T) {
			deps := runtimectx.Dependencies{Accounting: project}
			var seen runtimectx.TokenCountRequest
			if mode == "exact" {
				deps.Counter = exactCounter{tokens: 17, seen: &seen}
			}
			assessment, err := runtimectx.NewManager(deps).AssessModelRequest(t.Context(), request, window, policy)
			requireNoError(t, err)
			if assessment.SerializedBytes != int64(len(accountingBytes)) {
				t.Fatalf("canonical bytes omitted: %+v", assessment)
			}
			if mode == "estimated" {
				if assessment.TokenCountSource != runtimectx.CountEstimated || assessment.HardTokenEstimate != int64((len(accountingBytes)*125+99)/100) || runtimectx.WithinHardBudget(assessment, policy) {
					t.Fatalf("canonical input escaped the hard budget: %+v", assessment)
				}
			} else if assessment.TokenCountSource != runtimectx.CountExact || assessment.HardTokenEstimate != 17 ||
				!bytes.Equal(seen.Payload, original) || seen.Context != window.TokenCountContext {
				t.Fatalf("exact counting was relabeled or changed its input: %+v", assessment)
			}
			after, err := model.MarshalRequestForInputCount(request)
			requireNoError(t, err)
			if !bytes.Equal(after, original) {
				t.Fatal("projection modified the executable request")
			}
		})
	}
}

func TestCanonicalAccountingRejectsMutationAndPropagatesFailure(t *testing.T) {
	request := model.Request{RunID: "run", BindingRef: "route", Model: "model",
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("protected")}}}
	window := runtimectx.ModelWindow{ContextTokens: 4096, ReservedOutputTokens: 256}
	for _, tc := range []struct {
		name   string
		mutate func(*model.Request)
	}{
		{"drop transcript", func(r *model.Request) { r.Messages = nil }},
		{"change route", func(r *model.Request) { r.BindingRef = "other" }},
		{"change model", func(r *model.Request) { r.Model = "other" }},
		{"alter content", func(r *model.Request) { r.Messages[0].Content = model.TextParts("substitute") }},
		{"wrong ceiling", func(r *model.Request) { r.MaxOutputTokens++ }},
		{"non-system prefix", func(r *model.Request) {
			r.Messages = append([]model.Message{{Role: model.RoleUser, Content: model.TextParts("not system")}}, r.Messages...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := runtimectx.NewManager(runtimectx.Dependencies{Accounting: func(_ context.Context, r model.Request, w runtimectx.ModelWindow) (model.Request, error) {
				r.MaxOutputTokens = w.ReservedOutputTokens
				tc.mutate(&r)
				return r, nil
			}})
			if _, err := manager.AssessModelRequest(t.Context(), request, window, runtimectx.Policy{}); !errors.Is(err, runtimectx.ErrInvalidInput) {
				t.Fatalf("accounting mutation was accepted: %v", err)
			}
		})
	}
	unavailable := errors.New("gateway policy unavailable")
	manager := runtimectx.NewManager(runtimectx.Dependencies{Accounting: func(context.Context, model.Request, runtimectx.ModelWindow) (model.Request, error) {
		return model.Request{}, unavailable
	}})
	if _, err := manager.AssessModelRequest(t.Context(), request, window, runtimectx.Policy{}); !errors.Is(err, unavailable) {
		t.Fatalf("projection failure silently fell back: %v", err)
	}
}
