package tools_test

import (
	"context"
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

// Returning an arbitrary disposition, or a rejected receipt without its
// typed error, must not become a terminal Tool fact consumed by Agent state.
func TestSuccessfulToolReceiptDispositionMustBeKnownTerminalOrPending(t *testing.T) {
	for _, disposition := range []string{"provider_unknown", "rejected", "external_done", "failed", "", "COMMITTED"} {
		t.Run(disposition, func(t *testing.T) {
			result := tools.ExecutionResult{
				Content: json.RawMessage(`{}`),
				Receipt: tools.Receipt{ExecutionID: "original-call", Disposition: disposition},
			}
			if err := tools.ValidateExecutionResult(result); !errors.Is(err, tools.ErrInvalidCall) {
				t.Fatalf("unrecognized/no-error rejected disposition silently treated as terminal: %q err=%v", disposition, err)
			}
		})
	}
	for _, disposition := range []string{"read", "committed", tools.ReceiptDispositionPending} {
		t.Run("known-"+disposition, func(t *testing.T) {
			result := tools.ExecutionResult{
				Content: json.RawMessage(`{}`),
				Receipt: tools.Receipt{ExecutionID: "original-call", Disposition: disposition},
			}
			if err := tools.ValidateExecutionResult(result); err != nil {
				t.Fatalf("legal Tool disposition was rejected: %q err=%v", disposition, err)
			}
		})
	}
}

// A retryable error is never sufficient evidence to reenter a physical
// Handler. The exact original-ID rejected receipt must also survive the
// Tool Registry and the Agent execution policy.
func TestRetryableNoEffectReceiptNeedsExactCallAndRetryableCause(t *testing.T) {
	cause := tools.NewRetryableExecutionError(errors.New("transient, explicitly no remote send"))
	result, err := tools.RetryableNoEffectResult("original-call", cause)
	if !errors.Is(err, cause) || !tools.ValidRetryableNoEffectReceipt(result, err, "original-call") {
		t.Fatalf("original-ID no-effect retry attestation was not recognized: %+v err=%v", result, err)
	}
	if tools.ValidRetryableNoEffectReceipt(result, err, "different-call") ||
		tools.ValidRetryableNoEffectReceipt(result, errors.New("untyped error"), "original-call") {
		t.Fatalf("retry proof was not tied to exact identity and error: %+v", result)
	}
	bad := result
	bad.Receipt.Disposition = "committed"
	if tools.ValidRetryableNoEffectReceipt(bad, err, "original-call") {
		t.Fatal("a positive COMMIT is not a retry authorization")
	}
	result, err = tools.RetryableNoEffectResult("original-call", errors.New("untyped error"))
	if err == nil || tools.ValidRetryableNoEffectReceipt(result, err, "original-call") {
		t.Fatal("untyped error fabricated a no-effect retry proof")
	}
}

// A non-error rejected disposition is not an ordinary successful Tool call
// even when the Handler and Client happen to agree on the same Call ID.
func TestRegistryRejectsPositiveUnknownOrNoEffectDisposition(t *testing.T) {
	for _, disposition := range []string{"unrecognized", "rejected"} {
		t.Run(disposition, func(t *testing.T) {
			handler := tools.HandlerFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
				return tools.ExecutionResult{Content: json.RawMessage(`{}`),
					Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: disposition}}, nil
			})
			registry, err := tools.NewRegistry([]tools.Registration{{
				Definition: tools.Definition{Key: "unsafe", Name: "unsafe", InputSchema: json.RawMessage(`{"type":"object"}`)},
				Handler:    handler,
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry.Execute(t.Context(), tools.ExecutionRequest{RunID: "run", Call: tools.Call{
				ID: "original-call", ToolKey: "unsafe", Arguments: json.RawMessage(`{}`),
			}})
			if !errors.Is(err, tools.ErrInvalidCall) {
				t.Fatalf("unknown/incorrect terminal status was admitted: %q err=%v", disposition, err)
			}
		})
	}
}
