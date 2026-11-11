package harness

import (
	"encoding/json"
	"testing"
)

// Receipt authority attaches to the complete original business response,
// not merely an Interaction ID and Resolved status. Both read-only recovery
// and the Store's continuation admission consume this same identity.
func TestHostBusinessReceiptBindsCompleteOriginalResolvedSource(t *testing.T) {
	i, owner, response, claim, done := continuationReceiptFixture()
	i.Key = "original-approval"
	i.Kind = InteractionChoice
	i.Schema = json.RawMessage(`{"type":"object","properties":{"allowed":{"type":"boolean"}}}`)
	i.Response = json.RawMessage(`{"allowed":true}`)
	source := auditResolvedInteractionSource(i, owner)
	source.Seq = response.Seq - 1
	original := []Item{source, response, claim, done}
	if !hasVerifiedInteractionResponse(original, i, owner) ||
		!hasCompletedInteractionContinuation(original, i, owner) {
		t.Fatal("complete original Host business receipt should be valid")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Interaction)
	}{
		{"response-changed", func(v *Interaction) { v.Response = json.RawMessage(`{"allowed":false}`) }},
		{"response-extra-key", func(v *Interaction) { v.Response = json.RawMessage(`{"allowed":true,"extra":true}`) }},
		{"original-kind-changed", func(v *Interaction) { v.Kind = InteractionConfirmation }},
		{"original-key-changed", func(v *Interaction) { v.Key = "other-approval" }},
		{"original-schema-changed", func(v *Interaction) { v.Schema = json.RawMessage(`{"type":"string"}`) }},
		{"original-parent-changed", func(v *Interaction) { v.ParentItemID = "foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			altered := i
			tc.mutate(&altered)
			if hasVerifiedInteractionResponse(original, altered, owner) ||
				hasCompletedInteractionContinuation(original, altered, owner) {
				t.Fatalf("Host business response from a different original Interaction was accepted after mutation: %s", tc.name)
			}
			if evidence := classifyContinuationEvidence(original, altered, owner); evidence != continuationContradictory {
				t.Fatalf("corrupted historical proof was not fenced: %s evidence=%d", tc.name, evidence)
			}
		})
	}
}

func TestPhysicalDispatchStartGrantCannotBorrowForeignPhysicalRun(t *testing.T) {
	grant := Item{
		ID: "physical-original", TurnID: "turn-A", InvocationID: "owner-A", RunID: "run-A",
		Seq: 5, Kind: ItemDiagnostic, Status: ItemStarted,
		ParentItemID: ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"foreign-run"}`),
	}
	result := Item{
		ID: stableID("hdispatchdone", grant.ID), TurnID: grant.TurnID,
		InvocationID: grant.InvocationID, RunID: grant.RunID, Seq: 6,
		Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: grant.ID,
		Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
	}
	if _, allowed := ValidExecutionDispatchGrantAdmission(grant); allowed {
		t.Fatal("start grant authorized a foreign physical target")
	}
	if _, settled := VerifiedExecutionDispatchReceipt(grant, []Item{result}); settled {
		t.Fatal("completion for a different physical target settled the original grant")
	}
	grant.Payload = json.RawMessage(`{"phase":"start","physicalRunID":"run-A"}`)
	if _, allowed := ValidExecutionDispatchGrantAdmission(grant); !allowed {
		t.Fatal("original physical Run was wrongly rejected")
	}
	if _, settled := VerifiedExecutionDispatchReceipt(grant, []Item{result}); !settled {
		t.Fatal("valid same-Run returned dispatch was rejected")
	}
}
