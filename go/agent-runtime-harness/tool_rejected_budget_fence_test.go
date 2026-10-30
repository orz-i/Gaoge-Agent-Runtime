package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// After physical Tool dispatch, a "recoverable" error without an original
// CallID-bound rejection is neither a settled Tool charge nor proof that a
// remote effect did not occur. The same held reservation blocks redispatch.
func TestToolRecoverableErrorWithoutRejectedProofPreservesBudget(t *testing.T) {
	for _, variant := range []string{"missing", "wrong-id"} {
		t.Run(variant, func(t *testing.T) {
			store, shared, _ := hostedBudgetFixture(t, budget.Limits{MaxToolCalls: 1})
			request := tools.ExecutionRequest{RunID: "hosted-run",
				Call: tools.Call{ID: "frozen-error", ToolKey: "local.read", Arguments: json.RawMessage(`{}`)}}
			invocation := plugin.ToolInvocation{
				Run:        kernel.Run{ID: request.RunID},
				Definition: tools.Definition{Key: "local.read", Name: "read", InputSchema: json.RawMessage(`{}`)},
				Request:    request,
			}
			physical := 0
			cause := tools.NewRecoverableCallError("tool.read_failed", "reply lost after effect", errors.New("remote side effect may have occurred"))
			_, err := shared.Tool(t.Context(), invocation, func(context.Context) (tools.ExecutionResult, error) {
				physical++
				if variant == "wrong-id" {
					return tools.RecoverableNoEffectResult("other-call", cause)
				}
				return tools.ExecutionResult{}, cause
			})
			if !errors.Is(err, cause) || physical != 1 {
				t.Fatalf("missing initial physical dispatch: err=%v count=%d", err, physical)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil || ledger.View("").Reserved.ToolCalls != 1 || ledger.View("").Usage.ToolCalls != 0 {
				t.Fatalf("ambiguous error released Tool quota: view=%+v err=%v", ledger.View(""), err)
			}
			_, err = shared.Tool(t.Context(), invocation, func(context.Context) (tools.ExecutionResult, error) {
				physical++
				return tools.ExecutionResult{Content: json.RawMessage(`{}`),
					Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
			})
			if !errors.Is(err, budget.ErrWaiting) || physical != 1 {
				t.Fatalf("unknown Tool physical effect was retried: err=%v physical=%d", err, physical)
			}
		})
	}
}

func TestToolExactRejectedNoEffectReceiptSettlesBudgetOnce(t *testing.T) {
	store, shared, _ := hostedBudgetFixture(t, budget.Limits{MaxToolCalls: 1})
	request := tools.ExecutionRequest{RunID: "hosted-run",
		Call: tools.Call{ID: "rejected-before-effect", ToolKey: "local.read", Arguments: json.RawMessage(`{}`)}}
	invocation := plugin.ToolInvocation{Run: kernel.Run{ID: request.RunID},
		Definition: tools.Definition{Key: "local.read", Name: "read", InputSchema: json.RawMessage(`{}`)},
		Request:    request}
	cause := tools.NewRecoverableCallError("tool.invalid_input", "invalid", errors.New("pre-effect"))
	physical := 0
	receipt, err := shared.Tool(t.Context(), invocation, func(context.Context) (tools.ExecutionResult, error) {
		physical++
		return tools.RecoverableNoEffectResult(request.Call.ID, cause)
	})
	if !errors.Is(err, cause) || physical != 1 || receipt.Receipt.Disposition != tools.ReceiptDispositionRejected {
		t.Fatalf("pre-effect no-effect attestation missing: %+v err=%v physical=%d", receipt, err, physical)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil || ledger.View("").Usage.ToolCalls != 1 || ledger.View("").Reserved.ToolCalls != 0 {
		t.Fatalf("explicit known rejected Tool failed accounting: view=%+v err=%v", ledger.View(""), err)
	}
}
