package agent_test

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// A poisoned current recovery obligation must prevent Direct Agent Model/Tool
// dispatch even after an existing execution lease is forcefully revoked.
// Repair is trusted-only and does not revive the previous owner.
func TestNativeDirectAgentCannotBypassRunQuarantine(t *testing.T) {
	base := memory.NewStore()
	plain, err := kernel.New(kernel.Dependencies{Store: base})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &stealerOnCreateStore{Store: base, ExecutionStore: base}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &nativeLeaseFixtureModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: modelClient,
		RunExecution: &agent.RunExecution{OwnerID: "direct-native", LeaseDuration: 3 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "native-verified-quarantine", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "quarantine before external work",
	})
	if !errors.Is(err, kernel.ErrExecutionConflict) || modelClient.calls != 0 {
		t.Fatalf("fixture unexpectedly executed Model: run=%+v err=%v calls=%d", started.Run, err, modelClient.calls)
	}
	oldOwner, err := plain.WithExecution(wrapped.stolen)
	if err != nil {
		t.Fatal(err)
	}
	block := kernel.ExecutionQuarantine{
		RunID: started.Run.ID, Revision: started.Run.Revision,
		Reason: "unverified_current_work",
	}
	if err = plain.QuarantineExecution(t.Context(), block); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Resume(t.Context(), started.Run.ID, started.Run.Revision); !errors.Is(err, kernel.ErrExecutionQuarantined) || modelClient.calls != 0 {
		t.Fatalf("Direct bypassed blocked Run: err=%v model=%d", err, modelClient.calls)
	}
	if err = oldOwner.RequireExecution(t.Context(), started.Run.ID); !errors.Is(err, kernel.ErrExecutionQuarantined) {
		t.Fatalf("blocked pre-dispatch owner stayed current: %v", err)
	}
	// The host has independently established this never-dispatched operation
	// needs no Provider receipt. Only now is the exact block repaired.
	if err = plain.RepairExecutionQuarantine(t.Context(), block); err != nil {
		t.Fatal(err)
	}
	if err = oldOwner.RequireExecution(t.Context(), started.Run.ID); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("quarantine repair revived old owner: %v", err)
	}
	final, err := runner.Resume(t.Context(), started.Run.ID, started.Run.Revision)
	if err != nil || final.Run.Status != kernel.RunStatusCompleted || modelClient.calls != 1 {
		t.Fatalf("verified repair failed native execution: run=%+v err=%v calls=%d",
			final.Run, err, modelClient.calls)
	}
}
