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
	if _, err = s.ReserveManagedRecovery(t.Context(), kernel.ManagedParkedProof{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unissued parked proof: %v", err)
	}
	if err = s.CloseManagedRecovery(t.Context(), kernel.ManagedClosureProof{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unissued closure proof: %v", err)
	}
	after, err := s.ManagedRecoveryAuthority(t.Context())
	if err != nil || a != after || len(s.managedReservations) != 0 || len(s.managedClosures) != 0 || len(s.items) != 0 {
		t.Fatal("denied proof mutated Host authority or history")
	}
	other, err := NewMemoryStore().ManagedRecoveryAuthority(t.Context())
	if err != nil || other == a {
		t.Fatal("independent Memory stores share authority")
	}
}

// Parsing proves only the exact persisted receipt shape. Store admission still
// rejects ordinary Append of this namespace; only the opaque closure writes it.
func TestManagedDispatchReceiptExactSchema(t *testing.T) {
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
			if got, ok := VerifiedExecutionDispatchReceipt(grant, []Item{receipt}); !ok || got != boundary {
				t.Fatal("exact managed closure rejected", got)
			}
			invalid := []json.RawMessage{
				json.RawMessage(strings.TrimSuffix(string(raw), "}") + `,"boundary":"` + boundary + `"}`),
				json.RawMessage(strings.TrimSuffix(string(raw), "}") + `,"extra":"unknown"}`),
			}
			for _, key := range []string{"boundary", "recoveryID", "mode", "selectionHash", "terminalHash"} {
				changed := make(map[string]string, len(fields))
				for k, v := range fields {
					changed[k] = v
				}
				delete(changed, key)
				b, _ := json.Marshal(changed)
				invalid = append(invalid, b)
				changed[key] = ""
				b, _ = json.Marshal(changed)
				invalid = append(invalid, b)
			}
			for _, hash := range []string{strings.Repeat("z", 64), strings.Repeat("A", 64), strings.Repeat("a", 63)} {
				changed := make(map[string]string, len(fields))
				for k, v := range fields {
					changed[k] = v
				}
				changed["selectionHash"] = hash
				b, _ := json.Marshal(changed)
				invalid = append(invalid, b)
			}
			opposite := kernel.ManagedCancel
			if mode == kernel.ManagedCancel {
				opposite = kernel.ManagedResume
			}
			fields["mode"] = string(opposite)
			b, _ := json.Marshal(fields)
			invalid = append(invalid, b)
			for i, payload := range invalid {
				bad := receipt
				bad.Payload = payload
				if _, ok := VerifiedExecutionDispatchReceipt(grant, []Item{bad}); ok {
					t.Fatalf("invalid payload %d accepted", i)
				}
			}
			for _, change := range []func(*Item){func(v *Item) { v.RunID = "foreign" }, func(v *Item) { v.InvocationID = "foreign" }, func(v *Item) { v.Seq = grant.Seq }, func(v *Item) { v.ID = "foreign" }} {
				bad := receipt
				change(&bad)
				if _, ok := VerifiedExecutionDispatchReceipt(grant, []Item{bad}); ok {
					t.Fatal("foreign managed receipt accepted")
				}
			}
			if _, ok := VerifiedExecutionDispatchReceipt(grant, []Item{receipt, receipt}); ok {
				t.Fatal("duplicate managed receipt accepted")
			}
			resume := grant
			resume.Payload = json.RawMessage(`{"phase":"resume","physicalRunID":"run"}`)
			if _, ok := VerifiedExecutionDispatchReceipt(resume, []Item{receipt}); ok {
				t.Fatal("managed receipt accepted for nonoriginal Start")
			}
			ordinary := receipt
			ordinary.Payload = json.RawMessage(`{"boundary":"physical_dispatch_returned"}`)
			if _, ok := VerifiedExecutionDispatchReceipt(grant, []Item{ordinary}); !ok {
				t.Fatal("ordinary exact receipt changed")
			}
			ordinary.Payload = json.RawMessage(`{"boundary":"physical_dispatch_returned","extra":"no"}`)
			if _, ok := VerifiedExecutionDispatchReceipt(grant, []Item{ordinary}); ok {
				t.Fatal("ordinary schema widened")
			}
		})
	}
}
