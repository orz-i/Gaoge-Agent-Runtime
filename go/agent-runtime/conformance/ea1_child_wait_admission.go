package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// RunEA1ChildWaitAdmissionSuite covers only WC-W-P0. ResolveWait has no
// caller Actor/route inputs; this suite does not certify Host authentication.
func RunEA1ChildWaitAdmissionSuite(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	for _, race := range []string{"checkpoint", "ancestor-terminal"} {
		t.Run(race, func(t *testing.T) {
			backing, peer := factory(t)
			store := &ea1P0BeginInterleave{Store: backing, ChildExecutionStore: backing.(kernel.ChildExecutionStore)}
			rt, err := kernel.New(kernel.Dependencies{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			peerRT, err := kernel.New(kernel.Dependencies{Store: peer})
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}
			parent, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "original parent", State: json.RawMessage("{}")})
			if err != nil {
				t.Fatal(err)
			}
			effect := &ea1WaitCounter{}
			runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: effect, DeferResumption: true})
			if err != nil {
				t.Fatal(err)
			}
			peerRunner, err := workflow.NewRunner(workflow.Dependencies{Runtime: peerRT, Effects: effect, DeferResumption: true})
			if err != nil {
				t.Fatal(err)
			}
			definition, err := workflow.CompileDefinition(ea1UnknownWaitDraft())
			if err != nil {
				t.Fatal(err)
			}
			original, err := runner.StartRun(t.Context(), workflow.StartRequest{ID: "original-child", ParentRunID: parent.Run.ID, Actor: actor, Thread: thread, Goal: "child wait admission", Definition: definition, Input: json.RawMessage("{}")})
			if err != nil || original.Run.Status != kernel.RunStatusWaitingInput || len(effect.requests) != 0 {
				t.Fatalf("zero-dispatch child Wait setup: %v", err)
			}
			assertPreflight := func() {
				ea1CompensationSnapshotUnchanged(t, rt, original)
				ea1CompensationSnapshotUnchanged(t, rt, parent)
				if len(effect.requests) != 0 {
					t.Fatal("preflight physically dispatched")
				}
				if _, e := rt.LookupChildExecution(t.Context(), original.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("preflight stranded Grant: %v", e)
				}
			}
			for _, revision := range []uint64{0, original.Run.Revision - 1, original.Run.Revision + 1} {
				if _, e := runner.ResolveWait(t.Context(), original.Run.ID, revision, json.RawMessage("{}")); !errors.Is(e, kernel.ErrConflict) {
					t.Fatalf("invalid revision accepted: %v", e)
				}
				assertPreflight()
			}
			if _, e := runner.ResolveWait(t.Context(), original.Run.ID, original.Run.Revision, json.RawMessage("invalid")); !errors.Is(e, workflow.ErrInvalidExecution) {
				t.Fatalf("invalid response accepted: %v", e)
			}
			assertPreflight()
			if _, e := runner.ResolveWait(t.Context(), "missing", original.Run.Revision, json.RawMessage("{}")); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("missing original adopted: %v", e)
			}
			assertPreflight()
			invalid, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "no-wait-origin", ParentRunID: parent.Run.ID, Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "no pending Wait", State: json.RawMessage("{}")})
			if err != nil {
				t.Fatal(err)
			}
			if _, e := runner.ResolveWait(t.Context(), invalid.Run.ID, invalid.Run.Revision, json.RawMessage("{}")); !errors.Is(e, workflow.ErrWaitPending) {
				t.Fatalf("origin without pending Wait accepted: %v", e)
			}
			ea1CompensationSnapshotUnchanged(t, rt, invalid)
			assertPreflight()
			var winner kernel.Snapshot
			store.before = func(ctx context.Context) error {
				var e error
				if race == "checkpoint" {
					winner, e = peerRunner.ResolveWait(ctx, original.Run.ID, original.Run.Revision, json.RawMessage(`{"approved":true}`))
				} else {
					winner, e = peerRT.Cancel(ctx, parent.Run.ID, parent.Run.Revision, "ancestor wins admission")
				}
				return e
			}
			if _, err = runner.ResolveWait(t.Context(), original.Run.ID, original.Run.Revision, json.RawMessage(`{"approved":false}`)); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("losing admission error: %v", err)
			}
			ea1CompensationSnapshotUnchanged(t, rt, winner)
			if len(effect.requests) != 0 {
				t.Fatal("losing admission physically dispatched")
			}
			if _, e := rt.LookupChildExecution(t.Context(), original.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("loser stranded Grant: %v", e)
			}
			if race == "ancestor-terminal" {
				ea1CompensationSnapshotUnchanged(t, rt, original)
				if _, e := runner.ResolveWait(t.Context(), original.Run.ID, original.Run.Revision, json.RawMessage("{}")); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("terminal ancestry bypass: %v", e)
				}
				ea1CompensationSnapshotUnchanged(t, rt, original)
				ea1CompensationSnapshotUnchanged(t, rt, winner)
				if len(effect.requests) != 0 {
					t.Fatal("child auto-promoted and dispatched")
				}
				if _, e := rt.LookupChildExecution(t.Context(), original.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("duplicate loser stranded Grant: %v", e)
				}
			} else {
				ea1CompensationSnapshotUnchanged(t, rt, parent)
				if _, e := runner.ResolveWait(t.Context(), original.Run.ID, winner.Run.Revision, json.RawMessage("{}")); !errors.Is(e, workflow.ErrWaitPending) {
					t.Fatalf("duplicate checkpoint accepted: %v", e)
				}
				ea1CompensationSnapshotUnchanged(t, rt, winner)
				ea1CompensationSnapshotUnchanged(t, rt, parent)
				finished, e := runner.Resume(t.Context(), original.Run.ID, winner.Run.Revision)
				if e != nil || finished.Run.Status != kernel.RunStatusCompleted || len(effect.requests) != 1 {
					t.Fatalf("original winning continuation failed: %v", e)
				}
				assertEA1WaitReceipt(t, finished, original, definition, effect.requests)
				if finished.Run.ParentRunID != parent.Run.ID || finished.Run.Actor != actor || finished.Run.Thread != thread {
					t.Fatal("winning continuation changed ancestry")
				}
				view, e := workflow.ViewState(finished)
				if e != nil || len(view.Waits) != 1 || !bytes.Equal(view.Waits[0].Response, json.RawMessage(`{"approved":true}`)) {
					t.Fatal("loser replaced original response")
				}
				ea1CompensationSnapshotUnchanged(t, rt, parent)
				if _, e = rt.LookupChildExecution(t.Context(), original.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("winner did not retire Grant: %v", e)
				}
			}
		})
	}
}
