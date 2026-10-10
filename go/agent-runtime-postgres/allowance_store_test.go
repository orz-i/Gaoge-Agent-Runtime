package postgres

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestKernelStoreRetainsBudgetPauseAcrossReopenAndCASGrant(t *testing.T) {
	t.Parallel()
	db := openKernelStoreTestDB(t)
	first, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	started, err := first.Create(t.Context(), kernel.CreateRequest{
		ID: "durable-budget-run", Kind: kernel.RunKind("agent"),
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "operator"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "continue work", State: json.RawMessage(`{"allowance":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := first.Apply(t.Context(), started.Run.ID, started.Run.Revision, kernel.Mutation{
		Status:    kernel.RunStatusPausedBudget,
		State:     json.RawMessage(`{"allowance":1,"pendingTool":"tool-b"}`),
		ErrorCode: "agent.tool_allowance_exhausted",
		Events:    []kernel.EventDraft{{Type: "agent.budget_paused"}},
	})
	if err != nil || paused.Run.EndedAt != nil {
		t.Fatalf("pause=%#v err=%v", paused.Run, err)
	}
	restored, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restored.Load(t.Context(), started.Run.ID)
	if err != nil || loaded.Run.Status != kernel.RunStatusPausedBudget ||
		string(loaded.State) != string(paused.State) || loaded.Run.Revision != paused.Run.Revision {
		t.Fatalf("reopen lost pause or pending tool: %#v state=%s err=%v", loaded.Run, loaded.State, err)
	}
	resumed, err := restored.Apply(t.Context(), loaded.Run.ID, loaded.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning,
		State:  json.RawMessage(`{"allowance":2,"pendingTool":"tool-b"}`),
		Events: []kernel.EventDraft{{Type: "agent.allowance_granted", Wakeup: true}},
	})
	if err != nil || resumed.Run.EndedAt != nil || resumed.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("resume=%#v err=%v", resumed.Run, err)
	}
	if _, err = first.Apply(t.Context(), paused.Run.ID, paused.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: paused.State,
	}); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("stale resumed Run: %v", err)
	}
	replay, err := restored.Load(t.Context(), started.Run.ID)
	if err != nil || replay.Run.Revision != resumed.Run.Revision ||
		string(replay.State) != string(resumed.State) {
		t.Fatalf("reopen lost granted state %#v state=%s err=%v", replay.Run, replay.State, err)
	}
}
