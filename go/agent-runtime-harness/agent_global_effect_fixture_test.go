package harness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// These first-party Agent facts test the global Store admission boundary.
// The retired Host Application interpreter is not necessary to establish
// the original Start/Resume grant and its immutable return receipt.
type frozenAgentEffectFixture struct {
	store *MemoryStore
	root  Invocation
	now   time.Time
}

func newFrozenAgentEffectFixture(t *testing.T) frozenAgentEffectFixture {
	t.Helper()
	now := time.Now().UTC()
	const turnID = "frozen-turn"
	input := json.RawMessage(`{"goal":"original agent"}`)
	root := Invocation{
		ID: "frozen-root", TurnID: turnID, ExecutionRefID: "original-agent-run",
		Input: input, InputHash: hashInvocationBytes(input),
		Status: InvocationFailed, Attempt: 1, Revision: 3,
		CreatedAt: now, UpdatedAt: now,
	}
	store := NewMemoryStore()
	store.turns[turnID] = Turn{ID: turnID, Status: TurnFailed, Revision: 3}
	store.invocations[root.ID] = root
	store.invocationExecutionRefs[root.ExecutionRefID] = root.ID

	appendItem := func(item Item) {
		item.Seq = uint64(len(store.items[turnID]) + 1)
		item.CreatedAt, item.UpdatedAt = now, now
		store.items[turnID] = append(store.items[turnID], item)
		store.itemIDs[item.ID] = item
		if item.ParentItemID != "" {
			store.itemChildren[item.ParentItemID] = append(store.itemChildren[item.ParentItemID], item)
		}
	}
	for _, phase := range []string{"start", "resume"} {
		id := "hdispatch_original_agent_" + phase
		payload, err := json.Marshal(struct {
			Phase         string `json:"phase"`
			PhysicalRunID string `json:"physicalRunID"`
		}{Phase: phase, PhysicalRunID: root.ExecutionRefID})
		if err != nil {
			t.Fatal(err)
		}
		grant := Item{
			ID: id, TurnID: turnID, InvocationID: root.ID, RunID: root.ExecutionRefID,
			Kind: ItemDiagnostic, Status: ItemStarted,
			ParentItemID: ExecutionDispatchParentItemID, Payload: payload,
		}
		appendItem(grant)
		appendItem(Item{
			ID: stableID("hdispatchdone", grant.ID), TurnID: turnID,
			InvocationID: grant.InvocationID, RunID: grant.RunID,
			Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: grant.ID,
			Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		})
	}
	facts, err := (&Runner{store: store}).loadGlobalPhysicalEvidence(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	receipts := executionDispatchReceiptIndex(facts)
	for _, grantID := range []string{"hdispatch_original_agent_start", "hdispatch_original_agent_resume"} {
		grant, err := store.GetItem(t.Context(), grantID)
		if err != nil {
			t.Fatal(err)
		}
		if boundary, ok := VerifiedExecutionDispatchReceipt(grant, receipts[grant.ID]); !ok || boundary != "physical_dispatch_returned" {
			t.Fatalf("invalid original first-party evidence: grant=%s boundary=%s ok=%v", grantID, boundary, ok)
		}
	}
	return frozenAgentEffectFixture{store: store, root: root, now: now}
}

func (fx frozenAgentEffectFixture) retryRoot(t *testing.T) Invocation {
	t.Helper()
	ctx, err := withOriginalKernelRetryWitness(t.Context(), fx.root, kernel.Snapshot{
		Run: kernel.Run{ID: fx.root.ExecutionRefID, Status: kernel.RunStatusFailed},
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := fx.store.RetryInvocation(ctx, fx.root.ID, fx.root.Revision, "new-agent-run", fx.now.Add(time.Second))
	if err != nil || next.Attempt != 2 || next.ExecutionRefID == fx.root.ExecutionRefID {
		t.Fatalf("original Agent retry failed: %+v err=%v", next, err)
	}
	next.Status = InvocationRunning
	next.UpdatedAt = fx.now.Add(2 * time.Second)
	next, err = fx.store.UpdateInvocation(context.Background(), next, next.Revision)
	if err != nil {
		t.Fatalf("Accepted-to-Running Agent retry denied: %v", err)
	}
	return next
}
