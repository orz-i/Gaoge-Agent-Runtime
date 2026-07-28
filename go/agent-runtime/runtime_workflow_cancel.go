package agentruntime

import (
	"context"
	"errors"
	"fmt"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Engine) cancelWorkflowRun(ctx context.Context, initial model.Run) (bool, error) {
	for attempt := 0; attempt < 5; attempt++ {
		run, err := s.workflowRunForAttempt(ctx, initial, initial.Actor, initial.RunID, attempt)
		if err != nil {
			return false, err
		}
		done, retry, cancelErr := s.cancelWorkflowRunAttempt(ctx, run)
		if cancelErr != nil {
			return false, cancelErr
		}
		if retry {
			continue
		}
		return done, nil
	}
	return false, ErrWorkflowVersionConflict
}

func (s *Engine) cancelWorkflowRunAttempt(ctx context.Context, run model.Run) (bool, bool, error) {
	if run.EndedAt != nil || run.Status == model.RunStatusSuspended {
		return true, false, nil
	}
	execution, err := s.repo.GetWorkflowExecution(ctx, run.Actor, run.RunID)
	if err != nil {
		return false, false, err
	}
	prepared, err := s.prepareWorkflowCancellation(ctx, run, *execution)
	if err != nil {
		return false, false, err
	}
	_, events, applied, err := s.repo.ApplyWorkflowTransition(ctx, run.Actor, run.RunID, prepared.transition)
	if errors.Is(err, ErrWorkflowVersionConflict) || err == nil && !applied {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	s.publishRunEvents(run.RunID, events)
	s.wakeContinuationJobs()
	s.cancelWorkflowChildWaits(ctx, run.Actor, prepared.waits)
	return true, false, nil
}

type workflowCancellationPreparation struct {
	transition model.WorkflowTransition
	waits      map[string]model.WorkflowWait
}

func (s *Engine) prepareWorkflowCancellation(
	ctx context.Context,
	run model.Run,
	execution model.WorkflowExecution,
) (workflowCancellationPreparation, error) {
	state, budget, err := decodeWorkflowExecutionState(execution)
	if err != nil {
		return workflowCancellationPreparation{}, err
	}
	state.CancelRequested = true
	stateJSON, varsJSON, waitsJSON, compensationJSON, budgetJSON, err := encodeWorkflowExecutionState(state, budget)
	if err != nil {
		return workflowCancellationPreparation{}, err
	}
	nextExecution := execution
	nextExecution.Version++
	nextExecution.Status = model.WorkflowExecutionCancelling
	nextExecution.StateJSON, nextExecution.VarsJSON, nextExecution.WaitsJSON = stateJSON, varsJSON, waitsJSON
	nextExecution.CompensationJSON, nextExecution.BudgetJSON = compensationJSON, budgetJSON
	nextRun := run
	nextRun.Status, nextRun.StatusReason = model.RunStatusCancelling, "Workflow cancellation requested"
	continuation := runContinuation{
		SemanticVersion: RuntimeSnapshotVersion,
		SegmentKey:      fmt.Sprintf("%s:workflow:%d", run.RunID, nextExecution.Version),
		Type:            runContinuationWorkflowExecute,
		TargetStatus:    model.RunStatusRunning,
		StepID:          run.CurrentStepID,
	}
	checkpoint := newRunContinuationCheckpoint(run, run.CurrentStepID, "workflow_cancel", continuation)
	job := s.newWorkflowContinuationJob(ctx, run, *checkpoint, "workflow_cancel", s.now())
	return workflowCancellationPreparation{
		transition: model.WorkflowTransition{
			ExpectedVersion: execution.Version,
			Execution:       nextExecution,
			Run:             nextRun,
			Checkpoints:     []model.Checkpoint{*checkpoint},
			ContinuationJobs: []model.ContinuationJob{
				*job,
			},
			Events: []model.Event{
				newRunEvent(run, "workflow.cancellation.requested", run.CurrentStepID, "Workflow cancellation requested", nil, nil),
				newRunEvent(run, "run.cancelling", run.CurrentStepID, "Workflow cancellation requested", map[string]interface{}{workflowPayloadRuntimeKind: model.RuntimeKindWorkflow}, nil),
				newRunEvent(run, "checkpoint.created", run.CurrentStepID, "Workflow cancellation checkpoint", map[string]interface{}{workflowPayloadCheckpointID: checkpoint.CheckpointID}, nil),
			},
		},
		waits: state.Waits,
	}, nil
}

func (s *Engine) cancelWorkflowChildWaits(ctx context.Context, actor model.ActorRef, waits map[string]model.WorkflowWait) {
	for _, wait := range waits {
		if wait.ChildRunID != "" {
			_, _ = s.CancelRun(context.WithoutCancel(ctx), actor, wait.ChildRunID)
		}
	}
}
