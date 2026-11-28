package agent

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// externalToolElection retains control custody in the ORIGINAL Run, across
// receipt consumption and Grant retirement. It is not another execution ledger.
// Only receipt-only recovery or the original Harness cancellation may advance it.
type externalToolElection struct {
	ID, TargetRunID string
	Control         ExternalToolControl
	GrantID         string
	SourceRevision  uint64
	SourceHash      string
	Call            tools.Call
	Committed       *rootToolReceipt `json:",omitempty"`
}

// ElectExternalToolCancellation retains an already elected cancellation on an
// exact returned external wait. It never calls the owner or changes accounting.
// Hosts must authorize the original scope before calling this trusted port.
// A retry must use the same election and target, including after a lost reply.
func (r *Runner) ElectExternalToolCancellation(ctx context.Context, runID string, revision uint64, electionID, targetRunID string) (kernel.Snapshot, error) {
	if !controlIdentity(electionID) || !controlIdentity(targetRunID) {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	s, state, err := r.loadState(ctx, runID)
	if err != nil {
		return s, err
	}
	if previous := state.ExternalToolElection; previous != nil {
		if previous.ID != electionID || previous.TargetRunID != targetRunID {
			return s, kernel.ErrConflict
		}
		if revision != s.Run.Revision && revision != previous.SourceRevision {
			return s, kernel.ErrConflict
		}
		return s, r.verifyExternalElection(ctx, s, previous)
	}
	wait := state.ToolWait
	if wait == nil {
		return s, ErrToolOutcomeUnknown
	}
	// The SDK election is not the Host owner's ControlID. The latter is bound
	// exactly once, after Harness verifies its original frozen delegation chain.
	c := ExternalToolControl{Actor: s.Run.Actor, RunID: runID, CallID: wait.CallID, OperationID: wait.OperationID, InputDigest: wait.InputDigest, ControlID: electionID}
	original, err := r.externalControlWait(ctx, revision, c)
	if err != nil {
		return s, err
	}
	state.ExternalToolElection = &externalToolElection{ID: electionID, TargetRunID: targetRunID,
		GrantID: original.grant.ID, SourceRevision: original.snapshot.Run.Revision, SourceHash: original.returnHash, Call: tools.CloneCall(original.request.Call)}
	return r.applyExternalControlState(ctx, original.snapshot, original.grant, state, nil)
}

// BindExternalToolCancellationControl binds the original owner control once.
// It is idempotent for the same control and cannot substitute a foreign one.
// Source revisions remain valid only for this exact retained original wait.
func (r *Runner) BindExternalToolCancellationControl(ctx context.Context, revision uint64, electionID, targetRunID string, control ExternalToolControl) error {
	s, state, err := r.loadState(ctx, control.RunID)
	if err != nil {
		return err
	}
	p := state.ExternalToolElection
	if p == nil || p.ID != electionID || p.TargetRunID != targetRunID {
		return kernel.ErrConflict
	}
	if p.Control.ControlID != "" {
		if p.Control != control {
			return ErrToolReceiptMismatch
		}
		if revision != s.Run.Revision && revision != p.SourceRevision {
			return kernel.ErrConflict
		}
		return r.verifyExternalElection(ctx, s, p)
	}
	if _, err = r.externalControlWait(ctx, revision, control); err != nil {
		return err
	}
	copy := *p
	copy.Control = control
	state.ExternalToolElection = &copy
	g, err := r.runtime.LookupChildExecution(ctx, s.Run.ID)
	if err != nil || g.ID != p.GrantID {
		return errors.Join(ErrRootOutcomeUnknown, err)
	}
	_, err = r.applyExternalControlState(ctx, s, g, state, nil)
	return err
}

// VerifyExternalToolCancellationElection checks only the retained SDK election,
// including its original native callback/retirement. Absence is not a proof.
func (r *Runner) VerifyExternalToolCancellationElection(ctx context.Context, runID, electionID, targetRunID string) (bool, error) {
	s, state, err := r.loadState(ctx, runID)
	if err != nil || state.ExternalToolElection == nil {
		return false, err
	}
	p := state.ExternalToolElection
	if p.ID != electionID || p.TargetRunID != targetRunID {
		return true, kernel.ErrConflict
	}
	return true, r.verifyExternalElection(ctx, s, p)
}

// AuthorizeExternalToolCancellation requires the same bound owner control for
// the final native Cancel. Empty controlID permits only the pre-owner phase;
// it cannot finish another caller's bound cancellation after receipt recovery.
func (r *Runner) AuthorizeExternalToolCancellation(ctx context.Context, runID, electionID, targetRunID, controlID string) error {
	s, state, err := r.loadState(ctx, runID)
	if err != nil || state.ExternalToolElection == nil {
		return err
	}
	p := state.ExternalToolElection
	if p.ID != electionID || p.TargetRunID != targetRunID || p.Control.ControlID != controlID {
		return kernel.ErrConflict
	}
	return r.verifyExternalElection(ctx, s, p)
}

func (r *Runner) verifyExternalElection(ctx context.Context, s kernel.Snapshot, p *externalToolElection) error {
	if !controlIdentity(p.ID) || !controlIdentity(p.TargetRunID) || p.Call.ID == "" || p.SourceRevision == 0 || p.SourceRevision >= s.Run.Revision || len(p.SourceHash) != 64 {
		return ErrToolReceiptMismatch
	}
	g, err := r.runtime.LookupChildExecution(ctx, s.Run.ID)
	var c kernel.OwnedPhysicalCallbackCompletion
	switch {
	case err == nil && g.ID == p.GrantID:
		c, err = r.runtime.LookupOwnedPhysicalCallbackReturn(ctx, g)
	case errors.Is(err, kernel.ErrNotFound):
		var receipt kernel.OwnedRetirementReceipt
		receipt, err = r.runtime.LoadOwnedRetirement(ctx, p.GrantID)
		if err == nil && kernel.ManagedDigest(receipt.Evidence.Snapshot) != kernel.ManagedDigest(s) {
			// Native Cancel preserves the exact settled Feature state.
			if s.Run.Status != kernel.RunStatusCancelled || kernel.ManagedDigest(receipt.Evidence.Snapshot.State) != kernel.ManagedDigest(s.State) {
				return ErrToolReceiptMismatch
			}
		}
		c = receipt.Evidence.Completion
	default:
		return errors.Join(ErrRootOutcomeUnknown, err)
	}
	if err != nil || !c.HostCompleted || c.GrantID != p.GrantID || c.ReturnRevision != p.SourceRevision || c.ReturnSourceHash != p.SourceHash {
		return errors.Join(ErrRootOutcomeUnknown, err)
	}
	return nil
}
