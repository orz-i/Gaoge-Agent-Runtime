package agent

import (
	"context"
	"errors"
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

// The native ticket is still a reservation, not dispatch permission. An
// elected Ledger cancellation (or changed original reservation) may have
// intervened while the physical intent was written. Before chain entry,
// retain the no-entry proof until both admission authorities pass.
func (t *toolBudgetAdmission) requireEntry(ctx context.Context) error {
	if t == nil || t.ledger.ID == "" {
		return nil
	}
	l, err := t.adapter.coordinator.Store.LoadBudget(ctx, t.ledger.ID)
	if err != nil {
		return err
	}
	if l.ID != t.ledger.ID || l.Limits != t.ledger.Limits {
		return ErrToolReceiptMismatch
	}
	expected := originalToolReservation(t.request)
	res, ok := l.Reservations[expected.ID]
	if !ok || !exactUnsentToolReservation(res, expected) {
		return ErrToolReceiptMismatch
	}
	if l.RunCancelled(t.request.RunID) {
		return budget.ErrExhausted
	}
	if res.Status != budget.ReservationHeld {
		return ErrToolReceiptMismatch
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
func (r *Runner) invokeNativeRun(ctx context.Context, source kernel.Snapshot, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	frame := &nativeRunAdmission{invocation: inv, managedObligations: r.managedObligationSource}
	if source.Run.ParentRunID == "" {
		// The actual first Host callback is AFTER all setup/Store reads,
		// including Managed Leave. Never use an ACK or earlier lease check
		// as current authority, and never enter this scope twice.
		if r.activeRootGrant == nil || r.rootCallbacks == nil || r.activeRootGrant.ChildRunID != inv.RunID ||
			r.rootCallbacks.entered.Load() {
			return source, kernel.ErrExecutionConflict
		}
		deadlineElapsed, err := r.runtime.CheckOwnedPhysicalEntry(ctx, source)
		if err != nil {
			return source, err
		}
		if deadlineElapsed {
			// No new Host callback may be entered. Consume at most one
			// previously committed receipt through the native-only reducer.
			if inv.Operation != plugin.RunResume || r.activeRootGrant.AdmissionOperation != "resume" {
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
	} else if r.rootCallbacks != nil {
		r.rootCallbacks.entered.Store(true)
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

// The actual DispatchTool return, not the earlier admission read, decides
// whether native ledger cancellation refused this exact original Tool.
// A speculative dispatched candidate, missing/foreign reservation, or
// unknown storage failure cannot qualify this return.
func (t *toolBudgetAdmission) cancelledDispatch(l budget.Ledger, err error) bool {
	if t == nil || !errors.Is(err, budget.ErrExhausted) || l.ID == "" || l.ID != t.ledger.ID || l.Limits != t.ledger.Limits || l.Revision < t.ledger.Revision {
		return false
	}
	old, ok := t.ledger.Runs[t.request.RunID]
	actual, registered := l.Runs[t.request.RunID]
	if !ok || !registered || old != actual || !l.RunCancelled(t.request.RunID) {
		return false
	}
	expected := originalToolReservation(t.request)
	res, found := l.Reservations[expected.ID]
	return found && exactUnsentToolReservation(res, expected) &&
		(res.Status == budget.ReservationHeld || res.Status == budget.ReservationWaiting || res.Status == budget.ReservationReleased)
}
