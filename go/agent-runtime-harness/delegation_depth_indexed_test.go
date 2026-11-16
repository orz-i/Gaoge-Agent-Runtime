package harness

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// The existing globally indexed Item store is the only evidence required for
// an ancestor chain. Full Turn timelines can contain unrelated business
// artifacts, but must never be traversed to compute a depth bound.
func TestDelegationDepthUsesExactAncestryNotFullTimeline(t *testing.T) {
	const turnID = "depth-turn"
	backing := NewMemoryStore()
	for i := range 2000 {
		if _, _, err := backing.AppendItem(t.Context(), Item{
			ID: fmt.Sprintf("unrelated-%05d", i), TurnID: turnID,
			Kind: ItemArtifact, Status: ItemCompleted,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []Item{
		{ID: "root-to-middle", TurnID: turnID, Kind: ItemDelegation, Status: ItemStarted, InvocationID: "root"},
		{ID: "middle-to-child", TurnID: turnID, Kind: ItemDelegation, Status: ItemStarted, InvocationID: "middle"},
	} {
		if _, _, err := backing.AppendItem(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	backing.invocations["root"] = Invocation{ID: "root", TurnID: turnID}
	backing.invocations["middle"] = Invocation{ID: "middle", TurnID: turnID, ParentItemID: "root-to-middle"}
	child := Invocation{ID: "child", TurnID: turnID, ParentItemID: "middle-to-child"}
	spy := &indexOnlyParentStore{Store: backing}
	depth, err := invocationDelegationDepth(t.Context(), spy, child)
	if err != nil || depth != 2 || spy.listCalls != 0 ||
		len(spy.itemCalls) != 2 || spy.itemCalls[0] != "middle-to-child" ||
		spy.itemCalls[1] != "root-to-middle" {
		t.Fatalf("depth=%d err=%v list=%d indexed=%+v", depth, err, spy.listCalls, spy.itemCalls)
	}
	for _, tc := range []struct {
		maxDepth int
		allowed  bool
	}{
		{1, false}, {2, false}, {3, true}, {0, true},
	} {
		allowed, err := delegationAllowed(t.Context(), spy, DelegationPolicySnapshot{MaxDepth: tc.maxDepth}, child)
		if err != nil || allowed != tc.allowed {
			t.Fatalf("depth %d allowed=%v want=%v err=%v", tc.maxDepth, allowed, tc.allowed, err)
		}
	}
	if spy.listCalls != 0 {
		t.Fatalf("depth computation still paginates unrelated business history: %d", spy.listCalls)
	}
	zero := &indexOnlyParentStore{Store: backing}
	depth, err = invocationDelegationDepth(t.Context(), zero, backing.invocations["root"])
	if err != nil || depth != 0 || zero.listCalls != 0 || len(zero.itemCalls) != 0 {
		t.Fatalf("root depth unnecessarily read a Store: depth=%d err=%v %+v", depth, err, zero)
	}
}

type adversarialDepthIndexStore struct {
	Store
	returnItem    *Item
	itemErr       error
	invocationErr error
}

func (store adversarialDepthIndexStore) GetItem(ctx context.Context, id string) (Item, error) {
	if store.itemErr != nil {
		return Item{}, store.itemErr
	}
	if store.returnItem != nil {
		return *store.returnItem, nil
	}
	return store.Store.GetItem(ctx, id)
}
func (store adversarialDepthIndexStore) GetInvocation(ctx context.Context, id string) (Invocation, error) {
	if store.invocationErr != nil {
		return Invocation{}, store.invocationErr
	}
	return store.Store.GetInvocation(ctx, id)
}

func TestIndexedDelegationDepthFailsClosedOnForeignOwnersCyclesAndReadErrors(t *testing.T) {
	const turnID = "depth-original-turn"
	backing := NewMemoryStore()
	for _, item := range []Item{
		{ID: "foreign-item", TurnID: "other-turn", Kind: ItemDelegation, Status: ItemStarted, InvocationID: "root"},
		{ID: "local-to-foreign", TurnID: turnID, Kind: ItemDelegation, Status: ItemStarted, InvocationID: "foreign-parent"},
		{ID: "local-to-root", TurnID: turnID, Kind: ItemDelegation, Status: ItemStarted, InvocationID: "root"},
		{ID: "cycle-a", TurnID: turnID, Kind: ItemDelegation, Status: ItemStarted, InvocationID: "cycle-A"},
		{ID: "cycle-b", TurnID: turnID, Kind: ItemDelegation, Status: ItemStarted, InvocationID: "cycle-B"},
	} {
		if _, _, err := backing.AppendItem(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	backing.invocations["root"] = Invocation{ID: "root", TurnID: turnID}
	backing.invocations["foreign-parent"] = Invocation{ID: "foreign-parent", TurnID: "other-turn"}
	backing.invocations["cycle-A"] = Invocation{ID: "cycle-A", TurnID: turnID, ParentItemID: "cycle-b"}
	backing.invocations["cycle-B"] = Invocation{ID: "cycle-B", TurnID: turnID, ParentItemID: "cycle-a"}
	missingFailure := errors.New("fixture indexed storage read failed")
	for _, tc := range []struct {
		name, parentID string
		store          Store
		want           error
	}{
		{"missing_item", "missing", backing, ErrConflict},
		{"foreign_turn", "foreign-item", backing, ErrConflict},
		{"foreign_parent_invocation", "local-to-foreign", backing, ErrConflict},
		{"cycle", "cycle-a", backing, ErrConflict},
		{"wrong_global_item_id", "local-to-root",
			adversarialDepthIndexStore{Store: backing, returnItem: &Item{ID: "some-other-id", TurnID: turnID}}, ErrConflict},
		{"indexed_get_failure", "local-to-root",
			adversarialDepthIndexStore{Store: backing, itemErr: missingFailure}, missingFailure},
		{"indexed_parent_invocation_failure", "local-to-root",
			adversarialDepthIndexStore{Store: backing, invocationErr: missingFailure}, missingFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &indexOnlyParentStore{Store: tc.store}
			allowed, err := delegationAllowed(t.Context(), spy, DelegationPolicySnapshot{MaxDepth: 3},
				Invocation{ID: "child", TurnID: turnID, ParentItemID: tc.parentID})
			if allowed || !errors.Is(err, tc.want) || spy.listCalls != 0 {
				t.Fatalf("invalid chain accepted: allowed=%v err=%v want=%v list=%d", allowed, err, tc.want, spy.listCalls)
			}
		})
	}
}
