package harness

import (
	"errors"
	"testing"
	"time"
)

// A Host Application root is authoritative via its durable Invocation; it
// deliberately has no Kernel Run. Enforcing root liveness must not invent one
// or make live Application descendants unconditionally impossible.
func TestRootApplicationParentFenceUsesInvocationNotKernelRun(t *testing.T) {
	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	root, fresh, err := store.CreateInvocation(t.Context(), Invocation{
		ID: "app-root", TurnID: "app-turn", CapabilityKey: "application.root",
		ExecutionClass: ExecutionApplication, ExecutionRefID: "host-app-effect",
		Status: InvocationRunning, Attempt: 1, Revision: 1,
		OutputRefs: []HostRef{}, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !fresh {
		t.Fatalf("create root Application Invocation: %#v %v", root, err)
	}
	_, _, err = store.AppendItem(t.Context(), Item{
		ID: "unowned-host-item", TurnID: root.TurnID,
		Kind: ItemArtifact, Status: ItemCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{store: store} // no Kernel Runtime; Application has no Run
	child := Invocation{ID: "app-child", TurnID: root.TurnID, ParentItemID: "unowned-host-item"}
	if err = runner.requireLiveInvocationParents(t.Context(), child); err != nil {
		t.Fatalf("live Application root wrongly required a Kernel Run: %v", err)
	}
	root.Status = InvocationCancelled
	root.UpdatedAt = now.Add(time.Second)
	_, err = store.UpdateInvocation(t.Context(), root, root.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.requireLiveInvocationParents(t.Context(), child); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled Application root permitted new child effect: %v", err)
	}
}

func TestMissingRootCannotAuthorizeOwnerlessChild(t *testing.T) {
	store := NewMemoryStore()
	_, _, err := store.AppendItem(t.Context(), Item{
		ID: "orphaned-parent", TurnID: "orphaned-turn",
		Kind: ItemArtifact, Status: ItemCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{store: store}
	if err := runner.requireLiveInvocationParents(t.Context(), Invocation{
		ID: "orphaned-child", TurnID: "orphaned-turn", ParentItemID: "orphaned-parent",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("orphaned parent was allowed to authorize child effect: %v", err)
	}
}
