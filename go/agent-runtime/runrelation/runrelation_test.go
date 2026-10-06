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
		Kind: runrelation.KindCapability, OwnerNodeID: "step",
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

func TestRegistryQueriesRelationsInDeterministicOrder(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	registry, err := runrelation.New(memory.NewRunRelationStore(), relationClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	drafts := []runrelation.Draft{
		{ParentRunID: "parent", ChildRunID: "child-b", Kind: runrelation.KindCapability, OwnerNodeID: "step-b"},
		{ParentRunID: "other", ChildRunID: "child-other", Kind: runrelation.KindTeamMember, OwnerNodeID: "member"},
		{ParentRunID: "parent", ChildRunID: "child-a", Kind: runrelation.KindCapability, OwnerNodeID: "step-a"},
	}
	for _, draft := range drafts {
		if _, err = registry.Ensure(t.Context(), draft); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := registry.GetByChild(t.Context(), " child-b ")
	if err != nil || resolved.OwnerNodeID != "step-b" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	children, err := registry.ListChildren(t.Context(), " parent ")
	if err != nil || len(children) != 2 ||
		children[0].ChildRunID != "child-a" || children[1].ChildRunID != "child-b" {
		t.Fatalf("children=%#v err=%v", children, err)
	}
	all, err := registry.ListAll(t.Context())
	if err != nil || len(all) != 3 ||
		all[0].ChildRunID != "child-a" || all[1].ChildRunID != "child-b" ||
		all[2].ChildRunID != "child-other" {
		t.Fatalf("all=%#v err=%v", all, err)
	}
	descriptor := registry.Descriptor()
	if descriptor.Name != "runrelation" || len(descriptor.Provides) != 1 ||
		descriptor.Provides[0] != runrelation.CapabilityRelations {
		t.Fatalf("descriptor=%#v", descriptor)
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
	if _, err = registry.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: "parent", ChildRunID: "child",
		Kind: runrelation.KindCapability, OwnerNodeID: "step",
	}); err != nil {
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
	valid := runrelation.Relation{
		ParentRunID: "parent", ChildRunID: "child",
		Kind: runrelation.KindCapability, OwnerNodeID: "step", CreatedAt: now,
	}
	cases := []runrelation.Relation{
		{},
		{ParentRunID: "same", ChildRunID: "same", Kind: runrelation.KindCapability, OwnerNodeID: "step", CreatedAt: now},
		{ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.Kind("unknown"), OwnerNodeID: "step", CreatedAt: now},
		{ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.KindCapability, CreatedAt: now},
		{ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.KindCapability, OwnerNodeID: "step"},
	}
	for _, relation := range cases {
		if _, err := runrelation.Prepare(relation); !errors.Is(err, runrelation.ErrInvalidInput) {
			t.Fatalf("relation %#v error=%v", relation, err)
		}
	}
	prepared, err := runrelation.Prepare(runrelation.Relation{
		ParentRunID: " parent ", ChildRunID: " child ",
		Kind: runrelation.Kind(" capability "), OwnerNodeID: " step ", CreatedAt: now.In(time.FixedZone("test", 3600)),
	})
	if err != nil || prepared.ParentRunID != valid.ParentRunID || prepared.ChildRunID != valid.ChildRunID ||
		prepared.Kind != valid.Kind || prepared.OwnerNodeID != valid.OwnerNodeID || !prepared.CreatedAt.Equal(now) {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
}

func TestSortUsesTimestampKindOwnerAndChildTieBreakers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	items := []runrelation.Relation{
		{ChildRunID: "z", Kind: runrelation.KindCapability, OwnerNodeID: "same", CreatedAt: now},
		{ChildRunID: "later", Kind: runrelation.KindCapability, OwnerNodeID: "a", CreatedAt: now.Add(time.Second)},
		{ChildRunID: "kind", Kind: runrelation.KindTeamMember, OwnerNodeID: "a", CreatedAt: now},
		{ChildRunID: "owner", Kind: runrelation.KindCapability, OwnerNodeID: "b", CreatedAt: now},
		{ChildRunID: "a", Kind: runrelation.KindCapability, OwnerNodeID: "same", CreatedAt: now},
	}
	runrelation.Sort(items)
	got := []string{items[0].ChildRunID, items[1].ChildRunID, items[2].ChildRunID, items[3].ChildRunID, items[4].ChildRunID}
	want := []string{"owner", "a", "z", "kind", "later"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("sort order=%v want=%v", got, want)
		}
	}
}
