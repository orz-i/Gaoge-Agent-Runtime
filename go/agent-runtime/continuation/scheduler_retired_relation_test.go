package continuation

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// A historical Capability relation may still be present in an old database.
// Neither committed-transition projection nor paged reconciliation may turn
// that retired native child edge into a parent Agent continuation.
func TestRetiredCapabilityRelationCannotScheduleParent(t *testing.T) {
	relation := runrelation.Relation{
		ParentRunID: "parent", ChildRunID: "child",
		Kind: runrelation.Kind("capability"), OwnerNodeID: "legacy",
	}
	scheduler := &Scheduler{}
	if err := scheduler.scheduleRelation(t.Context(), relation,
		kernel.Snapshot{Run: kernel.Run{ID: "child"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("retired relation authorized transition projection: %v", err)
	}
	if err := scheduler.reconcileCandidate(context.Background(),
		runrelation.ReconciliationCandidate{Relation: relation}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("retired relation authorized reconcile: %v", err)
	}
}
