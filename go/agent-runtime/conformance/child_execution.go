package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// AdmitChildFixture exercises the unified, source-sealed admission in the
// existing low-level conformance cases. The expected revision is supplied
// explicitly for stale/future-source tests.
func AdmitChildFixture(ctx context.Context, rt *kernel.Runtime, id string, rev uint64) (kernel.ChildExecutionGrant, error) {
	source, err := rt.Load(ctx, id)
	if err != nil {
		return kernel.ChildExecutionGrant{}, err
	}
	source.Run.Revision = rev
	lease, bound := rt.BoundExecutionLease()
	q := kernel.ExecutionAcquireRequest{}
	if !bound {
		q = kernel.ExecutionAcquireRequest{RunID: id, OwnerID: "child-fixture", LeaseDuration: time.Minute}
	}
	operation := "start"
	if source.Run.Status == kernel.RunStatusWaitingInput {
		operation = "approval"
	}
	proof := featurecontrol.Seal(id, rev, "agent_owned_physical_admission", kernel.PhysicalAdmissionSubject(q, lease, operation, ""), source.State, source)
	scoped, result, err := rt.AdmitPhysicalExecution(ctx, source, q, proof, operation, "")
	if err == nil {
		_, err = scoped.CheckOwnedPhysicalEntry(ctx, source)
	}
	return result.Grant, err
}

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
			s, err := rt.Create(t.Context(), kernel.CreateRequest{ID: id, Kind: "agent", ParentRunID: parent, ExecutionRequired: parent != "",
				Actor: actor, Thread: thread, Goal: "shared-physical-authority", RequestID: id, State: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		root := newRun("root", "")
		middle := newRun("middle", root.Run.ID)
		child := newRun("child", middle.Run.ID)
		q := kernel.ExecutionAcquireRequest{RunID: child.Run.ID, OwnerID: "child-fixture", LeaseDuration: time.Minute}
		admissionProof := featurecontrol.Seal(child.Run.ID, child.Run.Revision, "agent_owned_physical_admission", kernel.PhysicalAdmissionSubject(q, kernel.ExecutionLease{}, "start", ""), child.State, child)
		if _, _, e := rt.AdmitPhysicalExecution(t.Context(), child, q, admissionProof, "resume", ""); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("child admission accepted proof for a different operation: %v", e)
		}
		grant, err := AdmitChildFixture(t.Context(), rt, child.Run.ID, child.Run.Revision)
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
		if _, err = AdmitChildFixture(t.Context(), rt, child.Run.ID, child.Run.Revision); !errors.Is(err, kernel.ErrChildExecutionBusy) {
			t.Fatalf("concurrent second physical Grant was accepted: %v", err)
		}
		store := underlying.(kernel.PhysicalExecutionStore)
		if err = rt.FinishChildExecution(t.Context(), grant, nil); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("outside SDK Feature could retire Grant without trusted proof: %v", err)
		}
		if observed, lookupErr := rt.LookupChildExecution(t.Context(), child.Run.ID); lookupErr != nil || observed != grant {
			t.Fatalf("untrusted retirement affected original Grant: %+v err=%v", observed, lookupErr)
		}
		if err = store.FinishChildExecution(t.Context(), kernel.ChildExecutionGrant{ID: grant.ID, ChildRunID: "foreign", ExpectedRevision: grant.ExpectedRevision}, kernel.ChildExecutionSettlement{}); err == nil {
			t.Fatal("foreign child retired trusted Grant")
		}
		if err = store.FinishChildExecution(t.Context(), grant, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("original Grant was retired before physical callback completion: %v", err)
		}
		if markErr := underlying.(kernel.PhysicalExecutionStore).RecordPhysicalCallbackReturn(t.Context(), grant, grant.ExpectedRevision, kernel.PhysicalCallbackOutcome{HostCompleted: true}, kernel.PhysicalCallbackReturn{}); !errors.Is(markErr, kernel.ErrExecutionConflict) {
			t.Fatalf("untrusted direct Store Mark succeeded: %v", markErr)
		}
		latest, loadErr := rt.Load(t.Context(), child.Run.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		proof := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "agent_physical_settled", grant.ID, latest.State, latest)
		outcome := kernel.PhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: true}
		callback := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "agent_physical_callback_return", grant.ID, kernel.PhysicalCallbackReturnPayload(latest, outcome), nil)
		if err = rt.RecordPhysicalCallbackReturn(t.Context(), grant, latest.Run.Revision, outcome, proof); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatal("settlement proof manufactured an original return", err)
		}

		if err = rt.RecordPhysicalCallbackReturn(t.Context(), grant, latest.Run.Revision, outcome, callback); err != nil {
			t.Fatalf("trusted Agent post-callback settlement marker was rejected: %v", err)
		}
		returned, returnErr := rt.LookupPhysicalCallbackReturn(t.Context(), grant)
		if returnErr != nil || returned.GrantID != grant.ID || !returned.MatchesReturnSnapshot(latest) || !returned.LeaseOwned || returned.ControlCustody != "" {
			t.Fatalf("original child return fact: %+v %v", returned, returnErr)
		}
		if err = rt.RecordPhysicalCallbackReturn(t.Context(), grant, latest.Run.Revision, outcome, callback); err != nil {
			t.Fatalf("exact original return replay: %v", err)
		}

		forgedRevision := grant
		forgedRevision.ExpectedRevision++
		if err = store.FinishChildExecution(t.Context(), forgedRevision, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("settled original Grant could be retired using a forged StartRevision: original=%d forged=%d err=%v", grant.ExpectedRevision, forgedRevision.ExpectedRevision, err)
		}
		if err = store.FinishChildExecution(t.Context(), grant, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("marked borrowed grant accepted empty Finish permit: %v", err)
		}
		if err = rt.FinishChildExecution(t.Context(), grant, callback); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatal("callback proof supplied settlement permission", err)
		}
		if err = rt.FinishChildExecution(t.Context(), grant, proof); err != nil {
			t.Fatal(err)
		}
		if _, lookupErr := rt.LookupChildExecution(t.Context(), child.Run.ID); !errors.Is(lookupErr, kernel.ErrNotFound) {
			t.Fatalf("settled physical Grant still reads active: %v", lookupErr)
		}
		if err = store.FinishChildExecution(t.Context(), grant, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("replayed Grant retirement succeeded: %v", err)
		}
		reuse, e := kernel.New(kernel.Dependencies{Store: underlying, IDs: fixedGrantIDs{grant.ID}})
		if e != nil {
			t.Fatal(e)
		}
		if _, err = AdmitChildFixture(t.Context(), reuse, child.Run.ID, latest.Run.Revision); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("finished borrowed identity became available again: %v", err)
		}
		cancelled, err := rt.Cancel(t.Context(), root.Run.ID, root.Run.Revision, "after physical receipt")
		if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled {
			t.Fatalf("verified completion failed: %+v err=%v", cancelled.Run, err)
		}
		if _, err = AdmitChildFixture(t.Context(), rt, child.Run.ID, child.Run.Revision); !errors.Is(err, kernel.ErrExecutionConflict) {
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
		parent, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "agent",
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
		_, err = rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "agent",
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
