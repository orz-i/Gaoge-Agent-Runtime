package harness

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRetiredRoleHandoffInputsCannotAuthorizeMemberDelegation(t *testing.T) {
	for _, raw := range []string{
		`{"id":"d","memberID":"analyst","childRunID":"child","goal":"inspect","status":"queued","roleID":"researcher"}`,
		`{"id":"d","memberID":"analyst","childRunID":"child","goal":"inspect","status":"queued","RoleRevision":3}`,
		`{"id":"d","memberID":"analyst","childRunID":"child","goal":"inspect","status":"queued","ROLEname":"analyst"}`,
	} {
		if _, err := decodeFrozenHandoffDelegation(json.RawMessage(raw)); !errors.Is(err, ErrConflict) {
			t.Fatalf("retired Role authority accepted: %s, err=%v", raw, err)
		}
		id, err := InvocationID("turn", "parent-item", CapabilityAgent, "d")
		if err != nil {
			t.Fatal(err)
		}
		inv := Invocation{
			ID: id, TurnID: "turn", ParentItemID: "parent-item",
			CapabilityKey: CapabilityAgent, ExecutionClass: ExecutionAgent,
			ExecutionRefID: "child", Input: json.RawMessage(raw),
			InputHash: hashInvocationBytes(json.RawMessage(raw)),
		}
		if isHandoffDelegationInvocation(inv) {
			t.Fatalf("legacy role child acquired native Handoff identity: %s", raw)
		}
	}
	// Frozen ancestry is a separate proof: this parser must not accidentally
	// reject normal Run-root/parent metadata when retiring Role fields.
	frozen, err := decodeFrozenHandoffDelegation(json.RawMessage(
		`{"id":"d","memberID":"analyst","memberRevision":"v3","childRunID":"child","goal":"inspect","status":"queued","harnessRoot":{"root":"original"},"harnessParents":[{"attempt":1}]}`,
	))
	if err != nil || frozen.MemberID != "analyst" || frozen.MemberRevision != "v3" {
		t.Fatalf("current frozen member rejected: %+v, err=%v", frozen, err)
	}
}
