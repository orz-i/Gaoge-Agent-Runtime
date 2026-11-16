package harness

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// A terminal parent must never authorize a later delegated/native child
// approval, even while the Harness Turn projection remains Running.
func TestSubAgentApprovalRequiresLiveRelationAncestors(t *testing.T) {
	for _, kind := range []runrelation.Kind{runrelation.KindDelegation} {
		t.Run(string(kind), func(t *testing.T) {
			rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
			parent, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "agent", Actor: actor, Thread: thread, Goal: "parent", State: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			child, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "child", Kind: "agent", Actor: actor, Thread: thread, Goal: "child", ParentRunID: parent.Run.ID, State: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			// A non-root child whose relation index is missing cannot
			// borrow root approval authority through the no-edge fallback.
			// The physical identity alone is insufficient to authorize work.
			missing := &Runner{runtime: rt, relationReader: relations, store: NewMemoryStore()}
			if err := missing.requireLiveApprovalAncestors(t.Context(), child.Run.ID); !errors.Is(err, ErrRuntimeCancellationUnverified) {
				t.Fatalf("missing child relation authorized approval: %v", err)
			}
			if _, err := relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID, Kind: kind, OwnerNodeID: "delegated-child"}); err != nil {
				t.Fatal(err)
			}
			runner := &Runner{runtime: rt, relationReader: relations}
			if err := runner.requireLiveApprovalAncestors(t.Context(), child.Run.ID); err != nil {
				t.Fatalf("live ancestor denied: %v", err)
			}
			if _, err := rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "parent cancelled"); err != nil {
				t.Fatalf("cancel parent: %v", err)
			}
			if err := runner.requireLiveApprovalAncestors(t.Context(), child.Run.ID); !errors.Is(err, ErrRuntimeCancellationUnverified) {
				t.Fatalf("terminal parent permitted child approval: %v", err)
			}
		})
	}
}

func TestSubAgentApprovalRejectsCancelledGrandparentAndForgedParentEdge(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	makeRun := func(id, parent string) kernel.Snapshot {
		t.Helper()
		run, e := rt.Create(t.Context(), kernel.CreateRequest{ID: id, Kind: "agent", Actor: actor, Thread: thread, Goal: id, ParentRunID: parent, State: []byte(`{}`)})
		if e != nil {
			t.Fatal(e)
		}
		return run
	}
	root := makeRun("root", "")
	middle := makeRun("middle", root.Run.ID)
	leaf := makeRun("leaf", middle.Run.ID)
	foreign := makeRun("foreign", "")
	runner := &Runner{runtime: rt, relationReader: relations}
	if _, e := relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: foreign.Run.ID, ChildRunID: leaf.Run.ID, OwnerNodeID: "bad-parent", Kind: runrelation.KindDelegation}); e != nil {
		t.Fatal(e)
	}
	if e := runner.requireLiveApprovalAncestors(t.Context(), leaf.Run.ID); !errors.Is(e, ErrRuntimeCancellationUnverified) {
		t.Fatalf("forged relation parent accepted: %v", e)
	}
	// A valid multi-level relation chain is tested separately because
	// relation ownership is immutable and cannot be repaired in-place.
	other := makeRun("other", middle.Run.ID)
	if _, e := relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: middle.Run.ID, ChildRunID: other.Run.ID, OwnerNodeID: "subagent", Kind: runrelation.KindDelegation}); e != nil {
		t.Fatal(e)
	}
	if _, e := relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: root.Run.ID, ChildRunID: middle.Run.ID, OwnerNodeID: "middle", Kind: runrelation.KindDelegation}); e != nil {
		t.Fatal(e)
	}
	if e := runner.requireLiveApprovalAncestors(t.Context(), other.Run.ID); e != nil {
		t.Fatalf("valid child ancestry rejected: %v", e)
	}
	if _, e := rt.Cancel(t.Context(), root.Run.ID, root.Run.Revision, "root cancelled"); e != nil {
		t.Fatal(e)
	}
	if e := runner.requireLiveApprovalAncestors(t.Context(), other.Run.ID); !errors.Is(e, ErrRuntimeCancellationUnverified) {
		t.Fatalf("cancelled grandparent authorized descendant approval: %v", e)
	}
}
