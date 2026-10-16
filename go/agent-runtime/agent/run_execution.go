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
		value.LeaseDuration-time.Second > max(policy.Model.Timeout, policy.Tool.Timeout)
}

// withRunOwner acquires a fresh authoritative segment before native
// Model/Tool dispatch and before Resume middleware. StartRun middleware
// still runs before Create/claim for compatibility with host composition.
// On clean return, release is best effort. A failed, cancelled or ambiguous
// segment retains its owner until Store TTL expiration.
// No hidden goroutine or independent lease service.
func (runner *Runner) withRunOwner(ctx context.Context, runID string,
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
	bound, err := runner.runtime.WithExecution(lease)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	scoped := *runner
	scoped.runtime = bound
	// The stable bearer identity survives RenewExecution; one lease can span
	// many internal CAS operations without creating extra work items.
	scoped.activeRunLease = &lease
	result, driveErr := execute(ctx, &scoped)
	if driveErr == nil {
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
