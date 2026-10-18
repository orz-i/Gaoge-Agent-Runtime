package continuation_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func createTenantRun(t *testing.T, runtime *kernel.Runtime, id, tenant string) kernel.Snapshot {
	t.Helper()
	run, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: id, Kind: "test.tenant",
		Actor:  kernel.ActorRef{TenantID: tenant, ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "scope recovery", State: json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func crossTenantRelationFixture(t *testing.T) (schedulerFixture, kernel.Snapshot, kernel.Snapshot) {
	t.Helper()
	f := newSchedulerFixture(t)
	foreignParent := createTenantRun(t, f.runtime, "scope-foreign-parent", "tenant-A")
	foreignChild := createTenantRun(t, f.runtime, "scope-foreign-child", "tenant-B")
	healthyParent := createTenantRun(t, f.runtime, "scope-healthy-parent", "tenant-A")
	healthyChild := createTenantRun(t, f.runtime, "scope-healthy-child", "tenant-A")
	for _, pair := range []struct{ parent, child kernel.Snapshot }{
		{foreignParent, foreignChild}, {healthyParent, healthyChild},
	} {
		ensureRelation(t, f.relations, runrelation.Draft{
			ParentRunID: pair.parent.Run.ID, ChildRunID: pair.child.Run.ID,
			Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
		})
		completeRun(t, f.runtime, pair.child)
	}
	return f, foreignParent, healthyParent
}

func assertOnlyHealthyChildRecovery(t *testing.T, f schedulerFixture, healthyParent kernel.Snapshot) {
	t.Helper()
	jobs := queuedJobs(t, f.delivery, 1)
	var payload continuation.Payload
	if err := json.Unmarshal(jobs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RunID != healthyParent.Run.ID || payload.SourceRunID != "scope-healthy-child" ||
		payload.Trigger != continuation.TriggerChildTerminal {
		t.Fatalf("cross-tenant child caused a wakeup or healthy run was starved: %+v", payload)
	}
}

// Relation metadata has no authority to grant Tenant B's child completion
// the power to wake a Tenant A parent. Corrupt relations must be reported
// without blocking projection for a different, healthy same-Tenant Run.
func TestCrossTenantTerminalProjectionFailsClosedAndIsolatesHealthyRun(t *testing.T) {
	f, _, healthy := crossTenantRelationFixture(t)
	if err := f.scheduler.Project(t.Context()); !errors.Is(err, continuation.ErrInvalidInput) {
		t.Fatalf("cross-tenant relation must fail closed, got %v", err)
	}
	assertOnlyHealthyChildRecovery(t, f, healthy)
}

func TestCrossTenantTerminalReconcileFailsClosedAndIsolatesHealthyRun(t *testing.T) {
	f, _, healthy := crossTenantRelationFixture(t)
	if err := f.scheduler.Reconcile(t.Context()); !errors.Is(err, continuation.ErrInvalidInput) {
		t.Fatalf("cross-tenant relation must not be reconciled, got %v", err)
	}
	assertOnlyHealthyChildRecovery(t, f, healthy)
}

// More permanent poison relations than the in-memory retry budget must not
// stop a later healthy relation forever. Their durable unobserved watermarks
// remain eligible when the bounded keyset scan wraps back to the beginning.
func TestCrossTenantPoisonBacklogDoesNotStarveLaterHealthyRelation(t *testing.T) {
	f := newSchedulerFixture(t)
	for index := 0; index < runrelation.MaxReconcilePageSize+1; index++ {
		parent := createTenantRun(t, f.runtime, fmt.Sprintf("bad-parent-%03d", index), "tenant-A")
		child := createTenantRun(t, f.runtime, fmt.Sprintf("bad-child-%03d", index), "tenant-B")
		ensureRelation(t, f.relations, runrelation.Draft{
			ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
			Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
		})
		completeRun(t, f.runtime, child)
	}
	healthy := createTenantRun(t, f.runtime, "zz-healthy-parent", "tenant-A")
	child := createTenantRun(t, f.runtime, "zz-healthy-child", "tenant-A")
	ensureRelation(t, f.relations, runrelation.Draft{
		ParentRunID: healthy.Run.ID, ChildRunID: child.Run.ID,
		Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
	})
	completeRun(t, f.runtime, child)
	for tick := 0; tick < 2; tick++ {
		if err := f.scheduler.Reconcile(t.Context()); !errors.Is(err, continuation.ErrInvalidInput) {
			t.Fatalf("tick %d did not report invalid relations: %v", tick, err)
		}
	}
	jobs, err := f.delivery.List(t.Context(), continuation.QueueName, "")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("healthy candidate behind full poison retry backlog was starved: jobs=%+v err=%v", jobs, err)
	}
	var payload continuation.Payload
	if err = json.Unmarshal(jobs[0].Payload, &payload); err != nil ||
		payload.RunID != healthy.Run.ID || payload.SourceRunID != child.Run.ID {
		t.Fatalf("unexpected recovery behind backlog: %+v, %v", payload, err)
	}
}
