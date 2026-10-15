package continuation_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

type declaredDurableQueue struct{ *queuecore.Memory }

func (*declaredDurableQueue) DurableQueue() bool { return true }

func TestSchedulerProductionRequiresDurableQueue(t *testing.T) {
	fixture := newSchedulerFixture(t)
	dependencies := continuation.SchedulerDependencies{
		Outbox: fixture.store, Queue: fixture.delivery,
		Relations: fixture.relations, Runs: fixture.runtime,
		Clock: fixedClock{}, RequireDurableQueue: true,
	}
	if _, err := continuation.NewScheduler(dependencies); !errors.Is(err, continuation.ErrDurableQueueRequired) {
		t.Fatalf("volatile delivery was accepted for durable recovery: %v", err)
	}
	dependencies.Queue = &declaredDurableQueue{Memory: fixture.delivery}
	if _, err := continuation.NewScheduler(dependencies); err != nil {
		t.Fatalf("explicit durable queue was rejected: %v", err)
	}
}
