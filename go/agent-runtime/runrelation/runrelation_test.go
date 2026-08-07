package runrelation_test

import (
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

type relationClock struct{ now time.Time }

func (clock relationClock) Now() time.Time { return clock.now }

func TestRegistryEnsuresStableRelation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	registry, err := runrelation.New(memory.NewRunRelationStore(), relationClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	draft := runrelation.Draft{
		ParentRunID: "parent", ChildRunID: "child",
		Kind: runrelation.KindPlanStep, OwnerNodeID: "step",
	}
	first, err := registry.Ensure(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Ensure(t.Context(), draft)
	if err != nil || !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("stable relation = (%#v, %#v), err=%v", first, second, err)
	}
}
