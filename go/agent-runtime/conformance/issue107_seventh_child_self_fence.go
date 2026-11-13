package conformance

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RunAuditPhysicalChildSelfFence verifies that every physically admitted
// child is fenced against its OWN terminal transition, not just against the
// terminal transitions of its immutable ancestors.
func RunAuditPhysicalChildSelfFence(t *testing.T, makeStore func(*testing.T) kernel.Store) {
	t.Helper()
	t.Run("admitted child self-cancel while parent cancel denies", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: makeStore(t)})
		if err != nil {
			t.Fatal(err)
		}
		actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
		thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
		parent, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "root", Kind: "workflow", Actor: actor, Thread: thread, Goal: "original root", State: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		child, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "child", ParentRunID: parent.Run.ID, Kind: "workflow",
			Actor: actor, Thread: thread, Goal: "original child", State: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		grant, err := rt.BeginChildExecution(t.Context(), child.Run.ID, child.Run.Revision)
		if err != nil {
			t.Fatal(err)
		}
		_, parentErr := rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "parent during original physical callback")
		if !errors.Is(parentErr, kernel.ErrChildExecutionBusy) {
			t.Fatalf("root parent unexpectedly committed during child Effect: %v", parentErr)
		}
		cancelled, childErr := rt.Cancel(t.Context(), child.Run.ID, child.Run.Revision, "child during its own original physical callback")
		t.Logf("originalGrant=%s expectedRevision=%d selfCancelErr=%v selfStatus=%s",
			grant.ID, grant.ExpectedRevision, childErr, cancelled.Run.Status)
		if childErr == nil && cancelled.Run.Status == kernel.RunStatusCancelled {
			t.Fatal("the same physical child was terminally cancelled while its own persisted original Grant was still active")
		}
	})
	t.Run("admitted child remains version-fenced against external Apply", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: makeStore(t)})
		if err != nil {
			t.Fatal(err)
		}
		actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
		thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
		parent, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "root", Kind: "workflow", Actor: actor, Thread: thread, Goal: "root", State: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		child, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "child", ParentRunID: parent.Run.ID, Kind: "workflow",
			Actor: actor, Thread: thread, Goal: "child", State: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		grant, err := rt.BeginChildExecution(t.Context(), child.Run.ID, child.Run.Revision)
		if err != nil {
			t.Fatal(err)
		}
		updated, applyErr := rt.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: []byte(`{"surprise":"other writer"}`),
		})
		t.Logf("grant=%s concurrentApplyErr=%v revisionAfter=%d", grant.ID, applyErr, updated.Run.Revision)
		if applyErr == nil && updated.Run.Revision > child.Run.Revision {
			t.Fatal("a foreign writer advanced the admitted child's revision while the original child Effect was physically executing")
		}
	})
	t.Run("rejected child Cancel does not revoke original execution lease", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: makeStore(t)})
		if err != nil {
			t.Fatal(err)
		}
		actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
		thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
		parent, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "root", Kind: "workflow", Actor: actor, Thread: thread, Goal: "original root", State: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		child, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "child", ParentRunID: parent.Run.ID, Kind: "agent", ExecutionRequired: true,
			Actor: actor, Thread: thread, Goal: "physical owner", State: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		lease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
			RunID: child.Run.ID, OwnerID: "original-worker", LeaseDuration: 2 * time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		grant, err := rt.BeginChildExecution(t.Context(), child.Run.ID, child.Run.Revision)
		if err != nil {
			t.Fatal(err)
		}
		_, cancelErr := rt.Cancel(t.Context(), child.Run.ID, child.Run.Revision, "denied during physical callback")
		if !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) {
			t.Fatalf("Agent self-Cancel escaped its Grant: %v", cancelErr)
		}
		bound, err := rt.WithExecution(lease)
		if err != nil {
			t.Fatal(err)
		}
		if err = bound.RequireExecution(t.Context(), child.Run.ID); err != nil {
			t.Fatalf("no-effect Cancel secretly revoked original Agent Execution Epoch: %v", err)
		}
		if observed, lookupErr := rt.LookupChildExecution(t.Context(), child.Run.ID); lookupErr != nil || observed != grant {
			t.Fatalf("no-effect cancel altered physical Grant: %+v err=%v", observed, lookupErr)
		}
	})
}
