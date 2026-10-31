package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// The original PR #82 root proof is insufficient for a grandchild whose
// intermediate parent retries while the root remains on the same Attempt.
// Test both Host artifacts (without a RunID) and delegation Items, whose
// RunID refers to the child rather than the recorded Invocation owner.
func TestChildParentAttemptRemainsFrozenAfterIntermediateRetry(t *testing.T) {
	for _, kind := range []ItemKind{ItemArtifact, ItemDelegation, ItemInvocation} {
		t.Run(string(kind), func(t *testing.T) {
			store := NewMemoryStore()
			parent := rootBoundChildFixture(t, store, "nested-attempt-"+string(kind))
			parent.Status = InvocationRunning
			parent, _, err := store.CreateInvocation(t.Context(), parent)
			if err != nil {
				t.Fatal(err)
			}
			parentItem := Item{
				ID: "parent-attempt-item", TurnID: parent.TurnID,
				ParentItemID: parent.ParentItemID, InvocationID: parent.ID,
				Kind: kind, Status: ItemCompleted,
			}
			parentItem.RunID = map[ItemKind]string{
				ItemDelegation: "delegated-child-not-parent-run",
				ItemInvocation: parent.ExecutionRefID,
			}[kind]
			if kind == ItemDelegation {
				parentItem.Payload, err = json.Marshal(delegationItemPayload{
					ParentRunID: parent.ExecutionRefID, ChildRunID: parentItem.RunID,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := store.AppendItem(t.Context(), parentItem); err != nil {
				t.Fatal(err)
			}
			runner := &Runner{store: store}
			parents, err := runner.freezeChildParentAttempts(t.Context(), parent.TurnID, parentItem.ID, mustGetAuditRoot(t, store))
			if err != nil || len(parents) != 1 || parents[0] != invocationAttempt(parent) {
				t.Fatalf("incorrect frozen parent Attempt: %+v %v", parents, err)
			}
			raw, hash, err := marshalInvocationValue(struct {
				Goal string `json:"goal"`
			}{Goal: "grandchild"})
			if err != nil {
				t.Fatal(err)
			}
			input, hash, err := sealChildRootInput(raw, hash, mustGetAuditRoot(t, store), parents...)
			if err != nil {
				t.Fatal(err)
			}
			grandchild := Invocation{
				ID: "nested-grandchild", TurnID: parent.TurnID, ParentItemID: parentItem.ID,
				CapabilityKey: "app.nested", ExecutionClass: ExecutionApplication,
				Input: input, InputHash: hash, ExecutionRefID: "nested-child-attempt-1",
				Status: InvocationAccepted, Attempt: 1, Revision: 1,
				OutputRefs: []HostRef{}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := runner.requireLiveInvocationParents(t.Context(), grandchild); err != nil {
				t.Fatalf("live original parent rejected: %v", err)
			}
			if _, _, err := store.CreateInvocation(t.Context(), grandchild); err != nil {
				t.Fatal(err)
			}
			// Without the complete parent proof a child is never permitted,
			// even when every ancestor currently appears live.
			legacy := grandchild
			legacy.Input, legacy.InputHash, err = sealChildRootInput(raw, hashInvocationBytes(raw), mustGetAuditRoot(t, store))
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.requireLiveInvocationParents(t.Context(), legacy); !errors.Is(err, ErrConflict) {
				t.Fatalf("unbound ancestor authorized grandchild: %v", err)
			}

			parent.Status = InvocationFailed
			failed, err := store.UpdateInvocation(t.Context(), parent, parent.Revision)
			if err != nil {
				t.Fatal(err)
			}
			retried, err := store.RetryInvocation(t.Context(), parent.ID, failed.Revision, "parent-attempt-2", time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			retried.Status = InvocationRunning
			if _, err := store.UpdateInvocation(t.Context(), retried, retried.Revision); err != nil {
				t.Fatal(err)
			}
			if err := runner.requireLiveInvocationParents(t.Context(), grandchild); !errors.Is(err, ErrConflict) {
				t.Fatalf("old grandchild rebound to parent's new Attempt: %v", err)
			}
		})
	}
}

func mustGetAuditRoot(t *testing.T, store *MemoryStore) Invocation {
	t.Helper()
	root, err := store.GetInvocation(t.Context(), "indexed-root")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// A delegated child can inherit owners omitted from the last Item's chain.
// Its proof must bind both the direct owner and the transitive middle owner.
func TestNestedChildInheritsFrozenParentAttemptsAcrossDelegationItem(t *testing.T) {
	store := NewMemoryStore()
	owner := rootBoundChildFixture(t, store, "nested-inherited-owners")
	owner.Status = InvocationRunning
	if _, _, err := store.CreateInvocation(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	root := mustGetAuditRoot(t, store)
	raw, hash, err := marshalInvocationValue(struct {
		Goal string `json:"goal"`
	}{Goal: "delegator"})
	if err != nil {
		t.Fatal(err)
	}
	chain, hash, err := sealChildRootInput(raw, hash, root, invocationAttempt(owner))
	if err != nil {
		t.Fatal(err)
	}
	delegator := Invocation{
		ID: "delegator", TurnID: owner.TurnID, ParentItemID: "delegator-parent",
		CapabilityKey: "app.delegator", ExecutionClass: ExecutionApplication,
		Input: chain, InputHash: hash, ExecutionRefID: "delegator-run", Status: InvocationWaitingInput,
		Attempt: 1, Revision: 1, OutputRefs: []HostRef{},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if _, _, err := store.CreateInvocation(t.Context(), delegator); err != nil {
		t.Fatal(err)
	}
	delegationPayload, err := json.Marshal(delegationItemPayload{
		ParentRunID: delegator.ExecutionRefID, ChildRunID: "a-different-delegated-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AppendItem(t.Context(), Item{
		ID: "delegation-boundary", TurnID: owner.TurnID, InvocationID: delegator.ID,
		RunID: "a-different-delegated-run", Kind: ItemDelegation, Status: ItemStarted,
		Payload: delegationPayload,
	}); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{store: store}
	inherited, err := runner.freezeChildParentAttempts(t.Context(), owner.TurnID, "delegation-boundary", root)
	if err != nil || len(inherited) != 2 ||
		inherited[0] != invocationAttempt(delegator) || inherited[1] != invocationAttempt(owner) {
		t.Fatalf("incomplete transitive Attempt freeze: %+v %v", inherited, err)
	}
	childRaw, childHash, err := marshalInvocationValue(struct {
		Goal string `json:"goal"`
	}{Goal: "nested specialist"})
	if err != nil {
		t.Fatal(err)
	}
	childRaw, childHash, err = sealChildRootInput(childRaw, childHash, root, inherited...)
	if err != nil {
		t.Fatal(err)
	}
	grandchild := Invocation{ID: "nested-specialist", TurnID: owner.TurnID, ParentItemID: "delegation-boundary",
		Input: childRaw, InputHash: childHash, ExecutionRefID: "specialist-run-1", Attempt: 1}
	if err := runner.requireLiveInvocationParents(t.Context(), grandchild); err != nil {
		t.Fatalf("valid inherited parent proof rejected: %v", err)
	}
	owner.Status = InvocationFailed
	failed, err := store.UpdateInvocation(t.Context(), owner, owner.Revision)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryInvocation(t.Context(), owner.ID, failed.Revision, "owner-run-2", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	retried.Status = InvocationRunning
	if _, err := store.UpdateInvocation(t.Context(), retried, retried.Revision); err != nil {
		t.Fatal(err)
	}
	if err := runner.requireLiveInvocationParents(t.Context(), grandchild); !errors.Is(err, ErrConflict) {
		t.Fatalf("nested descendant rebound to intermediate owner's new Attempt: %v", err)
	}
}
