package continuation_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

type retryPager struct {
	runrelation.ReconciliationPager
	id       string
	attempts int
}

func (p *retryPager) MarkEnqueued(ctx context.Context, id string, parent, child uint64) error {
	if id == p.id {
		p.attempts++
		return errors.New("retry this item")
	}
	return p.ReconciliationPager.MarkEnqueued(ctx, id, parent, child)
}
func TestSchedulerReconcileRetriesEarlierError(t *testing.T) {
	f := newSchedulerFixture(t)
	for i := 0; i < runrelation.MaxReconcilePageSize+3; i++ {
		parent := createRun(t, f.runtime, fmt.Sprintf("retry-parent-%03d", i), "retry.parent")
		child := createRun(t, f.runtime, fmt.Sprintf("retry-child-%03d", i), "retry.child")
		ensureRelation(t, f.relations, runrelation.Draft{ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID, Kind: runrelation.KindTeamMember, OwnerNodeID: "member"})
		completeRun(t, f.runtime, child)
	}
	pager, _ := f.relations.ReconciliationPager()
	failing := &retryPager{ReconciliationPager: pager, id: "retry-child-000"}
	relations := &pagingOnlyRelations{Registry: f.relations, pager: failing}
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{Outbox: f.store, Queue: f.delivery, Relations: relations, Runs: f.runtime, Clock: fixedClock{}, ProjectorID: "retry-test"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := scheduler.Reconcile(t.Context()); err == nil {
			t.Fatal("expected error")
		}
		if failing.attempts != i {
			t.Fatalf("tick %d attempts %d", i, failing.attempts)
		}
	}
	if relations.allCalls != 0 {
		t.Fatal("legacy scan")
	}
	// A persistent error remains retriable while later pages still progress.
	// The first Job was accepted before its watermark failed; retries reuse
	// that identity while later pages still enqueue all other children.
	queuedJobs(t, f.delivery, runrelation.MaxReconcilePageSize+3)
}
