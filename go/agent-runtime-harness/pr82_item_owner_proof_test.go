package harness

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

var _ RunRelationSource = (*runrelation.Registry)(nil)

func TestPR82DelegationItemUsesFrozenParentRunNotChildRun(t *testing.T) {
	owner := childAttemptBinding{InvocationID: "owner", ExecutionRefID: "parent-attempt-2", Attempt: 2}
	runner := &Runner{}
	payload, err := json.Marshal(delegationItemPayload{ParentRunID: "parent-attempt-1", ChildRunID: "delegated-child-1"})
	if err != nil {
		t.Fatal(err)
	}
	item := Item{Kind: ItemDelegation, RunID: "delegated-child-1", Payload: payload}
	if err := runner.requireItemOwnerAttempt(t.Context(), item, owner); !errors.Is(err, ErrConflict) {
		t.Fatalf("old Delegation parent Run crossed Attempt: %v", err)
	}
	if err := runner.requireItemOwnerAttempt(t.Context(), Item{Kind: ItemDelegation, RunID: "delegated-child-1"}, owner); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing frozen Delegation origin was accepted: %v", err)
	}
	payload, err = json.Marshal(delegationItemPayload{ParentRunID: owner.ExecutionRefID, ChildRunID: item.RunID})
	if err != nil {
		t.Fatal(err)
	}
	item.Payload = payload
	if err := runner.requireItemOwnerAttempt(t.Context(), item, owner); err != nil {
		t.Fatalf("current Delegation parent Run rejected: %v", err)
	}
}
