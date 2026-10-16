package continuation

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Dispatcher routes a valid Job to the feature that owns the parent Run.
type Dispatcher struct {
	runs     SnapshotLoader
	resumers map[kernel.RunKind]Resumer
}

// NewDispatcher constructs a feature-neutral continuation router.
func NewDispatcher(runs SnapshotLoader, registrations ...ResumerRegistration) (*Dispatcher, error) {
	if runs == nil || len(registrations) == 0 {
		return nil, ErrInvalidInput
	}
	resumers := make(map[kernel.RunKind]Resumer, len(registrations))
	for _, registration := range registrations {
		if !validRegistrationKind(registration.kind) || registration.resumer == nil {
			return nil, ErrInvalidInput
		}
		if _, duplicate := resumers[registration.kind]; duplicate {
			return nil, ErrInvalidInput
		}
		resumers[registration.kind] = registration.resumer
	}
	return &Dispatcher{runs: runs, resumers: resumers}, nil
}

// Dispatch performs at most one successful revision-guarded feature resumption.
// Stale and already-terminal deliveries are successful no-ops.
func (dispatcher *Dispatcher) Dispatch(ctx context.Context, payload Payload) error {
	normalized, err := normalizePayload(payload)
	if err != nil {
		return err
	}
	if normalized.WaitGeneration != 0 {
		return dispatcher.dispatchToolWait(ctx, normalized)
	}
	if normalized.Trigger == TriggerChildTerminal {
		return dispatcher.dispatchChildTerminal(ctx, normalized)
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
	return dispatcher.resumeOnce(ctx, resumer, snapshot)
}

const maxToolWaitCASRetries = 8

// A child-terminal fact is still actionable after an unrelated parent Run
// revision changes. A CAS conflict does not prove that the child was consumed.
// Retry from the authoritative parent Snapshot; if contention persists, let
// the durable Worker NACK instead of losing the recovery obligation.
func (dispatcher *Dispatcher) dispatchChildTerminal(ctx context.Context, payload Payload) error {
	for attempt := 0; attempt < maxToolWaitCASRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, eligible, err := dispatcher.eligibleSnapshot(ctx, payload)
		if err != nil || !eligible {
			return err
		}
		resumer, err := dispatcher.resumer(snapshot.Run.Kind)
		if err != nil {
			return err
		}
		resumed, resumeErr := resumer.Resume(ctx, snapshot.Run.ID, snapshot.Run.Revision)
		if errors.Is(resumeErr, kernel.ErrConflict) {
			continue
		}
		return dispatcher.resumeResult(ctx, snapshot.Run.ID, resumed, resumeErr)
	}
	return kernel.ErrConflict
}

// A waiting generation outlives ordinary Run revisions. A concurrent mutation
// can land between the Dispatch load and ResumeToolWait's CAS; that conflict is
// not a consumed wait. Re-read the current Run before each bounded attempt and
// let the durable queue NACK/redeliver if contention persists.
func (dispatcher *Dispatcher) dispatchToolWait(ctx context.Context, payload Payload) error {
	for attempt := 0; attempt < maxToolWaitCASRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, eligible, err := dispatcher.eligibleSnapshot(ctx, payload)
		if err != nil || !eligible {
			return err
		}
		resumer, err := dispatcher.resumer(snapshot.Run.Kind)
		if err != nil {
			return err
		}
		waitResumer, ok := resumer.(ToolWaitResumer)
		if !ok {
			return ErrUnsupportedRunKind
		}
		resumed, resumeErr := waitResumer.ResumeToolWait(ctx, snapshot.Run.ID, snapshot.Run.Revision, payload.WaitGeneration)
		if errors.Is(resumeErr, kernel.ErrConflict) {
			continue
		}
		// An unresolved native Tool MUST NACK, irrespective of a non-empty
		// Snapshot; a terminal-looking result with an error needs Store proof.
		return dispatcher.resumeResult(ctx, snapshot.Run.ID, resumed, resumeErr)
	}
	return kernel.ErrConflict
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
	// A renewable allowance must be granted explicitly by the owning host.
	// A stale wakeup cannot dispatch new work while the Run is paused.
	stableFact := payload.Trigger == TriggerChildTerminal || payload.WaitGeneration != 0
	eligible := (snapshot.Run.Revision == payload.ExpectedRevision || stableFact) && snapshot.Run.Status == kernel.RunStatusRunning
	return snapshot, eligible, nil
}

func (dispatcher *Dispatcher) resumeOnce(ctx context.Context, resumer Resumer, snapshot kernel.Snapshot) error {
	resumed, err := resumer.Resume(ctx, snapshot.Run.ID, snapshot.Run.Revision)
	if errors.Is(err, kernel.ErrConflict) {
		// For a revision-bound wakeup, conflict is obsolete only once the
		// authoritative revision has moved (or the Run is already settled).
		current, loadErr := dispatcher.runs.Load(ctx, snapshot.Run.ID)
		if errors.Is(loadErr, kernel.ErrNotFound) {
			return nil
		}
		if loadErr != nil {
			return errors.Join(err, loadErr)
		}
		if current.Run.Revision != snapshot.Run.Revision || terminalSnapshotAfterResume(current) {
			return nil
		}
		return err
	}
	return dispatcher.resumeResult(ctx, snapshot.Run.ID, resumed, err)
}

// An error accompanied by a terminal-looking Snapshot is not proof of a
// committed transition. Only the authoritative Store can settle that error.
// Likewise a feature's ErrTerminal/ErrNotFound may describe an unrelated
// dependency rather than the parent Run that owns this delivery.
func (dispatcher *Dispatcher) resumeResult(ctx context.Context, runID string, resumed kernel.Snapshot, resumeErr error) error {
	if resumeErr == nil {
		return nil
	}
	if !terminalSnapshotAfterResume(resumed) && !errors.Is(resumeErr, kernel.ErrTerminal) &&
		!errors.Is(resumeErr, kernel.ErrNotFound) {
		return resumeErr
	}
	current, err := dispatcher.runs.Load(ctx, runID)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return errors.Join(resumeErr, err)
	}
	if terminalSnapshotAfterResume(current) {
		return nil
	}
	return resumeErr
}

// Terminal state is an ACK candidate only when loaded from the authoritative
// Run Store; a returned Feature Snapshot alone cannot settle an error.
func terminalSnapshotAfterResume(snapshot kernel.Snapshot) bool {
	switch snapshot.Run.Status {
	case kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled:
		return snapshot.Run.ID != ""
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
		return false
	default:
		return false
	}
}

func (dispatcher *Dispatcher) resumer(kind kernel.RunKind) (Resumer, error) {
	resumer, ok := dispatcher.resumers[kind]
	if !ok {
		return nil, ErrUnsupportedRunKind
	}
	return resumer, nil
}
