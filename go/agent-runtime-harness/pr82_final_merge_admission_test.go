package harness

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// The root Agent is no exception to physical Kernel ownership. Two Kernels
// can store identically named Runs; cancellation must not operate the foreign
// Kernel or project its cancelled state into this Harness Turn.
func TestPR82RootCancelRejectsForeignNativeKernelCanceller(t *testing.T) {
	f := newPR82HandoffFixture(t)
	foreign, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	original := f.parentRun.Run
	if _, err := foreign.Create(t.Context(), kernel.CreateRequest{
		ID: original.ID, Kind: original.Kind, Actor: original.Actor, Thread: original.Thread,
		RequestID: original.RequestID, Goal: original.Goal, State: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	f.runner.cancellation = foreign
	_, cancelErr := f.runner.Cancel(t.Context(), f.turn.ID, "wrong Kernel must not be used")
	local, err := f.runtime.Load(t.Context(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := foreign.Load(t.Context(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := f.store.GetTurn(t.Context(), f.turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(cancelErr, ErrConflict) ||
		elsewhere.Run.Status != kernel.RunStatusRunning || local.Run.Status != kernel.RunStatusRunning ||
		turn.Status == TurnCancelled {
		t.Fatalf("foreign native cancellation crossed ownership: local=%s foreign=%s turn=%s err=%v",
			local.Run.Status, elsewhere.Run.Status, turn.Status, cancelErr)
	}
}

// A successful Kernel-root cancel is not proof of remote descendant cleanup.
// Refresh is an observer, never a second cancellation completion authority.
func TestPR82RefreshCannotSettleUnverifiedRemoteHandoffCancellation(t *testing.T) {
	f, child := pr82StartedRemoteHandoff(t, true)
	_, cancelErr := f.runner.Cancel(t.Context(), f.turn.ID, "remote cancellation unverified")
	if !errors.Is(cancelErr, ErrHandoffCancellationUnverified) {
		t.Fatalf("cancellation fixture: %v", cancelErr)
	}
	_, refreshErr := f.runner.Refresh(t.Context(), f.turn.ID)
	turn, err := f.store.GetTurn(t.Context(), f.turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := f.store.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(refreshErr, ErrConflict) || turn.Status == TurnCancelled ||
		!ActiveCancellationFence(items) || terminalInvocationStatus(invocation.Status) {
		t.Fatalf("remote unknown cancellation projected terminal: turn=%s child=%s fence=%v refresh=%v",
			turn.Status, invocation.Status, ActiveCancellationFence(items), refreshErr)
	}
}

// RefreshInvocation cannot bind a remote Handoff to a coincidental local Run
// even if the local Run has the same child ID. It must not mutate child state.
func TestPR82RefreshHandoffRejectsForeignLocalSnapshot(t *testing.T) {
	f, child := pr82StartedRemoteHandoff(t, true)
	unrelated, err := f.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: child.ExecutionRefID, Kind: "team",
		Actor:     kernel.ActorRef{TenantID: "foreign", ActorID: "stranger"},
		Thread:    kernel.ThreadRef{Kind: "conversation", ID: "foreign-thread"},
		RequestID: "foreign-task", Goal: "foreign goal", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.runtime.Cancel(t.Context(), unrelated.Run.ID, unrelated.Run.Revision, "foreign run finished"); err != nil {
		t.Fatal(err)
	}
	_, refreshErr := f.runner.RefreshInvocation(t.Context(), child.ID)
	persisted, err := f.store.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(refreshErr, ErrHandoffCancellationUnverified) ||
		persisted.Status != child.Status || persisted.Revision != child.Revision {
		t.Fatalf("foreign Handoff Run projected: before=%s after=%s refresh=%v",
			child.Status, persisted.Status, refreshErr)
	}
}

// Fixed, exact same-Kernel Handoff children keep legitimate local observation;
// this hard cut must not disable the route-owned read path.
func TestPR82RefreshHandoffAllowsVerifiedFixedLocalChild(t *testing.T) {
	f := newPR82HandoffFixture(t)
	coordinator, err := handoff.New(&pr82BoundKernelChild{runtime: f.runtime})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.handoffs = coordinator
	d := f.delegation()
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, d, f.startedItemID); !errors.Is(err, handoff.ErrChildPending) {
		t.Fatalf("physical Handoff start: %v", err)
	}
	child, err := f.store.GetInvocationByExecutionRefID(t.Context(), d.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.RefreshInvocation(t.Context(), child.ID); err != nil {
		t.Fatalf("legitimate local Handoff Refresh rejected: %v", err)
	}
	updated, err := f.store.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != InvocationRunning {
		t.Fatalf("verified local child not observed: %s", updated.Status)
	}
}
