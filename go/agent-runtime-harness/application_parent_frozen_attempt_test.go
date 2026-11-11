package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// This fixture seeds immutable historical Item facts with original sequence
// numbers, then uses the real Store Retry/UpdateInvocation/AppendItem admission
// methods to validate a new parent Attempt. It is intentionally in the core
// package so the frozen harnessRoot authority can be tested without a weaker
// reimplementation of its private canonical encoding.
type frozenApplicationParentFixture struct {
	store       *MemoryStore
	root        Invocation
	child       Invocation
	interaction Interaction
	now         time.Time
}

func newFrozenApplicationParentFixture(t *testing.T, originalReturned, nested bool) frozenApplicationParentFixture {
	t.Helper()
	now := time.Now().UTC()
	const turnID, rootID, childID, interactionID = "frozen-turn", "frozen-root", "frozen-child", "frozen-interaction"
	rootInput := json.RawMessage(`{"goal":"original agent"}`)
	root := Invocation{
		ID: rootID, TurnID: turnID, CapabilityKey: CapabilityAgent,
		ExecutionClass: ExecutionAgent, ExecutionRefID: "original-agent-run",
		Input: rootInput, InputHash: hashInvocationBytes(rootInput),
		Status: InvocationRunning, Attempt: 1, Revision: 3,
		CreatedAt: now, UpdatedAt: now,
	}
	store := NewMemoryStore()
	store.turns[turnID] = Turn{ID: turnID, Status: TurnFailed, Revision: 3}
	store.invocationExecutionRefs[root.ExecutionRefID] = root.ID

	var parents []childAttemptBinding
	if nested {
		parentInput := json.RawMessage(`{"goal":"intermediate application"}`)
		sealed, digest, err := sealChildRootInput(parentInput, hashInvocationBytes(parentInput), root)
		if err != nil {
			t.Fatal(err)
		}
		ancestor := Invocation{
			ID: "intermediate-app", TurnID: turnID, ParentItemID: "intermediate-parent-item",
			ExecutionClass: ExecutionApplication, CapabilityKey: "app", Status: InvocationCompleted,
			ExecutionRefID: "original-intermediate-run", Attempt: 1, Revision: 2,
			Input: sealed, InputHash: digest, CreatedAt: now, UpdatedAt: now,
		}
		store.invocations[ancestor.ID] = ancestor
		store.invocationExecutionRefs[ancestor.ExecutionRefID] = ancestor.ID
		parents = append(parents, invocationAttempt(ancestor))
	}
	childInput := json.RawMessage(`{"goal":"original host callback"}`)
	sealed, digest, err := sealChildRootInput(childInput, hashInvocationBytes(childInput), root, parents...)
	if err != nil {
		t.Fatal(err)
	}
	child := Invocation{
		ID: childID, TurnID: turnID, ParentItemID: "original-parent-item",
		ExecutionClass: ExecutionApplication, CapabilityKey: "app",
		ExecutionRefID: "original-child-run", Status: InvocationCompleted,
		Input: sealed, InputHash: digest, Attempt: 1, Revision: 3,
		CreatedAt: now, UpdatedAt: now,
	}
	root.Status = InvocationFailed
	store.invocations[root.ID] = root
	store.invocations[child.ID] = child
	store.invocationExecutionRefs[child.ExecutionRefID] = child.ID
	interaction := Interaction{
		ID: interactionID, TurnID: turnID, InvocationID: child.ID, Status: InteractionResolved,
		Kind: InteractionChoice, Key: "decision", Schema: json.RawMessage(`{}`),
		Response: json.RawMessage(`{"ok":true}`),
	}
	store.interactions[interaction.ID] = interaction
	sourcePayload, err := interactionItemPayload(interaction)
	if err != nil {
		t.Fatal(err)
	}
	childPayload, err := invocationItemPayload(child)
	if err != nil {
		t.Fatal(err)
	}
	var items []Item
	appendItem := func(item Item) {
		item.Seq = uint64(len(items) + 1)
		item.CreatedAt, item.UpdatedAt = now, now
		items = append(items, item)
		store.itemIDs[item.ID] = item
		if item.ParentItemID != "" {
			store.itemChildren[item.ParentItemID] = append(store.itemChildren[item.ParentItemID], item)
		}
	}
	receipt := func(grant Item) Item {
		return Item{
			ID: stableID("hdispatchdone", grant.ID), TurnID: grant.TurnID,
			InvocationID: grant.InvocationID, RunID: grant.RunID,
			Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: grant.ID,
			Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		}
	}
	grant := func(id, phase string) Item {
		payload, _ := json.Marshal(struct {
			Phase         string `json:"phase"`
			PhysicalRunID string `json:"physicalRunID"`
		}{phase, root.ExecutionRefID})
		return Item{
			ID: id, TurnID: turnID, InvocationID: root.ID, RunID: root.ExecutionRefID,
			Kind: ItemDiagnostic, Status: ItemStarted,
			ParentItemID: ExecutionDispatchParentItemID, Payload: payload,
		}
	}
	if originalReturned {
		start := grant("hdispatch_original_agent_start", "start")
		appendItem(start)
		appendItem(receipt(start))
	}
	sourceID := stableID("hinteractionitem", interaction.ID, string(InteractionResolved))
	appendItem(Item{
		ID: sourceID, TurnID: turnID, InvocationID: child.ID,
		Kind: ItemInteraction, Status: ItemCompleted, Payload: sourcePayload,
	})
	appendItem(Item{
		ID: stableID("hbizverified", interaction.ID), TurnID: turnID,
		Kind: ItemDiagnostic, Status: ItemCompleted, InvocationID: child.ID,
		RunID: child.ExecutionRefID, ParentItemID: sourceID,
		Payload: json.RawMessage(`{"interactionID":"frozen-interaction","boundary":"host_response_verified"}`),
	})
	appendItem(Item{
		ID:     invocationLifecycleItemID(child, InvocationCompleted, child.Revision),
		TurnID: turnID, Kind: ItemInvocation, Status: ItemCompleted,
		InvocationID: child.ID, RunID: child.ExecutionRefID, Payload: childPayload,
	})
	if originalReturned {
		original := grant("hdispatch_original_agent_resume_after_child", "resume")
		appendItem(original)
		appendItem(receipt(original))
	}
	store.items[turnID] = items
	if !verifiedCompletedApplicationResponse(items, interaction, child) {
		t.Fatal("invalid original Host proof in fixture")
	}
	return frozenApplicationParentFixture{store: store, root: root, child: child, interaction: interaction, now: now}
}

func (fx frozenApplicationParentFixture) retryRoot(t *testing.T) Invocation {
	t.Helper()
	ctx := context.Background()
	if original, err := fx.store.GetInvocation(ctx, fx.root.ID); err != nil || original.Attempt != 1 {
		t.Fatalf("invalid original root: %v", err)
	}
	if fx.store.items[fx.root.TurnID][0].ParentItemID == ExecutionDispatchParentItemID {
		// An effectful original Start may retry only with the original,
		// matching terminal Kernel witness. The Store then owns the same
		// atomic Retry CAS as a real Runner.
		snapshot := kernel.Snapshot{Run: kernel.Run{ID: fx.root.ExecutionRefID, Status: kernel.RunStatusFailed}}
		var err error
		ctx, err = withOriginalKernelRetryWitness(ctx, fx.root, snapshot)
		if err != nil {
			t.Fatal(err)
		}
	}
	next, err := fx.store.RetryInvocation(ctx, fx.root.ID, fx.root.Revision, "new-agent-run", fx.now.Add(time.Second))
	if err != nil || next.Attempt != 2 || next.ExecutionRefID == fx.root.ExecutionRefID {
		t.Fatalf("real Store Retry rejected fixture: next=%+v err=%v", next, err)
	}
	next.Status = InvocationRunning
	next.UpdatedAt = fx.now.Add(2 * time.Second)
	next, err = fx.store.UpdateInvocation(context.Background(), next, next.Revision)
	if err != nil {
		t.Fatalf("real Store Accepted->Running failed: %v", err)
	}
	return next
}

func (fx frozenApplicationParentFixture) appendReturnedResume(t *testing.T, runID, id string) {
	t.Helper()
	payload, err := json.Marshal(struct {
		Phase         string `json:"phase"`
		PhysicalRunID string `json:"physicalRunID"`
	}{"resume", runID})
	if err != nil {
		t.Fatal(err)
	}
	grant, fresh, err := fx.store.AppendItem(context.Background(), Item{
		ID: id, TurnID: fx.root.TurnID, InvocationID: fx.root.ID, RunID: runID,
		ParentItemID: ExecutionDispatchParentItemID, Kind: ItemDiagnostic,
		Status: ItemStarted, Payload: payload, CreatedAt: fx.now, UpdatedAt: fx.now,
	})
	if err != nil || !fresh {
		t.Fatalf("real Store Resume grant: fresh=%v err=%v", fresh, err)
	}
	_, fresh, err = fx.store.AppendItem(context.Background(), Item{
		ID: stableID("hdispatchdone", id), TurnID: fx.root.TurnID,
		InvocationID: fx.root.ID, RunID: runID, ParentItemID: id,
		Kind: ItemDiagnostic, Status: ItemCompleted,
		Payload:   json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		CreatedAt: fx.now, UpdatedAt: fx.now,
	})
	if err != nil || !fresh {
		t.Fatalf("real Store Resume receipt: fresh=%v err=%v", fresh, err)
	}
	receipts := executionDispatchReceiptIndex(fx.store.items[fx.root.TurnID])
	if boundary, ok := VerifiedExecutionDispatchReceipt(grant, receipts[id]); !ok || boundary != "physical_dispatch_returned" {
		t.Fatal("Store did not admit a complete original-bound physical receipt")
	}
}

func (fx frozenApplicationParentFixture) observe() error {
	return (&Runner{store: fx.store}).requireObservedApplicationParentContinuation(
		context.Background(), fx.interaction, fx.child,
	)
}

func TestApplicationParentFrozenAttemptUsesOriginalReceiptAfterRealStoreRetry(t *testing.T) {
	t.Run("new-Attempt-cannot-settle-old-child", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, false, false)
		next := fx.retryRoot(t)
		if got := fx.observe(); !errors.Is(got, ErrApplicationParentContinuationUnverified) {
			t.Fatalf("without original continuation must remain unverified: %v", got)
		}
		fx.appendReturnedResume(t, next.ExecutionRefID, "hdispatch_new_attempt_resume")
		if got := fx.observe(); !errors.Is(got, ErrConflict) {
			t.Fatalf("new Attempt illegally authorized original child: %v", got)
		}
	})

	t.Run("original-receipt-survives-new-Attempt", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		next := fx.retryRoot(t)
		if got := fx.observe(); got != nil {
			t.Fatalf("valid historical original receipt lost after root Retry: %v", got)
		}
		fx.appendReturnedResume(t, next.ExecutionRefID, "hdispatch_unrelated_future_resume")
		if got := fx.observe(); got != nil {
			t.Fatalf("unrelated later Attempt invalidated complete original proof: %v", got)
		}
	})

	t.Run("rotated-root-reusing-original-Run-rejected", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		fx.retryRoot(t)
		rotated := fx.store.invocations[fx.root.ID]
		rotated.ExecutionRefID = fx.root.ExecutionRefID
		fx.store.invocations[rotated.ID] = rotated
		if got := fx.observe(); !errors.Is(got, ErrConflict) {
			t.Fatalf("new Attempt reused original physical Run: %v", got)
		}
	})
	t.Run("same-root-Attempt-retargeted-rejected", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		root := fx.store.invocations[fx.root.ID]
		root.ExecutionRefID = "other-run-with-same-Attempt"
		fx.store.invocations[root.ID] = root
		if got := fx.observe(); !errors.Is(got, ErrConflict) {
			t.Fatalf("original Attempt retargeted to different Run: %v", got)
		}
	})
	t.Run("frozen-nested-ancestor-succeeds", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, true)
		fx.retryRoot(t)
		if got := fx.observe(); got != nil {
			t.Fatalf("valid nested ancestor proof rejected: %v", got)
		}
	})
	t.Run("corrupt-nested-ancestor-rejected", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, true)
		fx.retryRoot(t)
		ancestor := fx.store.invocations["intermediate-app"]
		ancestor.Input = json.RawMessage(`{"other":"corrupted"}`)
		fx.store.invocations[ancestor.ID] = ancestor
		if got := fx.observe(); !errors.Is(got, ErrConflict) {
			t.Fatalf("corrupt intermediate ancestor falsely authorized: %v", got)
		}
	})
	t.Run("ancestor-run-retargeted-without-new-Attempt-rejected", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, true)
		fx.retryRoot(t)
		ancestor := fx.store.invocations["intermediate-app"]
		ancestor.ExecutionRefID = "retargeted-intermediate-run"
		fx.store.invocations[ancestor.ID] = ancestor
		if got := fx.observe(); !errors.Is(got, ErrConflict) {
			t.Fatalf("retargeted current intermediate Run falsely authorized: %v", got)
		}
	})
	t.Run("ancestor-later-Attempt-preserves-original-receipt", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, true)
		fx.retryRoot(t)
		ancestor := fx.store.invocations["intermediate-app"]
		ancestor.Attempt++
		ancestor.ExecutionRefID = "later-intermediate-run"
		fx.store.invocations[ancestor.ID] = ancestor
		if got := fx.observe(); got != nil {
			t.Fatalf("later ancestor Attempt erased valid original receipt: %v", got)
		}
	})
	t.Run("ancestor-later-Attempt-reusing-original-Run-rejected", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, true)
		fx.retryRoot(t)
		ancestor := fx.store.invocations["intermediate-app"]
		ancestor.Attempt++
		fx.store.invocations[ancestor.ID] = ancestor
		if got := fx.observe(); !errors.Is(got, ErrConflict) {
			t.Fatalf("ancestor's later Attempt reused old Run: %v", got)
		}
	})
	t.Run("malformed-child-seal-rejected", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		fx.retryRoot(t)
		fx.child.Input = json.RawMessage(`{"tampered":true}`)
		if got := fx.observe(); !errors.Is(got, ErrConflict) {
			t.Fatalf("tampered original seal falsely authorized: %v", got)
		}
	})
}

func FuzzApplicationParentFrozenOriginalReceipt(f *testing.F) {
	for _, mask := range []uint8{0, 1, 2, 4, 8, 16, 31, 255} {
		f.Add(mask)
	}
	f.Fuzz(func(t *testing.T, mask uint8) {
		hasOriginal := mask&1 != 0
		nested := mask&2 != 0
		fx := newFrozenApplicationParentFixture(t, hasOriginal, nested)
		next := fx.retryRoot(t)
		if mask&4 != 0 {
			fx.appendReturnedResume(t, next.ExecutionRefID, "hdispatch_fuzz_new_attempt")
		}
		if mask&8 != 0 {
			fx.child.Input = json.RawMessage(`{"tampered":true}`)
		}
		if mask&16 != 0 && nested {
			ancestor := fx.store.invocations["intermediate-app"]
			ancestor.InputHash = "invalid"
			fx.store.invocations[ancestor.ID] = ancestor
		}
		got := fx.observe()
		if mask&8 != 0 || mask&16 != 0 && nested {
			if !errors.Is(got, ErrConflict) {
				t.Fatalf("corrupt original source accepted (mask=%d): %v", mask, got)
			}
			return
		}
		if hasOriginal {
			if got != nil {
				t.Fatalf("correct original receipt rejected after later Retry (mask=%d): %v", mask, got)
			}
		} else if mask&4 != 0 {
			if !errors.Is(got, ErrConflict) {
				t.Fatalf("foreign Attempt receipt accepted (mask=%d): %v", mask, got)
			}
		} else if !errors.Is(got, ErrApplicationParentContinuationUnverified) {
			t.Fatalf("missing receipt was not fenced (mask=%d): %v", mask, got)
		}
	})
}
