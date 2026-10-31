package harness

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type indexOnlyParentStore struct {
	Store
	itemCalls []string
	listCalls int
}

func (store *indexOnlyParentStore) GetItem(ctx context.Context, id string) (Item, error) {
	store.itemCalls = append(store.itemCalls, id)
	return store.Store.GetItem(ctx, id)
}
func (store *indexOnlyParentStore) ListItems(_ context.Context, _ string, _ uint64, _ int) ([]Item, error) {
	store.listCalls++
	return nil, ErrConflict
}

func TestRequireLiveInvocationParentsReadsOnlyAncestorItems(t *testing.T) {
	backing := NewMemoryStore()
	const turnID = "long-ancestry"
	for index := range 10000 {
		_, _, err := backing.AppendItem(t.Context(), Item{
			ID: fmt.Sprintf("unrelated-%05d", index), TurnID: turnID,
			Kind: ItemArtifact, Status: ItemCompleted,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []Item{
		{ID: "root-owner-item", TurnID: turnID, InvocationID: "root-owner", Kind: ItemInvocation, Status: ItemStarted},
		{ID: "middle-owner-item", TurnID: turnID, InvocationID: "middle-owner", ParentItemID: "root-owner-item",
			Kind: ItemInvocation, Status: ItemStarted},
	} {
		if _, _, err := backing.AppendItem(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"root-owner", "middle-owner"} {
		backing.invocations[id] = Invocation{
			ID: id, TurnID: turnID, Status: InvocationRunning, ExecutionClass: ExecutionApplication,
			ExecutionRefID: "host-" + id, Attempt: 1,
		}
	}
	original, inputHash, err := marshalInvocationValue(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	input, inputHash, err := sealChildRootInput(original, inputHash, backing.invocations["root-owner"])
	if err != nil {
		t.Fatal(err)
	}
	spy := &indexOnlyParentStore{Store: backing}
	runner := &Runner{store: spy}
	child := Invocation{ID: "grandchild", TurnID: turnID, ParentItemID: "middle-owner-item", Input: input, InputHash: inputHash}
	if err := runner.requireLiveInvocationParents(t.Context(), child); err != nil {
		t.Fatalf("live direct ancestry was rejected: %v", err)
	}
	if spy.listCalls != 0 || len(spy.itemCalls) != 2 ||
		spy.itemCalls[0] != "middle-owner-item" || spy.itemCalls[1] != "root-owner-item" {
		t.Fatalf("10K unrelated Items were scanned or parent chain was bypassed: list=%d gets=%v",
			spy.listCalls, spy.itemCalls)
	}

	backing.mu.Lock()
	changed := backing.invocations["middle-owner"]
	changed.Status = InvocationCompleted
	backing.invocations["middle-owner"] = changed
	backing.mu.Unlock()
	if err := runner.requireLiveInvocationParents(t.Context(), child); !errors.Is(err, ErrConflict) {
		t.Fatalf("finished middle owner authorized the grandchild: %v", err)
	}
	backing.mu.Lock()
	changed.Status = InvocationRunning
	backing.invocations["middle-owner"] = changed
	backing.mu.Unlock()
	if err := runner.requireLiveInvocationParents(t.Context(), Invocation{
		ID: child.ID, TurnID: turnID, ParentItemID: "missing-ancestor",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("absent parent Item did not fail closed: %v", err)
	}
	if spy.listCalls != 0 {
		t.Fatalf("negative ancestry path scanned unrelated Items: count=%d", spy.listCalls)
	}
}

func TestIndexedAncestorWalkFailsClosedOnCyclesAndForeignTurns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		items  []Item
		parent string
	}{
		{name: "cycle", parent: "a", items: []Item{
			{ID: "a", TurnID: "turn", ParentItemID: "b", Kind: ItemInvocation, Status: ItemStarted},
			{ID: "b", TurnID: "turn", ParentItemID: "a", Kind: ItemInvocation, Status: ItemStarted},
		}},
		{name: "foreign-turn", parent: "foreign", items: []Item{
			{ID: "foreign", TurnID: "unrelated", Kind: ItemInvocation, Status: ItemStarted},
		}},
		{name: "empty-grandparent", parent: "a", items: []Item{
			{ID: "a", TurnID: "turn", ParentItemID: "nonexistent", Kind: ItemInvocation, Status: ItemStarted},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backing := NewMemoryStore()
			for _, item := range tc.items {
				if _, _, err := backing.AppendItem(t.Context(), item); err != nil {
					t.Fatal(err)
				}
			}
			spy := &indexOnlyParentStore{Store: backing}
			runner := &Runner{store: spy}
			err := runner.requireLiveInvocationParents(t.Context(), Invocation{
				ID: "child", TurnID: "turn", ParentItemID: tc.parent,
			})
			if !errors.Is(err, ErrConflict) || spy.listCalls != 0 {
				t.Fatalf("cycle/foreign Item bypassed fail-closed ancestry: err=%v scans=%d",
					err, spy.listCalls)
			}
		})
	}
}

func TestMemoryStoreGetItemCloneAndPaginateLongTurn(t *testing.T) {
	store := NewMemoryStore()
	const turnID = "pagination-turn"
	for i := range 1802 {
		if _, _, err := store.AppendItem(t.Context(), Item{
			ID: fmt.Sprintf("page-%05d", i), TurnID: turnID,
			Kind: ItemArtifact, Status: ItemCompleted,
		}); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		after uint64
		limit int
		first int
		want  int
	}{
		{after: 0, limit: 500, first: 0, want: 500},
		{after: 500, limit: 500, first: 500, want: 500},
		{after: 1500, limit: 500, first: 1500, want: 302},
		{after: 1802, limit: 500, first: 0, want: 0},
		{after: 20000, limit: 500, first: 0, want: 0},
		{after: 1799, limit: 0, first: 1799, want: 3},
		{after: 1000, limit: 2000, first: 1000, want: 500},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("after-%d-limit-%d", tc.after, tc.limit), func(t *testing.T) {
			page, err := store.ListItems(t.Context(), turnID, tc.after, tc.limit)
			if err != nil || len(page) != tc.want {
				t.Fatalf("page length=%d want=%d err=%v", len(page), tc.want, err)
			}
			if tc.want > 0 && (page[0].ID != fmt.Sprintf("page-%05d", tc.first) ||
				page[0].Seq != uint64(tc.first+1)) {
				t.Fatalf("cursor returned wrong first Item: %+v", page[0])
			}
		})
	}
	if _, err := store.GetItem(t.Context(), "page-00117"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetItem(t.Context(), "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := store.GetItem(t.Context(), " "); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal(err)
	}
	if _, err := store.ListItems(t.Context(), turnID, 0, -1); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal(err)
	}
	original, err := store.GetItem(t.Context(), "page-00117")
	if err != nil {
		t.Fatal(err)
	}
	original.Payload[0] = '['
	again, err := store.GetItem(t.Context(), "page-00117")
	if err != nil || string(again.Payload) != "{}" {
		t.Fatalf("returned Item aliases stored payload: %+v err=%v", again, err)
	}
}
