package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestContextProjectionIsSealedBeforeModelIntentAndNotRepeatedOnRestart(t *testing.T) {
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{Store: base, eventType: "agent.model_invocation.pending", afterCommit: true}
	provider := &invocationRecordingModel{}
	var prepared int
	first, err := agent.NewRunner(agent.Dependencies{
		Runtime: newModelInvocationRuntime(t, faults), Model: provider,
		PrepareModelRequest: func(_ context.Context, request model.Request) (model.Request, error) {
			prepared++
			request.Messages = append([]model.Message{{Role: model.RoleSystem, Content: "immutable prior conversation"}}, request.Messages...)
			return request, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.StartRun(t.Context(), startRequest("projection-pending", "projection-request", "new question"))
	if !errors.Is(err, errInjectedModelInvocationCrash) || prepared != 1 || len(provider.callsCopy()) != 0 {
		t.Fatalf("intent commit fault = %v, prepared=%d, provider=%d", err, prepared, len(provider.callsCopy()))
	}
	runtime := newModelInvocationRuntime(t, base)
	pending, err := runtime.Load(t.Context(), "projection-pending")
	if err != nil {
		t.Fatal(err)
	}
	before, err := agent.ViewState(pending)
	if err != nil || len(before.ModelInvocations) != 1 || before.ModelInvocations[0].Status != agent.ModelInvocationPending {
		t.Fatalf("missing frozen pending intent: %+v, err=%v", before, err)
	}
	if got := before.ModelInvocations[0].Request.Messages; len(got) != 2 || got[0].Content != "immutable prior conversation" {
		t.Fatalf("preparation missing from durable intent: %+v", got)
	}
	second, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider, Clock: modelInvocationClock{offset: 3 * time.Minute},
		PrepareModelRequest: func(context.Context, model.Request) (model.Request, error) {
			t.Fatal("frozen pending invocation must not be re-projected")
			return model.Request{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := second.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("resume: %v, status=%s", err, completed.Run.Status)
	}
	calls := provider.callsCopy()
	if len(calls) != 1 || len(calls[0].Messages) != 2 || calls[0].Messages[0].Content != "immutable prior conversation" {
		t.Fatalf("resumed request changed: %+v", calls)
	}
}

func TestContextPreparerCannotChangeFrozenExecutionAuthority(t *testing.T) {
	for _, change := range []struct {
		name    string
		prepare func(*model.Request)
	}{
		{"model", func(req *model.Request) { req.Model = "other" }},
		{"run", func(req *model.Request) { req.RunID = "other" }},
		{"tool_limit", func(req *model.Request) { v := 7; req.HostedToolCallLimit = &v }},
		{"empty_messages", func(req *model.Request) { req.Messages = nil }},
	} {
		t.Run(change.name, func(t *testing.T) {
			provider := &invocationRecordingModel{}
			runner, err := agent.NewRunner(agent.Dependencies{
				Runtime: newModelInvocationRuntime(t, memory.NewStore()), Model: provider,
				PrepareModelRequest: func(_ context.Context, req model.Request) (model.Request, error) {
					change.prepare(&req)
					return req, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = runner.StartRun(t.Context(), startRequest("projection-invalid-"+change.name, "req-"+change.name, "question"))
			if len(provider.callsCopy()) != 0 {
				t.Fatal("provider dispatched after invalid context projection")
			}
		})
	}
}

func TestClassifiedPreparationFailureIsDurableWithoutModelIntent(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		t.Run(map[bool]string{false: "permanent", true: "retryable"}[retryable], func(t *testing.T) {
			provider := &invocationRecordingModel{}
			runtime := newModelInvocationRuntime(t, memory.NewStore())
			cause := errors.New("input exceeds the model context window")
			failure := cause
			if retryable {
				failure = model.NewRetryableError(failure)
			}
			failure = model.NewClassifiedError(failure, "context.input_budget_exceeded")
			runner, err := agent.NewRunner(agent.Dependencies{
				Runtime: runtime, Model: provider,
				PrepareModelRequest: func(context.Context, model.Request) (model.Request, error) {
					return model.Request{}, failure
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := runner.StartRun(t.Context(), startRequest("prepare-failure", "prepare-request", "question"))
			if !errors.Is(err, cause) || len(provider.callsCopy()) != 0 {
				t.Fatalf("preparation error=%v physical calls=%d", err, len(provider.callsCopy()))
			}
			reloaded, err := runtime.Load(t.Context(), snapshot.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			state, err := agent.ViewState(reloaded)
			if err != nil || len(state.ModelInvocations) != 0 || state.Budget.Usage.LLMCalls != 0 {
				t.Fatalf("preparation spent a model attempt: state=%+v err=%v", state, err)
			}
			if retryable {
				if reloaded.Run.Status != kernel.RunStatusRunning || reloaded.Run.ErrorCode != "" {
					t.Fatalf("retryable preparation terminated: %+v", reloaded.Run)
				}
			} else if reloaded.Run.Status != kernel.RunStatusFailed || reloaded.Run.ErrorCode != "context.input_budget_exceeded" {
				t.Fatalf("permanent preparation failure was not committed: %+v", reloaded.Run)
			}
		})
	}
}
