package kernel

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

// These records are Store facts, not dispatch or acknowledgement capabilities.
type OwnedSettlementEvidence struct {
	Version    uint64
	Grant      ChildExecutionGrant
	Snapshot   Snapshot
	Completion OwnedPhysicalCallbackCompletion
	MarkOrigin string
}
type OwnedRetirementReceipt struct {
	Version     uint64
	Evidence    OwnedSettlementEvidence
	Closures    []ManagedTransfer
	Phase       string
	ACKIdentity string
}
type OwnedRetirementAckResult struct {
	ReceiptID              string
	SettledSourceDigest    string
	PreviouslyAcknowledged bool
	Disposition            string
}
type OwnedRetirementStore interface {
	LoadOwnedRetirement(context.Context, string) (OwnedRetirementReceipt, error)
	LookupOwnedRetirementForRun(context.Context, string) (OwnedRetirementReceipt, error)
	AcknowledgeOwnedRetirement(context.Context, ChildExecutionGrant, OwnedRetirementAcknowledgement) (OwnedRetirementAckResult, error)
}

func (e OwnedSettlementEvidence) Valid() bool {
	g, s, c := e.Grant, e.Snapshot, e.Completion
	if e.Version != 1 || !g.IsOwned() || g.ID == "" || !g.MatchesExecution(ExecutionLease{RunID: g.ChildRunID, OwnerID: g.ExecutionOwnerID, LeaseID: g.ExecutionLeaseID, Epoch: g.ExecutionEpoch}) || s.Run.ID != g.ChildRunID || s.Run.Kind != "agent" || s.Run.ParentRunID != "" || s.Run.Revision < g.ExpectedRevision {
		return false
	}
	if c.GrantID == "" {
		return c == (OwnedPhysicalCallbackCompletion{}) && e.MarkOrigin == "LEGACY_UNKNOWN"
	}
	return c.GrantID == g.ID && c.HostCompleted && c.ReturnRevision >= g.ExpectedRevision && c.ReturnRevision <= s.Run.Revision && len(c.ReturnSourceHash) == 64 && (e.MarkOrigin == "OWNER_RETURN" || e.MarkOrigin == "RECOVERED_RETURN" || e.MarkOrigin == "LEGACY_UNKNOWN")
}

// ValidateOwnedRetirementClosures validates only existing CLOSED T4 facts.
// The caller must enumerate all matching histories under the Run transaction.
func ValidateOwnedRetirementClosures(g ChildExecutionGrant, s Snapshot, a ManagedAuthority, histories []ManagedTransfer) ([]ManagedTransfer, error) {
	out := make([]ManagedTransfer, 0)
	for _, t := range histories {
		if t.SuccessorGrant != g {
			continue
		}
		if !t.SafePoint.Valid() || !t.Selection.Valid() || t.Selection.Mode != ManagedResume || t.SafePoint.Host.KernelAuthority != a || t.State != ManagedClosed || t.Revision == 0 || t.Terminal == nil || ManagedDigest(*t.Terminal) != ManagedDigest(s) {
			return nil, ErrExecutionConflict
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SafePoint.ID < out[j].SafePoint.ID })
	for i := 1; i < len(out); i++ {
		if out[i-1].SafePoint.ID == out[i].SafePoint.ID {
			return nil, ErrExecutionConflict
		}
	}
	return out, nil
}
func (r OwnedRetirementReceipt) Identity() string {
	return ManagedDigest(struct {
		Version  uint64
		Evidence OwnedSettlementEvidence
		Closures []ManagedTransfer
	}{r.Version, r.Evidence, r.Closures})
}
func (r OwnedRetirementReceipt) Valid() bool {
	if r.Version != 1 || !r.Evidence.Valid() || r.Closures == nil {
		return false
	}
	for i, t := range r.Closures {
		if !t.SafePoint.Valid() || !t.Selection.Valid() || t.Selection.Mode != ManagedResume || t.SuccessorGrant != r.Evidence.Grant || t.State != ManagedClosed || t.Revision == 0 || t.Terminal == nil || ManagedDigest(*t.Terminal) != ManagedDigest(r.Evidence.Snapshot) || (i > 0 && r.Closures[i-1].SafePoint.ID >= t.SafePoint.ID) {
			return false
		}
	}
	return (r.Phase == "RETIRED" && r.ACKIdentity == "") || (r.Phase == "ACKNOWLEDGED" && r.ACKIdentity == r.Identity())
}
func OwnedRetirementAcknowledgementPayload(r OwnedRetirementReceipt) json.RawMessage {
	b, _ := json.Marshal(struct{ Identity string }{r.Identity()})
	return b
}

type OwnedRetirementAcknowledgement struct{ proof *featurecontrol.Proof }

func (p OwnedRetirementAcknowledgement) Matches(g ChildExecutionGrant, r OwnedRetirementReceipt) bool {
	if !r.Valid() || r.Evidence.Grant != g || r.Evidence.Completion.GrantID == "" {
		return false
	}
	return p.proof.Matches(g.ChildRunID, r.Evidence.Snapshot.Run.Revision, "agent_owned_retirement_ack", g.ID, OwnedRetirementAcknowledgementPayload(r), nil)
}

// ReduceOwnedRetirementACK is shared by all Stores under their transaction.
// A repeated ACK is a historical read, never authority over current state.
func ReduceOwnedRetirementACK(r OwnedRetirementReceipt, g ChildExecutionGrant, p OwnedRetirementAcknowledgement, current Snapshot, epoch uint64, lease ExecutionLease, hasGrants bool) (OwnedRetirementReceipt, OwnedRetirementAckResult, bool, error) {
	if !p.Matches(g, r) {
		return r, OwnedRetirementAckResult{}, false, ErrExecutionConflict
	}
	result := OwnedRetirementAckResult{ReceiptID: g.ID, SettledSourceDigest: ManagedDigest(r.Evidence.Snapshot), PreviouslyAcknowledged: r.Phase == "ACKNOWLEDGED", Disposition: "preserved_borrowed"}
	if r.Evidence.Completion.LeaseOwned {
		result.Disposition = "released_owned"
	}
	if result.PreviouslyAcknowledged {
		return r, result, false, nil
	}
	if hasGrants || epoch != g.ExecutionEpoch || !g.MatchesExecution(lease) || ManagedDigest(current) != ManagedDigest(r.Evidence.Snapshot) {
		return r, OwnedRetirementAckResult{}, false, ErrExecutionConflict
	}
	r.Phase = "ACKNOWLEDGED"
	r.ACKIdentity = r.Identity()
	return r, result, r.Evidence.Completion.LeaseOwned, nil
}

// SelectOwnedRetirementForRun is a read-only selector, never an ACK permit.
// Stores supply all receipts for this Run under the same lock as native state.
func SelectOwnedRetirementForRun(runID string, receipts []OwnedRetirementReceipt, current Snapshot, epoch uint64, lease ExecutionLease, hasGrants bool) (OwnedRetirementReceipt, error) {
	var selected OwnedRetirementReceipt
	for _, r := range receipts {
		if !r.Valid() || r.Evidence.Grant.ChildRunID != runID {
			return OwnedRetirementReceipt{}, ErrExecutionConflict
		}
		if r.Phase == "ACKNOWLEDGED" {
			continue
		}
		g := r.Evidence.Grant
		// Valid historical rows remain durable facts, not candidates for a
		// different current source or native owner. Never ACK them by Run ID.
		if current.Run.ID != runID || epoch != g.ExecutionEpoch || !g.MatchesExecution(lease) || ManagedDigest(current) != ManagedDigest(r.Evidence.Snapshot) {
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
