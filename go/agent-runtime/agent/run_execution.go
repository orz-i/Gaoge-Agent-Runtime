package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RunExecution configures the Agent Kernel execution lease. Nil configuration
// uses a unique internal owner and a checked call-window duration.
// It uses the existing Store ExecutionLease; no extra scheduler, progress
// record, queue, or ActiveSegment table is introduced. The Store owns time.
type RunExecution struct {
	OwnerID       string
	LeaseDuration time.Duration
}

func validRunExecution(value RunExecution, policy ExecutionPolicy) bool {
	return strings.TrimSpace(value.OwnerID) != "" && strings.TrimSpace(value.OwnerID) == value.OwnerID &&
		len(value.OwnerID) <= 128 &&
		value.LeaseDuration >= time.Millisecond && value.LeaseDuration <= 24*time.Hour &&
		// One Model/Tool dispatch must not normally outlast the Run lease. This
		// does NOT fence physical side effects of a stalled external provider.
		value.LeaseDuration-time.Second > max(policy.Model.Timeout, policy.Tool.Timeout) &&
		safeToolRetryWindow(policy.Tool, value.LeaseDuration-time.Second)
}

// safeToolRetryWindow rejects configurations where the full configured Tool
// retry/backoff window cannot fit one lease. An uncooperative external Tool
// may STILL exceed Context timeout: durable dispatch intent prevents replay.
func safeToolRetryWindow(policy CallPolicy, available time.Duration) bool {
	attempts := time.Duration(policy.MaxAttempts)
	if attempts < 1 || available <= 0 || policy.Timeout <= 0 {
		return false
	}
	if policy.Timeout > available/attempts {
		return false
	}
	remainder := available - attempts*policy.Timeout
	if attempts == 1 {
		return true
	}
	// Retry delay has up to 20% deterministic jitter, rounded conservatively.
	if policy.MaxBackoff > 0 && policy.MaxBackoff > available/2 {
		return false
	}
	delay := policy.MaxBackoff + policy.MaxBackoff/5
	return delay <= remainder/(attempts-1)
}

// withReceiptOwner is only for unmetered verifier-only recovery. Ordinary
// execution acquires and retires its lease with its physical Grant instead.
// The observer cannot dispatch and releases only its own acquired lease.
func (runner *Runner) withReceiptOwner(ctx context.Context, runID string, expectedRevision uint64,
	execute func(context.Context, *Runner) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	source, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if source.Run.Revision != expectedRevision || source.Run.BudgetScopeID != "" || runner.activePhysicalGrant != nil {
		return source, kernel.ErrExecutionConflict
	}
	if lease, bound := runner.runtime.BoundExecutionLease(); bound {
		if lease.RunID != runID {
			return kernel.Snapshot{}, kernel.ErrExecutionConflict
		}
		if err := runner.runtime.RequireExecution(ctx, runID); err != nil {
			return kernel.Snapshot{}, err
		}
		scoped := *runner
		scoped.activeRunLease = &lease
		scoped.externalRunLease = true
		return execute(ctx, &scoped)
	}
	config := *runner.runExecution
	lease, err := runner.runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: runID, OwnerID: config.OwnerID, LeaseDuration: config.LeaseDuration,
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	// Before execute is called there are NO Model/Tool effects from this
	// newly claimed segment. Always relinquish it on preflight failure,
	// including a cancelled caller context, so the next recovery is not
	// blocked for a full TTL by a stale Revision.
	abortBeforeDispatch := func(snapshot kernel.Snapshot, cause error) (kernel.Snapshot, error) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		releaseErr := runner.runtime.ReleaseExecution(cleanupCtx, lease)
		if releaseErr != nil && !errors.Is(releaseErr, kernel.ErrExecutionConflict) {
			return snapshot, errors.Join(cause, releaseErr)
		}
		return snapshot, cause
	}
	bound, err := runner.runtime.WithExecution(lease)
	if err != nil {
		return abortBeforeDispatch(kernel.Snapshot{}, err)
	}
	// The pre-claim snapshot is not a grant. A concurrent valid control
	// can commit before we acquire, or a Store implementation can
	// commit before returning Acquire. Re-read under the acquired owner
	// BEFORE any middleware/Model/Tool sees this snapshot.
	persisted, err := bound.Load(ctx, runID)
	if err != nil {
		return abortBeforeDispatch(kernel.Snapshot{}, err)
	}
	if persisted.Run.Revision != expectedRevision {
		return abortBeforeDispatch(persisted, kernel.ErrConflict)
	}
	if persisted.Run.Status != kernel.RunStatusRunning {
		return abortBeforeDispatch(persisted, kernel.ErrExecutionRequired)
	}
	if err = bound.RequireExecution(ctx, runID); err != nil {
		return abortBeforeDispatch(persisted, err)
	}
	scoped := *runner
	scoped.runtime = bound
	// The stable bearer identity survives RenewExecution; one lease can span
	// many internal CAS operations without creating extra work items.
	scoped.activeRunLease = &lease
	result, driveErr := execute(ctx, &scoped)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if releaseErr := runner.runtime.ReleaseExecution(cleanup, lease); releaseErr != nil && !errors.Is(releaseErr, kernel.ErrExecutionConflict) {
		return result, errors.Join(driveErr, releaseErr)
	}
	return result, driveErr
}

// keepRunLeaseAlive renews only near the next bounded provider-call window,
// avoiding an extra SQLite/PostgreSQL write after every tiny CAS. The local
// clock is only a conservative renewal hint; Store time checks every commit
// and pre-dispatch authorization, so clock skew cannot grant authority.
func (runner *Runner) keepRunLeaseAlive(ctx context.Context) error {
	if runner.activeRunLease == nil {
		return nil
	}
	if runner.externalRunLease {
		return runner.runtime.RequireExecution(ctx, runner.activeRunLease.RunID)
	}
	horizon := max(runner.execution.Model.Timeout, runner.execution.Tool.Timeout) + time.Second
	if runner.clock.Now().UTC().Add(horizon).Before(runner.activeRunLease.ExpiresAt) {
		return nil
	}
	renewed, err := runner.runtime.RenewExecution(ctx, kernel.ExecutionRenewRequest{
		Lease: *runner.activeRunLease, LeaseDuration: runner.runExecution.LeaseDuration,
	})
	if err != nil {
		return err
	}
	*runner.activeRunLease = renewed
	return nil
}
