package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// The observed durable draft-interruption receipt is an independent execution
// invariant. An unrelated typed Provider error or cancellation joined to an
// inconsistent/missing interruption must never override the Host anomaly.
// Return only bounded local codes; keep the original cause for diagnostics.
func TestAuditPreInterruptionReceiptMismatchHasPriority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		committed bool
		cause     error
	}{
		{"committed-but-cancelled", true, context.Canceled},
		{"committed-but-deadline", true, context.DeadlineExceeded},
		{"committed-but-upstream-coded", true, model.NewClassifiedError(errors.New("private-provider-key"), "model.upstream_unavailable")},
		{"no-commit-but-joined-cancellation", false, errors.Join(errInterrupted, context.Canceled)},
		{"no-commit-but-joined-classified", false, model.NewClassifiedError(errInterrupted, "model.upstream_request_invalid")},
		{"committed-but-model-invalid", true, errors.Join(agent.ErrInvalidModelResponse, context.DeadlineExceeded)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := harness.Snapshot{Turn: harness.Turn{Status: harness.TurnRunning}}
			got := failureCode(classifyPreInterruptionFailure(original, tc.cause, tc.committed, 1), "unknown")
			if got != "task_interruption_evidence_inconsistent" {
				t.Errorf("expected structural committed-receipt contradiction to outrank typed failure; got %q", got)
			}
		})
	}
}

func TestAuditOriginalClassifiedFailureStillKeepsSpecificCode(t *testing.T) {
	classified := model.NewClassifiedError(errors.New("private-provider-key"), "model.upstream_unavailable")
	original := harness.Snapshot{Turn: harness.Turn{Status: harness.TurnFailed}}
	got := failureCode(classifyPreInterruptionFailure(original, errors.Join(agent.ErrModelFailure, classified), false, 0), "unknown")
	if got != "model.upstream_unavailable" {
		t.Fatalf("unrelated real Provider failure lost its typed code %q", got)
	}
}

// A wrapped marker is valid only when every underlying error is precisely the
// Host's injected receipt interruption. No Error.Is facade or auxiliary
// Provider/cancellation failure can manufacture an accepted recovery signal.
func TestAuditExpectedInterruptionRequiresPureReceiptSignal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  bool
	}{
		{"direct", errInterrupted, true},
		{"single-wrap", fmt.Errorf("harness: %w", errInterrupted), true},
		{"single-join", errors.Join(errInterrupted), true},
		{"nested-single-wrap", errors.Join(fmt.Errorf("wrapped: %w", errInterrupted)), true},
		{"nil", nil, false},
		{"ordinary-provider-error", errors.New("upstream unavailable"), false},
		{"joined-provider-error", errors.Join(errInterrupted, errors.New("untrusted provider response")), false},
		{"joined-cancel", errors.Join(errInterrupted, context.Canceled), false},
		{"joined-timeout", errors.Join(errInterrupted, context.DeadlineExceeded), false},
		{"classified-marker", model.NewClassifiedError(errInterrupted, "model.upstream_timeout"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := onlyExpectedDraftInterruption(tc.cause, 0)
			if got != tc.want {
				t.Fatalf("pure committed interruption=%t, want %t for %s", got, tc.want, tc.name)
			}
		})
	}
	nested := errInterrupted
	for i := 0; i < 40; i++ {
		nested = fmt.Errorf("level-%d: %w", i, nested)
	}
	if onlyExpectedDraftInterruption(nested, 0) {
		t.Fatal("unbounded wrapped input authorized recovery")
	}
}
