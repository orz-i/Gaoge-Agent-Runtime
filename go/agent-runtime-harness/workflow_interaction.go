package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func (runner *Runner) projectsWorkflowWaits(invocation Invocation) bool {
	_, ok := runner.interactions.(WorkflowWaitInteractionProjector)
	return invocation.ExecutionClass == ExecutionWorkflow && ok
}

// The root remains running while a subworkflow waits. Harness projects the
// pending descendant inputs without changing Workflow's execution ownership.
func (runner *Runner) invocationRuntimeStatus(ctx context.Context, invocation Invocation, snapshot kernel.Snapshot) (TurnStatus, error) {
	if snapshot.Run.Status == kernel.RunStatusRunning && runner.projectsWorkflowWaits(invocation) {
		runs, err := runner.workflowInteractionRuns(ctx, snapshot)
		if err != nil {
			return "", err
		}
		for _, run := range runs {
			if run.Run.Status == kernel.RunStatusWaitingInput {
				return TurnWaitingInput, nil
			}
		}
	}
	return turnStatusFromRuntime(snapshot.Run.Status)
}

// Only persisted subworkflow effects authorize traversal. A caller cannot
// redirect an interaction to an unrelated run by supplying a runtime ID.
func (runner *Runner) workflowInteractionRuns(ctx context.Context, root kernel.Snapshot) ([]kernel.Snapshot, error) {
	runs := []kernel.Snapshot{root}
	seen := map[string]bool{root.Run.ID: true}
	for index := 0; index < len(runs); index++ {
		current := runs[index]
		if current.Run.Status != kernel.RunStatusRunning {
			continue
		}
		view, err := workflow.ViewState(current)
		if err != nil {
			return nil, err
		}
		for _, effect := range view.Effects {
			if effect.Class != workflow.EffectClassSubworkflow || effect.Status != workflow.EffectPending || effect.ChildRunID == "" {
				continue
			}
			if seen[effect.ChildRunID] || len(runs) >= 10000 {
				return nil, fmt.Errorf("workflow child traversal: %w", ErrConflict)
			}
			child, err := runner.runtime.Load(ctx, effect.ChildRunID)
			if err != nil {
				return nil, err
			}
			if child.Run.Kind != workflow.RunKind || child.Run.Actor != root.Run.Actor || child.Run.Thread != root.Run.Thread || child.Run.RequestID != effect.ID {
				return nil, fmt.Errorf("workflow child ownership: root=%s child=%s effect=%s request=%s: %w", root.Run.ID, child.Run.ID, effect.ID, child.Run.RequestID, ErrConflict)
			}
			seen[child.Run.ID] = true
			runs = append(runs, child)
		}
	}
	return runs, nil
}

func (runner *Runner) projectWorkflowWaitInteraction(ctx context.Context, turn Turn, invocation Invocation, snapshot kernel.Snapshot) error {
	if !runner.projectsWorkflowWaits(invocation) || terminalRuntimeStatus(snapshot.Run.Status) {
		return nil
	}
	runs, err := runner.workflowInteractionRuns(ctx, snapshot)
	if err != nil {
		return err
	}
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return err
	}
	projector := runner.interactions.(WorkflowWaitInteractionProjector)
	interactions, err := runner.store.ListInteractions(ctx, turn.ID)
	if err != nil {
		return err
	}
	waitingKey := ""
	for _, item := range interactions {
		if item.Status != InteractionWaiting {
			continue
		}
		if item.InvocationID != invocation.ID {
			return nil
		}
		waitingKey = item.Key
	}
	// Store intentionally permits one active interaction per Turn. Present
	// simultaneous child waits one at a time and retain the current identity.
	for _, run := range runs {
		if run.Run.Status != kernel.RunStatusWaitingInput {
			continue
		}
		wait, err := workflow.WaitRequestFromCheckpoint(run.Checkpoint)
		if err != nil {
			return err
		}
		projection, err := projector.ProjectWorkflowWaitInteraction(ctx, WorkflowWaitInteractionContext{Wait: wait, Invocation: cloneInvocation(invocation), Session: session})
		if err != nil {
			return err
		}
		if waitingKey != "" && waitingKey != projection.Key {
			continue
		}
		parentID, err := runner.workflowInteractionParent(ctx, turn, invocation, projection.Key)
		if err != nil {
			return err
		}
		_, err = runner.RequestInteraction(ctx, turn.ID, RequestInteraction{
			InvocationID: invocation.ID, ParentItemID: parentID, Key: projection.Key, Kind: projection.Kind,
			ApplicationRef: projection.ApplicationRef, ArtifactRefs: projection.ArtifactRefs, Schema: projection.Schema, Presentation: projection.Presentation,
		})
		if err != nil {
			return fmt.Errorf("project workflow interaction %s: %w", projection.Key, err)
		}
		return nil
	}
	return nil
}

func (runner *Runner) workflowInteractionParent(ctx context.Context, turn Turn, invocation Invocation, key string) (string, error) {
	existing, err := runner.store.GetInteraction(ctx, interactionID(turn.ID, invocation.ID, key))
	if err == nil {
		return existing.ParentItemID, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	return invocationLifecycleItemID(invocation, invocation.Status, invocation.Revision), nil
}

func (runner *Runner) resumeWorkflowInteraction(ctx context.Context, turn Turn, invocation Invocation, root kernel.Snapshot, interaction Interaction) (Snapshot, error) {
	runs, err := runner.workflowInteractionRuns(ctx, root)
	if err != nil {
		return Snapshot{}, err
	}
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return Snapshot{}, err
	}
	if runner.workflows == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	runCtx, err := runner.restoreInvocationContext(ctx, turn, invocation)
	if err != nil {
		return Snapshot{}, err
	}
	for _, run := range runs {
		matched, resumed, resumeErr := runner.resolveMatchingWorkflowWait(runCtx, session, invocation, run, interaction)
		if resumeErr != nil && resumed.Run.ID == "" {
			return Snapshot{}, resumeErr
		}
		if !matched {
			continue
		}
		if run.Run.ID == root.Run.ID {
			root = resumed
		}
		var result Snapshot
		var syncErr error
		if root.Run.Status == kernel.RunStatusRunning {
			result, syncErr = runner.resumeRuntimeInteractionOwner(ctx, turn, invocation, root, nil)
		} else {
			result, syncErr = runner.finishResolvedInteractionOwner(ctx, turn, invocation, root)
		}
		return result, errors.Join(resumeErr, syncErr)
	}
	// An accepted response may be replayed after its effect has completed. Never
	// feed it to the current (different) wait or dispatch a new execution.
	return runner.finishResolvedInteractionOwner(ctx, turn, invocation, root)
}

func (runner *Runner) resolveMatchingWorkflowWait(ctx context.Context, session Session, invocation Invocation, run kernel.Snapshot, interaction Interaction) (bool, kernel.Snapshot, error) {
	view, err := workflow.ViewState(run)
	if err != nil {
		return false, kernel.Snapshot{}, err
	}
	projector := runner.interactions.(WorkflowWaitInteractionProjector)
	for _, wait := range view.Waits {
		projection, err := projector.ProjectWorkflowWaitInteraction(ctx, WorkflowWaitInteractionContext{
			Wait: workflow.WaitRequest{WaitID: wait.ID, NodeID: wait.NodeID, Kind: wait.Kind, Payload: wait.Payload}, Invocation: cloneInvocation(invocation), Session: session,
		})
		if err != nil {
			return false, kernel.Snapshot{}, err
		}
		if projection.Key != interaction.Key {
			continue
		}
		if wait.Status == workflow.WaitResolved {
			if !bytes.Equal(wait.Response, interaction.Response) {
				return false, kernel.Snapshot{}, ErrConflict
			}
			return true, run, nil
		}
		resolved, err := runner.workflows.ResolveWait(ctx, run.Run.ID, run.Run.Revision, interaction.Response)
		if resolved.Run.ID != "" {
			err = normalizedFeatureStartError(ExecutionWorkflow, resolved, err, nil)
		}
		return true, resolved, err
	}
	return false, run, nil
}
