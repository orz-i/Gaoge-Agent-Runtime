package conformance

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RunChildExecutionSuite is the cross-Store source-of-truth conformance for
// durable parent/child physical admissions. Every store must enforce these
// invariants in its own authoritative transaction, not via a Kernel Load.
func RunChildExecutionSuite(t *testing.T, create func(*testing.T) kernel.Store) {
	t.Helper()
	t.Run("nested ancestor Cancel cannot overtake physical child", func(t *testing.T) {
		underlying := create(t)
		rt, err := kernel.New(kernel.Dependencies{Store: underlying})
		if err != nil {
			t.Fatal(err)
		}
		actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
		thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
		newRun := func(id, parent string) kernel.Snapshot {
			t.Helper()
			s, err := rt.Create(t.Context(), kernel.CreateRequest{ID: id, Kind: "workflow", ParentRunID: parent,
				Actor: actor, Thread: thread, Goal: "shared-physical-authority", RequestID: id, State: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		root := newRun("root", "")
		middle := newRun("middle", root.Run.ID)
		child := newRun("child", middle.Run.ID)
		grant, err := rt.BeginChildExecution(t.Context(), child.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if observed, lookupErr := rt.LookupChildExecution(t.Context(), child.Run.ID); lookupErr != nil || observed != grant {
			t.Fatalf("Store lost original physical Grant: %+v err=%v", observed, lookupErr)
		}
		for _, owner := range []kernel.Snapshot{root, middle} {
			_, err = rt.Cancel(t.Context(), owner.Run.ID, owner.Run.Revision, "while child effects pending")
			if !errors.Is(err, kernel.ErrChildExecutionBusy) {
				t.Fatalf("ancestor %s committed terminal despite pending Grant: %v", owner.Run.ID, err)
			}
		}
		if _, err = rt.BeginChildExecution(t.Context(), child.Run.ID); !errors.Is(err, kernel.ErrChildExecutionBusy) {
			t.Fatalf("concurrent second physical Grant was accepted: %v", err)
		}
		store := underlying.(kernel.ChildExecutionStore)
		if err = rt.FinishChildExecution(t.Context(), grant, nil); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("outside SDK Feature could retire Grant without trusted proof: %v", err)
		}
		if observed, lookupErr := rt.LookupChildExecution(t.Context(), child.Run.ID); lookupErr != nil || observed != grant {
			t.Fatalf("untrusted retirement affected original Grant: %+v err=%v", observed, lookupErr)
		}
		if err = store.FinishChildExecution(t.Context(), kernel.ChildExecutionGrant{ID: grant.ID, ChildRunID: "foreign"}); err == nil {
			t.Fatal("foreign child retired trusted Grant")
		}
		if err = store.FinishChildExecution(t.Context(), grant); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("original Grant was retired before physical callback completion: %v", err)
		}
		if markErr := store.MarkChildExecutionSettled(t.Context(), grant, kernel.ChildExecutionSettlement{}); !errors.Is(markErr, kernel.ErrExecutionConflict) {
			t.Fatalf("untrusted direct Store Mark succeeded: %v", markErr)
		}
		latest, loadErr := rt.Load(t.Context(), child.Run.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		proof := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "workflow_child_physical_settled", grant.ID, latest.State, latest.Checkpoint)
		if err = rt.MarkChildExecutionSettled(t.Context(), grant, proof); err != nil {
			t.Fatalf("trusted Agent post-callback settlement marker was rejected: %v", err)
		}
		if err = store.FinishChildExecution(t.Context(), grant); err != nil {
			t.Fatal(err)
		}
		if _, lookupErr := rt.LookupChildExecution(t.Context(), child.Run.ID); !errors.Is(lookupErr, kernel.ErrNotFound) {
			t.Fatalf("settled physical Grant still reads active: %v", lookupErr)
		}
		if err = store.FinishChildExecution(t.Context(), grant); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("replayed Grant retirement succeeded: %v", err)
		}
		cancelled, err := rt.Cancel(t.Context(), root.Run.ID, root.Run.Revision, "after physical receipt")
		if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled {
			t.Fatalf("verified completion failed: %+v err=%v", cancelled.Run, err)
		}
		if _, err = rt.BeginChildExecution(t.Context(), child.Run.ID); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("cancelled ancestor admitted new work: %v", err)
		}
	})
	t.Run("parent cancellation blocks child Create", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: create(t)})
		if err != nil {
			t.Fatal(err)
		}
		actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
		thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
		parent, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "workflow",
			Actor: actor, Thread: thread, Goal: "parent", State: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		_, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "terminated before child")
		if err != nil {
			t.Fatal(err)
		}
		_, err = rt.Create(t.Context(), kernel.CreateRequest{ID: "late-child", Kind: "agent", ParentRunID: parent.Run.ID,
			Actor: actor, Thread: thread, Goal: "late", State: []byte(`{}`)})
		if !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("late child admitted: %v", err)
		}
	})
	t.Run("foreign tenancy and provenance cannot create child", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: create(t)})
		if err != nil {
			t.Fatal(err)
		}
		actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
		thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
		_, err = rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "workflow",
			Actor: actor, Thread: thread, Goal: "parent", State: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		for _, req := range []kernel.CreateRequest{
			{ID: "foreign-tenant", Kind: "agent", ParentRunID: "parent",
				Actor: kernel.ActorRef{TenantID: "foreign", ActorID: "actor"}, Thread: thread, Goal: "other tenant", State: []byte(`{}`)},
			{ID: "foreign-thread", Kind: "agent", ParentRunID: "parent",
				Actor: actor, Thread: kernel.ThreadRef{Kind: "conversation", ID: "other"}, Goal: "other thread", State: []byte(`{}`)},
			{ID: "missing-parent", Kind: "agent", ParentRunID: "not-present",
				Actor: actor, Thread: thread, Goal: "foreign owner", State: []byte(`{}`)},
		} {
			if _, createErr := rt.Create(t.Context(), req); createErr == nil {
				t.Fatalf("foreign child accepted: %+v", req)
			}
		}
	})
}
