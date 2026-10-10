package harness_test

import (
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

func TestHarnessAutomaticAllowanceDoesNotRewriteContextCheckpoint(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	correction := &autoCorrectionPolicy{reject: 1}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: directModel{},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 6},
		ModelMiddleware:    []plugin.ModelMiddleware{harness.NewContextWindowMiddleware()},
		CompletionPolicies: []agent.CompletionPolicy{correction},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: store, Clock: contextHarnessClock{},
		Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.Config.Instructions = "preserve context identity"
	request.Context = contextSeed("workspace instructions",
		contextEntry("source-one", "old-turn", model.RoleUser, "older question", false),
		contextEntry("source-two", "old-turn", model.RoleAssistant, "earlier answer", false),
		contextEntry("source-three", "current-turn", model.RoleUser, "continue now", true),
	)
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	request.AutoAllowance = &agent.AutoAllowancePolicy{
		ModelStep: 1, MaxModelRenewals: 1, MaxNoProgressWindow: 1,
	}
	snapshot, err := host.Start(t.Context(), request)
	if err != nil || snapshot.Turn.Status != harness.TurnCompleted || correction.checks != 2 {
		t.Fatalf("context-aware autonomous renewal: %#v err=%v checks=%d", snapshot.Turn, err, correction.checks)
	}
	if snapshot.Turn.ContextCheckpointID == "" ||
		snapshot.Turn.ContextRef.Generation != 1 ||
		snapshot.Turn.ContextCheckpointID != snapshot.Turn.ContextRef.ID {
		t.Fatalf("checkpoint rewritten by budget grant: %#v", snapshot.Turn.ContextRef)
	}
	stored, err := store.GetContextCheckpoint(t.Context(), snapshot.Turn.ContextCheckpointID)
	if err != nil || stored.CoveredThroughSourceID != "source-three" ||
		stored.ScopeID != snapshot.Turn.SessionID {
		t.Fatalf("context source was lost during automatic renewal: %#v err=%v", stored, err)
	}
	refreshed, err := host.Refresh(t.Context(), snapshot.Turn.ID)
	if err != nil || refreshed.Turn.ContextCheckpointID != snapshot.Turn.ContextCheckpointID ||
		refreshed.Turn.ContextRef != snapshot.Turn.ContextRef {
		t.Fatalf("context checkpoint changed after refresh: before=%#v after=%#v err=%v",
			snapshot.Turn.ContextRef, refreshed.Turn.ContextRef, err)
	}
}
