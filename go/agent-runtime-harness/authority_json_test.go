package harness

import (
	"encoding/json"
	"testing"
)

func TestAuthorityProofJSONRejectsDuplicateAndNonCanonicalFields(t *testing.T) {
	valid := json.RawMessage(`{"boundary":"cancel_scope_settled"}`)
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{"first-party", valid, true},
		{"whitespace", json.RawMessage(" { \"boundary\" : \"cancel_scope_settled\" } "), true},
		{"duplicated-key", json.RawMessage(`{"boundary":"unknown","boundary":"cancel_scope_settled"}`), false},
		{"escaped-duplicated-key", json.RawMessage(`{"boundary":"unknown","b\u006fundary":"cancel_scope_settled"}`), false},
		{"extra-unknown-key", json.RawMessage(`{"boundary":"cancel_scope_settled","extra":"ignored"}`), false},
		{"null-boundary", json.RawMessage(`{"boundary":null}`), false},
		{"array", json.RawMessage(`["cancel_scope_settled"]`), false},
		{"nonstring", json.RawMessage(`{"boundary":true}`), false},
		{"trailing-object", json.RawMessage(`{"boundary":"cancel_scope_settled"}{}`), false},
		{"nested-instead-of-string", json.RawMessage(`{"boundary":{"value":"cancel_scope_settled"}}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := authorityStringFields(tc.raw, "boundary")
			if ok != tc.want {
				t.Fatalf("proof %q accepted=%v want=%v parsed=%v", tc.raw, ok, tc.want, got)
			}
			if ok && got["boundary"] != "cancel_scope_settled" {
				t.Fatalf("changed first-party meaning: %v", got)
			}
		})
	}
	// The original Resolved Interaction has potentially nested Schema and
	// business Response. Duplicate keys in those nested values must not be
	// capable of making a different Host effect appear equivalent.
	original := json.RawMessage(`{"interactionID":"i","response":{"allow":true}}`)
	ambiguous := json.RawMessage(`{"interactionID":"i","response":{"allow":false,"allow":true}}`)
	if sameUniqueAuthorityJSON(original, ambiguous) {
		t.Fatal("nested duplicate business-response key was interpreted as original authority")
	}
	equivalent := json.RawMessage(`{"response":{"allow":true},"interactionID":"i"}`)
	if !sameUniqueAuthorityJSON(original, equivalent) {
		t.Fatal("different object field ordering changed identical proof meaning")
	}
}

func TestFourAuthorityFamiliesRejectAmbiguousStoredProofPayload(t *testing.T) {
	cancel, complete := cancellationReceiptFixture()
	complete.Payload = json.RawMessage(`{"boundary":"ignored","boundary":"cancel_scope_settled"}`)
	if VerifiedCancellationFenceReceipt(cancel, []Item{complete}) {
		t.Fatal("cancelled original was settled with duplicate completion boundary")
	}
	grant := Item{
		ID: "test-grant", TurnID: "turn-A", InvocationID: "owner-A", RunID: "run-A",
		Kind: ItemDiagnostic, Status: ItemStarted, ParentItemID: ExecutionDispatchParentItemID, Seq: 3,
		Payload: json.RawMessage(`{"phase":"resume","physicalRunID":"run-A"}`),
	}
	returned := Item{
		ID: stableID("hdispatchdone", grant.ID), TurnID: grant.TurnID,
		InvocationID: grant.InvocationID, RunID: grant.RunID,
		Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: grant.ID, Seq: 4,
		Payload: json.RawMessage(`{"boundary":"rejected","boundary":"physical_dispatch_returned"}`),
	}
	if _, verified := VerifiedExecutionDispatchReceipt(grant, []Item{returned}); verified {
		t.Fatal("physical dispatch settled with duplicate boundary")
	}
	i, owner, response, claim, done := continuationReceiptFixture()
	src := auditResolvedInteractionSource(i, owner)
	src.Seq = response.Seq - 1
	response.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"invalid","boundary":"host_response_verified"}`)
	if hasVerifiedInteractionResponse([]Item{src, response}, i, owner) {
		t.Fatal("Host callback reply with duplicate boundary was accepted")
	}
	response.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"host_response_verified"}`)
	claim.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"other","boundary":"runtime_continuation_claimed"}`)
	if _, found, ok := originalInteractionContinuationClaim([]Item{src, response, claim}, i, owner); !found || ok {
		t.Fatal("duplicate continuation claim boundary was accepted")
	}
	claim.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"runtime_continuation_claimed"}`)
	done.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"other","boundary":"runtime_continuation_completed"}`)
	if hasCompletedInteractionContinuation([]Item{src, response, claim, done}, i, owner) {
		t.Fatal("duplicate completed-continuation boundary was accepted")
	}
}
