package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RuntimeCanceller lets the host perform feature-owned cleanup, including
// cancelling remote tasks, before projecting a Runtime Run as terminal.
// IMPORTANT: kernel.ErrConflict from Cancel certifies that the rejected
// attempt performed NO external cleanup. A canceller which might already
// have changed remote state must return an outcome-unknown error instead.
type RuntimeCanceller interface {
	Cancel(context.Context, string, uint64, string) (kernel.Snapshot, error)
}

func (runner *Runner) cancelRuntimeRun(
	ctx context.Context,
	runID string,
	reason string,
) (kernel.Snapshot, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return kernel.Snapshot{}, false, ErrInvalidRequest
	}
	// A native Kernel control can distinguish a read-only/pre-Store rejection
	// from a write-capable Store outcome. Custom cancellers retain their
	// explicit contract that kernel.ErrConflict certifies zero external
	// cleanup; never infer that guarantee from a native Store conflict.
	_, nativeKernel := runner.cancellation.(*kernel.Runtime)
	var lastNativeNoEffect error
	for range maxRuntimeSyncRetryAttempts {
		current, err := runner.runtime.Load(ctx, runID)
		if errors.Is(err, kernel.ErrNotFound) {
			return kernel.Snapshot{}, false, nil
		}
		if err != nil {
			// This is a Harness-owned read-only check before calling ANY
			// RuntimeCanceller, native or custom. The private direct proof
			// certifies only that no cancellation callback ran in this
			// attempt; the underlying Store error proves nothing itself.
			return kernel.Snapshot{}, false, cancellationReadNoEffect{cause: err}
		}
		if terminalRuntimeStatus(current.Run.Status) {
			return current, true, nil
		}
		cancelled, err := runner.cancellation.Cancel(ctx, runID, current.Run.Revision, strings.TrimSpace(reason))
		if nativeKernel {
			if kernel.IsControlNoEffect(err) {
				if errors.Is(err, kernel.ErrConflict) {
					// A pre-Store CAS revision mismatch is safe to retry,
					// but preserve its native proof if all retries exhaust.
					lastNativeNoEffect = err
					continue
				}
				return cancelled, true, err
			}
			// Even a native Store ErrConflict can follow an uncertain
			// write-capable CAS. Never retry or release its intent blindly.
			return cancelled, true, err
		}
		if errors.Is(err, kernel.ErrConflict) {
			// The custom RuntimeCanceller contract reserves this error
			// for attempts with absolutely no external cleanup.
			continue
		}
		return cancelled, true, err
	}
	if nativeKernel && lastNativeNoEffect != nil {
		return kernel.Snapshot{}, false, lastNativeNoEffect
	}
	// Every custom canceller conflict was contractually no-effect.
	return kernel.Snapshot{}, false, kernel.ErrConflict
}

func (runner *Runner) syncTerminalDelegations(ctx context.Context, turn Turn, runs map[string]kernel.Snapshot) error {
	items, err := listAllItems(ctx, runner.store, turn.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Kind != ItemDelegation || item.Status != ItemStarted {
			continue
		}
		snapshot, found := runs[item.RunID]
		if !found || !terminalRuntimeStatus(snapshot.Run.Status) {
			continue
		}
		var payload delegationItemPayload
		if err = json.Unmarshal(item.Payload, &payload); err != nil {
			return err
		}
		invocation, loadErr := runner.store.GetInvocation(ctx, item.InvocationID)
		if loadErr != nil {
			return loadErr
		}
		delegation := handoff.Delegation{
			ID: payload.DelegationID, MemberID: payload.MemberID, MemberRevision: payload.MemberRevision, ChildRunID: payload.ChildRunID,
			Goal: payload.Goal, Status: handoff.Status(snapshot.Run.Status),
		}
		if payload.Execution != nil {
			delegation = *payload.Execution
			delegation.Status = handoff.Status(snapshot.Run.Status)
		}
		if snapshot.Run.Status == kernel.RunStatusCompleted && snapshot.Result != nil {
			delegation.Result = append(json.RawMessage(nil), snapshot.Result.Content...)
		}
		_, err = runner.recordDelegationItem(ctx, turn, invocation, delegation, delegationItemStatus(delegation.Status), item.ID)
		if err != nil {
			return err
		}
	}
	return nil
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
