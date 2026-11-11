package harness

import (
	"encoding/json"
	"testing"
)

// Fuzz all independent receipt/intent fields rather than only the
// previously observed individual examples. Unrecognized bits are masked;
// every changed field must fail closed while the original fact remains valid.
func FuzzCancellationReceiptOwnerBoundary(f *testing.F) {
	for _, mask := range []uint16{0, 1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4095} {
		f.Add(mask)
	}
	f.Fuzz(func(t *testing.T, input uint16) {
		mask := input & 4095
		intent, receipt := cancellationReceiptFixture()
		proofs := []Item{receipt}
		if mask&1 != 0 {
			receipt.TurnID = "foreign-turn"
		}
		if mask&2 != 0 {
			receipt.RunID = "foreign-run"
		}
		if mask&4 != 0 {
			receipt.InvocationID = "foreign-owner"
		}
		if mask&8 != 0 {
			receipt.Payload = json.RawMessage(`{"boundary":"physical_dispatch_returned"}`)
		}
		if mask&16 != 0 {
			receipt.Seq = intent.Seq
		}
		if mask&32 != 0 {
			receipt.Kind = ItemApproval
		}
		if mask&64 != 0 {
			receipt.Status = ItemWaiting
		}
		if mask&128 != 0 {
			receipt.ParentItemID = "foreign-parent"
		}
		if mask&256 != 0 {
			intent.Payload = json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"wrong"}`)
		}
		if mask&1024 != 0 {
			receipt.ID = ""
		}
		if mask&2048 != 0 {
			intent.Status = ItemCompleted
		}
		proofs[0] = receipt
		if mask&512 != 0 {
			extra := receipt
			extra.ID = "second-completion"
			extra.Seq = receipt.Seq + 1
			proofs = append(proofs, extra)
		}
		want := mask == 0
		if got := VerifiedCancellationFenceReceipt(intent, proofs); got != want {
			t.Fatalf("cancel mask=%#x verified=%t want=%t", mask, got, want)
		}
	})
}

func FuzzDispatchReceiptOriginalOwner(f *testing.F) {
	for _, mask := range []uint16{0, 1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2047} {
		f.Add(mask)
	}
	f.Fuzz(func(t *testing.T, input uint16) {
		mask := input & 2047
		grant := Item{
			ID: "physical-grant", TurnID: "turn-A", RunID: "run-A", InvocationID: "owner-A",
			Kind: ItemDiagnostic, Status: ItemStarted, Seq: 4,
			ParentItemID: ExecutionDispatchParentItemID,
			Payload:      json.RawMessage(`{"phase":"resume","physicalRunID":"run-A"}`),
		}
		done := Item{
			ID: stableID("hdispatchdone", grant.ID), TurnID: grant.TurnID, RunID: grant.RunID, InvocationID: grant.InvocationID,
			Kind: ItemDiagnostic, Status: ItemCompleted, Seq: 5, ParentItemID: grant.ID,
			Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		}
		proofs := []Item{done}
		if mask&1 != 0 {
			done.TurnID = "foreign"
		}
		if mask&2 != 0 {
			done.RunID = "foreign"
		}
		if mask&4 != 0 {
			done.InvocationID = "foreign"
		}
		if mask&8 != 0 {
			done.Payload = json.RawMessage(`{"boundary":"other"}`)
		}
		if mask&16 != 0 {
			done.Seq = grant.Seq
		}
		if mask&32 != 0 {
			done.Kind = ItemTool
		}
		if mask&64 != 0 {
			done.Status = ItemStarted
		}
		if mask&128 != 0 {
			done.ParentItemID = "foreign"
		}
		if mask&256 != 0 {
			grant.Status = ItemCompleted
		}
		if mask&512 != 0 {
			grant.ParentItemID = "foreign"
		}
		if mask&1024 != 0 {
			done.ID = ""
		}
		proofs[0] = done
		if mask&1024 != 0 {
			// An invalid non-empty extra receipt must never rescue a bad
			// original settlement.
			proofs = append(proofs, Item{ID: "second", ParentItemID: grant.ID, Kind: ItemDiagnostic, Status: ItemCompleted})
		}
		_, ok := VerifiedExecutionDispatchReceipt(grant, proofs)
		if want := mask == 0; ok != want {
			t.Fatalf("dispatch mask=%#x verified=%t want=%t", mask, ok, want)
		}
	})
}

func FuzzContinuationProofChainAndSource(f *testing.F) {
	for _, mask := range []uint16{0, 1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2047} {
		f.Add(mask)
	}
	f.Fuzz(func(t *testing.T, input uint16) {
		mask := input & 2047
		i, owner, response, claim, done := continuationReceiptFixture()
		source := auditResolvedInteractionSource(i, owner)
		source.Seq = response.Seq - 1
		if mask&1 != 0 {
			source.TurnID = "foreign"
		}
		if mask&2 != 0 {
			source.Payload = json.RawMessage(`{"interactionID":"foreign","status":"resolved"}`)
		}
		if mask&4 != 0 {
			source.Seq = response.Seq
		}
		if mask&8 != 0 {
			response.RunID = "foreign"
		}
		if mask&16 != 0 {
			response.Payload = json.RawMessage(`{"interactionID":"foreign","boundary":"host_response_verified"}`)
		}
		if mask&32 != 0 {
			claim.RunID = "foreign"
		}
		if mask&64 != 0 {
			claim.ParentItemID = "foreign"
		}
		if mask&128 != 0 {
			claim.Payload = json.RawMessage(`{"interactionID":"foreign","boundary":"runtime_continuation_claimed"}`)
		}
		if mask&256 != 0 {
			done.RunID = "foreign"
		}
		if mask&512 != 0 {
			done.Payload = json.RawMessage(`{"interactionID":"foreign","boundary":"runtime_continuation_completed"}`)
		}
		if mask&1024 != 0 {
			done.Seq = claim.Seq
		}
		items := []Item{source, response, claim, done}
		if want := mask == 0; hasCompletedInteractionContinuation(items, i, owner) != want {
			t.Fatalf("continuation mask=%#x hadAuthority=%t want=%t", mask, !want, want)
		}
	})
}
