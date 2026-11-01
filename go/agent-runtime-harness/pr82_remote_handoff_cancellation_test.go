package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A routed child can be running in an independently composed ChildRunner.
// The Harness-local Kernel returning ErrNotFound is NOT a cancellation receipt.
func pr82StartedRemoteHandoff(t *testing.T, role bool) (*pr82HandoffFixture, Invocation) {
	t.Helper()
	f := newPR82HandoffFixture(t)
	delegation := f.delegation()
	if !role {
		delegation.RoleID = ""
	}
	// Use an actual frozen Started Item for either route. A generic
	// fixture Role Item must not be treated as a Member-only admission.
	startedID, err := f.runner.recordDelegationItem(t.Context(), f.turn, f.parent, delegation, ItemStarted, "")
	if err != nil {
		t.Fatal(err)
	}
	f.startedItemID = startedID
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	started, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, delegation, f.startedItemID)
	if !errors.Is(err, handoff.ErrChildPending) || started.Status != handoff.StatusRunning || f.handoff.calls != 1 {
		t.Fatalf("Handoff start: %+v err=%v calls=%d", started, err, f.handoff.calls)
	}
	if _, err := f.runtime.Load(t.Context(), delegation.ChildRunID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("remote Run unexpectedly exists in Harness Kernel: %v", err)
	}
	child, err := f.store.GetInvocationByExecutionRefID(t.Context(), delegation.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	f.runner.cancellation = f.runtime
	return f, child
}

func assertPR82RemoteChildUnchanged(t *testing.T, f *pr82HandoffFixture, child Invocation) {
	t.Helper()
	persisted, err := f.store.GetInvocation(t.Context(), child.ID)
	if err != nil || persisted.Status != child.Status || persisted.Revision != child.Revision ||
		persisted.Attempt != child.Attempt || persisted.ExecutionRefID != child.ExecutionRefID {
		t.Fatalf("remote child reclassified without remote receipt: original=%+v current=%+v err=%v", child, persisted, err)
	}
	if f.handoff.calls != 1 {
		t.Fatalf("remote ChildRunner re-entered during local cancellation: %d", f.handoff.calls)
	}
}

func TestPR82CancelTurnDoesNotFabricateRemoteHandoffCancellation(t *testing.T) {
	for _, role := range []bool{false, true} {
		name := "member-only"
		if role {
			name = "role"
		}
		t.Run(name, func(t *testing.T) {
			f, child := pr82StartedRemoteHandoff(t, role)
			for attempt := range 2 {
				_, err := f.runner.Cancel(t.Context(), f.turn.ID, "stop")
				if !errors.Is(err, ErrHandoffCancellationUnverified) {
					t.Fatalf("attempt %d expected unresolved remote cancellation, got %v", attempt, err)
				}
				assertPR82RemoteChildUnchanged(t, f, child)
				items, listErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
				if listErr != nil || !ActiveCancellationFence(items) {
					t.Fatalf("attempt %d lost cancellation fence: %v", attempt, listErr)
				}
				root, loadErr := f.runtime.Load(t.Context(), f.parent.ExecutionRefID)
				if loadErr != nil || root.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("authoritative root cancellation missing: %+v %v", root.Run, loadErr)
				}
			}
		})
	}
}

// No physical grant means the HandoffStarter was never entered. A cancellation
// intent can safely retire this still-Accepted child without inventing a
// remote receipt. Keep this valid no-effect path after fencing real remote Runs.
func TestPR82NeverDispatchedHandoffCanBeCancelled(t *testing.T) {
	for _, role := range []bool{false, true} {
		f := newPR82HandoffFixture(t)
		delegation := f.delegation()
		if !role {
			delegation.RoleID = ""
		}
		if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
			t.Fatal(err)
		}
		runs, err := f.runner.cancelTurnDescendants(t.Context(), f.turn.ID, f.parent.ID, f.parent.ExecutionRefID, "stop")
		if err != nil || runs[delegation.ChildRunID].Run.Status != kernel.RunStatusCancelled || f.handoff.calls != 0 {
			t.Fatalf("no-effect cancellation failed: role=%t snapshot=%+v err=%v calls=%d", role, runs[delegation.ChildRunID].Run, err, f.handoff.calls)
		}
	}
}

// Older incomplete child records lack the frozen root/Attempt proof and
// cannot establish that there was no prior external Handoff dispatch.
// Hard-cut them closed rather than infer a no-effect cancellation.
func TestPR82UnsealedHandoffCannotClaimNoExternalEffect(t *testing.T) {
	f := newPR82HandoffFixture(t)
	d := f.delegation()
	d.RoleID = ""
	input, hash, err := marshalInvocationValue(d)
	if err != nil {
		t.Fatal(err)
	}
	id, err := InvocationID(f.turn.ID, f.startedItemID, CapabilityAgent, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	child := Invocation{ID: id, TurnID: f.turn.ID, ParentItemID: f.startedItemID,
		CapabilityKey: CapabilityAgent, ExecutionClass: ExecutionAgent,
		ExecutionRefID: d.ChildRunID, Input: input, InputHash: hash,
		Status: InvocationAccepted, Attempt: 1, Revision: 1,
		OutputRefs: []HostRef{}, CreatedAt: f.parent.CreatedAt, UpdatedAt: f.parent.UpdatedAt}
	if _, _, err := f.store.CreateInvocation(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	if !isHandoffDelegationInvocation(child) {
		t.Fatal("test did not create a legacy Handoff child identity")
	}
	if _, err := f.runner.cancelTurnDescendants(t.Context(), f.turn.ID, f.parent.ID, f.parent.ExecutionRefID, "stop"); !errors.Is(err, ErrHandoffCancellationUnverified) {
		t.Fatalf("unsealed Handoff was treated as no effect: %v", err)
	}
}

// A grant rejected by the final parent-liveness check explicitly proves
// that no HandoffStarter call happened. The durable no-effect receipt allows
// ordinary no-Run cancellation instead of permanently fencing it.
func TestPR82RejectedHandoffDispatchAllowsNoEffectCancellation(t *testing.T) {
	f := newPR82HandoffFixture(t)
	delegation := f.delegation()
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	wrap := &pr82HandoffInjectionStore{Store: f.store}
	wrap.onGrant = func(ctx context.Context, _ Item) error {
		owner, err := f.store.GetInvocation(ctx, f.parent.ID)
		if err != nil {
			return err
		}
		owner.Status = InvocationCancelled
		_, err = f.store.UpdateInvocation(ctx, owner, owner.Revision)
		return err
	}
	f.runner.store = wrap
	if _, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, delegation, f.startedItemID); !errors.Is(err, ErrConflict) || f.handoff.calls != 0 {
		t.Fatalf("pre-effect rejection did not block Handoff: err=%v calls=%d", err, f.handoff.calls)
	}
	f.runner.store = f.store
	runs, err := f.runner.cancelTurnDescendants(t.Context(), f.turn.ID, f.parent.ID, f.parent.ExecutionRefID, "stop")
	if err != nil || runs[delegation.ChildRunID].Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("proven no-effect grant blocked cancellation: run=%+v err=%v", runs[delegation.ChildRunID].Run, err)
	}
}

func TestPR82LocalHandoffChildStillUsesVerifiedKernelCancellation(t *testing.T) {
	f := newPR82HandoffFixture(t)
	delegation := f.delegation()
	// Explicitly compose the exact fixed ChildRunner that creates the local
	// Kernel Run. A mock Handoff returning matching metadata is NOT proof.
	coordinator, err := handoff.New(&pr82BoundKernelChild{runtime: f.runtime})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.handoffs = coordinator
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, delegation, f.startedItemID); !errors.Is(err, handoff.ErrChildPending) {
		t.Fatal(err)
	}
	f.runner.cancellation = f.runtime
	snapshot, found, err := f.runner.cancelRuntimeRun(t.Context(), delegation.ChildRunID, "stop")
	if err != nil || !found || snapshot.Run.ID != delegation.ChildRunID || snapshot.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("legitimate local Kernel cancellation lost: %+v found=%t err=%v", snapshot.Run, found, err)
	}
}

// A2A has a local shadow with a2a.remote RunKind. Default Kernel Cancel
// would terminate only that shadow and never call the remote A2A CancelTask.
// A Host-supplied RuntimeCanceller must own verified external cleanup.
func TestPR82DefaultKernelCancellerCannotFalsifyA2AShadowCancellation(t *testing.T) {
	for _, role := range []bool{false, true} {
		for _, rootCancel := range []bool{false, true} {
			f := newPR82HandoffFixture(t)
			delegation := f.delegation()
			if !role {
				delegation.RoleID = ""
			}
			f.handoff.result = func(d handoff.Delegation) (handoff.Delegation, error) {
				_, err := f.runtime.Create(t.Context(), kernel.CreateRequest{ID: d.ChildRunID,
					Kind: handoff.RemoteAgentRunKind, Actor: f.parentRun.Run.Actor,
					Thread: f.parentRun.Run.Thread, RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: d.Goal, State: []byte(`{}`)})
				if err != nil {
					return handoff.Delegation{}, err
				}
				d.Status = handoff.StatusRunning
				return d, handoff.ErrChildPending
			}
			if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, delegation, f.startedItemID); !errors.Is(err, handoff.ErrChildPending) {
				t.Fatal(err)
			}
			f.runner.cancellation = f.runtime
			var err error
			if rootCancel {
				_, err = f.runner.Cancel(t.Context(), f.turn.ID, "stop")
			} else {
				child, loadErr := f.store.GetInvocationByExecutionRefID(t.Context(), delegation.ChildRunID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				_, err = f.runner.CancelInvocation(t.Context(), child.ID, "stop")
			}
			if !errors.Is(err, ErrHandoffCancellationUnverified) {
				t.Fatalf("role=%t rootCancel=%t: local shadow cancellation falsely succeeded: %v", role, rootCancel, err)
			}
			shadow, loadErr := f.runtime.Load(t.Context(), delegation.ChildRunID)
			if loadErr != nil || shadow.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("local shadow was terminated without remote cleanup: %+v %v", shadow.Run, loadErr)
			}
			child, loadErr := f.store.GetInvocationByExecutionRefID(t.Context(), delegation.ChildRunID)
			if loadErr != nil || child.Status != InvocationAccepted {
				t.Fatalf("child falsely terminalized: %+v %v", child, loadErr)
			}
			items, loadErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
			if loadErr != nil || !ActiveCancellationFence(items) {
				t.Fatalf("remote cleanup attempt was not retained: %v", loadErr)
			}
		}
	}
}

func TestPR82CancelInvocationDoesNotFabricateRemoteHandoffCancellation(t *testing.T) {
	for _, role := range []bool{false, true} {
		name := "member-only"
		if role {
			name = "role"
		}
		t.Run(name, func(t *testing.T) {
			f, child := pr82StartedRemoteHandoff(t, role)
			_, err := f.runner.CancelInvocation(t.Context(), child.ID, "stop")
			if !errors.Is(err, ErrHandoffCancellationUnverified) {
				t.Fatalf("expected unresolved remote cancellation, got %v", err)
			}
			assertPR82RemoteChildUnchanged(t, f, child)
			items, listErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
			if listErr != nil || !ActiveCancellationFence(items) {
				t.Fatalf("unverified cancellation did not preserve intent: %v", listErr)
			}
		})
	}
}
