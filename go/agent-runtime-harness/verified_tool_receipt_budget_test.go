package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Read-only COMMIT settles exactly its dispatched Tool reservation. A duplicate
// verified receipt after a lost Agent CAS neither reexecutes nor double-charges.
func TestVerifiedToolOutcomeSettlementIsIdempotentAndFrozen(t *testing.T) {
	store, shared, _ := hostedBudgetFixture(t, budget.Limits{MaxToolCalls: 1})
	request := tools.ExecutionRequest{RunID: "hosted-run", Call: tools.Call{
		ID: "verified-tool", ToolKey: "local.read", Arguments: json.RawMessage("{}"),
	}}
	invocation := plugin.ToolInvocation{Run: kernel.Run{ID: request.RunID},
		Definition: tools.Definition{Key: "local.read", Name: "read", InputSchema: json.RawMessage("{}")},
		Request:    request}
	effects, verifications := 0, 0
	initial, err := shared.Tool(t.Context(), invocation, func(context.Context) (tools.ExecutionResult, error) {
		effects++
		return tools.ExecutionResult{Content: json.RawMessage("{}"),
			Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending}}, nil
	})
	if err != nil || initial.Receipt.Disposition != tools.ReceiptDispositionPending || effects != 1 {
		t.Fatalf("Tool was not dispatched and held: err=%v effects=%d result=%+v", err, effects, initial)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil || ledger.View("").Reserved.ToolCalls != 1 || ledger.View("").Usage.ToolCalls != 0 {
		t.Fatalf("physical effect not held: ledger=%+v err=%v", ledger.View(""), err)
	}
	verifier, err := shared.VerifiedToolOutcomeVerifier(agent.ToolOutcomeVerifierFunc(
		func(_ context.Context, candidate tools.ExecutionRequest) (tools.ExecutionResult, error) {
			verifications++
			if candidate.RunID != request.RunID || candidate.Call.ID != request.Call.ID {
				return tools.ExecutionResult{}, errors.New("unknown provider identity")
			}
			return tools.ExecutionResult{Content: json.RawMessage("{}"),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	mismatch := tools.CloneExecutionRequest(request)
	mismatch.Call.Arguments = json.RawMessage("{\"changed\":true}")
	if _, err = verifier.VerifyCommittedTool(t.Context(), mismatch); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("changed frozen input accepted: %v", err)
	}
	ledger, err = store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil || ledger.View("").Reserved.ToolCalls != 1 {
		t.Fatalf("mismatch released held Tool quota: %+v err=%v", ledger.View(""), err)
	}
	for count := 0; count < 2; count++ {
		receipt, verifyErr := verifier.VerifyCommittedTool(t.Context(), request)
		if verifyErr != nil || receipt.Receipt.ExecutionID != request.Call.ID {
			t.Fatalf("verified receipt failed: %+v %v", receipt, verifyErr)
		}
		ledger, err = store.LoadBudget(t.Context(), "hosted-budget-turn")
		if err != nil || ledger.View("").Usage.ToolCalls != 1 ||
			ledger.View("").Reserved.ToolCalls != 0 || effects != 1 {
			t.Fatalf("duplicate charged or executed business handler: %+v calls=%d err=%v",
				ledger.View(""), effects, err)
		}
	}
	if verifications != 3 {
		t.Fatalf("expected one mismatch and two identical verifier reads, got %d", verifications)
	}
}
