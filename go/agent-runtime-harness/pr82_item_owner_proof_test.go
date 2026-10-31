package harness

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestPR82ExecutionItemOriginFencesStaleAttemptsAndAllowsNestedRun(t *testing.T) {
	const ownerRunID = "root-attempt-2"
	owner := childAttemptBinding{InvocationID: "root", ExecutionRefID: ownerRunID, Attempt: 2}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{relationReader: relations}
	for _, kind := range []ItemKind{ItemTool, ItemAgentMessage, ItemArtifact, ItemApproval, ItemContext, ItemDiagnostic} {
		old := Item{ID: "old", InvocationID: owner.InvocationID, Kind: kind, RunID: "root-attempt-1"}
		if err := runner.requireItemOwnerAttempt(t.Context(), old, owner); !errors.Is(err, ErrConflict) {
			t.Fatalf("old physical fact %s was accepted under current owner: %v", kind, err)
		}
		old.RunID = ownerRunID
		if err := runner.requireItemOwnerAttempt(t.Context(), old, owner); err != nil {
			t.Fatalf("live physical fact %s rejected: %v", kind, err)
		}
	}
	// A nested Feature's Tool uses its own RunID, but an indexed immutable
	// RunRelation may prove that it belongs to the current owner's Attempt.
	if _, err := relations.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: ownerRunID, ChildRunID: "nested-current",
		Kind: runrelation.KindWorkflowEffect, OwnerNodeID: "effect-2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := runner.requireItemOwnerAttempt(t.Context(), Item{
		Kind: ItemTool, RunID: "nested-current",
	}, owner); err != nil {
		t.Fatalf("legitimately related nested Run was rejected: %v", err)
	}
	if _, err := relations.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: "root-attempt-1", ChildRunID: "nested-old",
		Kind: runrelation.KindWorkflowEffect, OwnerNodeID: "effect-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := runner.requireItemOwnerAttempt(t.Context(), Item{
		Kind: ItemTool, RunID: "nested-old",
	}, owner); !errors.Is(err, ErrConflict) {
		t.Fatalf("previous Attempt's nested Run was accepted: %v", err)
	}
}

func TestPR82DelegationItemUsesFrozenParentRunNotChildRun(t *testing.T) {
	owner := childAttemptBinding{InvocationID: "owner", ExecutionRefID: "parent-attempt-2", Attempt: 2}
	runner := &Runner{}
	payload, err := json.Marshal(delegationItemPayload{
		ParentRunID: "parent-attempt-1", ChildRunID: "delegated-child-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	item := Item{Kind: ItemDelegation, RunID: "delegated-child-1", Payload: payload}
	if err := runner.requireItemOwnerAttempt(t.Context(), item, owner); !errors.Is(err, ErrConflict) {
		t.Fatalf("old Delegation parent Run crossed Attempt: %v", err)
	}
	if err := runner.requireItemOwnerAttempt(t.Context(), Item{
		Kind: ItemDelegation, RunID: "delegated-child-1",
	}, owner); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing frozen Delegation origin was accepted: %v", err)
	}
	payload, err = json.Marshal(delegationItemPayload{
		ParentRunID: owner.ExecutionRefID, ChildRunID: item.RunID,
	})
	if err != nil {
		t.Fatal(err)
	}
	item.Payload = payload
	if err := runner.requireItemOwnerAttempt(t.Context(), item, owner); err != nil {
		t.Fatalf("current Delegation parent Run rejected: %v", err)
	}
}
