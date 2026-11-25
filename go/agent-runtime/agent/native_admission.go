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
	invocation plugin.RunInvocation
	denial     atomic.Pointer[nativeAdmissionDenied]
	entered    atomic.Bool
}

type nativeAdmissionDenied struct{ cause error }

func (e *nativeAdmissionDenied) Error() string { return e.cause.Error() }
func (e *nativeAdmissionDenied) Unwrap() error { return e.cause }

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
	if !callbackscope.Record(ctx, adapter, "budget.run.admission_denied", struct {
		Invocation plugin.RunInvocation
		Ledger     budget.Ledger
	}{inv, l}) {
		return nil
	}
	denial := &nativeAdmissionDenied{cause: budget.ErrExhausted}
	if !frame.denial.CompareAndSwap(nil, denial) {
		return nil
	}
	return denial
}

// Both Root and native parented executions consume this same original
// admission return. It leaves the existing snapshot unchanged so an already
// elected cancellation can perform its own Kernel CAS after retirement.
func (r *Runner) invokeNativeRun(ctx context.Context, source kernel.Snapshot, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	frame := &nativeRunAdmission{invocation: inv}
	result, err := r.runChain.Invoke(callbackscope.BindRunCall(ctx, frame), inv, func(c context.Context) (kernel.Snapshot, error) {
		frame.entered.Store(true)
		return next(c)
	})
	if denial := frame.denial.Load(); denial != nil {
		if frame.entered.Load() || !reflect.DeepEqual(result, kernel.Snapshot{}) || !sameOriginalToolError(denial, err) {
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
