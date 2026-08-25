package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func (runner *Runner) executeSubworkflowEffect(
	ctx context.Context,
	request EffectRequest,
) (EffectResult, error) {
	if runner == nil || runner.runtime == nil || runner.registry == nil ||
		request.Class != EffectClassSubworkflow || request.Definition == nil ||
		!validDefinitionReference(*request.Definition) || !json.Valid(request.Input) || request.NestedDepth <= 0 {
		return EffectResult{}, ErrInvalidExecution
	}
	childID := strings.TrimSpace(request.EffectID) + "-child"
	child, err := runner.runtime.Load(ctx, childID)
	if errors.Is(err, kernel.ErrNotFound) {
		return runner.startSubworkflowChild(ctx, request, childID)
	}
	if err != nil {
		return EffectResult{}, err
	}
	return runner.advanceSubworkflowChild(ctx, child)
}

func (runner *Runner) startSubworkflowChild(
	ctx context.Context,
	request EffectRequest,
	childID string,
) (EffectResult, error) {
	published, err := runner.registry.ResolveExact(ctx, DefinitionScope{
		Kind: DefinitionScopeActor, TenantID: request.Actor.TenantID, ActorID: request.Actor.ActorID,
	}, *request.Definition)
	if err != nil {
		return EffectResult{}, err
	}
	child, err := runner.StartRun(ctx, StartRequest{
		ID: childID, Actor: request.Actor, Thread: request.Thread, RequestID: request.EffectID,
		Goal: "subworkflow:" + published.Definition.ID, Definition: published.Definition,
		Input: cloneJSON(request.Input), NestedDepth: request.NestedDepth,
	})
	if errors.Is(err, kernel.ErrConflict) {
		child, err = runner.runtime.Load(ctx, childID)
	}
	if err != nil && !isWorkflowProgressError(err) {
		return EffectResult{}, err
	}
	return subworkflowEffectResult(child)
}

func (runner *Runner) advanceSubworkflowChild(
	ctx context.Context,
	child kernel.Snapshot,
) (EffectResult, error) {
	if child.Run.Status == kernel.RunStatusRunning {
		advanced, err := runner.Resume(ctx, child.Run.ID, child.Run.Revision)
		if err != nil && !isWorkflowProgressError(err) {
			return EffectResult{}, err
		}
		child = advanced
	}
	return subworkflowEffectResult(child)
}

func subworkflowEffectResult(snapshot kernel.Snapshot) (EffectResult, error) {
	result := EffectResult{ChildRunID: snapshot.Run.ID}
	switch snapshot.Run.Status {
	case kernel.RunStatusCompleted:
		if snapshot.Result == nil || !json.Valid(snapshot.Result.Content) {
			return EffectResult{}, ErrInvalidExecution
		}
		result.Disposition = DispositionCompleted
		result.ReceiptID = snapshot.Run.ID
		result.Output = cloneJSON(snapshot.Result.Content)
		return result, nil
	case kernel.RunStatusFailed, kernel.RunStatusCancelled:
		result.Disposition = DispositionFailed
		result.ErrorCode = snapshot.Run.ErrorCode
		result.ErrorDetail = snapshot.Run.ErrorDetail
		return result, nil
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput:
		result.Disposition = DispositionPending
		return result, nil
	default:
		return EffectResult{}, ErrInvalidExecution
	}
}

func isWorkflowProgressError(err error) bool {
	return errors.Is(err, ErrEffectPending) || errors.Is(err, ErrWaitPending) ||
		errors.Is(err, ErrSegmentYielded)
}
