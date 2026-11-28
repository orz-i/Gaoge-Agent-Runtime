package harnesspostgres_test

import (
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestFailedRootRetryPreservesIndependentTerminalChildProjection(t *testing.T) {
	for _, backend := range generationBackends {
		t.Run(backend, func(t *testing.T) {
			m := &generationModel{}
			r, store, rt, native, request := newGenerationHarnessBackend(t, backend, m)
			started, err := r.StartAgentTurn(t.Context(), request)
			if err != nil || len(started.Subtasks) != 1 {
				t.Fatalf("delegated fixture: %+v %v", started.Subtasks, err)
			}
			root, _ := harness.TopLevelInvocation(started)
			child, err := rt.Load(t.Context(), started.Subtasks[0].RunID)
			if err != nil {
				t.Fatal(err)
			}
			completed, err := native.Resume(t.Context(), child.Run.ID, child.Run.Revision)
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("original child did not settle: %+v %v", completed.Run, err)
			}
			originalChild, err := store.GetInvocationByExecutionRefID(t.Context(), child.Run.ID)
			if err != nil || originalChild.Status != harness.InvocationRunning {
				t.Fatalf("independent child projection fixture: %+v %v", originalChild, err)
			}
			parent, err := rt.Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			repaired, err := native.ReconcileRootDeferredChildren(t.Context(), parent.Run.ID, parent.Run.Revision)
			if err != nil {
				t.Fatal(err)
			}
			m.calls, m.failFirst = 0, true
			failed, err := native.Resume(t.Context(), parent.Run.ID, repaired.Run.Revision)
			if failed.Run.Status != kernel.RunStatusFailed {
				t.Fatalf("known post-child refusal: %+v %v", failed.Run, err)
			}
			observed, err := r.Refresh(t.Context(), started.Turn.ID)
			if err != nil || observed.Turn.Status != harness.TurnFailed {
				t.Fatalf("original terminal proof: %+v %v", observed.Turn, err)
			}
			facts, err := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			m.calls, m.failFirst = 10, false
			retried, err := r.RetryInvocation(t.Context(), root.ID)
			if err != nil || retried.Turn.Status != harness.TurnCompleted || m.calls != 11 {
				t.Fatalf("full terminal receipt blocked Retry or replayed child Model: status=%s calls=%d err=%v", retried.Turn.Status, m.calls, err)
			}
			retained, err := store.GetInvocation(t.Context(), originalChild.ID)
			// Native recovery may project the independently completed child exactly
			// once. It must keep the original generation, input, and Run identity.
			if err != nil || retained.Status != harness.InvocationCompleted || retained.Revision != originalChild.Revision+1 ||
				retained.ExecutionRefID != originalChild.ExecutionRefID || retained.InputHash != originalChild.InputHash ||
				retained.Attempt != originalChild.Attempt || retained.ParentItemID != originalChild.ParentItemID {
				t.Fatalf("Retry changed independent child ownership or outcome: %+v %v", retained, err)
			}
			childAfter, err := rt.Load(t.Context(), child.Run.ID)
			if err != nil || kernel.ManagedDigest(childAfter) != kernel.ManagedDigest(completed) {
				t.Fatalf("Retry mutated original terminal child Run: %v", err)
			}
			for _, original := range facts {
				retained, err := store.GetItem(t.Context(), original.ID)
				if err != nil || retained.Seq != original.Seq || string(retained.Payload) != string(original.Payload) {
					t.Fatalf("Retry rewrote original effect/accounting fact: %s %v", original.ID, err)
				}
			}
			if _, err = r.Cancel(t.Context(), started.Turn.ID, "observe completed replacement"); err != nil {
				t.Fatalf("accepted historical child blocked post-Retry cleanup: %v", err)
			}
		})
	}
}
