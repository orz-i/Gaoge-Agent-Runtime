package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Fake model reads the real authorized archived artifact, then returns a
// schema-valid final answer WITHOUT writing the required draft. This is a
// clean terminal model outcome rather than a Provider/Context error.
type earlyFinalWithoutDraftModel struct{ scriptedModel }

func (earlyFinalWithoutDraftModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	if _, found := toolMessage(request.Messages, harness.ContextArtifactToolKey); found {
		return model.Response{Content: `{"release":"R-17","deploymentCode":"UNVERIFIED","window":"UNKNOWN","artifactID":"not-saved","draftID":"not-saved"}`}, nil
	}
	return scriptedModel{}.Generate(ctx, request)
}

func TestPreInterruptionCleanModelTerminalDistinguishedFromFailedProvider(t *testing.T) {
	config := scriptedSettings(t)
	report, err := runTask(t.Context(), config, func() (model.Client, error) {
		return earlyFinalWithoutDraftModel{}, nil
	}, defaultSource())
	if err == nil || report.FailureCode != "task_completed_without_draft" ||
		report.Status != "failed" || report.Stage != "evidence" || report.DraftWrites != 0 ||
		report.ArtifactReads != 1 || report.LookupCalls != 1 || report.ModelCalls != 3 ||
		report.RecoveryChecks.InterruptionObserved || report.OraclePassed {
		t.Fatalf("early terminal incorrectly conflated with unclassified Provider error: report=%+v err=%v", report, err)
	}
	if report.RunID == "" || report.TurnID == "" {
		t.Fatalf("lost original committed identity: %+v", report)
	}
}

func TestPreInterruptionProviderFailureUsesSafeTypedCode(t *testing.T) {
	report, err := runTask(t.Context(), scriptedSettings(t), func() (model.Client, error) {
		return errorFixtureModel{}, nil
	}, defaultSource())
	if err == nil || report.Status != "failed" ||
		report.FailureCode != "task_model_failed_before_interruption" ||
		report.ModelCalls != 1 || report.ToolCalls != 0 || report.DraftWrites != 0 {
		t.Fatalf("early Provider failure incorrectly classified as a missing interruption: %+v err=%v", report, err)
	}
	sample, marshalErr := json.Marshal(sampleMetrics(report, "code-01", "code-01-01", 1))
	if marshalErr != nil || bytes.Contains(sample, []byte("private-fixture-key")) ||
		bytes.Contains(sample, []byte("untrusted upstream")) {
		t.Fatalf("provider-controlled diagnostic leaked into public report: %s", sample)
	}
}

func TestPreInterruptionTypedCauseKeepsOriginalClassification(t *testing.T) {
	cause := errors.New("secret provider body")
	typed := errors.Join(cause, errInterrupted)
	if got := failureCode(taskFailed("task_interrupt_receipt_missing", typed), "unknown"); got != "task_interrupt_receipt_missing" {
		t.Fatalf("untrusted provider error became public code: %q", got)
	}
}

// An independent lifecycle matrix exercises every accepted pre-interruption
// boundary, not only the originally reported early text completion and model
// transport error. The test oracle derives its expected public codes from
// trusted status + typed error facts, never from an untrusted error string.
func TestPreInterruptionStatusAndTypedCauseMatrix(t *testing.T) {
	secret := "api-key-UNTRUSTED-ORIGIN-20261108"
	fixtures := []struct {
		name    string
		turn    harness.TurnStatus
		drafts  int
		err     error
		receipt bool
		want    string
	}{
		{"successful-terminal-no-draft", harness.TurnCompleted, 0, nil, false, "task_completed_without_draft"},
		{"successful-terminal-draft-but-no-fault", harness.TurnCompleted, 1, nil, false, "task_completed_before_interruption"},
		{"incomplete-running-return", harness.TurnRunning, 0, nil, false, "task_returned_without_interruption"},
		{"failed-status-without-typed-error", harness.TurnFailed, 0, nil, false, "task_failed_before_interruption"},
		{"cancelled-status-without-typed-error", harness.TurnCancelled, 0, nil, false, "cancelled"},
		{"paused-budget-status-without-typed-error", harness.TurnPausedBudget, 0, nil, false, "task_returned_without_interruption"},
		{"untyped-failed-run", harness.TurnFailed, 0, fmt.Errorf("upstream response: %s", secret), false, "task_failed_before_interruption"},
		{"typed-model-failed", harness.TurnFailed, 0, errors.Join(agent.ErrModelFailure, fmt.Errorf("secret %s", secret)), false, "task_model_failed_before_interruption"},
		{"typed-invalid-model-response", harness.TurnFailed, 0, errors.Join(agent.ErrInvalidModelResponse, fmt.Errorf("secret %s", secret)), false, "task_model_response_invalid_before_interruption"},
		{"typed-context-budget", harness.TurnFailed, 0, errors.Join(agent.ErrModelFailure, runtimecontext.ErrBudgetExceeded), false, "context_budget_exceeded"},
		{"typed-cancellation", harness.TurnCancelled, 0, context.Canceled, false, "cancelled"},
		{"missing-committed-fault-marker", harness.TurnRunning, 1, errInterrupted, false, "task_interruption_evidence_inconsistent"},
		{"committed-marker-with-nil-error", harness.TurnRunning, 1, nil, true, "task_interruption_evidence_inconsistent"},
		{"committed-marker-with-other-error", harness.TurnFailed, 1, agent.ErrModelFailure, true, "task_interruption_evidence_inconsistent"},
	}
	rng := rand.New(rand.NewSource(20261108))
	for iteration := 0; iteration < 660; iteration++ {
		tc := fixtures[rng.Intn(len(fixtures))]
		t.Run(fmt.Sprintf("%03d/%s", iteration, tc.name), func(t *testing.T) {
			snapshot := harness.Snapshot{Turn: harness.Turn{Status: tc.turn, ErrorCode: secret, ErrorDetail: secret}}
			wrapped := classifyPreInterruptionFailure(snapshot, tc.err, tc.receipt, tc.drafts)
			got := failureCode(wrapped, "unclassified")
			if got != tc.want {
				t.Fatalf("seed 20261108 case %s: got %q want %q", tc.name, got, tc.want)
			}
			sample, err := json.Marshal(sampleMetrics(taskReport{Status: "failed", Stage: "evidence", FailureCode: got}, "code-01", "code-01-01", 1))
			if err != nil || bytes.Contains(sample, []byte(secret)) || strings.Contains(string(sample), "api-key") {
				t.Fatalf("secret from trusted Store detail / untrusted Provider error leaked into report: %s %v", sample, err)
			}
			if tc.err != nil && !errors.Is(wrapped, tc.err) {
				t.Fatalf("typed failure was lost on wrapping: %v", wrapped)
			}
		})
	}
}
