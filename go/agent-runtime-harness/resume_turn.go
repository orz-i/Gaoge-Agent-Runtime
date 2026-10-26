package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ResumeTurn continues an existing direct-Agent root without creating a Run or
// starting another attempt. The host must authorize the Turn before calling.
// SessionClient.Resume supplies the corresponding Session identity check.
//
// Waiting, budget-paused and terminal executions are returned without driving
// them. An accepted Turn without a Runtime Run returns ErrNotFound: replay the
// original Start or Prompt to repair admission, including its original Context
// and allowances. Resume never reconstructs those inputs from current settings.
func (runner *Runner) ResumeTurn(ctx context.Context, turnID string) (Snapshot, error) {
	if runner == nil || runner.runtime == nil || runner.store == nil || runner.agent == nil || strings.TrimSpace(turnID) == "" {
		return Snapshot{}, ErrInvalidRequest
	}
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return Snapshot{}, err
	}
	invocation, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil {
		return runner.resumeTurnError(ctx, turn, nil, err)
	}
	if invocation.ExecutionClass != ExecutionAgent {
		return runner.resumeTurnError(ctx, turn, nil, ErrInvalidRequest)
	}
	if terminalTurnStatus(turn.Status) {
		return runner.Refresh(ctx, turn.ID)
	}
	if turn.Status == TurnWaitingInput {
		pending, pendingErr := runner.resolvedInteractionClaimPending(ctx, turn.ID, "")
		if pendingErr != nil {
			return Snapshot{}, pendingErr
		}
		if pending {
			return runner.loadSnapshot(ctx, turn, nil)
		}
	}
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return runner.resumeTurnError(ctx, turn, nil, err)
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err != nil {
		if errors.Is(err, kernel.ErrNotFound) {
			err = errors.Join(ErrNotFound, err)
		}
		return runner.resumeTurnError(ctx, turn, nil, err)
	}
	if runtimeSnapshotConflicts(invocation, runtimeSnapshot) || runtimeSnapshot.Run.Kind != agent.RunKind || runtimeSnapshot.Run.Actor != session.Actor {
		return Snapshot{}, ErrConflict
	}
	status, err := runner.invocationRuntimeStatus(ctx, invocation, runtimeSnapshot)
	if err != nil {
		return runner.resumeTurnError(ctx, turn, &runtimeSnapshot, err)
	}
	if err = runner.repairStartItems(ctx, turn, invocation); err != nil {
		return runner.resumeTurnError(ctx, turn, &runtimeSnapshot, err)
	}
	// Approval or allowance control may already be committed while the Turn
	// projection is stale. The Run and unresolved interactions own this gate.
	if status != TurnRunning {
		return runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, runtimeSnapshot)
	}
	if err = runner.requireFrozenAdmission(ctx, turn); err != nil {
		return runner.resumeTurnError(ctx, turn, &runtimeSnapshot, err)
	}
	runCtx, err := runner.restoreInvocationContext(ctx, turn, invocation)
	if err != nil {
		return runner.resumeTurnError(ctx, turn, &runtimeSnapshot, err)
	}
	// Agent owns the current revision, invocation claim, RetryAfter, and any
	// unresolved Tool dispatch. Do not retry this physical execution on CAS
	// conflict or interpret its errors as permission to create another Run.
	resumed, resumeErr := runner.resumeInvocationExecution(runCtx, invocation, runtimeSnapshot.Run.Revision)
	if resumed.Run.ID == "" {
		if resumeErr == nil {
			resumeErr = ErrInvalidRequest
		}
		return runner.resumeTurnError(ctx, turn, &runtimeSnapshot, resumeErr)
	}
	snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, resumed)
	return snapshot, errors.Join(resumeErr, syncErr)
}

func (runner *Runner) resumeTurnError(ctx context.Context, turn Turn, runtimeSnapshot *kernel.Snapshot, cause error) (Snapshot, error) {
	snapshot, loadErr := runner.loadSnapshot(ctx, turn, runtimeSnapshot)
	return snapshot, errors.Join(cause, loadErr)
}
