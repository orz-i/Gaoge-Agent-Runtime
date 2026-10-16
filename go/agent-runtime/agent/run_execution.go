package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RunExecution is an opt-in, Stage 2B direct-Agent executor identity.
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

// withRunOwner acquires a fresh authoritative segment before native
// Model/Tool dispatch and before Resume middleware. StartRun middleware
// still runs before Create/claim for compatibility with host composition.
// On clean return, release is best effort. A failed, cancelled or ambiguous
// segment retains its owner until Store TTL expiration.
// No hidden goroutine or independent lease service.
func (runner *Runner) withRunOwner(ctx context.Context, runID string, expectedRevision uint64,
	execute func(context.Context, *Runner) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	if runner.runExecution == nil {
		if err := runner.runtime.RequireExecution(ctx, runID); err != nil {
			return kernel.Snapshot{}, err
		}
		return execute(ctx, runner)
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
	// A late successful provider call is already recorded atomically with
	// its receipt. Release so an explicit Resume can consume that known fact.
	if driveErr == nil || errors.Is(driveErr, ErrToolLateResult) ||
		errors.Is(driveErr, ErrModelLateResult) {
		// The store retains the sticky Epoch fence after release. If Release fails
		// (e.g. lease already replaced), a later recovery can still use its expiry.
		releaseErr := runner.runtime.ReleaseExecution(ctx, lease)
		if releaseErr != nil && !errors.Is(releaseErr, kernel.ErrExecutionConflict) {
			return result, releaseErr
		}
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
