package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// The first attempt truthfully reports a pure pre-dispatch NoEffect.
// The SECOND attempt actually reaches an external transport and returns
// an ambiguous error after send. The old PhysicalNoEffect bit must not
// survive the second dispatch and authorize Grant settlement or retry.
type auditNoEffectAttemptCarryover struct {
	attempts, physicalWrites int
}

func (e *auditNoEffectAttemptCarryover) Execute(_ context.Context, req workflow.EffectRequest) (workflow.EffectResult, error) {
	e.attempts++
	switch e.attempts {
	case 1:
		return workflow.EffectResult{
			Disposition: workflow.DispositionFailed, ErrorCode: "provider.safe_preflight_reject",
			ErrorDetail: "verified no network request", PhysicalNoEffect: true,
		}, nil
	case 2:
		e.physicalWrites++ // synthetic send completed, response lost
		return workflow.EffectResult{}, errors.New("connection lost after issuing external request")
	default:
		e.physicalWrites++ // if replayed, physical request was DUPLICATED
		return workflow.EffectResult{
			Disposition: workflow.DispositionCompleted, ReceiptID: "later_receipt",
			Output: json.RawMessage(`{"ok":true}`),
		}, nil
	}
}
func TestAuditIssue107NoEffectFromPriorAttemptSettlesUnknownLaterDispatch(t *testing.T) {
	rt := newWorkflowRuntime(t)
	executor := &auditNoEffectAttemptCarryover{}
	runner := newWorkflowRunner(t, rt, executor)
	definition := compileWorkflow(t, []workflow.Node{
		{ID: "write", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
			Kind: "external.write", Input: json.RawMessage(`{"mutable":true}`),
			Retry: workflow.RetryPolicy{MaxAttempts: 3, RetryableErrorCodes: []string{"*"}},
		}},
		returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.Limits{})
	request := workflowRequest(definition)
	request.ID = "root-carryover"
	first, firstErr := runner.StartRun(t.Context(), request)
	if !errors.Is(firstErr, workflow.ErrEffectPending) ||
		executor.attempts != 1 || executor.physicalWrites != 0 {
		t.Fatalf("failed to establish honest first pre-dispatch no-effect receipt: err=%v calls=%d physical=%d",
			firstErr, executor.attempts, executor.physicalWrites)
	}
	if _, err := rt.LookupChildExecution(t.Context(), first.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("truthful pre-dispatch failure should retire its original Grant: %v", err)
	}
	second, secondErr := runner.Resume(t.Context(), first.Run.ID, first.Run.Revision)
	if !errors.Is(secondErr, workflow.ErrEffectPending) ||
		executor.attempts != 2 || executor.physicalWrites != 1 {
		t.Fatalf("second physical send did not return ambiguous error with retry intent: err=%v calls=%d physical=%d",
			secondErr, executor.attempts, executor.physicalWrites)
	}
	view, viewErr := workflow.ViewState(second)
	if viewErr != nil {
		t.Fatal(viewErr)
	}
	grant, lookupErr := rt.LookupChildExecution(t.Context(), second.Run.ID)
	t.Logf("attempts=%d physicalWrites=%d secondErr=%v persistedNoEffect=%v "+
		"GrantPresentAfterAmbiguousError=%v currentRevision=%d",
		executor.attempts, executor.physicalWrites, secondErr, len(view.Effects) == 1 && view.Effects[0].PhysicalNoEffect,
		lookupErr == nil, second.Run.Revision)
	if errors.Is(lookupErr, kernel.ErrNotFound) {
		third, thirdErr := runner.Resume(t.Context(), second.Run.ID, second.Run.Revision)
		t.Logf("illegally admitted next attempt: status=%s err=%v externalWrites=%d",
			third.Run.Status, thirdErr, executor.physicalWrites)
		t.Fatal("a true NoEffect receipt from a PRIOR attempt was reused to settle an ambiguous later external send; the next retry duplicated the physical request")
	}
	if lookupErr != nil {
		t.Fatalf("original unknown physical Grant disappeared unexpectedly: %+v err=%v", grant, lookupErr)
	}
}
