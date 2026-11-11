package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Admit one deliberately malformed historical child of a globally unique
// Dispatch Grant. No modern first-party AppendItem can create this row; the
// reference Store's global indexes model the exact persisted corruption that
// SQL can also contain. All public decision paths must agree that the original
// physical Resume is unresolved, independent of the child's bogus Turn.
func addForeignGlobalDispatchSibling(t *testing.T, fx frozenApplicationParentFixture, kind ItemKind, differentOwner bool) {
	t.Helper()
	const grantID = "hdispatch_original_agent_resume_after_child"
	existing, err := fx.store.ListItemChildren(t.Context(), grantID, ItemChildProofAll)
	if err != nil || len(existing) != 1 {
		t.Fatalf("expected original canonical physical receipt: count=%d err=%v", len(existing), err)
	}
	frozen := existing[0]
	frozen.ID = "historical_global_conflicting_dispatch_receipt"
	frozen.TurnID = "historically_damaged_other_turn"
	frozen.Kind = kind
	frozen.Seq = 1
	if differentOwner {
		frozen.InvocationID = "different_invocation"
		frozen.RunID = "different_run"
	}
	fx.store.items[frozen.TurnID] = append(fx.store.items[frozen.TurnID], frozen)
	fx.store.itemIDs[frozen.ID] = frozen
	fx.store.itemChildren[grantID] = append(fx.store.itemChildren[grantID], frozen)
	grant, err := fx.store.GetItem(t.Context(), grantID)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := fx.store.ListItemChildren(t.Context(), grantID, ItemChildProofAll)
	if err != nil || len(complete) != 2 {
		t.Fatalf("incomplete global physical proof: count=%d err=%v", len(complete), err)
	}
	if _, settled := VerifiedExecutionDispatchReceipt(grant, complete); settled {
		t.Fatal("invalid test fixture: typed original physical proof is settled")
	}
}

func retryOriginalRootWithWitness(ctx context.Context, fx frozenApplicationParentFixture, nextRunID string) (Invocation, error) {
	original, err := fx.store.GetInvocation(ctx, fx.root.ID)
	if err != nil {
		return Invocation{}, err
	}
	ctx, err = withOriginalKernelRetryWitness(ctx, original, kernel.Snapshot{
		Run: kernel.Run{ID: original.ExecutionRefID, Status: kernel.RunStatusFailed},
	})
	if err != nil {
		return Invocation{}, err
	}
	return fx.store.RetryInvocation(ctx, original.ID, original.Revision, nextRunID, time.Now().UTC())
}

// This is the original independent review's three RED cases, plus terminal
// Invocation and Turn CAS variants. A shared globally complete proof rather
// than per-method exceptions must make every one fail closed.
func TestGlobalOriginalDispatchEvidenceFencesEveryMemoryAdmission(t *testing.T) {
	for _, kind := range []ItemKind{ItemArtifact, ItemDiagnostic} {
		for _, differentOwner := range []bool{false, true} {
			name := string(kind)
			if differentOwner {
				name += "-foreign-owner-run"
			}
			t.Run(name, func(t *testing.T) {
				t.Run("RetryInvocation", func(t *testing.T) {
					fx := newFrozenApplicationParentFixture(t, true, false)
					addForeignGlobalDispatchSibling(t, fx, kind, differentOwner)
					_, err := retryOriginalRootWithWitness(t.Context(), fx, "attempt2")
					if !errors.Is(err, ErrConflict) {
						t.Fatalf("global original physical contradiction authorized Retry: %v", err)
					}
				})
				t.Run("new-physical-Resume-grant", func(t *testing.T) {
					fx := newFrozenApplicationParentFixture(t, true, false)
					next := fx.retryRoot(t)
					addForeignGlobalDispatchSibling(t, fx, kind, differentOwner)
					grant := Item{
						ID: "new_grant_after_global_contradiction", TurnID: fx.root.TurnID,
						Kind: ItemDiagnostic, Status: ItemStarted,
						InvocationID: next.ID, RunID: next.ExecutionRefID,
						ParentItemID: ExecutionDispatchParentItemID,
						Payload:      json.RawMessage(`{"phase":"resume","physicalRunID":"new-agent-run"}`),
						CreatedAt:    fx.now, UpdatedAt: fx.now,
					}
					_, created, err := fx.store.AppendItem(t.Context(), grant)
					if !errors.Is(err, ErrConflict) || created {
						t.Fatalf("globally contradictory grant authorized new execution: created=%v err=%v", created, err)
					}
				})
				t.Run("terminal-Invocation-CAS", func(t *testing.T) {
					fx := newFrozenApplicationParentFixture(t, true, false)
					next := fx.retryRoot(t)
					addForeignGlobalDispatchSibling(t, fx, kind, differentOwner)
					ctx, err := withOriginalKernelTerminalWitness(t.Context(), next, kernel.Snapshot{
						Run: kernel.Run{ID: next.ExecutionRefID, Status: kernel.RunStatusFailed},
					})
					if err != nil {
						t.Fatal(err)
					}
					next.Status = InvocationFailed
					next.UpdatedAt = fx.now.Add(time.Minute)
					_, err = fx.store.UpdateInvocation(ctx, next, next.Revision)
					if !errors.Is(err, ErrConflict) {
						t.Fatalf("terminal Invocation incorrectly hid original contradictory effect: %v", err)
					}
				})
				t.Run("terminal-Turn-CAS", func(t *testing.T) {
					fx := newFrozenApplicationParentFixture(t, true, false)
					// The earlier Retry fixture deliberately seeds a minimal
					// Turn. Store.UpdateTurn additionally validates its full
					// Host identity, so provide a legitimate original envelope.
					turnEnvelope := fx.store.turns[fx.root.TurnID]
					turnEnvelope.SessionID = "frozen-session"
					turnEnvelope.HostTurn = HostRef{Kind: "conversation_turn", ID: "frozen-host-turn"}
					turnEnvelope.ConfigSnapshotID = "frozen-config"
					turnEnvelope.CreatedAt = fx.now
					turnEnvelope.UpdatedAt = fx.now
					fx.store.turns[turnEnvelope.ID] = turnEnvelope
					next := fx.retryRoot(t)
					ctx, err := withOriginalKernelTerminalWitness(t.Context(), next, kernel.Snapshot{
						Run: kernel.Run{ID: next.ExecutionRefID, Status: kernel.RunStatusFailed},
					})
					if err != nil {
						t.Fatal(err)
					}
					failed := next
					failed.Status = InvocationFailed
					failed.UpdatedAt = fx.now.Add(time.Minute)
					failed, err = fx.store.UpdateInvocation(ctx, failed, failed.Revision)
					if err != nil {
						t.Fatalf("baseline terminal Invocation without corruption: %v", err)
					}
					turn := fx.store.turns[fx.root.TurnID]
					turn.Status = TurnFailed
					turn.UpdatedAt = fx.now.Add(time.Minute)
					ctx, err = withOriginalKernelTerminalWitness(t.Context(), failed, kernel.Snapshot{
						Run: kernel.Run{ID: failed.ExecutionRefID, Status: kernel.RunStatusFailed},
					})
					if err != nil {
						t.Fatal(err)
					}
					addForeignGlobalDispatchSibling(t, fx, kind, differentOwner)
					_, err = fx.store.UpdateTurn(ctx, turn, turn.Revision)
					if !errors.Is(err, ErrConflict) {
						t.Fatalf("terminal Turn hid contradictory original physical proof: %v", err)
					}
				})
				t.Run("Runner-cancel-pending", func(t *testing.T) {
					fx := newFrozenApplicationParentFixture(t, true, false)
					addForeignGlobalDispatchSibling(t, fx, kind, differentOwner)
					pending, err := (&Runner{store: fx.store}).pendingExecutionDispatch(
						t.Context(), fx.root.TurnID, nil, nil,
					)
					if err != nil || !pending {
						t.Fatalf("read-only cancellation guard considered unknown original effect settled: pending=%v err=%v", pending, err)
					}
				})
			})
		}
	}
}

func TestGlobalDispatchReceiptReaderRejectsSplitBrainAndPreservesCanonicalEvidence(t *testing.T) {
	fx := newFrozenApplicationParentFixture(t, true, false)
	// The entire old Turn is still internally valid and must not be denied
	// merely because its physical dispatch parent has a global index.
	got, err := (&Runner{store: fx.store}).loadGlobalPhysicalEvidence(t.Context(), fx.root.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	seen := executionDispatchReceiptIndex(got)
	grant, err := fx.store.GetItem(t.Context(), "hdispatch_original_agent_resume_after_child")
	if err != nil {
		t.Fatal(err)
	}
	if boundary, valid := VerifiedExecutionDispatchReceipt(grant, seen[grant.ID]); !valid || boundary != "physical_dispatch_returned" {
		t.Fatalf("valid original grant unexpectedly became unknown: boundary=%s valid=%v", boundary, valid)
	}
	// A Store returning different immutable Seq for one globally indexed
	// receipt cannot be trusted as an original physical execution source.
	local := fx.store.items[fx.root.TurnID]
	globalReceipt, err := fx.store.ListItemChildren(t.Context(), grant.ID, ItemChildProofAll)
	if err != nil || len(globalReceipt) != 1 {
		t.Fatal(err)
	}
	faulty := globalReceipt[0]
	faulty.Seq++
	_, err = completeGlobalPhysicalReceiptFacts(local, func(id string) ([]Item, error) {
		if id == grant.ID {
			return []Item{faulty}, nil
		}
		return fx.store.ListItemChildren(context.Background(), id, ItemChildProofAll)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("disagreeing per-Turn and global immutable receipts were accepted: %v", err)
	}
	if _, err = completeGlobalPhysicalReceiptFacts(local, func(string) ([]Item, error) {
		return []Item{{ID: "one", ParentItemID: grant.ID}, {ID: "two", ParentItemID: grant.ID}, {ID: "three", ParentItemID: grant.ID}}, nil
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unbounded global parent reader bypassed proof contract: %v", err)
	}
}

// Fuzz the original operation × admission boundary × corruption class rather
// than only the recovery endpoint that first exposed the flaw. The Store
// must never turn a second historical child into permission for new effects.
func FuzzGlobalOriginalDispatchAdmission(f *testing.F) {
	for _, mask := range []uint8{0, 1, 2, 3, 4, 5, 7, 8, 12, 16, 31, 63, 127, 255} {
		f.Add(mask)
	}
	f.Fuzz(func(t *testing.T, mask uint8) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		kind := ItemDiagnostic
		if mask&4 != 0 {
			kind = ItemArtifact
		}
		if mask&3 == 0 {
			// One canonical returned physical receipt is real authority.
			if _, err := retryOriginalRootWithWitness(t.Context(), fx, "fuzz_new_run"); err != nil {
				t.Fatalf("positive original return rejected (mask=%d): %v", mask, err)
			}
			return
		}
		var root Invocation
		if mask&3 != 1 {
			root = fx.retryRoot(t)
		}
		addForeignGlobalDispatchSibling(t, fx, kind, mask&8 != 0)
		switch mask & 3 {
		case 1:
			_, err := retryOriginalRootWithWitness(t.Context(), fx, "fuzz_new_run")
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("wrong global proof authorized Retry (mask=%d): %v", mask, err)
			}
		case 2:
			value := Item{
				ID: "fuzz_second_physical_grant", TurnID: fx.root.TurnID,
				ParentItemID: ExecutionDispatchParentItemID,
				Kind:         ItemDiagnostic, Status: ItemStarted,
				InvocationID: root.ID, RunID: root.ExecutionRefID,
				Payload:   json.RawMessage(`{"phase":"resume","physicalRunID":"new-agent-run"}`),
				CreatedAt: fx.now, UpdatedAt: fx.now,
			}
			_, created, err := fx.store.AppendItem(t.Context(), value)
			if created || !errors.Is(err, ErrConflict) {
				t.Fatalf("wrong global proof authorized new physical Grant (mask=%d): created=%v err=%v", mask, created, err)
			}
		case 3:
			pending, err := (&Runner{store: fx.store}).pendingExecutionDispatch(
				t.Context(), fx.root.TurnID, nil, nil,
			)
			if err != nil || !pending {
				t.Fatalf("wrong global proof settled cancellation guard (mask=%d): pending=%v err=%v", mask, pending, err)
			}
		}
	})
}
