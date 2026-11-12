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
)

// PhysicalCallOutcome is an observation of one actual first-party call, not a
// caller-supplied completion assertion or authority to execute another call.
type PhysicalCallOutcome struct {
	runID     string
	operation plugin.RunOperation
	kind      PhysicalCallOutcomeKind
}

func (o PhysicalCallOutcome) Matches(id string, operation plugin.RunOperation) bool {
	return o.kind != PhysicalCallUnknown && o.runID == id && o.operation == operation
}
func (o PhysicalCallOutcome) Kind() PhysicalCallOutcomeKind { return o.kind }

type physicalObservationKey struct{}
type claimedPhysicalObservationKey struct{}
type physicalObservation struct {
	mu                       sync.Mutex
	runID                    string
	operation                plugin.RunOperation
	claimed, closed, invalid bool
	runner                   *Runner
	kind                     PhysicalCallOutcomeKind
}

func (runner *Runner) ObservePhysicalCall(ctx context.Context, runID string, operation plugin.RunOperation, invoke func(context.Context) (kernel.Snapshot, error)) (kernel.Snapshot, PhysicalCallOutcome, error) {
	if runner == nil || ctx == nil || invoke == nil || runID == "" {
		return kernel.Snapshot{}, PhysicalCallOutcome{}, ErrInvalidRequest
	}
	o := &physicalObservation{runID: runID, operation: operation, runner: runner}
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
	if o.closed || o.claimed || o.runID != runID || o.operation != operation || o.runner != runner {
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
		o.kind = kind
	}
}
