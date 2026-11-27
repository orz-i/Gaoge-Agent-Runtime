package harness

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestManagedHostMemoryRejectsUnissuedProofs(t *testing.T) {
	s := NewMemoryStore()
	a, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil || !a.Valid() {
		t.Fatal(a, err)
	}
	if err = s.VerifyManagedOriginalDispatch(t.Context(), kernel.ManagedHostBinding{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unissued parked proof: %v", err)
	}
	after, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil || a != after || len(s.items) != 0 {
		t.Fatal("denied proof mutated Host authority or history")
	}
	other, err := NewMemoryStore().ManagedRecoveryAuthority(t.Context())
	if err != nil || other == a {
		t.Fatal("independent Memory stores share authority")
	}
}

// Neither a syntactically exact retired Managed receipt nor a malformed
// variant can authorize settlement after the native-only cutover.
func TestRetiredManagedDispatchReceiptRejected(t *testing.T) {
	grant := Item{ID: "original", TurnID: "turn", InvocationID: "invocation", RunID: "run", Kind: ItemDiagnostic, Status: ItemStarted, ParentItemID: ExecutionDispatchParentItemID, Seq: 1, Payload: json.RawMessage(`{"phase":"start","physicalRunID":"run"}`)}
	for _, mode := range []kernel.ManagedRecoveryMode{kernel.ManagedResume, kernel.ManagedCancel} {
		t.Run(string(mode), func(t *testing.T) {
			boundary := "managed_dispatch_transferred"
			if mode == kernel.ManagedCancel {
				boundary = "managed_dispatch_cancelled_before_next_effect"
			}
			fields := map[string]string{"boundary": boundary, "recoveryID": "original-recovery", "mode": string(mode), "selectionHash": strings.Repeat("a", 64), "terminalHash": strings.Repeat("b", 64)}
			raw, _ := json.Marshal(fields)
			receipt := Item{ID: stableID("hdispatchdone", grant.ID), TurnID: grant.TurnID, InvocationID: grant.InvocationID, RunID: grant.RunID, Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: grant.ID, Seq: 2, Payload: raw}
			if _, ok := VerifiedExecutionDispatchReceipt(grant, []Item{receipt}); ok || !IsManagedDispatchClosure(receipt) {
				t.Fatal("retired managed closure admitted")
			}
			if _, _, err := NewMemoryStore().AppendItem(t.Context(), receipt); !errors.Is(err, ErrConflict) {
				t.Fatalf("forged closure accepted: %v", err)
			}
			receipt.Payload = json.RawMessage(`{"boundary":"physical_dispatch_returned"}`)
			if _, ok := VerifiedExecutionDispatchReceipt(grant, []Item{receipt}); !ok {
				t.Fatal("ordinary exact receipt changed")
			}
			receipt.Payload = json.RawMessage(`{"boundary":"physical_dispatch_returned","extra":"no"}`)
			if _, ok := VerifiedExecutionDispatchReceipt(grant, []Item{receipt}); ok {
				t.Fatal("ordinary schema widened")
			}
		})
	}
}
