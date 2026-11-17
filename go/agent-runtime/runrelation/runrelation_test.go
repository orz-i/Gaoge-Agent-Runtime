package runrelation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

type relationClock struct{ now time.Time }

func (clock relationClock) Now() time.Time {
	return clock.now
}
func TestRegistryEnsuresStableRelation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	registry, err := runrelation.New(memory.NewRunRelationStore(), relationClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	draft := runrelation.Draft{ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.KindDelegation, OwnerNodeID: "step"}
	first, err := registry.Ensure(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Ensure(t.Context(), draft)
	if err != nil || !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("stable relation = (%#v, %#v), err=%v", first, second, err)
	}
}
func TestRegistryRejectsUnavailableAndInvalidInputs(t *testing.T) {
	t.Parallel()
	if _, err := runrelation.New(nil, nil); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("nil store error=%v", err)
	}
	registry, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Ensure(t.Context(), runrelation.Draft{ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.KindDelegation, OwnerNodeID: "step"}); err != nil {
		t.Fatalf("default clock ensure: %v", err)
	}
	if _, err = registry.ListChildren(t.Context(), " "); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("empty parent error=%v", err)
	}
	var unavailable *runrelation.Registry
	if _, err = unavailable.Ensure(context.Background(), runrelation.Draft{}); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("nil ensure error=%v", err)
	}
	if _, err = unavailable.GetByChild(context.Background(), "child"); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("nil get error=%v", err)
	}
	if _, err = unavailable.ListChildren(context.Background(), "parent"); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("nil children error=%v", err)
	}
	if _, err = unavailable.ListAll(context.Background()); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("nil all error=%v", err)
	}
}
func TestPrepareRejectsInvalidRelationContracts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	valid := runrelation.Relation{ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.KindDelegation, OwnerNodeID: "step", CreatedAt: now}
	cases := []runrelation.Relation{{}, {ParentRunID: "parent", ChildRunID: "legacy", Kind: runrelation.Kind("capability"), OwnerNodeID: "old-native-child", CreatedAt: now}, {ParentRunID: "same", ChildRunID: "same", Kind: runrelation.KindDelegation, OwnerNodeID: "step", CreatedAt: now}, {ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.Kind("unknown"), OwnerNodeID: "step", CreatedAt: now}, {ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.KindDelegation, CreatedAt: now}, {ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.KindDelegation, OwnerNodeID: "step"}}
	for _, relation := range cases {
		if _, err := runrelation.Prepare(relation); !errors.Is(err, runrelation.ErrInvalidInput) {
			t.Fatalf("relation %#v error=%v", relation, err)
		}
	}
	prepared, err := runrelation.Prepare(runrelation.Relation{ParentRunID: " parent ", ChildRunID: " child ", Kind: runrelation.Kind(" delegation "), OwnerNodeID: " step ", CreatedAt: now.In(time.FixedZone("test", 3600))})
	if err != nil || prepared.ParentRunID != valid.ParentRunID || prepared.ChildRunID != valid.ChildRunID || prepared.Kind != valid.Kind || prepared.OwnerNodeID != valid.OwnerNodeID || !prepared.CreatedAt.Equal(now) {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
}
