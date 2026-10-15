package continuation_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// The optional pager must not break host-defined legacy relation Stores.
type legacyRelationStore struct{ runrelation.Store }

func TestSchedulerReconcileLegacyStoreCompatibility(t *testing.T) {
	store := memory.NewStore()
	runtime := newRuntimeWithStore(t, store)
	registry, err := runrelation.New(legacyRelationStore{Store: memory.NewRunRelationStore()}, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	if _, supported := registry.ReconciliationPager(); supported {
		t.Fatal("legacy Store unexpectedly advertises reconciliation paging")
	}
	queue := queuecore.NewMemory(queuecore.Dependencies{Clock: fixedClock{}})
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: store, Queue: queue, Relations: registry, Runs: runtime,
		Clock: fixedClock{}, ProjectorID: "legacy-reconcile",
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := createRun(t, runtime, "legacy-parent", "legacy.parent")
	child := createRun(t, runtime, "legacy-child", "legacy.child")
	ensureRelation(t, registry, runrelation.Draft{
		ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
		Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
	})
	completeRun(t, runtime, child)
	if err := scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queuedJobs(t, queue, 1)
}
