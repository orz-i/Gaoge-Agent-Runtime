package kernel

import (
	"bytes"
	"encoding/json"
)

// These records are immutable Store facts, not execution capabilities.
type OwnedSettlementEvidence struct {
	Version    uint64
	Grant      ChildExecutionGrant
	Snapshot   Snapshot
	Completion PhysicalCallbackCompletion
}
type ManagedClosureRef struct {
	SourceID string
	Digest   string
}

type OwnedRetirementReceipt struct {
	Version  uint64
	Evidence OwnedSettlementEvidence
	// Retain the wire key for empty historical ordinary receipts. Managed
	// retirement binds one digest, not a second full transfer/snapshot.
	ClosureRefs []ManagedClosureRef `json:"Closures"`
	// The immutable Finish fact has one integrity digest, no ACK state.
	Digest string
}

// No intermediate retirement or old ACK tuple is a Finish fact.
func (r *OwnedRetirementReceipt) UnmarshalJSON(raw []byte) error {
	type fact OwnedRetirementReceipt
	var value fact
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return err
	}
	*r = OwnedRetirementReceipt(value)
	return nil
}

func (e OwnedSettlementEvidence) Valid() bool {
	g, s, c := e.Grant, e.Snapshot, e.Completion
	if e.Version != 1 || !g.IsOwned() || g.ID == "" || !g.MatchesExecution(ExecutionLease{RunID: g.ChildRunID, OwnerID: g.ExecutionOwnerID, LeaseID: g.ExecutionLeaseID, Epoch: g.ExecutionEpoch}) || s.Run.ID != g.ChildRunID || s.Run.Kind != "agent" || s.Run.Revision < g.ExpectedRevision {
		return false
	}
	if c.GrantID == "" {
		return false
	}
	if c.NotEntered() {
		return c.MatchesNotEnteredSnapshot(g, s)
	}
	return ValidatePhysicalCompletion(g, c) == nil && c.HostCompleted && c.ReturnRevision <= s.Run.Revision &&
		(s.Run.ParentRunID == "" || (c.MatchesReturnSnapshot(s) && c.ControlCustody == "" && c.ToolReturns == ""))
}

// The original selected Transfer is stored once. Its terminal view is
// reconstructed from the single committed retirement Snapshot; the digest
// binds every source/selection/lease field to that exact view.
func closeManagedRetirement(g ChildExecutionGrant, s Snapshot, a ManagedAuthority, t ManagedTransfer) (ManagedTransfer, error) {
	if s.Run.ParentRunID != "" || g.AdmissionOperation != "managed_resume" || !t.Valid() || !managedResumeSelectionMatches(t) ||
		t.SafePoint.Host.KernelAuthority != a || t.SafePoint.Host.RunID != g.ChildRunID ||
		t.SuccessorGrant != g || t.Revision == 0 || t.Terminal != nil || s.Run.ID != g.ChildRunID {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	t.Revision = s.Run.Revision
	terminal := managedClone(s)
	t.Terminal = &terminal
	return t, nil
}

func ObserveManagedRetirement(t ManagedTransfer, r OwnedRetirementReceipt) (ManagedTransfer, error) {
	closed, err := closeManagedRetirement(t.SuccessorGrant, r.Evidence.Snapshot, t.SafePoint.Host.KernelAuthority, t)
	if err != nil || !r.MatchesManagedClosure(closed, r.Evidence.Snapshot) {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	return closed, nil
}

func (r OwnedRetirementReceipt) MatchesManagedClosure(t ManagedTransfer, s Snapshot) bool {
	return r.Valid() && t.Resolved() && t.Selection.Mode == ManagedResume &&
		t.SuccessorGrant == r.Evidence.Grant && t.Revision == s.Run.Revision &&
		len(r.ClosureRefs) == 1 && r.ClosureRefs[0] == (ManagedClosureRef{SourceID: t.SafePoint.ID, Digest: ManagedDigest(t)}) &&
		ManagedDigest(*t.Terminal) == ManagedDigest(s) && ManagedDigest(r.Evidence.Snapshot) == ManagedDigest(s)
}

func (r OwnedRetirementReceipt) Identity() string {
	return ManagedDigest(struct {
		Version  uint64
		Evidence OwnedSettlementEvidence
		Closures []ManagedClosureRef
	}{r.Version, r.Evidence, r.ClosureRefs})
}
func (r OwnedRetirementReceipt) Valid() bool {
	if r.Version != 1 || !r.Evidence.Valid() || r.ClosureRefs == nil {
		return false
	}
	if r.Evidence.Grant.AdmissionOperation == "managed_resume" {
		if len(r.ClosureRefs) != 1 || r.ClosureRefs[0].SourceID == "" || !validToolReceiptDigest(r.ClosureRefs[0].Digest) ||
			r.Evidence.Completion.NotEntered() || !r.Evidence.Completion.HostCompleted || !r.Evidence.Completion.LeaseOwned {
			return false
		}
	} else if len(r.ClosureRefs) != 0 {
		return false
	}
	return r.Digest != "" && r.Digest == r.Identity()
}

// SelectOwnedRetirementForRun observes only the exact completed physical
// source at the current epoch. It performs no retirement or lease write.
func SelectOwnedRetirementForRun(runID string, receipts []OwnedRetirementReceipt, current Snapshot, epoch uint64, lease ExecutionLease, hasGrants bool) (OwnedRetirementReceipt, error) {
	var selected OwnedRetirementReceipt
	for _, r := range receipts {
		if !r.Valid() || r.Evidence.Grant.ChildRunID != runID {
			return OwnedRetirementReceipt{}, ErrExecutionConflict
		}
		g := r.Evidence.Grant
		if current.Run.ID != runID || epoch != g.ExecutionEpoch || ManagedDigest(current) != ManagedDigest(r.Evidence.Snapshot) {
			continue
		}
		if r.Evidence.Completion.LeaseOwned {
			if lease.OwnerID != "" || lease.LeaseID != "" || !lease.ExpiresAt.IsZero() {
				continue
			}
		} else if !g.MatchesExecution(lease) {
			continue
		}
		if selected.Version != 0 {
			return OwnedRetirementReceipt{}, ErrExecutionConflict
		}
		selected = r
	}
	if selected.Version == 0 {
		return OwnedRetirementReceipt{}, ErrNotFound
	}
	if hasGrants {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	return selected, nil
}
