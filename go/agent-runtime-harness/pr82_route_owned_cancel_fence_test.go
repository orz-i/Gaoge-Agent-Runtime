package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestPR82ExactOriginCollisionCannotAuthorizeRemoteCancellation(t *testing.T) {
	f, child := pr82StartedRemoteHandoff(t, false)
	d := f.delegation()
	d.RoleID = ""
	remote, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	request := kernel.CreateRequest{
		ID: child.ExecutionRefID, Kind: agent.RunKind, Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread,
		RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: d.Goal, State: []byte(`{}`),
	}
	if _, err = remote.Create(t.Context(), request); err != nil {
		t.Fatalf("seed actual separate remote Kernel Run: %v", err)
	}
	if _, err = f.runtime.Create(t.Context(), request); err != nil {
		t.Fatalf("seed colliding local Kernel Run: %v", err)
	}
	_, cancelErr := f.runner.CancelInvocation(t.Context(), child.ID, "remote cancellation cannot be proven by local Run")
	local, err := f.runtime.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := f.store.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	actualRemote, err := remote.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	if local.Run.Status != kernel.RunStatusRunning || persisted.Status != child.Status || !errors.Is(cancelErr, ErrHandoffCancellationUnverified) || actualRemote.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("P1: identical metadata permitted cancellation of unrelated local execution and misprojected remote Handoff: local=%s actualRemote=%s child=%s cancel=%v", local.Run.Status, actualRemote.Run.Status, persisted.Status, cancelErr)
	}
}
func TestPR82RootCancelCannotSettleRemoteFromMatchingLocalRun(t *testing.T) {
	f, child := pr82StartedRemoteHandoff(t, true)
	d := f.delegation()
	remote, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	request := kernel.CreateRequest{ID: child.ExecutionRefID, Kind: agent.RunKind,
		Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread,
		RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: d.Goal, State: []byte(`{}`)}
	if _, err = remote.Create(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err = f.runtime.Create(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	_, cancelErr := f.runner.Cancel(t.Context(), f.turn.ID, "cancel including routed specialist")
	local, err := f.runtime.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	actualRemote, err := remote.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := f.store.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if local.Run.Status != kernel.RunStatusRunning || persisted.Status != child.Status || actualRemote.Run.Status != kernel.RunStatusRunning || !errors.Is(cancelErr, ErrHandoffCancellationUnverified) {
		t.Fatalf("P1 root: local unrelated=%s remote actual=%s child=%s cancelErr=%v", local.Run.Status, actualRemote.Run.Status, persisted.Status, cancelErr)
	}
}

// A fixed ChildRunner bound to a concrete Kernel is explicit route ownership;
// a matching ID in some other local Kernel is not.
type pr82BoundKernelChild struct{ runtime *kernel.Runtime }

func (c *pr82BoundKernelChild) OwnsKernelRuntime(runtime *kernel.Runtime) bool {
	return c != nil && c.runtime != nil && c.runtime == runtime
}
func (c *pr82BoundKernelChild) LoadRun(ctx context.Context, id string) (kernel.Snapshot, error) {
	return c.runtime.Load(ctx, id)
}
func (c *pr82BoundKernelChild) StartRun(ctx context.Context, request agent.StartRequest) (kernel.Snapshot, error) {
	return c.runtime.Create(ctx, kernel.CreateRequest{ID: request.ID, Kind: agent.RunKind,
		Actor: request.Actor, Thread: request.Thread, RequestID: request.RequestID,
		Goal: request.Goal, State: []byte(`{}`)})
}

// A real Coordinator must not promote current metadata into routing authority.
// Static local binding is accepted, but a different actual Kernel or even a
// currently matching dynamically resolved route cannot prove original ownership.
func TestPR82HandoffCancelRequiresFixedActualChildKernel(t *testing.T) {
	for _, scenario := range []string{"static-local", "static-remote", "routed-local"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			local := f.runtime
			childRuntime := local
			if scenario == "static-remote" {
				var err error
				childRuntime, err = kernel.New(kernel.Dependencies{Store: memory.NewStore()})
				if err != nil {
					t.Fatal(err)
				}
			}
			childRunner := &pr82BoundKernelChild{runtime: childRuntime}
			var coordinator *handoff.Coordinator
			var err error
			if scenario == "routed-local" {
				coordinator, err = handoff.NewRouted(handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) { return childRunner, nil }))
			} else {
				coordinator, err = handoff.New(childRunner)
			}
			if err != nil {
				t.Fatal(err)
			}
			f.runner.handoffs = coordinator
			f.runner.cancellation = local
			d := f.delegation()
			if err = f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, f.startedItemID); err != nil {
				t.Fatal(err)
			}
			if started, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, d, f.startedItemID); !errors.Is(err, handoff.ErrChildPending) || started.Status != handoff.StatusRunning {
				t.Fatalf("physical Handoff start=%+v err=%v", started, err)
			}
			child, err := f.store.GetInvocationByExecutionRefID(t.Context(), d.ChildRunID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "static-remote" {
				_, err = local.Create(t.Context(), kernel.CreateRequest{ID: d.ChildRunID, Kind: agent.RunKind,
					Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread,
					RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: d.Goal, State: []byte(`{}`)})
				if err != nil {
					t.Fatal(err)
				}
			}
			_, cancelErr := f.runner.CancelInvocation(t.Context(), child.ID, "cancel at original ChildRunner")
			current, err := f.store.GetInvocation(t.Context(), child.ID)
			if err != nil {
				t.Fatal(err)
			}
			localSnapshot, err := local.Load(t.Context(), d.ChildRunID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "static-local" {
				if cancelErr != nil || current.Status != InvocationCancelled || localSnapshot.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("owned local child did not cancel: err=%v child=%s local=%s", cancelErr, current.Status, localSnapshot.Run.Status)
				}
				return
			}
			actual, err := childRuntime.Load(t.Context(), d.ChildRunID)
			if err != nil {
				t.Fatal(err)
			}
			if !errors.Is(cancelErr, ErrHandoffCancellationUnverified) || current.Status != child.Status ||
				localSnapshot.Run.Status != kernel.RunStatusRunning || actual.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("foreign/dynamic route incorrectly cancelled: err=%v child=%s local=%s actual=%s", cancelErr, current.Status, localSnapshot.Run.Status, actual.Run.Status)
			}
		})
	}
}

// Proof of no physical Handoff dispatch cannot become permission to cancel
// a coincidentally present local Run with the same ID.
func TestPR82HandoffWithoutGrantCannotCancelCollidingRun(t *testing.T) {
	f := newPR82HandoffFixture(t)
	d := f.delegation()
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	_, err := f.runtime.Create(t.Context(), kernel.CreateRequest{ID: d.ChildRunID, Kind: agent.RunKind,
		Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread,
		RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: d.Goal, State: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.cancellation = f.runtime
	child, err := f.store.GetInvocationByExecutionRefID(t.Context(), d.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	_, cancelErr := f.runner.CancelInvocation(t.Context(), child.ID, "never dispatched")
	original, err := f.runtime.Load(t.Context(), d.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelErr != nil || original.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("undispatched Handoff attempted physical cancellation: err=%v Run=%s", cancelErr, original.Run.Status)
	}
	current, err := f.store.GetInvocation(t.Context(), child.ID)
	if err != nil || current.Status != InvocationCancelled {
		t.Fatalf("no-effect child not projected cancelled: status=%s err=%v", current.Status, err)
	}
}

type pr82NonterminalChildCanceller struct{ runtime *kernel.Runtime }

func (c pr82NonterminalChildCanceller) OwnsKernelRuntime(runtime *kernel.Runtime) bool {
	return c.runtime != nil && c.runtime == runtime
}

func (c pr82NonterminalChildCanceller) Cancel(ctx context.Context, id string, _ uint64, _ string) (kernel.Snapshot, error) {
	return c.runtime.Load(ctx, id)
}

func TestPR82NonterminalCancellationReceiptCannotSettleHandoff(t *testing.T) {
	f := newPR82HandoffFixture(t)
	d := f.delegation()
	coordinator, err := handoff.New(&pr82BoundKernelChild{runtime: f.runtime})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.handoffs = coordinator
	if err = f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, d, f.startedItemID); !errors.Is(err, handoff.ErrChildPending) {
		t.Fatalf("start=%v", err)
	}
	child, err := f.store.GetInvocationByExecutionRefID(t.Context(), d.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	f.runner.cancellation = pr82NonterminalChildCanceller{runtime: f.runtime}
	_, cancelErr := f.runner.CancelInvocation(t.Context(), child.ID, "must be terminal")
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.store.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(cancelErr, ErrHandoffCancellationUnverified) || !ActiveCancellationFence(items) || current.Status != child.Status {
		t.Fatalf("nonterminal Cancel claimed success: err=%v fenced=%v child=%s", cancelErr, ActiveCancellationFence(items), current.Status)
	}
}

// A trusted static ChildRunner is still insufficient if the configured
// Canceller operates on a different Kernel with an identical Run identity.
func TestPR82HandoffCancelRejectsForeignKernelCanceller(t *testing.T) {
	f := newPR82HandoffFixture(t)
	d := f.delegation()
	coordinator, err := handoff.New(&pr82BoundKernelChild{runtime: f.runtime})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.handoffs = coordinator
	if err = f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, d, f.startedItemID); !errors.Is(err, handoff.ErrChildPending) {
		t.Fatalf("start=%v", err)
	}
	foreign, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = foreign.Create(t.Context(), kernel.CreateRequest{ID: d.ChildRunID, Kind: agent.RunKind,
		Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread,
		RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: d.Goal, State: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.store.GetInvocationByExecutionRefID(t.Context(), d.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	f.runner.cancellation = foreign
	_, cancelErr := f.runner.CancelInvocation(t.Context(), child.ID, "must use owning Canceller")
	localSnapshot, err := f.runtime.Load(t.Context(), d.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	foreignSnapshot, err := foreign.Load(t.Context(), d.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := f.store.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(cancelErr, ErrHandoffCancellationUnverified) || localSnapshot.Run.Status != kernel.RunStatusRunning ||
		foreignSnapshot.Run.Status != kernel.RunStatusRunning || persisted.Status != child.Status {
		t.Fatalf("P1: wrong Canceller mutated foreign Kernel: err=%v local=%s foreign=%s child=%s", cancelErr, localSnapshot.Run.Status, foreignSnapshot.Run.Status, persisted.Status)
	}
}
