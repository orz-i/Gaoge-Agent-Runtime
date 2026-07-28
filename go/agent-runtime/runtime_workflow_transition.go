package agentruntime

import (
	"context"
	"fmt"
	"sort"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (r *workflowRunner) commit() (bool, error) {
	transition, nextJob, err := r.buildTransition()
	if err != nil {
		return false, err
	}
	var saved []model.Event
	var applied bool
	commit := func(txCtx context.Context) error {
		_, events, wasApplied, applyErr := r.service.repo.ApplyWorkflowTransition(txCtx, r.run.Actor, r.run.RunID, transition)
		if applyErr != nil {
			return applyErr
		}
		saved, applied = events, wasApplied
		if !applied {
			return nil
		}
		return r.applyTerminalProjection(txCtx, transition.Run)
	}
	if r.service.unitOfWork != nil {
		err = r.service.unitOfWork.Within(r.ctx, commit)
	} else {
		err = commit(r.ctx)
	}
	if err != nil {
		return false, err
	}
	if !applied {
		return false, nil
	}
	r.service.publishRunEvents(r.run.RunID, saved)
	if nextJob {
		r.service.wakeContinuationJobs()
	}
	return true, nil
}

func (r *workflowRunner) buildTransition() (model.WorkflowTransition, bool, error) {
	status, nextAt := r.nextRunStatus()
	nextRun, nextExecution := r.transitionBase(status)
	r.appendWorkflowSegmentYieldEvent()
	r.appendRunTransitionEvent(nextRun)
	checkpointRows, jobRows, nextJob := []model.Checkpoint(nil), []model.ContinuationJob(nil), false
	if r.terminalOutcome != "" {
		r.applyTerminalTransition(&nextRun, &nextExecution)
	} else if r.dispatchEffectID == "" && r.shouldScheduleContinuation(status, nextAt) {
		checkpointRows, jobRows = r.transitionContinuation(nextRun, nextExecution, status, nextAt)
		nextJob = true
	}
	if err := r.applyTransitionState(&nextExecution); err != nil {
		return model.WorkflowTransition{}, false, err
	}
	return model.WorkflowTransition{
		ExpectedVersion: r.execution.Version, Execution: nextExecution, Run: nextRun,
		Steps: r.transitionSteps(), Interactions: r.interactionRows, Checkpoints: checkpointRows, ContinuationJobs: jobRows,
		Events: r.events, Result: r.result, CacheEntries: r.cacheEntries,
	}, nextJob, nil
}

func (r *workflowRunner) transitionBase(status string) (model.Run, model.WorkflowExecution) {
	nextRun := r.run
	nextRun.Status, nextRun.ErrorCode, nextRun.ErrorMessage = status, "", ""
	nextRun.PendingInteractionID = r.pendingInteractionSummary()
	nextRun.StatusReason = workflowStatusReason(status, len(r.state.Waits))
	nextExecution := r.execution
	nextExecution.Version++
	nextExecution.Status = workflowExecutionStatus(status)
	return nextRun, nextExecution
}

func (r *workflowRunner) appendRunTransitionEvent(nextRun model.Run) {
	if r.terminalOutcome != "" || nextRun.Status == r.run.Status {
		return
	}
	eventType := "run." + nextRun.Status
	if nextRun.Status == model.RunStatusRunning {
		eventType = "run.resumed"
	}
	r.events = append(r.events, newRunEvent(nextRun, eventType, nextRun.CurrentStepID, nextRun.StatusReason, map[string]interface{}{workflowPayloadRuntimeKind: model.RuntimeKindWorkflow, "waitCount": len(r.state.Waits)}, nil))
}

func (r *workflowRunner) applyTerminalTransition(run *model.Run, execution *model.WorkflowExecution) {
	switch r.terminalOutcome {
	case model.TerminalCompleted:
		run.Status, execution.Status = model.RunStatusCompleted, model.WorkflowExecutionCompleted
		run.StatusReason = "Workflow completed"
	case model.TerminalFailed:
		r.applyTerminalFailure(run, execution, model.RunStatusFailed, model.WorkflowExecutionFailed)
	case model.TerminalCancelled:
		r.applyTerminalFailure(run, execution, model.RunStatusCancelled, model.WorkflowExecutionCancelled)
	case model.RunStatusSuspended:
		r.applyTerminalFailure(run, execution, model.RunStatusSuspended, model.WorkflowExecutionSuspended)
	}
	if r.terminalOutcome != model.RunStatusSuspended {
		run.EndedAt, execution.EndedAt = &r.now, &r.now
		run.TotalLatencyMS = r.now.Sub(run.StartedAt).Milliseconds()
	}
	r.appendTerminalEvents(*run)
}

func (r *workflowRunner) applyTerminalFailure(run *model.Run, execution *model.WorkflowExecution, runStatus, executionStatus string) {
	run.Status, execution.Status = runStatus, executionStatus
	run.ErrorCode, run.ErrorMessage = r.terminalCode, r.terminalMessage
	execution.ErrorCode, execution.ErrorMessage = r.terminalCode, r.terminalMessage
}

func (r *workflowRunner) shouldScheduleContinuation(status string, nextAt *time.Time) bool {
	if nextAt != nil || r.hasPollableWaits() {
		return true
	}
	_, ok := workflowContinuationStatuses[status]
	return ok
}

func (r *workflowRunner) transitionContinuation(run model.Run, execution model.WorkflowExecution, status string, nextAt *time.Time) ([]model.Checkpoint, []model.ContinuationJob) {
	continuation := runContinuation{
		SemanticVersion: RuntimeSnapshotVersion,
		SegmentKey:      fmt.Sprintf("%s:workflow:%d", r.run.RunID, execution.Version),
		Type:            runContinuationWorkflowExecute,
		TargetStatus:    model.RunStatusRunning,
		StepID:          firstNonEmptyString(run.CurrentStepID, r.run.CurrentStepID),
	}
	checkpoint := newRunContinuationCheckpoint(run, continuation.StepID, "workflow_transition", continuation)
	availableAt := workflowContinuationAvailableAt(r.now, status, nextAt)
	job := r.service.newWorkflowContinuationJob(r.ctx, run, *checkpoint, "workflow_transition", availableAt)
	r.events = append(r.events, newRunEvent(run, "checkpoint.created", continuation.StepID, "Workflow transition checkpoint", map[string]interface{}{workflowPayloadCheckpointID: checkpoint.CheckpointID, "executionVersion": execution.Version}, nil))
	return []model.Checkpoint{*checkpoint}, []model.ContinuationJob{*job}
}

func workflowContinuationAvailableAt(now time.Time, status string, nextAt *time.Time) time.Time {
	if nextAt != nil {
		return *nextAt
	}
	if status == model.RunStatusWaitingHandoff || status == model.RunStatusCancelling || status == model.RunStatusCompensating {
		return now.Add(time.Second)
	}
	return now
}

func (r *workflowRunner) applyTransitionState(execution *model.WorkflowExecution) error {
	stateJSON, varsJSON, waitsJSON, compensationJSON, budgetJSON, err := encodeWorkflowExecutionState(r.state, r.budget)
	if err != nil {
		return err
	}
	if len(stateJSON) > r.budget.Limits.MaxStateBytes {
		return ErrWorkflowStateTooLarge
	}
	execution.StateJSON, execution.VarsJSON, execution.WaitsJSON = stateJSON, varsJSON, waitsJSON
	execution.CompensationJSON, execution.BudgetJSON = compensationJSON, budgetJSON
	return nil
}

func (r *workflowRunner) transitionSteps() []model.Step {
	steps := make([]model.Step, 0, len(r.changedSteps))
	for _, stepID := range r.stepOrder {
		if _, changed := r.changedSteps[stepID]; changed {
			steps = append(steps, r.steps[stepID])
		}
	}
	return steps
}

func (r *workflowRunner) summarizeWaits() workflowWaitSummary {
	var summary workflowWaitSummary
	for _, wait := range r.state.Waits {
		switch wait.Kind {
		case model.WorkflowWaitInteraction:
			summary.hasInteraction = true
		case model.WorkflowWaitAgent, model.WorkflowWaitWorkflow:
			summary.hasChild = true
		case model.WorkflowWaitTimer:
			summary.earliest = earlierWorkflowWake(summary.earliest, wait.WakeAt)
		}
	}
	return summary
}

func earlierWorkflowWake(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	if current == nil || candidate.Before(*current) {
		value := *candidate
		return &value
	}
	return current
}

func (r *workflowRunner) hasRunningCompensation() bool {
	for _, compensation := range r.state.Compensations {
		if compensation.Status == model.WorkflowCompensationRunning {
			return true
		}
	}
	return false
}

func (r *workflowRunner) nextRunStatus() (string, *time.Time) {
	if r.state.CancelRequested {
		return model.RunStatusCancelling, nil
	}
	waits := r.summarizeWaits()
	if r.state.ErrorMessage != "" && waits.hasChild {
		return model.RunStatusWaitingHandoff, nil
	}
	if r.hasRunningCompensation() {
		return model.RunStatusCompensating, nil
	}
	if r.state.ErrorMessage != "" && len(r.state.Compensations) > 0 {
		return model.RunStatusCompensating, nil
	}
	return workflowStatusForWaits(waits)
}

func workflowStatusForWaits(waits workflowWaitSummary) (string, *time.Time) {
	if waits.hasInteraction {
		return model.RunStatusWaitingInput, waits.earliest
	}
	if waits.earliest != nil {
		return model.RunStatusWaitingTimer, waits.earliest
	}
	if waits.hasChild {
		return model.RunStatusWaitingHandoff, nil
	}
	return model.RunStatusRunning, nil
}

func workflowExecutionStatus(runStatus string) string {
	switch runStatus {
	case model.RunStatusCancelling:
		return model.WorkflowExecutionCancelling
	case model.RunStatusCompensating:
		return model.WorkflowExecutionCompensating
	case model.RunStatusWaitingInput, model.RunStatusWaitingTimer, model.RunStatusWaitingHandoff:
		return model.WorkflowExecutionWaiting
	default:
		return model.WorkflowExecutionRunning
	}
}

func workflowStatusReason(status string, waits int) string {
	switch status {
	case model.RunStatusWaitingInput, model.RunStatusWaitingTimer, model.RunStatusWaitingHandoff:
		return fmt.Sprintf("Workflow has %d pending wait(s)", waits)
	case model.RunStatusCancelling:
		return "Workflow cancellation is settling child runs"
	case model.RunStatusCompensating:
		return "Workflow compensation is running"
	default:
		return "Workflow is running"
	}
}

func (r *workflowRunner) pendingInteractionSummary() string {
	ids := make([]string, 0)
	for _, wait := range r.state.Waits {
		if wait.Kind == model.WorkflowWaitInteraction && wait.InteractionID != "" {
			ids = append(ids, wait.InteractionID)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func (r *workflowRunner) appendTerminalEvents(run model.Run) {
	switch run.Status {
	case model.RunStatusCompleted:
		r.events = append(r.events, newRunEvent(run, "workflow.completed", run.CurrentStepID, "Workflow completed", map[string]interface{}{workflowPayloadContentHash: r.result.ContentHash}, &run.OutputProjection))
		r.events = append(r.events, newRunEvent(run, "run.completed", run.CurrentStepID, "Workflow completed", map[string]interface{}{workflowPayloadRuntimeKind: model.RuntimeKindWorkflow}, &run.OutputProjection))
	case model.RunStatusFailed:
		event := newRunEvent(run, "workflow.failed", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, &run.OutputProjection)
		event.ErrorJSON = mustRunJSON(map[string]interface{}{workflowPayloadCode: run.ErrorCode, workflowPayloadMessage: run.ErrorMessage})
		r.events = append(r.events, event, newRunEvent(run, "run.failed", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, &run.OutputProjection))
	case model.RunStatusCancelled:
		r.events = append(r.events, newRunEvent(run, "workflow.cancelled", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, &run.OutputProjection))
		r.events = append(r.events, newRunEvent(run, "run.cancelled", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, &run.OutputProjection))
	case model.RunStatusSuspended:
		r.events = append(r.events, newRunEvent(run, "workflow.compensation.failed", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, nil))
		r.events = append(r.events, newRunEvent(run, "run.suspended", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, nil))
	}
}

func (r *workflowRunner) applyTerminalProjection(ctx context.Context, run model.Run) error {
	if r.terminalOutcome == "" || r.terminalOutcome == model.RunStatusSuspended || r.service.turnProjections == nil {
		return nil
	}
	projection := TurnProjection{Input: run.InputProjection, Output: run.OutputProjection}
	usage := TurnUsage{
		InputTokens: run.InputTokens, OutputTokens: run.OutputTokens, CacheReadTokens: run.CacheReadTokens,
		CacheWriteTokens: run.CacheWriteTokens, ReasoningTokens: run.ReasoningTokens, LatencyMS: run.TotalLatencyMS,
		BilledCurrency: run.BilledCurrency, BilledNanousd: run.BilledNanousd,
	}
	switch r.terminalOutcome {
	case model.TerminalCompleted:
		_, err := r.service.turnProjections.CompleteTurn(ctx, CompleteTurnRequest{
			Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Projection: projection,
			ContentType: workflowContentTypeText, Content: r.state.Presentation, Usage: usage,
		})
		return err
	case model.TerminalFailed:
		_, err := r.service.turnProjections.FailTurn(ctx, FailTurnRequest{
			Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Projection: projection,
			ContentType: workflowContentTypeText, Content: r.state.Presentation, Usage: usage, ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage,
		})
		return err
	case model.TerminalCancelled:
		_, err := r.service.turnProjections.CancelTurn(ctx, CancelTurnRequest{
			Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Projection: projection,
			ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage,
		})
		return err
	default:
		return nil
	}
}
