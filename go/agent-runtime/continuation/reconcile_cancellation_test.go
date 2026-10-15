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

type cancelDuringReconcileLoader struct {
	loader continuation.SnapshotLoader
	cancel context.CancelFunc
	calls  int
}

func (loader *cancelDuringReconcileLoader) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	loader.calls++
	// The second candidate starts after the first was already processed.
	if loader.calls == 3 {
		loader.cancel()
	}
	return loader.loader.Load(ctx, id)
}

func TestSchedulerReconcileCancellationPreservesUnprocessedPage(t *testing.T) {
	fixture := newSchedulerFixture(t)
	const total = runrelation.MaxReconcilePageSize + 3
	for index := 0; index < total; index++ {
		parent := createRun(t, fixture.runtime, fmt.Sprintf("cancel-parent-%03d", index), "cancel.parent")
		child := createRun(t, fixture.runtime, fmt.Sprintf("cancel-child-%03d", index), "cancel.child")
		ensureRelation(t, fixture.relations, runrelation.Draft{
			ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
			Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
		})
		completeRun(t, fixture.runtime, child)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	loader := &cancelDuringReconcileLoader{loader: fixture.runtime, cancel: cancel}
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: fixture.store, Queue: fixture.delivery, Relations: fixture.relations,
		Runs: loader, Clock: fixedClock{}, ProjectorID: "cancel-paged",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Reconcile(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-page cancellation: %v", err)
	}
	for range 3 {
		if err := scheduler.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	queuedJobs(t, fixture.delivery, total)
}
