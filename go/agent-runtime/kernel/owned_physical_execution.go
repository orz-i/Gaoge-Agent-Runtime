package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

// IsOwned distinguishes ANY native binding from legacy child/Workflow grants.
// Partial bindings are owned-invalid, never a fallback to legacy authority.
func (g ChildExecutionGrant) IsOwned() bool {
	return g.ExecutionOwnerID != "" || g.ExecutionLeaseID != "" || g.ExecutionEpoch != 0 || g.AdmissionSourceHash != "" || g.AdmissionOperation != ""
}
func (g ChildExecutionGrant) MatchesExecution(l ExecutionLease) bool {
	return g.ChildRunID == l.RunID && g.ExecutionOwnerID != "" && g.ExecutionLeaseID != "" && g.ExecutionEpoch > 0 && len(g.AdmissionSourceHash) == 64 && g.AdmissionOperation != "" && strings.TrimSpace(g.AdmissionOperation) == g.AdmissionOperation && g.ExecutionOwnerID == l.OwnerID && g.ExecutionLeaseID == l.LeaseID && g.ExecutionEpoch == l.Epoch
}

// OwnedPhysicalExecutionStore adds an atomic Root Agent barrier to its native
// lease/receipt protocol. Neither method grants fresh dispatch authority.
type OwnedPhysicalExecutionStore interface {
	BeginOwnedPhysicalExecution(context.Context, ChildExecutionGrant, ExecutionLease, OwnedPhysicalAdmission) error
	ApplyOwnedPhysicalRecovery(context.Context, ChildExecutionGrant, uint64, StoreMutation, OwnedPhysicalRecovery) (Snapshot, error)
}

type OwnedPhysicalAdmission struct{ proof *featurecontrol.Proof }

func (p OwnedPhysicalAdmission) Matches(g ChildExecutionGrant, s Snapshot, l ExecutionLease) bool {
	return s.Run.Kind == "agent" && s.Run.ParentRunID == "" && (s.Run.Status == RunStatusRunning || s.Run.Status == RunStatusWaitingInput) && s.Run.Revision == g.ExpectedRevision && g.ID != "" && g.MatchesExecution(l) && g.AdmissionSourceHash == ownedAdmissionHash(s, l, g.AdmissionOperation) && p.proof.Matches(s.Run.ID, s.Run.Revision, "agent_owned_physical_admission", l.LeaseID, s.State, s.Checkpoint)
}
func ownedAdmissionHash(s Snapshot, l ExecutionLease, operation string) string {
	encoded, e := json.Marshal(struct {
		ID                        string
		Kind                      RunKind
		Actor                     ActorRef
		Thread                    ThreadRef
		RequestID, ParentID, Goal string
		Revision                  uint64
		State                     json.RawMessage
		Checkpoint                *Checkpoint
		OwnerID, LeaseID          string
		Epoch                     uint64
	}{s.Run.ID, s.Run.Kind, s.Run.Actor, s.Run.Thread, s.Run.RequestID, s.Run.ParentRunID, s.Run.Goal, s.Run.Revision, s.State, s.Checkpoint, l.OwnerID, l.LeaseID, l.Epoch})
	if e != nil {
		return ""
	}
	encoded = append(append([]byte(operation), 0), encoded...)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// BoundExecutionLease returns a copy; it cannot renew, acquire or transfer it.
func (r *Runtime) BoundExecutionLease() (ExecutionLease, bool) {
	if r == nil || r.execution == nil {
		return ExecutionLease{}, false
	}
	return *r.execution, true
}
func (r *Runtime) SupportsOwnedPhysicalExecution() bool {
	if r == nil {
		return false
	}
	_, ok := r.store.(OwnedPhysicalExecutionStore)
	return ok
}
func (r *Runtime) BeginOwnedPhysicalExecution(ctx context.Context, id string, revision uint64, proof *featurecontrol.Proof, operation ...string) (ChildExecutionGrant, error) {
	if r == nil || ctx == nil || id == "" || revision == 0 || len(operation) != 1 || strings.TrimSpace(operation[0]) == "" || operation[0] != strings.TrimSpace(operation[0]) {
		return ChildExecutionGrant{}, ErrInvalidInput
	}
	store, ok := r.store.(OwnedPhysicalExecutionStore)
	if !ok {
		return ChildExecutionGrant{}, ErrExecutionRequired
	}
	l, ok := r.BoundExecutionLease()
	if !ok || l.RunID != id {
		return ChildExecutionGrant{}, ErrExecutionRequired
	}
	s, e := r.Load(ctx, id)
	if e != nil {
		return ChildExecutionGrant{}, e
	}
	if s.Run.Revision != revision {
		return ChildExecutionGrant{}, ErrConflict
	}
	gid, e := r.NewID("ownedgrant")
	if e != nil {
		return ChildExecutionGrant{}, e
	}
	g := ChildExecutionGrant{ID: gid, ChildRunID: id, ExpectedRevision: revision, ExecutionOwnerID: l.OwnerID, ExecutionLeaseID: l.LeaseID, ExecutionEpoch: l.Epoch, AdmissionSourceHash: ownedAdmissionHash(s, l, operation[0]), AdmissionOperation: operation[0]}
	p := OwnedPhysicalAdmission{proof: proof}
	if !p.Matches(g, s, l) {
		return ChildExecutionGrant{}, ErrExecutionConflict
	}
	if e = store.BeginOwnedPhysicalExecution(ctx, g, l, p); e != nil {
		// Only the exact persisted identity can disambiguate committed admission ACK loss.
		if physical, yes := r.store.(ChildExecutionStore); yes {
			stored, lookupErr := physical.LookupChildExecution(context.WithoutCancel(ctx), id)
			if lookupErr == nil && stored == g {
				return g, nil
			}
		}
		return ChildExecutionGrant{}, e
	}
	return g, nil
}

// OwnedPhysicalRecovery is first-party-only, exact source AND target CAS
// authority. It cannot be used as a generic Store write or provider permission.
type OwnedPhysicalRecovery struct{ source, target *featurecontrol.Proof }

func (p OwnedPhysicalRecovery) Matches(g ChildExecutionGrant, current Snapshot, m StoreMutation) bool {
	if !g.IsOwned() || g.ID == "" || g.ChildRunID != current.Run.ID || current.Run.Kind != "agent" || current.Run.ParentRunID != "" || current.Run.Status != RunStatusRunning || current.Run.Revision < g.ExpectedRevision || m.Record.Run.Status != RunStatusRunning || m.Record.Run.Revision != current.Run.Revision+1 {
		return false
	}
	run := m.Record.Run
	run.Revision = current.Run.Revision
	run.UpdatedAt = current.Run.UpdatedAt
	run.ErrorCode = current.Run.ErrorCode
	run.ErrorDetail = current.Run.ErrorDetail
	if !reflect.DeepEqual(run, current.Run) || m.Record.ExecutionRequired || !reflect.DeepEqual(m.Record.Result, current.Result) {
		return false
	}
	return p.source.Matches(current.Run.ID, current.Run.Revision, "agent_owned_receipt_recovery_source", g.ID, current.State, current.Checkpoint) && p.target.Matches(current.Run.ID, current.Run.Revision, "agent_owned_receipt_recovery_target", g.ID, m.Record.State, m.Record.Checkpoint)
}

// PhysicalPermit is usable only for this already-validated receipt-only CAS.
func (p OwnedPhysicalRecovery) PhysicalPermit(g ChildExecutionGrant, s Snapshot, m StoreMutation) PhysicalApplyPermit {
	if !p.Matches(g, s, m) {
		return PhysicalApplyPermit{}
	}
	return PhysicalApplyPermit{grant: g, proof: featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_physical_apply", g.ID, s.State, s.Checkpoint)}
}
func (r *Runtime) ApplyOwnedPhysicalRecovery(ctx context.Context, g ChildExecutionGrant, revision uint64, m Mutation, source, target *featurecontrol.Proof) (Snapshot, error) {
	if r == nil || ctx == nil || revision == 0 || hasReservedApplyEvidence(m.Events) {
		return Snapshot{}, ErrInvalidInput
	}
	store, ok := r.store.(OwnedPhysicalExecutionStore)
	if !ok {
		return Snapshot{}, ErrExecutionRequired
	}
	s, e := r.Load(ctx, g.ChildRunID)
	if e != nil {
		return Snapshot{}, e
	}
	if s.Run.Revision != revision {
		return Snapshot{}, ErrConflict
	}
	if e = validateMutation(s, m); e != nil {
		return Snapshot{}, e
	}
	proposed := r.makeStoreMutation(s, m)
	p := OwnedPhysicalRecovery{source: source, target: target}
	if !p.Matches(g, s, proposed) {
		return Snapshot{}, ErrExecutionConflict
	}
	return store.ApplyOwnedPhysicalRecovery(ctx, g, revision, proposed, p)
}

// MarkOwnedPhysicalRecovered never supplies an execution lease. Agent must
// prove durable callback-returned, Host completion, and all original receipts.
func (r *Runtime) MarkOwnedPhysicalRecovered(ctx context.Context, g ChildExecutionGrant, proof *featurecontrol.Proof) error {
	if r == nil || ctx == nil || !g.IsOwned() {
		return ErrInvalidInput
	}
	store, ok := r.store.(ChildExecutionStore)
	if !ok {
		return ErrExecutionRequired
	}
	s, e := r.Load(ctx, g.ChildRunID)
	if e != nil {
		return e
	}
	p := ChildExecutionSettlement{proof: proof, recovery: true}
	if !p.Matches(g, s) {
		return ErrExecutionConflict
	}
	return store.MarkChildExecutionSettled(ctx, g, p)
}
