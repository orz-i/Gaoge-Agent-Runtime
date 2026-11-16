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
