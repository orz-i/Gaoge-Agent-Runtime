package memory_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestRunRelationReconciliationPagerBoundedAndMonotonic(t *testing.T) {
	t.Parallel()
	store := memory.NewRunRelationStore()
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	for index := 0; index < runrelation.MaxReconcilePageSize+3; index++ {
		relation := runrelation.Relation{
			ParentRunID: fmt.Sprintf("parent-%03d", index),
			ChildRunID:  fmt.Sprintf("child-%03d", index),
			Kind:        runrelation.KindTeamMember, OwnerNodeID: "member", CreatedAt: now,
		}
		if _, _, err := store.Put(t.Context(), relation); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ListReconcileCandidates(t.Context(), "", 0); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("zero page size: %v", err)
	}
	if _, err := store.ListReconcileCandidates(t.Context(), "", runrelation.MaxReconcilePageSize+1); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("unbounded page size: %v", err)
	}
	first, err := store.ListReconcileCandidates(t.Context(), "", runrelation.MaxReconcilePageSize)
	if err != nil || len(first) != runrelation.MaxReconcilePageSize ||
		first[0].Relation.ChildRunID != "child-000" ||
		first[len(first)-1].Relation.ChildRunID != "child-127" {
		t.Fatalf("first page: count=%d, %v", len(first), err)
	}
	second, err := store.ListReconcileCandidates(t.Context(), first[len(first)-1].Relation.ChildRunID, runrelation.MaxReconcilePageSize)
	if err != nil || len(second) != 3 || second[0].Relation.ChildRunID != "child-128" {
		t.Fatalf("second page: %#v, %v", second, err)
	}
	if err := store.MarkEnqueued(t.Context(), "child-000", 7, 4); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkEnqueued(t.Context(), "child-000", 2, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkEnqueued(t.Context(), "child-000", 6, 8); err != nil {
		t.Fatal(err)
	}
	marked, err := store.ListReconcileCandidates(t.Context(), "", 1)
	if err != nil || len(marked) != 1 ||
		marked[0].LastParentRevision != 7 || marked[0].LastChildRevision != 8 {
		t.Fatalf("monotonic watermark: %#v, %v", marked, err)
	}
	if err := store.MarkEnqueued(t.Context(), "missing", 1, 1); !errors.Is(err, runrelation.ErrNotFound) {
		t.Fatalf("missing relation: %v", err)
	}
	if err := store.MarkEnqueued(t.Context(), "child-000", 0, 1); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("invalid revision: %v", err)
	}
}
