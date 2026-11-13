package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestFinalAuditTerminalChildWithUnknownDispatchCannotBeReconciled(t *testing.T) {
	f := newLocalAgentProofFixture(t)
	hosted, err := agent.NewRunner(agent.Dependencies{
		Runtime: f.runtime, Model: localAgentProofModel{},
		PrepareModelRequest: func(_ context.Context, r model.Request) (model.Request, error) { return r, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.executor.original = hosted
	router, err := NewStaticEffectRouter(nil, EffectRoute{Class: EffectClassAgent, Executor: f.executor})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.effects = router
	if hosted.OwnsKernelRuntime(f.runtime) {
		t.Fatal("fixture incorrectly native")
	}
	f.child.Run.Status = kernel.RunStatusCancelled
	f.child.State = json.RawMessage(`{"budget":{"limits":{"maxLLMCalls":4,"maxToolCalls":4},"usage":{}},"toolDispatchID":"unknown-side-effect-execution","pendingCalls":[{"id":"call","toolKey":"evidence","name":"read","arguments":{}}]}`)
	if _, err := agent.ViewState(f.child); err != nil {
		t.Fatalf("must use valid Agent state: %v", err)
	}
	if f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, true) {
		t.Fatal("cancelled Agent with unacknowledged external Tool dispatch was treated as fully settled")
	}
}
