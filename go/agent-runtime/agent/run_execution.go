package agent

import (
	"context"
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
