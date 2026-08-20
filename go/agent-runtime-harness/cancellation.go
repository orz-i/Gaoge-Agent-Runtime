package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

func (runner *Runner) cancelRuntimeRun(
	ctx context.Context,
	runID string,
	reason string,
) (kernel.Snapshot, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return kernel.Snapshot{}, false, ErrInvalidRequest
	}
	for range maxRuntimeSyncRetryAttempts {
		current, err := runner.runtime.Load(ctx, runID)
		if errors.Is(err, kernel.ErrNotFound) {
			return kernel.Snapshot{}, false, nil
		}
		if err != nil {
			return kernel.Snapshot{}, false, err
		}
		if terminalRuntimeStatus(current.Run.Status) {
			return current, true, nil
		}
		cancelled, err := runner.runtime.Cancel(ctx, runID, current.Run.Revision, strings.TrimSpace(reason))
		if errors.Is(err, kernel.ErrConflict) {
			continue
		}
		return cancelled, true, err
	}
	return kernel.Snapshot{}, false, ErrConflict
}

func (runner *Runner) cancelTurnDescendants(
	ctx context.Context,
	turnID string,
	rootInvocationID string,
	rootRunID string,
	reason string,
) (map[string]kernel.Snapshot, error) {
	invocations, err := runner.store.ListInvocations(ctx, turnID)
	if err != nil {
		return nil, err
	}
	seeds := childInvocationRunIDs(invocations, rootInvocationID)
	delegatedRunIDs, err := runner.delegatedRunIDs(ctx, turnID)
	if err != nil {
		return nil, err
	}
	seeds = append(seeds, delegatedRunIDs...)
	runs, err := runner.cancelRelatedRuns(ctx, rootRunID, seeds, reason)
	if err != nil {
		return nil, err
	}
	addMissingInvocationCancellations(runs, invocations, rootInvocationID, reason)
	return runs, nil
}

func childInvocationRunIDs(invocations []Invocation, rootInvocationID string) []string {
	result := make([]string, 0, len(invocations))
	for _, invocation := range invocations {
		if invocation.ID != rootInvocationID && strings.TrimSpace(invocation.ExecutionRefID) != "" {
			result = append(result, invocation.ExecutionRefID)
		}
	}
	return result
}

func addMissingInvocationCancellations(
	runs map[string]kernel.Snapshot,
	invocations []Invocation,
	rootInvocationID string,
	reason string,
) {
	for _, invocation := range invocations {
		if invocation.ID == rootInvocationID || terminalInvocationStatus(invocation.Status) {
			continue
		}
		runID := strings.TrimSpace(invocation.ExecutionRefID)
		if _, ok := runs[runID]; runID != "" && !ok {
			runs[runID] = cancelledRuntimeSnapshot(runID, reason)
		}
	}
}

func (runner *Runner) delegatedRunIDs(ctx context.Context, turnID string) ([]string, error) {
	items, err := listAllItems(ctx, runner.store, turnID)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0)
	for _, item := range items {
		if item.Kind != ItemDelegation {
			continue
		}
		var payload delegationItemPayload
		if err = json.Unmarshal(item.Payload, &payload); err != nil {
			return nil, err
		}
		if runID := strings.TrimSpace(payload.ChildRunID); runID != "" {
			result = append(result, runID)
		}
	}
	return result, nil
}

func (runner *Runner) cancelRelatedRuns(
	ctx context.Context,
	rootRunID string,
	seedRunIDs []string,
	reason string,
) (map[string]kernel.Snapshot, error) {
	rootRunID = strings.TrimSpace(rootRunID)
	if rootRunID == "" {
		return nil, ErrInvalidRequest
	}
	queue := append([]string{rootRunID}, seedRunIDs...)
	visited := make(map[string]struct{}, len(queue))
	result := make(map[string]kernel.Snapshot)
	for len(queue) > 0 {
		runID := strings.TrimSpace(queue[0])
		queue = queue[1:]
		if _, ok := visited[runID]; ok {
			continue
		}
		visited[runID] = struct{}{}
		snapshot, found, err := runner.cancelDescendantRun(ctx, rootRunID, runID, reason)
		if err != nil {
			return nil, err
		}
		if found {
			result[runID] = snapshot
		}
		children, err := runner.relatedChildRunIDs(ctx, runID)
		if err != nil {
			return nil, err
		}
		queue = append(queue, children...)
	}
	return result, nil
}

func (runner *Runner) cancelDescendantRun(
	ctx context.Context,
	rootRunID string,
	runID string,
	reason string,
) (kernel.Snapshot, bool, error) {
	if runID == rootRunID {
		return kernel.Snapshot{}, false, nil
	}
	return runner.cancelRuntimeRun(ctx, runID, reason)
}

func (runner *Runner) relatedChildRunIDs(ctx context.Context, runID string) ([]string, error) {
	if runner.relationReader == nil {
		return nil, nil
	}
	relations, err := runner.relationReader.ListChildren(ctx, runID)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(relations))
	for _, relation := range relations {
		result = append(result, relation.ChildRunID)
	}
	return result, nil
}

func (runner *Runner) syncInvocationRuns(
	ctx context.Context,
	turn Turn,
	runs map[string]kernel.Snapshot,
) error {
	invocations, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return err
	}
	for _, invocation := range invocations {
		runtimeSnapshot, ok := runs[strings.TrimSpace(invocation.ExecutionRefID)]
		if !ok || strings.TrimSpace(invocation.ParentItemID) == "" {
			continue
		}
		if err = runner.syncChildInvocationWithRetry(ctx, turn, invocation.ID, runtimeSnapshot); err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) syncChildInvocationWithRetry(
	ctx context.Context,
	turn Turn,
	invocationID string,
	runtimeSnapshot kernel.Snapshot,
) error {
	for range maxRuntimeSyncRetryAttempts {
		invocation, err := runner.store.GetInvocation(ctx, invocationID)
		if err != nil {
			return err
		}
		if terminalInvocationStatus(invocation.Status) {
			return nil
		}
		_, err = runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
		if !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return ErrConflict
}

func cancelledRuntimeSnapshot(runID string, reason string) kernel.Snapshot {
	return kernel.Snapshot{Run: kernel.Run{
		ID: runID, Status: kernel.RunStatusCancelled,
		ErrorCode: "run.cancelled", ErrorDetail: strings.TrimSpace(reason),
	}}
}
