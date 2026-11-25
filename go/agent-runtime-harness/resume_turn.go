package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ResumeTurn continues an existing top-level Agent Run without creating a Run or
// starting another attempt. The host must authorize the Turn before calling.
// SessionClient.Resume supplies the corresponding Session identity check.
//
// Waiting, budget-paused and terminal executions are returned without driving
// them. An accepted Turn without a Runtime Run returns ErrNotFound: replay the
// original Start or Prompt to repair admission, including its original Context
// and allowances. Resume never reconstructs those inputs from current settings.
func (runner *Runner) ResumeTurn(ctx context.Context, turnID string) (Snapshot, error) {
	return runner.resumeTurn(ctx, turnID, nil, "", 0)
}

// ResumeTurnAtRevision continues the original root and revision selected by a
// durable scheduler. An obsolete wakeup cannot attach to a newer root or phase.
func (runner *Runner) ResumeTurnAtRevision(ctx context.Context, turnID, runID string, revision uint64) (Snapshot, error) {
	if strings.TrimSpace(runID) == "" || revision == 0 {
		return Snapshot{}, ErrInvalidRequest
	}
	return runner.resumeTurn(ctx, turnID, nil, runID, revision)
}

// The Session facade carries its Host-authorized Actor/Thread to the native
// physical continuation gate. Direct Runner callers keep their own Host auth.
func (runner *Runner) resumeTurnForSession(ctx context.Context, turnID string, actor kernel.ActorRef, thread kernel.ThreadRef) (Snapshot, error) {
	return runner.resumeTurn(ctx, turnID, &sessionBoundOwner{actor: actor, thread: thread}, "", 0)
}

func (runner *Runner) resumeTurn(ctx context.Context, turnID string, expected *sessionBoundOwner, runID string, revision uint64) (Snapshot, error) {
	if runner == nil || runner.runtime == nil || runner.store == nil || runner.agent == nil || strings.TrimSpace(turnID) == "" {
		return Snapshot{}, ErrInvalidRequest
	}
	turn, err := loadVerifiedTurn(ctx, runner.store, strings.TrimSpace(turnID))
	if err != nil {
		return Snapshot{}, err
	}
	invocation, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil {
		return runner.resumeTurnError(ctx, turn, nil, err)
	}
	if runID != "" && invocation.ExecutionRefID != runID {
		return Snapshot{}, kernel.ErrConflict
	}
	if expected != nil && requireSessionRootOwner(turn, invocation, *expected) != nil {
		return Snapshot{}, ErrConflict
	}
	if terminalTurnStatus(turn.Status) {
		return runner.Refresh(ctx, turn.ID)
	}
	pending, pendingErr := runner.resolvedInteractionClaimPending(ctx, turn.ID, "")
	if pendingErr != nil {
		return Snapshot{}, pendingErr
	}
	if pending {
		return runner.loadSnapshot(ctx, turn, nil)
	}
	session, err := loadVerifiedSession(ctx, runner.store, turn.SessionID)
	if err != nil {
		return runner.resumeTurnError(ctx, turn, nil, err)
	}
	if expected != nil && session.Actor != expected.actor {
		return Snapshot{}, ErrConflict
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err != nil {
		if errors.Is(err, kernel.ErrNotFound) {
			err = errors.Join(ErrNotFound, err)
		}
		return runner.resumeTurnError(ctx, turn, nil, err)
	}
	if revision != 0 && runtimeSnapshot.Run.Revision != revision {
		return Snapshot{}, kernel.ErrConflict
	}
	if runtimeSnapshotConflicts(invocation, runtimeSnapshot) || runtimeSnapshot.Run.Kind != agent.RunKind || runtimeSnapshot.Run.Actor != session.Actor {
		return Snapshot{}, ErrConflict
	}
	if expected != nil && (runtimeSnapshot.Run.Actor != expected.actor || runtimeSnapshot.Run.Thread != expected.thread) {
		// Cross-check the independent Kernel owner immediately before any
		// executable Agent.Resume work. A retargeted Session Store alone
		// cannot authorize someone else's original physical invocation.
		return Snapshot{}, ErrConflict
	}
	// Matching Run ID, Kind and Session Actor do not authorize a physical
	// continuation from another frozen Thread or Goal. Verify the original
	// root execution before granting Agent.Resume any work.
	if err = validateRootRuntimeOwnership(invocation, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	// Managed recovery is a separate source-bound continuation. Validate
	// frozen admission first, but do not run ordinary restoration/preparation
	// or terminal projection before Kernel PARK/ENABLE or exact T4 repair.
	if err = runner.requireFrozenAdmission(ctx, turn); err != nil {
		return runner.resumeTurnError(ctx, turn, &runtimeSnapshot, err)
	}
	if recovered, managed, recoveryErr := runner.recoverManagedInvocation(ctx, invocation); managed {
		if recoveryErr != nil {
			return runner.resumeTurnError(ctx, turn, &runtimeSnapshot, recoveryErr)
		}
		if recovered.Run.Status == kernel.RunStatusCancelled {
			return runner.syncRuntimeSnapshotWithRetryAfterCancellation(ctx, turn, invocation, recovered)
		}
		return runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, recovered)
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
	if err = runner.refreshStandardRemoteChildren(ctx, turn); err != nil {
		return runner.resumeTurnError(ctx, turn, &runtimeSnapshot, err)
	}
	if runner.standardRemoteCoordinator() != nil {
		// The original Agent owns the retained physical Tool grant. Recover
		// its exact terminal child receipts before issuing another Resume
		// grant; never re-enter a still-dispatched delegation Tool.
		original, ok := runner.agent.(*agent.Runner)
		if !ok {
			return Snapshot{}, ErrConflict
		}
		if scope, scopeErr := original.RootDeferredCleanupEligibility(ctx, runtimeSnapshot.Run.ID, runtimeSnapshot.Run.Revision); scopeErr == nil && scope.Valid() {
			repaired, receiptErr := original.ReconcileRootDeferredChildren(ctx, runtimeSnapshot.Run.ID, runtimeSnapshot.Run.Revision, scope)
			if errors.Is(receiptErr, agent.ErrToolOutcomeUnknown) {
				// The peer has not reached terminal evidence yet. No new
				// physical execution is admitted just to poll a child.
				return runner.loadSnapshot(ctx, turn, &repaired)
			}
			if receiptErr != nil {
				return runner.resumeTurnError(ctx, turn, &repaired, receiptErr)
			}
			runtimeSnapshot = repaired
		}
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
