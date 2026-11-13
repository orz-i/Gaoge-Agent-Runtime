package workflow

import (
	"bytes"
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
	if !verifiedSubworkflowChildOrigin(request, child) {
		return EffectResult{}, ErrInvalidExecution
	}
	if err := runner.ensureSubworkflowRelation(ctx, request, childID); err != nil {
		return EffectResult{}, err
	}
	return runner.advanceSubworkflowChild(ctx, child)
}

// The parent Effect intent is already durable. Its deterministic child must
// have an immutable ownership edge before native Create, Resume or receipt.
// Reuse the exact same owner and Attempt rules as the post-dispatch observer,
// including node-owned fan-out relations during recovery. If the write fails,
// no new child may be started; an already-existing child is not advanced.
func (runner *Runner) ensureSubworkflowRelation(ctx context.Context, request EffectRequest, childID string) error {
	if runner.relations == nil {
		return nil // Standalone Workflow callers without an index; Harness fails closed.
	}
	intent := Effect{
		ID: request.EffectID, NodeID: request.NodeID,
		ChildRunID: childID, Attempt: request.Attempt,
		OutputKey: request.OutputKey, Mapped: request.Mapped,
	}
	if strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.OwnerNodeID) == "" ||
		effectRelationOwnerID(intent) != request.OwnerNodeID {
		return ErrInvalidExecution
	}
	return runner.ensureEffectRelation(ctx, request.RunID, intent)
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
	if err := runner.ensureSubworkflowRelation(ctx, request, childID); err != nil {
		return EffectResult{}, err
	}
	child, err := runner.StartRun(ctx, StartRequest{
		ID: childID, Actor: request.Actor, Thread: request.Thread, RequestID: request.EffectID,
		ParentRunID: request.RunID,
		Goal:        "subworkflow:" + published.Definition.ID, Definition: published.Definition,
		Input: cloneJSON(request.Input), NestedDepth: request.NestedDepth,
	})
	if errors.Is(err, kernel.ErrConflict) {
		child, err = runner.runtime.Load(ctx, childID)
	}
	if err != nil && !isWorkflowProgressError(err) {
		return EffectResult{}, err
	}
	if !verifiedSubworkflowChildOrigin(request, child) {
		return EffectResult{}, ErrInvalidExecution
	}
	return subworkflowEffectResult(child)
}

// A deterministic ID identifies the desired task, not every Run occupying it.
// Validate before Resume or accepting a terminal receipt, including recovery
// from a concurrent Create conflict. Definition and input are immutable origin.
func verifiedSubworkflowChildOrigin(request EffectRequest, child kernel.Snapshot) bool {
	if request.Definition == nil || !validDefinitionReference(*request.Definition) ||
		strings.TrimSpace(request.EffectID) == "" || request.NestedDepth <= 0 ||
		!json.Valid(request.Input) || child.Run.Kind != RunKind ||
		child.Run.ID != strings.TrimSpace(request.EffectID)+"-child" ||
		child.Run.Actor != request.Actor || child.Run.Thread != request.Thread ||
		child.Run.ParentRunID != request.RunID ||
		child.Run.RequestID != strings.TrimSpace(request.EffectID) ||
		child.Run.Goal != strings.TrimSpace("subworkflow:"+request.Definition.ID) {
		return false
	}
	state, err := decodeExecutionState(child.State)
	return err == nil && state.Definition.ID == request.Definition.ID &&
		state.Definition.Revision == request.Definition.Revision &&
		state.Definition.Hash == request.Definition.Hash &&
		state.NestedDepth == request.NestedDepth &&
		bytes.Equal(normalizeJSON(state.Input), normalizeJSON(request.Input))
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
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
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
