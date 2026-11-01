package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// Independent audit of product-visible read: remote delegated child must
// not inherit a colliding Harness-local foreign Run's terminal state.
func TestAuditPR82ForeignLocalRunCannotProjectRemoteSubtaskOnLoadOrRefresh(t *testing.T) {
	f, child := pr82StartedRemoteHandoff(t, true)
	foreign, err := f.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: child.ExecutionRefID, Kind: "team",
		Actor:     kernel.ActorRef{TenantID: "foreign", ActorID: "other"},
		Thread:    kernel.ThreadRef{Kind: "conversation", ID: "other"},
		RequestID: "unrelated-request", Goal: "foreign-result", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.runtime.Cancel(t.Context(), foreign.Run.ID, foreign.Run.Revision, "foreign cancellation"); err != nil {
		t.Fatal(err)
	}
	read, err := f.runner.Load(t.Context(), f.turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Subtasks) != 1 {
		t.Fatalf("expected one remote Handoff subtask: %+v", read.Subtasks)
	}
	// Without authoritative remote observation, the frozen Item may still
	// describe the child as queued; it must never inherit local cancellation.
	if read.Subtasks[0].Status == "cancelled" || read.Subtasks[0].Goal != childGoalForAudit(f) {
		t.Fatalf("P1: read projected unrelated local Kernel Run into remote Handoff: subtask=%+v", read.Subtasks[0])
	}
}
func childGoalForAudit(f *pr82HandoffFixture) string { return f.delegation().Goal }

// Remote child still active: a foreign terminal Run with the same ID must
// not let CancelSubtask report success without any owner cancellation.
func TestAuditPR82CancelSubtaskCannotTrustForeignLocalTerminalRun(t *testing.T) {
	f, child := pr82StartedRemoteHandoff(t, true)
	remote, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = remote.Create(t.Context(), kernel.CreateRequest{
		ID: child.ExecutionRefID, Kind: agent.RunKind,
		Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread,
		RequestID: f.parentRun.Run.ID + ":" + f.delegation().ID,
		Goal:      f.delegation().Goal, State: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	local, err := f.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: child.ExecutionRefID, Kind: "team",
		Actor:     kernel.ActorRef{TenantID: "other", ActorID: "other"},
		Thread:    kernel.ThreadRef{Kind: "conversation", ID: "another-thread"},
		RequestID: "foreign", Goal: "foreign", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.runtime.Cancel(t.Context(), local.Run.ID, local.Run.Revision, "foreign cancellation"); err != nil {
		t.Fatal(err)
	}
	observed, cancelErr := f.runner.CancelSubtask(t.Context(), f.turn.ID, f.delegation().ID, "stop actual remote child")
	remoteAfter, err := remote.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.store.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	// Existing remote-only contract does not support native cancellation;
	// it must at least fail closed rather than report successful cleanup.
	if cancelErr == nil || remoteAfter.Run.Status != kernel.RunStatusRunning || current.Status != child.Status || !ActiveCancellationFence(items) {
		t.Fatalf("P1: CancelSubtask silently skipped remote execution: status=%s remote=%s child=%s intent=%v cancelErr=%v", observed.Subtasks[0].Status, remoteAfter.Run.Status, current.Status, ActiveCancellationFence(items), cancelErr)
	}
}

type auditRootSnapshotCanceller struct{ snapshot kernel.Snapshot }

func (c auditRootSnapshotCanceller) Cancel(context.Context, string, uint64, string) (kernel.Snapshot, error) {
	return c.snapshot, nil
}

func TestAuditPR82CustomRootCancelNonterminalReceiptMustNotCloseIntent(t *testing.T) {
	f := newPR82HandoffFixture(t)
	f.runner.cancellation = auditRootSnapshotCanceller{snapshot: f.parentRun}
	observed, cancelErr := f.runner.Cancel(t.Context(), f.turn.ID, "do not settle a running receipt")
	persisted, err := f.store.GetTurn(t.Context(), f.turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.runtime.Load(t.Context(), f.parentRun.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if cancelErr == nil || !ActiveCancellationFence(items) {
		t.Fatalf("P1: custom Cancel returned nonterminal, but intent settled with nil error: returned=%s persisted=%s kernel=%s intent=%v err=%v", observed.Turn.Status, persisted.Status, run.Run.Status, ActiveCancellationFence(items), cancelErr)
	}
}

func TestAuditPR82CustomRootCancelForeignTerminalCannotSettle(t *testing.T) {
	f := newPR82HandoffFixture(t)
	foreign, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := foreign.Create(t.Context(), kernel.CreateRequest{
		ID: f.parentRun.Run.ID, Kind: "team",
		Actor:     kernel.ActorRef{TenantID: "foreign", ActorID: "wrong"},
		Thread:    kernel.ThreadRef{Kind: "conversation", ID: "other"},
		RequestID: "fake", Goal: "unrelated", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := foreign.Cancel(t.Context(), result.Run.ID, result.Run.Revision, "different task")
	if err != nil {
		t.Fatal(err)
	}
	f.runner.cancellation = auditRootSnapshotCanceller{snapshot: cancelled}
	observed, cancelErr := f.runner.Cancel(t.Context(), f.turn.ID, "original root")
	real, err := f.runtime.Load(t.Context(), f.parentRun.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := f.store.GetTurn(t.Context(), f.turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelErr == nil || persisted.Status == TurnCancelled || real.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("P1: custom Cancel foreign same-ID terminal projected: returned=%s persisted=%s real=%s err=%v", observed.Turn.Status, persisted.Status, real.Run.Status, cancelErr)
	}
}

func TestAuditPR82CancelDuringAlreadyFailedNativeRootMustNotStrandTurn(t *testing.T) {
	f := newPR82HandoffFixture(t)
	f.runner.cancellation = f.runtime
	original := f.parentRun
	mutation := kernel.Mutation{Status: kernel.RunStatusFailed, State: original.State, ErrorCode: "run.failed", ErrorDetail: "kernel root failed before Harness projection"}
	updated, err := f.runtime.Apply(t.Context(), original.Run.ID, original.Run.Revision, mutation)
	if err != nil || updated.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("fixture failure: run=%s err=%v", updated.Run.Status, err)
	}
	_, firstErr := f.runner.Cancel(t.Context(), f.turn.ID, "cancel while already terminal")
	read, secondErr := f.runner.Refresh(t.Context(), f.turn.ID)
	turn, err := f.store.GetTurn(t.Context(), f.turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if ActiveCancellationFence(items) || turn.Status == TurnRunning || secondErr != nil {
		t.Fatalf("P1: terminal native root stranded by Cancel's own fence: firstErr=%v refreshErr=%v read=%s persisted=%s activeCancelFence=%v", firstErr, secondErr, read.Turn.Status, turn.Status, ActiveCancellationFence(items))
	}
}

func TestAuditPR82FailedRootNoDescendantsCancelCannotSelfDeadlock(t *testing.T) {
	f := newPR82HandoffFixture(t)
	// Strip the fixture's extraneous staged delegation to construct a
	// pure native root with no child/remote runs or pending physical effects.
	f.store.mu.Lock()
	delete(f.store.itemIDs, f.startedItemID)
	f.store.items[f.turn.ID] = nil
	f.store.mu.Unlock()
	f.runner.cancellation = f.runtime
	root := f.parentRun
	if _, err := f.runtime.Apply(t.Context(), root.Run.ID, root.Run.Revision,
		kernel.Mutation{Status: kernel.RunStatusFailed, State: root.State, ErrorCode: "model.failure", ErrorDetail: "normal failure"}); err != nil {
		t.Fatal(err)
	}
	_, cancelErr := f.runner.Cancel(t.Context(), f.turn.ID, "cancel already-failed direct root")
	_, refreshErr := f.runner.Refresh(t.Context(), f.turn.ID)
	turn, err := f.store.GetTurn(t.Context(), f.turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Status == TurnRunning && ActiveCancellationFence(items) && errors.Is(cancelErr, ErrConflict) && errors.Is(refreshErr, ErrConflict) {
		t.Fatalf("P1: no child effect, yet failed native root permanently fenced: turn=%s active=%v Cancel=%v Refresh=%v", turn.Status, ActiveCancellationFence(items), cancelErr, refreshErr)
	}
}

// A previously admitted cancellation may have observed a failed root before
// the remote child was reconciled. Retrying the SAME intent must recheck that
// child rather than remain blocked simply because the root status is Failed.
func TestPR82FailedRootRemoteCleanupRetryStillOwned(t *testing.T) {
	f, child := pr82StartedRemoteHandoff(t, true)
	root := f.parentRun
	if _, err := f.runtime.Apply(t.Context(), root.Run.ID, root.Run.Revision,
		kernel.Mutation{Status: kernel.RunStatusFailed, State: root.State,
			ErrorCode: "model.failure", ErrorDetail: "parent failed while remote child executes"}); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		_, err := f.runner.Cancel(t.Context(), f.turn.ID, "retry external cleanup")
		if !errors.Is(err, ErrHandoffCancellationUnverified) {
			t.Fatalf("attempt %d lost the remote-cancel failure: %v", attempt, err)
		}
		items, loadErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
		if loadErr != nil || !ActiveCancellationFence(items) {
			t.Fatalf("attempt %d lost unresolved intent: %v", attempt, loadErr)
		}
		kept, loadErr := f.store.GetInvocation(t.Context(), child.ID)
		if loadErr != nil || kept.Status != child.Status {
			t.Fatalf("attempt %d projected unverified child: %+v %v", attempt, kept, loadErr)
		}
	}
}

type pr82LocalCustomCanceller struct{ runtime *kernel.Runtime }

func (c pr82LocalCustomCanceller) Cancel(ctx context.Context, runID string, revision uint64, reason string) (kernel.Snapshot, error) {
	return c.runtime.Cancel(ctx, runID, revision, reason)
}

// Valid custom cancellers which actually apply Kernel control retain their
// original success path after receipt identity and terminal verification.
func TestPR82CustomLocalCancellationSucceedsWithKernelProof(t *testing.T) {
	f := newPR82HandoffFixture(t)
	f.store.mu.Lock()
	delete(f.store.itemIDs, f.startedItemID)
	f.store.items[f.turn.ID] = nil
	f.store.mu.Unlock()
	f.runner.cancellation = pr82LocalCustomCanceller{runtime: f.runtime}
	result, err := f.runner.Cancel(t.Context(), f.turn.ID, "cancel real root")
	if err != nil || result.Turn.Status != TurnCancelled {
		t.Fatalf("valid local custom cancellation rejected: %s %v", result.Turn.Status, err)
	}
	observed, err := f.runtime.Load(t.Context(), f.parentRun.Run.ID)
	if err != nil || observed.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("physical root did not cancel: %s %v", observed.Run.Status, err)
	}
}
