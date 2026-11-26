package kernel

import "context"

// These records are immutable Store facts, not execution capabilities.
type OwnedSettlementEvidence struct {
	Version    uint64
	Grant      ChildExecutionGrant
	Snapshot   Snapshot
	Completion OwnedPhysicalCallbackCompletion
	MarkOrigin string
}
type OwnedRetirementReceipt struct {
	Version  uint64
	Evidence OwnedSettlementEvidence
	Closures []ManagedTransfer
	// Preserve the existing final on-disk representation. Both values are
	// written once at Finish; no intermediate retirement is executable.
	Phase       string
	ACKIdentity string
}
type OwnedRetirementStore interface {
	LoadOwnedRetirement(context.Context, string) (OwnedRetirementReceipt, error)
	LookupOwnedRetirementForRun(context.Context, string) (OwnedRetirementReceipt, error)
}

func (e OwnedSettlementEvidence) Valid() bool {
	g, s, c := e.Grant, e.Snapshot, e.Completion
	if e.Version != 1 || !g.IsOwned() || g.ID == "" || !g.MatchesExecution(ExecutionLease{RunID: g.ChildRunID, OwnerID: g.ExecutionOwnerID, LeaseID: g.ExecutionLeaseID, Epoch: g.ExecutionEpoch}) || s.Run.ID != g.ChildRunID || s.Run.Kind != "agent" || s.Run.ParentRunID != "" || s.Run.Revision < g.ExpectedRevision {
		return false
	}
	if c.GrantID == "" {
		return false
	}
	if c.NotEntered() {
		return e.MarkOrigin == "NOT_ENTERED" && c.MatchesNotEnteredSnapshot(g, s)
	}
	return ValidateOwnedPhysicalCompletion(g, c) == nil && c.HostCompleted && c.ReturnRevision <= s.Run.Revision &&
		(e.MarkOrigin == "CALLBACK_RETURN" || e.MarkOrigin == "OWNER_RETURN" || e.MarkOrigin == "RECOVERED_RETURN" || e.MarkOrigin == "LEGACY_UNKNOWN")
}

// CloseManagedRetirement derives the managed closure only from the exact
// entered successor and its settled Run. Caller validates the retirement
// evidence and commits the closure, retirement receipt and Grant deletion
// together. A managed Grant without its selected current point cannot finish.
func CloseManagedRetirement(g ChildExecutionGrant, s Snapshot, a ManagedAuthority, t ManagedTransfer) ([]ManagedTransfer, error) {
	if g.AdmissionOperation != "managed_resume" {
		return []ManagedTransfer{}, nil
	}
	if !t.SafePoint.Valid() || !managedResumeSelectionMatches(t) ||
		t.SafePoint.Host.KernelAuthority != a || t.SafePoint.Host.RunID != g.ChildRunID ||
		t.SuccessorGrant != g || t.LegacyState != "" ||
		t.Revision == 0 || t.Terminal != nil || s.Run.ID != g.ChildRunID {
		return nil, ErrExecutionConflict
	}
	t.Revision = s.Run.Revision
	terminal := managedClone(s)
	t.Terminal = &terminal
	return []ManagedTransfer{t}, nil
}

// MatchesManagedClosure verifies the same committed retirement fact; no
// separate pre-retirement closure can certify a returned successor.
func ObserveManagedRetirement(t ManagedTransfer, r OwnedRetirementReceipt) (ManagedTransfer, error) {
	closures, err := CloseManagedRetirement(t.SuccessorGrant, r.Evidence.Snapshot, t.SafePoint.Host.KernelAuthority, t)
	if err != nil || len(closures) != 1 || !r.MatchesManagedClosure(closures[0], r.Evidence.Snapshot) {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	return closures[0], nil
}

func (r OwnedRetirementReceipt) MatchesManagedClosure(t ManagedTransfer, s Snapshot) bool {
	return r.Valid() && r.Evidence.Grant == t.SuccessorGrant &&
		len(r.Closures) == 1 && ManagedDigest(r.Closures[0]) == ManagedDigest(t) &&
		ManagedDigest(r.Evidence.Snapshot) == ManagedDigest(s)
}

func (r OwnedRetirementReceipt) Identity() string {
	return ManagedDigest(struct {
		Version  uint64
		Evidence OwnedSettlementEvidence
		Closures []ManagedTransfer
	}{r.Version, r.Evidence, r.Closures})
}
func (r OwnedRetirementReceipt) Valid() bool {
	if r.Version != 1 || !r.Evidence.Valid() || r.Evidence.Completion.GrantID == "" || r.Closures == nil || (r.Evidence.Completion.NotEntered() && len(r.Closures) != 0) {
		return false
	}
	if r.Evidence.Grant.AdmissionOperation == "managed_resume" {
		if len(r.Closures) != 1 || !r.Evidence.Completion.HostCompleted || !r.Evidence.Completion.LeaseOwned {
			return false
		}
	} else if len(r.Closures) != 0 {
		return false
	}
	for i, t := range r.Closures {
		if !t.SafePoint.Valid() || !managedResumeSelectionMatches(t) || t.SuccessorGrant != r.Evidence.Grant || !t.Resolved() || t.Revision == 0 || ManagedDigest(*t.Terminal) != ManagedDigest(r.Evidence.Snapshot) || (i > 0 && r.Closures[i-1].SafePoint.ID >= t.SafePoint.ID) {
			return false
		}
	}
	return r.Phase == "ACKNOWLEDGED" && r.ACKIdentity == r.Identity()
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
