package harness

import (
	"errors"
	"fmt"
	"testing"
)

// The admission-time parent lookup must share the indexed Store contract
// already used by the physical-dispatch ancestor fence, rather than scanning
// a whole Turn before the execution grant exists.
func TestChildParentValidationUsesExactIndexedItem(t *testing.T) {
	const turnID = "indexed-child-admission"
	backing := NewMemoryStore()
	for i := range 2001 {
		if _, _, err := backing.AppendItem(t.Context(), Item{
			ID: fmt.Sprintf("unrelated-%05d", i), TurnID: turnID,
			Kind: ItemArtifact, Status: ItemCompleted,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := backing.AppendItem(t.Context(), Item{
		ID: "actual-parent", TurnID: turnID,
		Kind: ItemInvocation, Status: ItemStarted,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backing.AppendItem(t.Context(), Item{
		ID: "foreign-parent", TurnID: "another-turn",
		Kind: ItemInvocation, Status: ItemStarted,
	}); err != nil {
		t.Fatal(err)
	}
	spy := &indexOnlyParentStore{Store: backing}
	runner := &Runner{store: spy}
	if err := runner.validateParentItem(t.Context(), turnID, "actual-parent"); err != nil {
		t.Fatalf("indexed valid parent rejected: %v", err)
	}
	if spy.listCalls != 0 || len(spy.itemCalls) != 1 || spy.itemCalls[0] != "actual-parent" {
		t.Fatalf("parent admission scanned unrelated Items: lists=%d gets=%v", spy.listCalls, spy.itemCalls)
	}
	for _, invalid := range []string{"missing-parent", "foreign-parent"} {
		if err := runner.validateParentItem(t.Context(), turnID, invalid); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid parent %q accepted or wrong error: %v", invalid, err)
		}
	}
	if spy.listCalls != 0 || len(spy.itemCalls) != 3 {
		t.Fatalf("negative parent lookups used Timeline scan: lists=%d gets=%v", spy.listCalls, spy.itemCalls)
	}
	// Root interactions may deliberately omit a parent Item; the child-only
	// entry points, not this shared validator, enforce child parent presence.
	if err := runner.validateParentItem(t.Context(), turnID, ""); err != nil {
		t.Fatalf("root interaction without ParentItemID rejected: %v", err)
	}
	if len(spy.itemCalls) != 3 {
		t.Fatalf("empty optional parent performed Store I/O: %#v", spy.itemCalls)
	}
}
