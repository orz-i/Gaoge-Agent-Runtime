package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestHostPolicyNormalizationIsTypedAndNotRepeatedOnRestart(t *testing.T) {
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{Store: base, eventType: "agent.model_invocation.pending", afterCommit: true}
	provider := &invocationRecordingModel{}
	count := 0
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: newModelInvocationRuntime(t, faults), Model: provider,
		NormalizeModelPolicy: func(_ context.Context, request model.Request) (model.PolicyBinding, error) {
			count++
			request.Model = "not-authorized"
			request.Messages[0].Content = model.TextParts("not-authorized")
			request.MaxOutputTokens = 999
			return model.PolicyBinding{ModelOptions: json.RawMessage(`{"temperature":0.3}`), BindingRef: "immutable-route"}, nil
		},
		PrepareModelRequest: func(_ context.Context, request model.Request) (model.Request, error) {
			if request.Model == "not-authorized" || request.MaxOutputTokens != 0 || model.TextValue(request.Messages[0].Content) == "not-authorized" {
				t.Fatal("normalizer mutated authority")
			}
			if request.BindingRef != "immutable-route" || string(request.ModelOptions) != `{"temperature":0.3}` {
				t.Fatal("policy was not frozen before preparer")
			}
			return request, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartRun(t.Context(), startRequest("normalized-pending", "normalized-request", "question"))
	if !errors.Is(err, errInjectedModelInvocationCrash) || count != 1 || len(provider.callsCopy()) != 0 {
		t.Fatalf("fault=%v normalizations=%d", err, count)
	}
	runtime := newModelInvocationRuntime(t, base)
	pending, err := runtime.Load(t.Context(), "normalized-pending")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider, Clock: modelInvocationClock{offset: 3 * time.Minute},
		NormalizeModelPolicy: func(context.Context, model.Request) (model.PolicyBinding, error) {
			t.Fatal("pending invocation renormalized")
			return model.PolicyBinding{}, nil
		},
		PrepareModelRequest: func(context.Context, model.Request) (model.Request, error) {
			t.Fatal("pending invocation re-prepared")
			return model.Request{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := resumed.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("resume %v", err)
	}
	calls := provider.callsCopy()
	if len(calls) != 1 || calls[0].BindingRef != "immutable-route" || string(calls[0].ModelOptions) != `{"temperature":0.3}` {
		t.Fatal("frozen policy lost on restart")
	}
}

func TestInvalidHostPolicyCannotCreateModelAttempt(t *testing.T) {
	for name, policy := range map[string]model.PolicyBinding{
		"invalid binding":   {BindingRef: " bad"},
		"duplicate options": {ModelOptions: json.RawMessage(`{"x":1,"x":2}`)},
		"nonobject options": {ModelOptions: json.RawMessage(`[]`)},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &invocationRecordingModel{}
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: newModelInvocationRuntime(t, memory.NewStore()), Model: provider,
				NormalizeModelPolicy: func(context.Context, model.Request) (model.PolicyBinding, error) { return policy, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.StartRun(t.Context(), startRequest("invalid-policy", "invalid-policy-request", "question"))
			if !errors.Is(err, agent.ErrInvalidRequest) || len(provider.callsCopy()) != 0 {
				t.Fatalf("invalid policy dispatched: %v", err)
			}
		})
	}
}
