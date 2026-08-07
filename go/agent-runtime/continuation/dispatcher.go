package continuation

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

// Dispatcher routes a valid Job to the feature that owns the parent Run.
type Dispatcher struct {
	runs      SnapshotLoader
	agent     Resumer
	plans     Resumer
	workflows Resumer
	teams     Resumer
}

// DispatcherDependencies enumerate every explicit Runtime Kind recovery surface.
type DispatcherDependencies struct {
	Runs      SnapshotLoader
	Agent     Resumer
	Plans     Resumer
	Workflows Resumer
	Teams     Resumer
}

// NewDispatcher constructs a feature-neutral continuation router.
func NewDispatcher(dependencies DispatcherDependencies) (*Dispatcher, error) {
	if dependencies.Runs == nil || dependencies.Agent == nil || dependencies.Plans == nil ||
		dependencies.Workflows == nil || dependencies.Teams == nil {
		return nil, ErrInvalidInput
	}
	return &Dispatcher{
		runs: dependencies.Runs, agent: dependencies.Agent, plans: dependencies.Plans,
		workflows: dependencies.Workflows, teams: dependencies.Teams,
	}, nil
}

// Dispatch performs at most one revision-guarded feature resumption. Stale and
// already-terminal deliveries are successful no-ops.
func (dispatcher *Dispatcher) Dispatch(ctx context.Context, payload Payload) error {
	normalized, err := normalizePayload(payload)
	if err != nil {
		return err
	}
	snapshot, eligible, err := dispatcher.eligibleSnapshot(ctx, normalized)
	if err != nil {
		return err
	}
	if !eligible {
		return nil
	}
	resumer, err := dispatcher.resumer(snapshot.Run.Kind)
	if err != nil {
		return err
	}
	return resumeOnce(ctx, resumer, snapshot)
}

func (dispatcher *Dispatcher) eligibleSnapshot(
	ctx context.Context,
	payload Payload,
) (kernel.Snapshot, bool, error) {
	snapshot, err := dispatcher.runs.Load(ctx, payload.RunID)
	if errors.Is(err, kernel.ErrNotFound) {
		return kernel.Snapshot{}, false, nil
	}
	if err != nil {
		return kernel.Snapshot{}, false, err
	}
	eligible := snapshot.Run.Revision == payload.ExpectedRevision && !terminal(snapshot.Run.Status) &&
		snapshot.Run.Status != kernel.RunStatusWaitingInput
	return snapshot, eligible, nil
}

func resumeOnce(ctx context.Context, resumer Resumer, snapshot kernel.Snapshot) error {
	resumed, err := resumer.Resume(ctx, snapshot.Run.ID, snapshot.Run.Revision)
	if resumed.Run.ID != "" || nonRetryableResumeError(err) {
		return nil
	}
	return err
}

func nonRetryableResumeError(err error) bool {
	return err == nil || errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrTerminal) ||
		errors.Is(err, kernel.ErrNotFound)
}

func (dispatcher *Dispatcher) resumer(kind kernel.RunKind) (Resumer, error) {
	switch kind {
	case kernel.RunKindAgent:
		return dispatcher.agent, nil
	case kernel.RunKindPlanExecute:
		return dispatcher.plans, nil
	case kernel.RunKindWorkflow:
		return dispatcher.workflows, nil
	case kernel.RunKindTeam:
		return dispatcher.teams, nil
	default:
		return nil, ErrUnsupportedRunKind
	}
}
