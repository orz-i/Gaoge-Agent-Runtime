package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestModelOutputLimitIsPerSamplingAndSeparateFromCumulativeUsage(t *testing.T) {
	provider := &structuredFixtureModel{supports: true, responses: []model.Response{
		{Content: `{"answer":123}`, Usage: &model.Usage{InputTokens: 3, OutputTokens: 3}},
		{Content: `{"answer":"fixed"}`, Usage: &model.Usage{InputTokens: 4, OutputTokens: 4}},
	}}
	runtime := newModelInvocationRuntime(t, memory.NewStore())
	prepared := 0
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider,
		PrepareModelRequest: func(_ context.Context, request model.Request) (model.Request, error) {
			prepared++
			if request.MaxOutputTokens != 64 {
				t.Fatalf("preparer did not receive the frozen sampling ceiling: %d", request.MaxOutputTokens)
			}
			return request, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := structuredRequest("sampling-output-limit")
	request.MaxModelOutputTokens = 64
	request.Limits.MaxOutputTokens = 8
	completed, err := runner.StartRun(t.Context(), request)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(provider.requests) != 2 || prepared != 2 {
		t.Fatalf("repair did not complete within the independent limits: status=%s calls=%d prepared=%d err=%v", completed.Run.Status, len(provider.requests), prepared, err)
	}
	for _, call := range provider.requests {
		if call.MaxOutputTokens != 64 {
			t.Fatalf("subsequent sampling lost its own ceiling: %d", call.MaxOutputTokens)
		}
	}
	view := mustModelInvocationView(t, completed)
	if view.MaxModelOutputTokens != 64 || view.Budget.Limits.MaxOutputTokens != 8 || view.Budget.Usage.OutputTokens != 7 || view.Budget.Usage.LLMCalls != 2 {
		t.Fatalf("sampling ceiling became cumulative usage: %+v", view)
	}
}

func TestModelOutputLimitRejectsNegativeAdmissionBeforeRunCreation(t *testing.T) {
	runtime := newModelInvocationRuntime(t, memory.NewStore())
	provider := &invocationRecordingModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider,
		PrepareModelRequest: func(context.Context, model.Request) (model.Request, error) {
			t.Fatal("negative sampling ceiling reached request preparation")
			return model.Request{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("negative-sampling-output", "negative-sampling-request", "answer")
	request.MaxModelOutputTokens = -1
	_, err = runner.StartRun(t.Context(), request)
	if !errors.Is(err, agent.ErrInvalidRequest) || len(provider.callsCopy()) != 0 {
		t.Fatalf("negative ceiling was admitted: %v", err)
	}
	if _, err = runtime.Load(t.Context(), request.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("negative ceiling created a durable Run: %v", err)
	}
}

func TestModelOutputLimitCannotBeChangedByPreparer(t *testing.T) {
	for _, ceiling := range []int64{0, 32, 65} {
		t.Run(strconv.FormatInt(ceiling, 10), func(t *testing.T) {
			provider := &invocationRecordingModel{}
			runner, err := agent.NewRunner(agent.Dependencies{
				Runtime: newModelInvocationRuntime(t, memory.NewStore()), Model: provider,
				PrepareModelRequest: func(_ context.Context, request model.Request) (model.Request, error) {
					request.MaxOutputTokens = ceiling
					return request, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := startRequest("preparer-sampling-ceiling", "preparer-sampling-request", "answer")
			request.MaxModelOutputTokens = 64
			_, err = runner.StartRun(t.Context(), request)
			if !errors.Is(err, agent.ErrInvalidRequest) || len(provider.callsCopy()) != 0 {
				t.Fatalf("preparer changed sampling ceiling: %v calls=%d", err, len(provider.callsCopy()))
			}
		})
	}
}

func TestModelOutputLimitZeroPreservesLegacyStateAndPendingCapIsChecked(t *testing.T) {
	for _, ceiling := range []int64{0, 64} {
		t.Run(strconv.FormatInt(ceiling, 10), func(t *testing.T) {
			base := memory.NewStore()
			faults := &modelInvocationFaultStore{Store: base, eventType: "agent.model_invocation.pending", afterCommit: true}
			runner := newModelInvocationRunner(t, faults, &invocationRecordingModel{})
			request := startRequest("sampling-state", "sampling-state-request", "answer")
			request.MaxModelOutputTokens = ceiling
			_, err := runner.StartRun(t.Context(), request)
			if !errors.Is(err, errInjectedModelInvocationCrash) {
				t.Fatal(err)
			}
			snapshot := mustLoadModelInvocationRun(t, newModelInvocationRuntime(t, base), request.ID)
			view := mustModelInvocationView(t, snapshot)
			if view.MaxModelOutputTokens != ceiling || view.ModelInvocations[0].Request.MaxOutputTokens != ceiling {
				t.Fatal("sampling ceiling was not persisted in both Run and model intent")
			}
			if ceiling == 0 {
				if strings.Contains(string(snapshot.State), `"maxModelOutputTokens"`) {
					t.Fatal("unspecified ceiling changed the legacy state encoding")
				}
				return
			}
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(snapshot.State, &fields); err != nil {
				t.Fatal(err)
			}
			// Preserve the valid frozen invocation and its hash while corrupting
			// only the aggregate ceiling. Recovery must not dispatch that intent.
			for _, raw := range []json.RawMessage{json.RawMessage(`-1`), json.RawMessage(`0`), json.RawMessage(`32`), json.RawMessage(`128`)} {
				fields["maxModelOutputTokens"] = raw
				snapshot.State, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = agent.ViewState(snapshot); !errors.Is(err, agent.ErrInvalidRequest) {
					t.Fatalf("persisted ceiling inconsistent with the frozen intent was accepted: %s, %v", raw, err)
				}
			}
		})
	}
}
