package harness

import (
	"encoding/json"
	"testing"
)

func TestCorruptedOriginalAuthorityCannotDisappearFromRecovery(t *testing.T) {
	cancel, closed := cancellationReceiptFixture()
	if !ActiveCancellationFence([]Item{cancel}) {
		t.Fatal("valid cancellation must be pending")
	}
	damaged := cancel
	damaged.Status = ItemCompleted
	if !ActiveCancellationFence([]Item{damaged, closed}) {
		t.Fatal("cancel original with corrupt Status disappeared from admission fence")
	}
	damaged = cancel
	damaged.Kind = ItemTool
	if !ActiveCancellationFence([]Item{damaged, closed}) {
		t.Fatal("cancel original with corrupt Kind disappeared from admission fence")
	}
	grant := Item{
		ID: "physical-start", TurnID: "turn-A", InvocationID: "owner-A", RunID: "run-A",
		Kind: ItemDiagnostic, Status: ItemStarted, Seq: 4,
		ParentItemID: ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"run-A"}`),
	}
	completed := Item{
		ID: stableID("hdispatchdone", grant.ID), TurnID: grant.TurnID, InvocationID: grant.InvocationID,
		RunID: grant.RunID, Kind: ItemDiagnostic, Status: ItemCompleted, Seq: 5,
		ParentItemID: grant.ID, Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
	}
	if PendingExecutionDispatch([]Item{grant, completed}, "owner-A") {
		t.Fatal("valid physical grant did not settle")
	}
	damaged = grant
	damaged.Status = ItemCompleted
	if !PendingExecutionDispatch([]Item{damaged, completed}, "owner-A") {
		t.Fatal("corrupt Status of original dispatch authorized another physical attempt")
	}
	damaged = grant
	damaged.Kind = ItemTool
	if !PendingExecutionDispatch([]Item{damaged, completed}, "owner-A") {
		t.Fatal("corrupt Kind of original dispatch authorized another physical attempt")
	}
}
