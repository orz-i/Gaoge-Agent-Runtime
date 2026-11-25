package conformance

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func RunReconciliationFaultIsolationSuite(t *testing.T, store kernel.Store, relations runrelation.Store, seedLegacy func(runrelation.Relation) error) {
	ctx := t.Context()
	rt, e := kernel.New(kernel.Dependencies{Store: store})
	if e != nil {
		t.Fatal(e)
	}
	create := func(id, tenant string, done bool) kernel.Snapshot {
		t.Helper()
		v, e := rt.Create(ctx, kernel.CreateRequest{ID: id, Kind: "agent", Actor: kernel.ActorRef{TenantID: tenant, ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "reconcile", State: []byte(`{}`)})
		if e != nil {
			t.Fatal(e)
		}
		if done {
			v, e = rt.Apply(ctx, id, v.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: v.State, Result: &kernel.Result{ContentType: "text", Content: []byte(`"done"`)}})
			if e != nil {
				t.Fatal(e)
			}
		}
		return v
	}
	oldParent := create("old-parent", "legacy-tenant", false)
	oldChild := create("a-legacy-child", "legacy-tenant", true)
	healthyParent := create("healthy-parent", "other-tenant", false)
	healthyChild := create("z-healthy-child", "other-tenant", true)
	legacy := runrelation.Relation{ChildRunID: oldChild.Run.ID, ParentRunID: oldParent.Run.ID, Kind: "capability", OwnerNodeID: "legacy", CreatedAt: time.Now().UTC().Truncate(time.Second)}
	if e = seedLegacy(legacy); e != nil {
		t.Fatal(e)
	}
	reg, e := runrelation.New(relations, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = reg.Ensure(ctx, runrelation.Draft{ChildRunID: healthyChild.Run.ID, ParentRunID: healthyParent.Run.ID, Kind: runrelation.KindDelegation, OwnerNodeID: "valid"}); e != nil {
		t.Fatal(e)
	}
	pager, ok := relations.(runrelation.ReconciliationPager)
	if !ok {
		t.Fatal("missing relational pager")
	}
	bad, e := pager.ListReconcilePage(ctx, "", 1)
	if e != nil || bad.NextCursor != legacy.ChildRunID || !bad.HasMore || len(bad.Candidates) != 0 || len(bad.RejectedChildIDs) != 1 || bad.RejectedChildIDs[0] != legacy.ChildRunID || !errors.Is(bad.RejectionError(), runrelation.ErrConflict) {
		t.Fatalf("invalid-only page lost scan cursor: %+v %v", bad, e)
	}
	good, e := pager.ListReconcilePage(ctx, bad.NextCursor, 1)
	if e != nil || len(good.Candidates) != 1 || good.Candidates[0].Relation.ChildRunID != healthyChild.Run.ID || good.RejectionError() != nil {
		t.Fatalf("healthy next page blocked: %+v %v", good, e)
	}
	q := queue.NewMemory(queue.Dependencies{})
	s, e := continuation.NewScheduler(continuation.SchedulerDependencies{Outbox: store.(kernel.TransitionOutbox), Queue: q, Relations: reg, Runs: rt})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		next, err := s.ReconcilePage(ctx, "")
		if next != "" || !errors.Is(err, runrelation.ErrConflict) {
			t.Fatalf("invalid relation must be reported, healthy page must finish: %q %v", next, err)
		}
		err = s.Reconcile(ctx)
		if !errors.Is(err, runrelation.ErrConflict) {
			t.Fatalf("regular tick lost invalid relation evidence: %v", err)
		}
	}
	jobs, e := q.List(ctx, continuation.QueueName, "")
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("healthy queued jobs=%d", len(jobs))
	if len(jobs) != 1 {
		t.Fatal("legacy relation starved unrelated valid child delivery")
	}
	var p continuation.Payload
	if e = json.Unmarshal(jobs[0].Payload, &p); e != nil || p.RunID != healthyParent.Run.ID || p.SourceRunID != healthyChild.Run.ID {
		t.Fatalf("wrong job: %+v %v", p, e)
	}
}
