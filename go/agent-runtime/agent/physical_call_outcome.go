package agent

import (
	"context"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type PhysicalCallOutcomeKind uint8

const (
	PhysicalCallUnknown PhysicalCallOutcomeKind = iota
	PhysicalCallSettled
	PhysicalCallDurableYield
	PhysicalCallCreated
	physicalCallStaleResume
	// PhysicalCallNotEntered is the original call's exact acknowledged
	// retirement with no callback entered. It is neither a Host return nor
	// proof that the Run was never created.
	PhysicalCallNotEntered
)

// PhysicalCallOutcome is an observation of one actual first-party call, not a
// caller-supplied completion assertion or authority to execute another call.
type PhysicalCallOutcome struct {
	runID     string
	operation plugin.RunOperation
	kind      PhysicalCallOutcomeKind
}

func (o PhysicalCallOutcome) Matches(id string, operation plugin.RunOperation) bool {
	return o.kind != PhysicalCallUnknown && o.kind != physicalCallStaleResume && o.runID == id && o.operation == operation
}

// RejectedStaleResume identifies the original native Resume revision check,
// before any middleware, verifier, execution lease or physical callback.
func (o PhysicalCallOutcome) RejectedStaleResume(runID string) bool {
	return o.kind == physicalCallStaleResume && o.operation == plugin.RunResume && o.runID == runID
}

func (o PhysicalCallOutcome) Kind() PhysicalCallOutcomeKind { return o.kind }

type physicalObservationKey struct{}
type claimedPhysicalObservationKey struct{}
type physicalObservation struct {
	mu                       sync.Mutex
	runID                    string
	dispatchID               string
	operation                plugin.RunOperation
	claimed, closed, invalid bool
	runner                   *Runner
	kind                     PhysicalCallOutcomeKind
}

func (runner *Runner) ObservePhysicalCall(ctx context.Context, runID string, operation plugin.RunOperation, invoke func(context.Context) (kernel.Snapshot, error)) (kernel.Snapshot, PhysicalCallOutcome, error) {
	return runner.observePhysicalCall(ctx, runID, operation, "", invoke)
}

// ObservePhysicalDispatch binds the original Host's durable dispatch identity
// to the same one-shot observation. Correlation does not grant execution.
func (runner *Runner) ObservePhysicalDispatch(ctx context.Context, runID string, operation plugin.RunOperation, dispatchID string, invoke func(context.Context) (kernel.Snapshot, error)) (kernel.Snapshot, PhysicalCallOutcome, error) {
	if dispatchID == "" {
		return kernel.Snapshot{}, PhysicalCallOutcome{}, ErrInvalidRequest
	}
	return runner.observePhysicalCall(ctx, runID, operation, dispatchID, invoke)
}

func (runner *Runner) observePhysicalCall(ctx context.Context, runID string, operation plugin.RunOperation, dispatchID string, invoke func(context.Context) (kernel.Snapshot, error)) (kernel.Snapshot, PhysicalCallOutcome, error) {
	if runner == nil || ctx == nil || invoke == nil || runID == "" {
		return kernel.Snapshot{}, PhysicalCallOutcome{}, ErrInvalidRequest
	}
	o := &physicalObservation{runID: runID, operation: operation, runner: runner, dispatchID: dispatchID}
	defer func() { o.mu.Lock(); o.closed = true; o.mu.Unlock() }()
	snapshot, err := invoke(context.WithValue(ctx, physicalObservationKey{}, o))
	o.mu.Lock()
	o.closed = true
	outcome := PhysicalCallOutcome{runID: o.runID, operation: o.operation, kind: o.kind}
	o.mu.Unlock()
	return snapshot, outcome, err
}
func (runner *Runner) claimPhysicalObservation(ctx context.Context, runID string, operation plugin.RunOperation) context.Context {
	o, _ := ctx.Value(physicalObservationKey{}).(*physicalObservation)
	if o == nil {
		return ctx
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.runID != runID {
		// Nested foreign Run contexts cannot claim or invalidate the parent's
		// call observation. They receive neither the observer nor its claim.
		isolated := context.WithValue(ctx, physicalObservationKey{}, (*physicalObservation)(nil))
		return context.WithValue(isolated, claimedPhysicalObservationKey{}, (*physicalObservation)(nil))
	}
	if o.closed || o.claimed || o.operation != operation || o.runner != runner {
		o.invalid = true
		o.kind = PhysicalCallUnknown
		return context.WithValue(ctx, claimedPhysicalObservationKey{}, (*physicalObservation)(nil))
	}
	o.claimed = true
	return context.WithValue(ctx, claimedPhysicalObservationKey{}, o)
}
func recordPhysicalObservation(ctx context.Context, runID string, kind PhysicalCallOutcomeKind) {
	o, _ := ctx.Value(claimedPhysicalObservationKey{}).(*physicalObservation)
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.closed && !o.invalid && o.claimed && o.runID == runID {
		if kind == PhysicalCallCreated && o.kind == PhysicalCallNotEntered {
			return
		}
		o.kind = kind
	}
}

// Only the current claimed invocation may bind its own native admission.
func (runner *Runner) originalPhysicalDispatch(ctx context.Context, runID, operation string) string {
	o, _ := ctx.Value(claimedPhysicalObservationKey{}).(*physicalObservation)
	if o == nil {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.invalid || !o.claimed || o.runner != runner || o.runID != runID {
		return ""
	}
	switch operation {
	case "start":
		if o.operation != plugin.RunStart {
			return ""
		}
	case "resume":
		if o.operation != plugin.RunResume {
			return ""
		}
	case "approval":
		if o.operation != plugin.RunResolveApproval {
			return ""
		}
	default:
		return ""
	}
	return o.dispatchID
}
