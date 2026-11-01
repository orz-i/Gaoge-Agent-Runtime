package harness

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPR82PhysicalDispatchSettlementRequiresUniqueOwnerBoundReceipt(t *testing.T) {
	grant := Item{
		ID: "dispatch-1", TurnID: "turn-1", InvocationID: "owner-1", RunID: "run-1",
		Kind: ItemDiagnostic, Status: ItemStarted, ParentItemID: ExecutionDispatchParentItemID,
		Seq: 10, Payload: json.RawMessage(`{"phase":"start"}`),
	}
	valid := Item{
		ID: "settled-1", TurnID: grant.TurnID, InvocationID: grant.InvocationID,
		RunID: grant.RunID, ParentItemID: grant.ID, Seq: 11,
		Kind: ItemDiagnostic, Status: ItemCompleted,
		Payload: json.RawMessage(`{"boundary":"physical_dispatch_rejected"}`),
	}
	physical := valid
	physical.ID = "physical-executed"
	physical.Payload = json.RawMessage(`{"boundary":"physical_dispatch_returned"}`)
	tests := []struct {
		name       string
		receipts   []Item
		wantClosed bool
	}{
		{"definitive-no-effect", []Item{valid}, true},
		{"effectful-return", []Item{physical}, true},
		{"missing-receipt", nil, false},
		{"unknown-owner", []Item{func() Item { v := valid; v.InvocationID = "other-owner"; return v }()}, false},
		{"unknown-run", []Item{func() Item { v := valid; v.RunID = "other-run"; return v }()}, false},
		{"unknown-turn", []Item{func() Item { v := valid; v.TurnID = "other-turn"; return v }()}, false},
		{"unknown-status", []Item{func() Item { v := valid; v.Status = ItemFailed; return v }()}, false},
		{"unknown-boundary", []Item{func() Item { v := valid; v.Payload = json.RawMessage(`{"boundary":"unknown"}`); return v }()}, false},
		{"receipt-before-grant", []Item{func() Item { v := valid; v.Seq = grant.Seq - 1; return v }()}, false},
		{"conflicting-receipts", []Item{physical, valid}, false},
		{"duplicate-no-effect-receipts", []Item{valid, func() Item { v := valid; v.ID = "another-completion"; v.Seq++; return v }()}, false},
		{"foreign-and-valid", []Item{valid, func() Item { v := valid; v.ID = "foreign-completion"; v.InvocationID = "other"; return v }()}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, closed := VerifiedExecutionDispatchReceipt(grant, tc.receipts)
			if closed != tc.wantClosed {
				t.Fatalf("verified receipt=%t, want=%t", closed, tc.wantClosed)
			}
			items := append([]Item{grant}, tc.receipts...)
			if pending := PendingExecutionDispatch(items, grant.InvocationID); pending == tc.wantClosed {
				t.Fatalf("pending=%t contradicts verified closure=%t", pending, tc.wantClosed)
			}
			// A terminal composite can Retry ONLY from the sole trustworthy
			// no-effect receipt, never from an effectful or contradictory one.
			rotating := Invocation{
				ID: grant.InvocationID, ExecutionRefID: grant.RunID,
				ExecutionClass: ExecutionTeam, Status: InvocationFailed,
			}
			err := RetryChildEffectFence(nil, items, rotating)
			wantSafe := tc.name == "definitive-no-effect"
			if wantSafe && err != nil {
				t.Fatalf("first-party no-effect was rejected: %v", err)
			}
			if !wantSafe && !errors.Is(err, ErrConflict) {
				t.Fatalf("unsafe composite Retry authorized: %v", err)
			}
		})
	}
}

func TestPR82OwnedRunlessItemCannotGrantNewPhysicalAttempt(t *testing.T) {
	runner := &Runner{}
	owner := childAttemptBinding{InvocationID: "root", ExecutionRefID: "root-attempt-two", Attempt: 2}
	for _, kind := range []ItemKind{ItemTool, ItemArtifact, ItemInteraction, ItemContext, ItemDiagnostic, ItemInvocation} {
		item := Item{ID: "old-" + string(kind), InvocationID: owner.InvocationID, Kind: kind}
		if err := runner.requireItemOwnerAttempt(t.Context(), item, owner); !errors.Is(err, ErrConflict) {
			t.Fatalf("owner-stamped %s without RunID crossed Attempt: %v", kind, err)
		}
		item.InvocationID = "" // a genuinely Host-owned parent has no runtime owner
		if err := runner.requireItemOwnerAttempt(t.Context(), item, owner); err != nil {
			t.Fatalf("unowned Host fact %s rejected: %v", kind, err)
		}
	}
}
