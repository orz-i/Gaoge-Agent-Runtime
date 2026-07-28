package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (r *workflowRunner) cancelRuntimeOwnedChildren() {
	for _, activation := range r.state.Activations {
		if activation.ChildRunID == "" {
			continue
		}
		child, err := r.service.repo.GetRun(r.ctx, r.run.Actor, activation.ChildRunID)
		if err != nil || child.EndedAt != nil {
			continue
		}
		_, _ = r.service.CancelRun(context.WithoutCancel(r.ctx), r.run.Actor, child.RunID)
	}
}

func (r *workflowRunner) cancelNonChildWaits(reason string) {
	waitIDs := make([]string, 0, len(r.state.Waits))
	for waitID := range r.state.Waits {
		waitIDs = append(waitIDs, waitID)
	}
	sort.Strings(waitIDs)
	for _, waitID := range waitIDs {
		wait := r.state.Waits[waitID]
		if wait.Kind == model.WorkflowWaitAgent || wait.Kind == model.WorkflowWaitWorkflow {
			continue
		}
		delete(r.state.Waits, waitID)
		activation, ok := r.state.Activations[wait.ActivationKey]
		if ok && activation.Status == model.WorkflowStepStatusWaiting {
			activation.Status, activation.WaitID, activation.WakeAt = model.WorkflowStepStatusCancelled, "", nil
			activation.ErrorCode, activation.ErrorMessage = "workflow_wait_cancelled", reason
			r.saveActivation(activation)
			step := r.steps[activation.StepID]
			step.Status, step.WaitingKind, step.WaitingID = model.WorkflowStepStatusCancelled, "", ""
			step.ResultSummary = reason
			step.ErrorJSON = mustRunJSON(map[string]interface{}{workflowPayloadCode: activation.ErrorCode, workflowPayloadMessage: reason})
			endedAt := r.now
			step.EndedAt = &endedAt
			r.steps[activation.StepID] = step
			r.changedSteps[activation.StepID] = struct{}{}
			r.events = append(r.events, newRunEvent(r.run, "step.cancelled", activation.StepID, reason, map[string]interface{}{workflowPayloadNodeID: activation.NodeID, workflowPayloadWaitKind: wait.Kind}, nil))
		}
		if wait.InteractionID != "" {
			interaction, exists := r.interactions[wait.InteractionID]
			if exists && interaction.Status == model.InteractionPending {
				interaction.Status, interaction.ResolvedAt, interaction.UpdatedAt = model.InteractionCancelled, &r.now, r.now
				r.interactions[interaction.InteractionID] = interaction
				r.interactionRows = append(r.interactionRows, interaction)
				r.events = append(r.events, newRunEvent(r.run, "interaction.cancelled", wait.StepID, reason, map[string]interface{}{workflowPayloadInteractionID: interaction.InteractionID}, nil))
			}
		}
	}
}

func (r *workflowRunner) settleRuntimeOwnedChildren(reason string) bool {
	paths := make([]string, 0, len(r.state.Activations))
	for path := range r.state.Activations {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	allSettled := true
	for _, path := range paths {
		activation := r.state.Activations[path]
		if !workflowActivationHasReservedChild(activation) {
			continue
		}
		if !r.settleRuntimeOwnedChild(activation, reason) {
			allSettled = false
		}
	}
	return allSettled
}

func workflowActivationHasReservedChild(activation workflowActivationState) bool {
	if activation.ChildRunID == "" {
		return false
	}
	return activation.WaitID != "" || activation.ReservedLLM != 0 || activation.ReservedTools != 0 || activation.ReservedChildren != 0
}

func (r *workflowRunner) settleRuntimeOwnedChild(activation workflowActivationState, reason string) bool {
	child, err := r.service.repo.GetRun(r.ctx, r.run.Actor, activation.ChildRunID)
	if err == nil && child.EndedAt == nil {
		return false
	}
	settlement := r.workflowChildSettlement(child, err)
	r.applyWorkflowChildSettlementBudget(activation, settlement)
	r.recordWorkflowChildSettlement(activation, settlement, reason)
	return true
}

func (r *workflowRunner) workflowChildSettlement(child *model.Run, loadErr error) workflowChildSettlement {
	settlement := workflowChildSettlement{actualChildren: 1}
	if loadErr != nil || child == nil {
		return settlement
	}
	settlement.llmCalls, settlement.toolCalls = child.LLMCallsCount, child.ToolCallsCount
	if child.RuntimeKind != model.RuntimeKindWorkflow {
		return settlement
	}
	execution, err := r.service.repo.GetWorkflowExecution(r.ctx, r.run.Actor, child.RunID)
	if err != nil {
		return settlement
	}
	var childBudget model.WorkflowBudget
	if json.Unmarshal([]byte(execution.BudgetJSON), &childBudget) != nil {
		return settlement
	}
	settlement.llmCalls, settlement.toolCalls = childBudget.UsedLLMCalls, childBudget.UsedToolCalls
	settlement.actualChildren += childBudget.ChildRuns
	return settlement
}

func (r *workflowRunner) applyWorkflowChildSettlementBudget(activation workflowActivationState, settlement workflowChildSettlement) {
	if unusedChildren := activation.ReservedChildren - settlement.actualChildren; unusedChildren > 0 {
		r.budget.ChildRuns = max(0, r.budget.ChildRuns-unusedChildren)
	}
	r.budget.ConcurrentRuns = max(0, r.budget.ConcurrentRuns-1)
	r.budget.ReservedLLMCalls = max(0, r.budget.ReservedLLMCalls-activation.ReservedLLM)
	r.budget.ReservedToolCalls = max(0, r.budget.ReservedToolCalls-activation.ReservedTools)
	r.budget.UsedLLMCalls += settlement.llmCalls
	r.budget.UsedToolCalls += settlement.toolCalls
	r.run.LLMCallsCount += settlement.llmCalls
	r.run.ToolCallsCount += settlement.toolCalls
}

func (r *workflowRunner) recordWorkflowChildSettlement(activation workflowActivationState, settlement workflowChildSettlement, reason string) {
	delete(r.state.Waits, activation.WaitID)
	activation.Status, activation.WaitID, activation.WakeAt = model.WorkflowStepStatusCancelled, "", nil
	activation.ReservedLLM, activation.ReservedTools, activation.ReservedChildren = 0, 0, 0
	activation.ErrorCode, activation.ErrorMessage = "workflow_child_cancelled", reason
	r.saveActivation(activation)
	step := r.steps[activation.StepID]
	step.Status, step.WaitingKind, step.WaitingID = model.WorkflowStepStatusCancelled, "", ""
	step.ResultSummary = reason
	step.ErrorJSON = mustRunJSON(map[string]interface{}{workflowPayloadCode: activation.ErrorCode, workflowPayloadMessage: reason})
	endedAt := r.now
	step.EndedAt = &endedAt
	r.steps[activation.StepID] = step
	r.changedSteps[activation.StepID] = struct{}{}
	r.events = append(r.events,
		newRunEvent(r.run, "workflow.child.settled", activation.StepID, reason, map[string]interface{}{workflowPayloadChildRunID: activation.ChildRunID, workflowPayloadLLMCalls: settlement.llmCalls, workflowPayloadToolCalls: settlement.toolCalls}, nil),
		newRunEvent(r.run, "workflow.budget.settled", activation.StepID, activation.NodeID, map[string]interface{}{workflowPayloadChildRunID: activation.ChildRunID, workflowPayloadLLMCalls: settlement.llmCalls, workflowPayloadToolCalls: settlement.toolCalls}, nil),
		newRunEvent(r.run, "step.cancelled", activation.StepID, reason, map[string]interface{}{workflowPayloadNodeID: activation.NodeID, workflowPayloadChildRunID: activation.ChildRunID}, nil),
	)
}

func (s *Engine) failWorkflowRun(ctx context.Context, run model.Run, cause error) {
	execution, err := s.repo.GetWorkflowExecution(ctx, run.Actor, run.RunID)
	if err != nil {
		return
	}
	state, budget, err := decodeWorkflowExecutionState(*execution)
	if err != nil {
		return
	}
	state.ErrorCode, state.ErrorMessage = workflowFailureCode(cause), cause.Error()
	stateJSON, varsJSON, waitsJSON, compensationJSON, budgetJSON, err := encodeWorkflowExecutionState(state, budget)
	if err != nil {
		return
	}
	nextExecution := *execution
	nextExecution.Version++
	nextExecution.StateJSON, nextExecution.VarsJSON, nextExecution.WaitsJSON = stateJSON, varsJSON, waitsJSON
	nextExecution.CompensationJSON, nextExecution.BudgetJSON = compensationJSON, budgetJSON
	nextExecution.Status = model.WorkflowExecutionRunning
	nextRun := run
	nextRun.Status, nextRun.ErrorCode, nextRun.ErrorMessage = model.RunStatusRunning, state.ErrorCode, state.ErrorMessage
	continuation := runContinuation{
		SemanticVersion: RuntimeSnapshotVersion, SegmentKey: fmt.Sprintf("%s:workflow:%d", run.RunID, nextExecution.Version),
		Type: runContinuationWorkflowExecute, TargetStatus: model.RunStatusRunning, StepID: run.CurrentStepID,
	}
	checkpoint := newRunContinuationCheckpoint(run, run.CurrentStepID, "workflow_failure", continuation)
	job := s.newWorkflowContinuationJob(ctx, run, *checkpoint, "workflow_failure", s.now())
	transition := model.WorkflowTransition{
		ExpectedVersion: execution.Version, Execution: nextExecution, Run: nextRun,
		Checkpoints: []model.Checkpoint{*checkpoint}, ContinuationJobs: []model.ContinuationJob{*job},
		Events: []model.Event{newRunEvent(run, "workflow.failure_requested", run.CurrentStepID, cause.Error(), map[string]interface{}{workflowPayloadCode: state.ErrorCode}, nil)},
	}
	_, events, applied, _ := s.repo.ApplyWorkflowTransition(ctx, run.Actor, run.RunID, transition)
	if applied {
		s.publishRunEvents(run.RunID, events)
		s.wakeContinuationJobs()
	}
}
