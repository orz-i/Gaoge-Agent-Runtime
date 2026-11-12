package agent

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestIssue107ManualGrantRecoveryOnlyAfterPhysicalStateSettles(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "original-parent", Kind: "workflow", Actor: actor, Thread: thread, Goal: "parent", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{Runtime: rt, Model: issue107FrozenStartTestModel{}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := runner.PrepareRun(t.Context(), StartRequest{
		ID: "original-child", RequestID: "effect", ParentRunID: parent.Run.ID,
		OriginFingerprint: FrozenOriginFingerprint([]byte(`{"isolatedContext":true}`)),
		Actor:             actor, Thread: thread, Goal: "execute", Model: "model",
	})
	if err != nil {
		t.Fatal(err)
	}
	original, err := rt.BeginChildExecution(t.Context(), child.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "unknown external work"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("lost Grant did not block parent terminal: %v", err)
	}
	state, err := decodeState(child.State)
	if err != nil {
		t.Fatal(err)
	}
	state.ToolDispatchID = "unsettled-tool"
	pending, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := rt.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: pending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.ReconcileChildPhysicalGrant(t.Context(), child.Run.ID); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unknown external dispatch was retired: %v", err)
	}
	if got, lookupErr := rt.LookupChildExecution(t.Context(), child.Run.ID); lookupErr != nil || got != original {
		t.Fatalf("unsettled original Grant lost: %+v %v", got, lookupErr)
	}
	// This is a synthetic trusted reconciler writing a completed Tool receipt
	// for the test. A production Host must obtain authoritative external proof.
	state.ToolDispatchID = ""
	settled, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.Apply(t.Context(), child.Run.ID, changed.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: settled,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The original physical callback has now returned; only the trusted
	// Agent can issue the exact revision-bound settlement marker. A mere
	// clean-looking state could not do so while the callback was alive.
	final, loadErr := rt.Load(t.Context(), child.Run.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	proof := featurecontrol.Seal(final.Run.ID, final.Run.Revision, "agent_child_physical_settled", original.ID, final.State, final.Checkpoint)
	if markErr := rt.MarkChildExecutionSettled(t.Context(), original, proof); markErr != nil {
		t.Fatal(markErr)
	}
	if err = runner.ReconcileChildPhysicalGrant(t.Context(), child.Run.ID); err != nil {
		t.Fatalf("known-satisfied physical Grant could not recover: %v", err)
	}
	if _, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "after trusted receipt"); err != nil {
		t.Fatal(err)
	}
	if err = runner.ReconcileChildPhysicalGrant(t.Context(), child.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("retired Grant replay accepted: %v", err)
	}
}
