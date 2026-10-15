package continuation_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

type pagingOnlyRelations struct {
	*runrelation.Registry
	pager    runrelation.ReconciliationPager
	allCalls int
}

func (relations *pagingOnlyRelations) ListAll(context.Context) ([]runrelation.Relation, error) {
	relations.allCalls++
	return nil, errors.New("unbounded ListAll must not be called")
}

func (relations *pagingOnlyRelations) ReconciliationPager() (runrelation.ReconciliationPager, bool) {
	return relations.pager, true
}

type countedRunLoader struct {
	loader continuation.SnapshotLoader
	calls  int
}

func (loader *countedRunLoader) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	loader.calls++
	return loader.loader.Load(ctx, runID)
}

func TestSchedulerReconcileUsesBoundedPagesWithoutHistoricalDuplicateJobs(t *testing.T) {
	fixture := newSchedulerFixture(t)
	pager, ok := fixture.relations.ReconciliationPager()
	if !ok {
		t.Fatal("memory registry must expose bounded reconciliation")
	}
	relations := &pagingOnlyRelations{Registry: fixture.relations, pager: pager}
	loader := &countedRunLoader{loader: fixture.runtime}
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: fixture.store, Queue: fixture.delivery, Relations: relations,
		Runs: loader, Clock: fixedClock{}, ProjectorID: "paged-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	const count = runrelation.MaxReconcilePageSize + 3
	for index := 0; index < count; index++ {
		parent := createRun(t, fixture.runtime, fmt.Sprintf("paged-parent-%03d", index), "paged.parent")
		child := createRun(t, fixture.runtime, fmt.Sprintf("paged-child-%03d", index), "paged.child")
		ensureRelation(t, fixture.relations, runrelation.Draft{
			ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
			Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
		})
		completeRun(t, fixture.runtime, child)
	}
	if err := scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if loader.calls != 2*runrelation.MaxReconcilePageSize {
		t.Fatalf("first recovery scan loaded %d Runs, expected bounded page", loader.calls)
	}
	if got := len(queuedJobs(t, fixture.delivery, runrelation.MaxReconcilePageSize)); got != runrelation.MaxReconcilePageSize {
		t.Fatalf("first page enqueued %d", got)
	}
	if err := scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queuedJobs(t, fixture.delivery, count)
	if loader.calls != 2*count || relations.allCalls != 0 {
		t.Fatalf("pagination escaped bounded path: loads=%d ListAll=%d", loader.calls, relations.allCalls)
	}
	for range 3 {
		if err := scheduler.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	queuedJobs(t, fixture.delivery, count)
	if relations.allCalls != 0 {
		t.Fatalf("legacy ListAll called %d times", relations.allCalls)
	}
}

func TestSchedulerReconcileReschedulesAfterParentWaitCommitButNotUnchanged(t *testing.T) {
	fixture := newSchedulerFixture(t)
	parent := createRun(t, fixture.runtime, "paged-early-parent", "paged.parent")
	child := createRun(t, fixture.runtime, "paged-early-child", "paged.child")
	ensureRelation(t, fixture.relations, runrelation.Draft{
		ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
		Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
	})
	completed := completeRun(t, fixture.runtime, child)
	if err := fixture.scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queuedJobs(t, fixture.delivery, 1)
	if err := fixture.scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queuedJobs(t, fixture.delivery, 1)
	committed, err := fixture.runtime.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: parent.State,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs := queuedJobs(t, fixture.delivery, 2)
	found := false
	for _, job := range jobs {
		if job.ClientJobID == fmt.Sprintf("%s:%s:%s:%d:%d",
			continuation.TriggerChildTerminal, parent.Run.ID, child.Run.ID, completed.Run.Revision, committed.Run.Revision) {
			found = true
		}
	}
	if !found {
		t.Fatalf("parent revision %d was not recovered after wait commit: %#v", committed.Run.Revision, jobs)
	}
	if err := fixture.scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queuedJobs(t, fixture.delivery, 2)
}

type failOnceReconcilePager struct {
	runrelation.ReconciliationPager
	fail bool
}

func (pager *failOnceReconcilePager) MarkEnqueued(ctx context.Context, childRunID string, parentRevision, childRevision uint64) error {
	if pager.fail {
		pager.fail = false
		return errors.New("simulated crash after enqueue, before watermark")
	}
	return pager.ReconciliationPager.MarkEnqueued(ctx, childRunID, parentRevision, childRevision)
}

func TestSchedulerReconcileRetriesAfterEnqueueBeforeWatermarkFailure(t *testing.T) {
	fixture := newSchedulerFixture(t)
	parent := createRun(t, fixture.runtime, "paged-fault-parent", "paged.parent")
	child := createRun(t, fixture.runtime, "paged-fault-child", "paged.child")
	ensureRelation(t, fixture.relations, runrelation.Draft{
		ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
		Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
	})
	completeRun(t, fixture.runtime, child)
	pager, ok := fixture.relations.ReconciliationPager()
	if !ok {
		t.Fatal("missing pager")
	}
	relations := &pagingOnlyRelations{
		Registry: fixture.relations,
		pager:    &failOnceReconcilePager{ReconciliationPager: pager, fail: true},
	}
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: fixture.store, Queue: fixture.delivery, Relations: relations,
		Runs: fixture.runtime, Clock: fixedClock{}, ProjectorID: "fault-paged",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Reconcile(t.Context()); err == nil {
		t.Fatal("watermark failure must be reported")
	}
	queuedJobs(t, fixture.delivery, 1)
	if err := scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queuedJobs(t, fixture.delivery, 1)
	if err := scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queuedJobs(t, fixture.delivery, 1)
	if relations.allCalls != 0 {
		t.Fatalf("legacy full scan invoked: %d", relations.allCalls)
	}
}
