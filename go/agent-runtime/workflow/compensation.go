package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var errCancellationRequested = errors.New("workflow cancellation requested")

// Cancel durably records cancellation intent and runs registered compensations
// before the Workflow becomes terminal.
func (runner *Runner) Cancel(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
	reason string,
) (kernel.Snapshot, error) {
	snapshot, state, err := runner.load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if snapshot.Run.Status != kernel.RunStatusRunning && snapshot.Run.Status != kernel.RunStatusWaitingInput {
		return snapshot, ErrWorkflowTerminal
	}
	detail := strings.TrimSpace(reason)
	if detail == "" {
		detail = errCancellationRequested.Error()
	}
	cancelled, cancelErr := runner.beginFailure(
		ctx, snapshot, state, kernel.RunStatusCancelled, "workflow.cancelled", errors.New(detail),
	)
	if cancelled.Run.Status == kernel.RunStatusCancelled &&
		(cancelled.Run.ErrorCode == "workflow.cancelled" || cancelled.Run.ErrorCode == "") {
		return cancelled, nil
	}
	return cancelled, cancelErr
}

func (runner *Runner) beginFailure(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	status kernel.RunStatus,
	code string,
	cause error,
) (kernel.Snapshot, error) {
	if state.Failure == nil {
		state.Failure = &FailureIntent{
			Status: status, Code: strings.TrimSpace(code), Detail: errorText(cause),
		}
	}
	if len(state.Compensations) == 0 {
		return runner.terminalizeFailure(ctx, snapshot, state, cause)
	}
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, errors.Join(cause, err)
	}
	persisted, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{Type: "workflow.failure.intent_created", Message: state.Failure.Code}},
	})
	if err != nil {
		return kernel.Snapshot{}, errors.Join(cause, err)
	}
	persistedState, err := decodeExecutionState(persisted.State)
	if err != nil {
		return kernel.Snapshot{}, errors.Join(cause, err)
	}
	compensated, compensationErr := runner.runCompensations(ctx, persisted, persistedState, &segmentBudget{})
	return compensated, errors.Join(cause, compensationErr)
}

func (runner *Runner) runCompensations(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	segment *segmentBudget,
) (kernel.Snapshot, error) {
	position := nextCompensationPosition(state)
	if position < 0 {
		return runner.terminalizeFailure(ctx, snapshot, state, nil)
	}
	compensation := state.Compensations[position]
	switch compensation.Status {
	case CompensationPending:
		return runner.prepareCompensation(ctx, snapshot, state, position, segment)
	case CompensationRunning:
		return runner.dispatchCompensation(ctx, snapshot, state, position, segment)
	case CompensationFailed:
		return runner.terminalizeCompensationFailure(ctx, snapshot, state, compensation)
	case CompensationCompleted:
		return runner.terminalizeCompensationFailure(ctx, snapshot, state, compensation)
	default:
		return runner.terminalizeCompensationFailure(ctx, snapshot, state, compensation)
	}
}

func nextCompensationPosition(state executionState) int {
	for index := len(state.Compensations) - 1; index >= 0; index-- {
		if state.Compensations[index].Status != CompensationCompleted {
			return index
		}
	}
	return -1
}

func (runner *Runner) prepareCompensation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	position int,
	segment *segmentBudget,
) (kernel.Snapshot, error) {
	compensation := &state.Compensations[position]
	if state.Budget.Effects >= state.Definition.Limits.MaxEffects ||
		state.Budget.CostUnitsUsed+state.Budget.CostUnitsReserved >
			state.Definition.Policy.MaxCostUnits-compensation.Call.MaxCostUnits {
		compensation.Status = CompensationFailed
		compensation.ErrorCode = "workflow.compensation_budget"
		compensation.Error = ErrBudgetExceeded.Error()
		return runner.terminalizeCompensationFailure(ctx, snapshot, state, *compensation)
	}
	effectID, err := runner.runtime.NewID("effect")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	compensation.Status = CompensationRunning
	compensation.EffectID = effectID
	state.Effects = append(state.Effects, Effect{
		ID: effectID, NodeID: compensation.NodeID, Class: compensation.Call.Class,
		Kind: compensation.Call.Kind, Revision: compensation.Call.Revision,
		Definition:   cloneDefinitionReference(compensation.Call.Definition),
		Compensation: true, Input: cloneJSON(compensation.Input),
		MaxCostUnits: compensation.Call.MaxCostUnits, NestedDepth: state.NestedDepth,
		Attempt: 1, Retry: compensation.Call.Retry, Status: EffectPending,
	})
	state.Budget.Effects++
	state.Budget.CostUnitsReserved += compensation.Call.MaxCostUnits
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	persisted, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{Type: "workflow.compensation.intent_created", Message: effectID}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	persistedState, err := decodeExecutionState(persisted.State)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.dispatchCompensation(ctx, persisted, persistedState, position, segment)
}

func (runner *Runner) dispatchCompensation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	position int,
	segment *segmentBudget,
) (kernel.Snapshot, error) {
	if segment.effects >= 1 {
		return runner.yield(ctx, snapshot, state, "compensation_effect_budget")
	}
	segment.effects++
	compensation := &state.Compensations[position]
	effectPosition := effectIndexByID(state, compensation.EffectID)
	if effectPosition < 0 || state.Effects[effectPosition].Status != EffectPending {
		return runner.terminalizeCompensationFailure(ctx, snapshot, state, *compensation)
	}
	effect := &state.Effects[effectPosition]
	result, err := runner.effects.Execute(
		ctx, buildEffectRequest(snapshot.Run.ID, state.Definition, *effect),
	)
	if err != nil {
		if scheduleEffectRetry(effect, "workflow.compensation_dispatch", err) {
			return runner.persistCompensationRetry(ctx, snapshot, state, compensation.ID)
		}
		return runner.markCompensationFailed(ctx, snapshot, state, position, effectPosition,
			"workflow.compensation_dispatch", err)
	}
	if result.Disposition == DispositionPending {
		return runner.yield(ctx, snapshot, state, "compensation_pending")
	}
	if result.Disposition == DispositionFailed {
		code := strings.TrimSpace(result.ErrorCode)
		if code == "" {
			code = "workflow.compensation_failed"
		}
		detail := strings.TrimSpace(result.ErrorDetail)
		if detail == "" {
			detail = ErrEffectFailed.Error()
		}
		if scheduleEffectRetry(effect, code, errors.New(detail)) {
			return runner.persistCompensationRetry(ctx, snapshot, state, compensation.ID)
		}
		return runner.markCompensationFailed(
			ctx, snapshot, state, position, effectPosition, code, errors.New(detail),
		)
	}
	if result.Disposition != DispositionCompleted || strings.TrimSpace(result.ReceiptID) == "" ||
		!json.Valid(result.Output) || result.CostUnits < 0 || result.CostUnits > effect.MaxCostUnits {
		return runner.markCompensationFailed(ctx, snapshot, state, position, effectPosition,
			"workflow.compensation_receipt", ErrInvalidExecution)
	}
	effect.Status = EffectCompleted
	effect.ReceiptID = strings.TrimSpace(result.ReceiptID)
	effect.Output = cloneJSON(result.Output)
	effect.CostUnits = result.CostUnits
	state.Budget.CostUnitsReserved -= effect.MaxCostUnits
	state.Budget.CostUnitsUsed += effect.CostUnits
	compensation.Status = CompensationCompleted
	compensation.ReceiptID = effect.ReceiptID
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	completed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{Type: "workflow.compensation.completed", Message: compensation.ID}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	completedState, err := decodeExecutionState(completed.State)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if nextCompensationPosition(completedState) < 0 {
		return runner.terminalizeFailure(ctx, completed, completedState, nil)
	}
	return runner.yield(ctx, completed, completedState, "compensation_progress")
}

func (runner *Runner) persistCompensationRetry(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	compensationID string,
) (kernel.Snapshot, error) {
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	yielded, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{Type: "workflow.compensation.retry_scheduled", Message: compensationID}},
	})
	return yielded, errors.Join(ErrEffectPending, err)
}

func (runner *Runner) markCompensationFailed(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	position int,
	effectPosition int,
	code string,
	cause error,
) (kernel.Snapshot, error) {
	compensation := &state.Compensations[position]
	compensation.Status = CompensationFailed
	compensation.ErrorCode = strings.TrimSpace(code)
	compensation.Error = errorText(cause)
	effect := &state.Effects[effectPosition]
	effect.Status = EffectFailed
	effect.ErrorCode = compensation.ErrorCode
	effect.Error = compensation.Error
	state.Budget.CostUnitsReserved -= effect.MaxCostUnits
	return runner.terminalizeCompensationFailure(ctx, snapshot, state, *compensation)
}

func (runner *Runner) terminalizeCompensationFailure(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	compensation Compensation,
) (kernel.Snapshot, error) {
	original := ""
	if state.Failure != nil {
		original = state.Failure.Code + ": " + state.Failure.Detail
	}
	detail := strings.TrimSpace(original + "; compensation " + compensation.ID + ": " + compensation.Error)
	state.Failure = &FailureIntent{
		Status: kernel.RunStatusFailed, Code: "workflow.compensation_failed", Detail: detail,
	}
	return runner.terminalizeFailure(ctx, snapshot, state, ErrEffectFailed)
}

func (runner *Runner) terminalizeFailure(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	cause error,
) (kernel.Snapshot, error) {
	if state.Failure == nil {
		return kernel.Snapshot{}, errors.Join(ErrInvalidExecution, cause)
	}
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, errors.Join(cause, err)
	}
	status := state.Failure.Status
	if status != kernel.RunStatusFailed && status != kernel.RunStatusCancelled {
		status = kernel.RunStatusFailed
	}
	if cause == nil {
		if status == kernel.RunStatusCancelled {
			cause = errCancellationRequested
		} else {
			cause = ErrEffectFailed
		}
	}
	eventType := "workflow.failed"
	if status == kernel.RunStatusCancelled {
		eventType = "workflow.cancelled"
	}
	terminal, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: status, State: encoded, ErrorCode: state.Failure.Code, ErrorDetail: state.Failure.Detail,
		Events: []kernel.EventDraft{{Type: eventType, Message: state.Failure.Code}},
	})
	return terminal, errors.Join(cause, err)
}
