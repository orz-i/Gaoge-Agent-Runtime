package tools_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestRecoverableNoEffectReceiptRequiresOriginalIdentityAndExplicitRejection(t *testing.T) {
	cause := tools.NewRecoverableCallError("tool.validation", "reject before effect", errors.New("no-effect"))
	receipt, refusal := tools.RecoverableNoEffectResult("frozen-call", cause)
	if !errors.Is(refusal, cause) ||
		!tools.ValidRecoverableNoEffectReceipt(receipt, refusal, "frozen-call") {
		t.Fatal("explicit original-ID no-effect receipt was not recognized")
	}
	cases := []struct {
		name    string
		receipt tools.ExecutionResult
		cause   error
		id      string
	}{
		{name: "wrong original ID", receipt: receipt, cause: cause, id: "another-call"},
		{name: "no typed refusal", receipt: receipt, cause: errors.New("plain error"), id: "frozen-call"},
		{name: "missing receipt", receipt: tools.ExecutionResult{}, cause: cause, id: "frozen-call"},
		{name: "wrong disposition", receipt: tools.ExecutionResult{
			Content: json.RawMessage(`{}`),
			Receipt: tools.Receipt{ExecutionID: "frozen-call", Disposition: "committed"},
		}, cause: cause, id: "frozen-call"},
		{name: "pending with rejection", receipt: tools.ExecutionResult{
			Content: json.RawMessage(`{}`),
			Receipt: tools.Receipt{ExecutionID: "frozen-call", Disposition: tools.ReceiptDispositionRejected},
			Pending: &tools.PendingExecution{Kind: tools.PendingScheduled},
		}, cause: cause, id: "frozen-call"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if tools.ValidRecoverableNoEffectReceipt(test.receipt, test.cause, test.id) {
				t.Fatalf("invalid/ambiguous proof accepted: %+v", test)
			}
		})
	}
	if bad, err := tools.RecoverableNoEffectResult("", cause); err == nil ||
		tools.ValidRecoverableNoEffectReceipt(bad, err, "frozen-call") {
		t.Fatal("missing Handler identity manufactured a receipt")
	}
}
