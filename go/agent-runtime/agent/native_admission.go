package agent

import (
	"context"
	"reflect"
	"sync/atomic"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// requireNativeAdmission is the final local/Store authorization boundary.
// A completed intent CAS is not permission to enter a callback. Recheck the
// current Context, deadline and Store owner, including after any blocking CAS.
func (r *Runner) requireNativeAdmission(ctx context.Context, run kernel.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if run.DeadlineAt != nil && !r.clock.Now().Before(*run.DeadlineAt) {
		return kernel.ErrDeadline
	}
	if err := r.runtime.RequireExecution(ctx, run.ID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if run.DeadlineAt != nil && !r.clock.Now().Before(*run.DeadlineAt) {
		return kernel.ErrDeadline
	}
	return nil
}

// One original RunChain invocation can return an acknowledged native denial
// before entering its terminal. The ticket and unique return identity prevent
// a Host middleware error or a nested foreign Run from qualifying this frame.
// No persistent execution authority is introduced here.
type nativeRunAdmission struct {
	invocation         plugin.RunInvocation
	denial             atomic.Pointer[nativeAdmissionDenied]
	entered            atomic.Bool
	receiptOnly        bool
	receiptLedger      string
	managedObligations *managedObligations
	managedHost        kernel.ManagedHostBinding
	budget             atomic.Pointer[runBudgetAdmission]
}

type nativeAdmissionDenied struct{ cause error }

func (e *nativeAdmissionDenied) Error() string { return e.cause.Error() }
func (e *nativeAdmissionDenied) Unwrap() error { return e.cause }
func (e *nativeAdmissionDenied) matches(err error) bool {
	return e != nil && sameOriginalToolError(e, err)
}

// Shared native refusal identity: only an actual trusted adapter receipt can
// mint it. The exact original invocation must independently consume it.
func recordNativeAdmissionDenial(ctx context.Context, adapter *BoundedBudgetAdapter, operation string, source any, cause error) *nativeAdmissionDenied {
	if cause == nil || !callbackscope.Record(ctx, adapter, operation, source) {
		return nil
	}
	return &nativeAdmissionDenied{cause: cause}
}

func recordBudgetRunDenial(ctx context.Context, adapter *BoundedBudgetAdapter, inv plugin.RunInvocation, l budget.Ledger) error {
	frame, _ := callbackscope.RunCall(ctx).(*nativeRunAdmission)
	if frame == nil || frame.entered.Load() || frame.invocation != inv || l.ID == "" || l.Revision == 0 || !l.RunCancelled(inv.RunID) {
		return nil
	}
	if _, ok := l.Runs[inv.RunID]; !ok {
		return nil
	}
	// The real native adapter owns this receipt. A typed budget error
	// returned by a Host port or an unrelated callback cannot set it.
	denial := recordNativeAdmissionDenial(ctx, adapter, "budget.run.admission_denied", struct {
		Invocation plugin.RunInvocation
		Ledger     budget.Ledger
	}{inv, l}, budget.ErrExhausted)
	if denial == nil || !frame.denial.CompareAndSwap(nil, denial) {
		return nil
	}
	return denial
}

// Both Root and native parented executions consume this same original
// admission return. It leaves the existing snapshot unchanged so an already
// elected cancellation can perform its own Kernel CAS after retirement.
func (r *Runner) invokeNativeRun(ctx context.Context, source kernel.Snapshot, inv plugin.RunInvocation, next runContinuation) (kernel.Snapshot, error) {
	if source.Run.BudgetScopeID != "" && r.boundedBudget == nil {
		return source, budget.ErrLedgerConflict
	}
	frame := &nativeRunAdmission{invocation: inv, managedObligations: r.managedObligationSource}
	if frame.managedObligations != nil {
		var ok bool
		if frame.managedHost, ok = r.runtime.ManagedDispatch(ctx, source.Run.ID); !ok {
			return source, ErrInvalidRequest
		}
	}
	grant := r.activePhysicalGrant
	if grant == nil || r.rootCallbacks == nil || grant.ChildRunID != inv.RunID || grant.ChildRunID != source.Run.ID ||
		grant.ExpectedRevision != source.Run.Revision || inv.ParentRunID != source.Run.ParentRunID ||
		!grant.IsOwned() || r.rootCallbacks.entered.Load() {
		return source, kernel.ErrExecutionConflict
	}
	// The durable Run binding is the authority, not a newly selected
	// ledger inferred from a pending request. Reject old split-store facts.
	state, e := decodeState(source.State)
	if e != nil {
		return source, e
	}
	if inv, ok := activeModelInvocation(state); ok && inv.SamplingPlan != nil && inv.SamplingPlan.LedgerID != source.Run.BudgetScopeID {
		return source, budget.ErrLedgerConflict
	}
	if a := state.ToolAdmission; a != nil && a.BudgetBound && a.LedgerID != source.Run.BudgetScopeID {
		return source, ErrToolReceiptMismatch
	}

	// Admission binds the lease and ancestry; entry consumes its epoch
	// exactly once before any provider or Host callback.
	deadlineElapsed, err := r.runtime.CheckOwnedPhysicalEntry(ctx, source)
	if err != nil {
		return source, err
	}
	if deadlineElapsed {
		if source.Run.ParentRunID != "" {
			return source, kernel.ErrDeadline
		}
		// No new Host callback may be entered. Consume at most one
		// previously committed receipt through the native-only reducer.
		if inv.Operation != plugin.RunResume || grant.AdmissionOperation != "resume" {
			return source, kernel.ErrDeadline
		}
		frame.receiptOnly = true
		receipt, receiptErr := r.expiredRootReceipt(source, frame)
		if receiptErr != nil {
			return source, receiptErr
		}
		// Same original chain, same admission frame, same terminal
		// settlement. Only its terminal is narrowed to one known receipt.
		next = receipt
	}
	if !r.rootCallbacks.entered.CompareAndSwap(false, true) {
		return source, kernel.ErrExecutionConflict
	}
	result, err := r.invokeFirstPartyRun(callbackscope.BindRunCall(ctx, frame), inv, func(c context.Context) (kernel.Snapshot, error) {
		frame.entered.Store(true)
		return next(c)
	})
	if denial := frame.denial.Load(); denial != nil {
		if frame.entered.Load() || !reflect.DeepEqual(result, kernel.Snapshot{}) || !denial.matches(err) {
			r.noteRootCallbackFailure(ErrRootOutcomeUnknown)
			return result, err
		}
		// This is the exact original return, not an error classification.
		// The whole callback lifetime and physical state must still qualify.
		r.noteNativeAdmissionReturn(source, err)
		return source, err
	}
	return result, err
}
