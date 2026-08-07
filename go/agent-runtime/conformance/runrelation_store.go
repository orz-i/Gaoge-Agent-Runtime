package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

// RunRelationStoreFactory creates one isolated relation store per test.
type RunRelationStoreFactory func(testing.TB) runrelation.Store

// RunRunRelationStoreSuite validates idempotence, conflicts and deterministic queries.
func RunRunRelationStoreSuite(t *testing.T, factory RunRelationStoreFactory) {
	t.Helper()
	if factory == nil {
		t.Fatal("run relation store factory is required")
	}
	t.Run("idempotent-owner", func(t *testing.T) { testRunRelationIdempotentOwner(t, factory(t)) })
	t.Run("conflicts", func(t *testing.T) { testRunRelationConflicts(t, factory(t)) })
	t.Run("queries", func(t *testing.T) { testRunRelationQueries(t, factory(t)) })
}

func testRunRelationIdempotentOwner(t *testing.T, store runrelation.Store) {
	t.Helper()
	relation := testRunRelation("parent-1", "child-1", runrelation.KindPlanStep, "step-1", 0)
	created, reused, err := store.Put(context.Background(), relation)
	if err != nil || reused || !runrelation.EqualIdentity(created, relation) {
		t.Fatalf("create = %#v, reused=%t, err=%v", created, reused, err)
	}
	replay := relation
	replay.CreatedAt = replay.CreatedAt.Add(time.Hour)
	existing, reused, err := store.Put(context.Background(), replay)
	if err != nil || !reused || !existing.CreatedAt.Equal(relation.CreatedAt) {
		t.Fatalf("replay = %#v, reused=%t, err=%v", existing, reused, err)
	}
}

func testRunRelationConflicts(t *testing.T, store runrelation.Store) {
	t.Helper()
	original := testRunRelation("parent-1", "child-1", runrelation.KindTeamMember, "member-1", 0)
	if _, _, err := store.Put(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	ownerConflict := testRunRelation("parent-1", "child-2", runrelation.KindTeamMember, "member-1", 1)
	if _, _, err := store.Put(context.Background(), ownerConflict); !errors.Is(err, runrelation.ErrConflict) {
		t.Fatalf("owner conflict error = %v", err)
	}
	childConflict := testRunRelation("parent-2", "child-1", runrelation.KindWorkflowEffect, "effect-1", 2)
	if _, _, err := store.Put(context.Background(), childConflict); !errors.Is(err, runrelation.ErrConflict) {
		t.Fatalf("child conflict error = %v", err)
	}
}

func testRunRelationQueries(t *testing.T, store runrelation.Store) {
	t.Helper()
	second := testRunRelation("parent-list", "child-2", runrelation.KindPlanStep, "step-2", 2)
	first := testRunRelation("parent-list", "child-1", runrelation.KindPlanStep, "step-1", 1)
	other := testRunRelation("parent-other", "child-3", runrelation.KindTeamMember, "member-1", 0)
	for _, relation := range []runrelation.Relation{second, other, first} {
		if _, _, err := store.Put(context.Background(), relation); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListChildren(context.Background(), "parent-list")
	if err != nil || len(items) != 2 || items[0].ChildRunID != "child-1" || items[1].ChildRunID != "child-2" {
		t.Fatalf("children = %#v, err=%v", items, err)
	}
	resolved, err := store.GetByChild(context.Background(), "child-2")
	if err != nil || resolved.OwnerNodeID != "step-2" {
		t.Fatalf("resolved = %#v, err=%v", resolved, err)
	}
}

func testRunRelation(parent, child string, kind runrelation.Kind, owner string, seconds int) runrelation.Relation {
	return runrelation.Relation{
		ParentRunID: parent, ChildRunID: child, Kind: kind, OwnerNodeID: owner,
		CreatedAt: time.Date(2026, 8, 8, 0, 0, seconds, 0, time.UTC),
	}
}
