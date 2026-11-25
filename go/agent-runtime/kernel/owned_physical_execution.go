package kernel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

// IsOwned distinguishes ANY native binding from separately owned child grants.
// Partial bindings are owned-invalid, never a fallback to unrelated authority.
func (g ChildExecutionGrant) IsOwned() bool {
	return g.ExecutionOwnerID != "" || g.ExecutionLeaseID != "" || g.ExecutionEpoch != 0 || g.AdmissionSourceHash != "" || g.AdmissionOperation != ""
}
func (g ChildExecutionGrant) MatchesExecution(l ExecutionLease) bool {
	return g.ChildRunID == l.RunID && g.ExecutionOwnerID != "" && g.ExecutionLeaseID != "" && g.ExecutionEpoch > 0 && len(g.AdmissionSourceHash) == 64 && g.AdmissionOperation != "" && strings.TrimSpace(g.AdmissionOperation) == g.AdmissionOperation && g.ExecutionOwnerID == l.OwnerID && g.ExecutionLeaseID == l.LeaseID && g.ExecutionEpoch == l.Epoch
}

// ErrPhysicalAdmissionCommitUnknown reports that a Store reached the
// transaction commit boundary but could not prove its result. It is neither
// a definite refusal nor dispatch permission. A Store must not use this
// outcome for a known pre-commit rejection. Only the original in-flight
// admission can recover an exact committed attempt.
var ErrPhysicalAdmissionCommitUnknown = errors.New("kernel physical admission commit is uncertain")

// OwnedPhysicalExecutionStore adds an atomic Root Agent barrier to its native
// lease/receipt protocol. Begin returns nil only for the original committed
// admission; a definite rejection cannot grant execution, even if the same
// public Grant ID is already present. Only an actual COMMIT failure may be
// returned through this attempt's OwnedPhysicalAdmission.CommitOutcome.
// Observations and receipt-only recovery never grant fresh dispatch authority.
type OwnedPhysicalExecutionStore interface {
	RecordOwnedPhysicalCallbackReturn(context.Context, ChildExecutionGrant, uint64, OwnedPhysicalCallbackOutcome, OwnedPhysicalCallbackReturn) error
	LookupOwnedPhysicalCallbackReturn(context.Context, ChildExecutionGrant) (OwnedPhysicalCallbackCompletion, error)
	BeginOwnedPhysicalExecution(context.Context, ChildExecutionGrant, ExecutionLease, OwnedPhysicalAdmission) error
	ApplyOwnedPhysicalRecovery(context.Context, ChildExecutionGrant, uint64, StoreMutation, OwnedPhysicalRecovery) (Snapshot, error)
}

// OwnedPhysicalAdmission binds the original source proof to one private
// invocation, not merely to a caller-configurable public Grant ID. The
// commitment is retained in the existing AdmissionSourceHash field; the
// attempt nonce is never exported or reconstructed from a stored Grant.
type OwnedPhysicalAdmission struct {
	proof   *featurecontrol.Proof
	attempt [32]byte
}

// The error classification alone is not evidence of this invocation's
// commit boundary. Keep the opaque attempt on the direct Store result.
type ownedAdmissionCommitUnknown struct {
	attempt [32]byte
	cause   error
}

func (e *ownedAdmissionCommitUnknown) Error() string {
	return ErrPhysicalAdmissionCommitUnknown.Error() + ": " + e.cause.Error()
}
func (e *ownedAdmissionCommitUnknown) Unwrap() error { return e.cause }
func (e *ownedAdmissionCommitUnknown) Is(target error) bool {
	return target == ErrPhysicalAdmissionCommitUnknown
}

// CommitOutcome reports the actual native COMMIT result, never a statement,
// validation or pre-commit rejection. It creates no execution authority:
// Runtime must independently recover the exact committed attempt. Preserve
// this direct result across Store wrappers; an arbitrary joined or wrapped
// error is not sufficient to opt in to transparent ACK recovery.
func (p OwnedPhysicalAdmission) CommitOutcome(err error) error {
	if err == nil || p.attempt == ([32]byte{}) {
		return err
	}
	for _, denial := range [...]error{
		ErrInvalidInput, ErrNotFound, ErrAlreadyExists, ErrConflict, ErrTerminal, ErrDeadline,
		ErrExecutionConflict, ErrExecutionRequired, ErrExecutionQuarantined, ErrChildExecutionBusy, ErrOwnedRetirementPending,
	} {
		if errors.Is(err, denial) {
			return err
		}
	}
	return &ownedAdmissionCommitUnknown{attempt: p.attempt, cause: err}
}

func (p OwnedPhysicalAdmission) commitUncertain(err error) bool {
	outcome, ok := err.(*ownedAdmissionCommitUnknown) //nolint:errorlint // Direct attempt-specific Store outcome only.
	return ok && outcome.attempt == p.attempt && p.attempt != ([32]byte{})
}

func (p OwnedPhysicalAdmission) Matches(g ChildExecutionGrant, s Snapshot, l ExecutionLease) bool {
	return p.attempt != ([32]byte{}) && s.Run.Kind == "agent" && s.Run.ParentRunID == "" && (s.Run.Status == RunStatusRunning || s.Run.Status == RunStatusWaitingInput) && s.Run.Revision == g.ExpectedRevision && g.Identity().Valid() && g.MatchesExecution(l) &&
		g.AdmissionSourceHash == ownedAdmissionAttemptHash(s, l, g.AdmissionOperation, g.ID, p.attempt) &&
		p.proof.Matches(s.Run.ID, s.Run.Revision, "agent_owned_physical_admission", l.LeaseID, s.State, s.Checkpoint)
}

func ownedAdmissionAttemptHash(s Snapshot, l ExecutionLease, operation, grantID string, attempt [32]byte) string {
	source := ownedAdmissionHash(s, l, operation)
	if source == "" || attempt == ([32]byte{}) {
		return ""
	}
	// Both source and attempt have a fixed length; the grant ID is the
	// remainder. This domain is distinct from the stable managed source hash.
	payload := append([]byte("owned-physical-admission-attempt/v1\x00"+source), attempt[:]...)
	digest := sha256.Sum256(append(payload, []byte(grantID)...))
	return hex.EncodeToString(digest[:])
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
	p := OwnedPhysicalAdmission{proof: proof}
	// Do not use configured IDSource or other Host callbacks for attempt
	// entropy: two contenders may deliberately return the same public ID.
	if _, e = rand.Read(p.attempt[:]); e != nil {
		return ChildExecutionGrant{}, e
	}
	g := ChildExecutionGrant{ID: gid, ChildRunID: id, ExpectedRevision: revision, ExecutionOwnerID: l.OwnerID, ExecutionLeaseID: l.LeaseID, ExecutionEpoch: l.Epoch, AdmissionSourceHash: ownedAdmissionAttemptHash(s, l, operation[0], gid, p.attempt), AdmissionOperation: operation[0]}
	if !p.Matches(g, s, l) {
		return ChildExecutionGrant{}, ErrExecutionConflict
	}
	if e = store.BeginOwnedPhysicalExecution(ctx, g, l, p); e != nil {
		return r.recoverOwnedAdmissionACK(ctx, g, p, e)
	}
	return g, nil
}

// Only an explicitly uncertain COMMIT of this in-flight invocation may be
// disambiguated. A lookup is observation, never an alternate admission path.
// Do not turn a definite Store refusal (even a joined error) into success.
func (r *Runtime) recoverOwnedAdmissionACK(ctx context.Context, g ChildExecutionGrant, attempt OwnedPhysicalAdmission, cause error) (ChildExecutionGrant, error) {
	if !attempt.commitUncertain(cause) {
		return ChildExecutionGrant{}, cause
	}
	if err := ctx.Err(); err != nil {
		return ChildExecutionGrant{}, errors.Join(cause, err)
	}
	physical, ok := r.store.(ChildExecutionStore)
	if !ok {
		return ChildExecutionGrant{}, cause
	}
	// Bound the reconciliation read and inherit caller cancellation. Never
	// resume physical execution from a detached cleanup context.
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	stored, err := physical.LookupChildExecution(readCtx, g.ChildRunID)
	if err != nil || stored != g {
		return ChildExecutionGrant{}, errors.Join(cause, err)
	}
	if err = readCtx.Err(); err != nil {
		return ChildExecutionGrant{}, errors.Join(cause, err)
	}
	return g, nil
}

// OwnedPhysicalRecovery is first-party-only, exact source AND target CAS
// authority. It cannot be used as a generic Store write or provider permission.
type OwnedPhysicalRecovery struct {
	source, target *featurecontrol.Proof
	modelEvidence  bool
}

// IsModelEvidence distinguishes evidence preservation from actual recovery.
// Only the purpose-specific Runtime entry can mint this opaque capability.
func (p OwnedPhysicalRecovery) IsModelEvidence() bool { return p.modelEvidence }

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
	if p.modelEvidence {
		if len(m.Events) != 0 || m.Record.Run.ErrorCode != current.Run.ErrorCode || m.Record.Run.ErrorDetail != current.Run.ErrorDetail || !reflect.DeepEqual(m.Record.Checkpoint, current.Checkpoint) {
			return false
		}
		return p.source.Matches(current.Run.ID, current.Run.Revision, "agent_owned_model_receipt_evidence_source", g.ID, current.State, current.Checkpoint) && p.target.Matches(current.Run.ID, current.Run.Revision, "agent_owned_model_receipt_evidence_target", g.ID, m.Record.State, m.Record.Checkpoint)
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
	return r.applyOwnedPhysicalPurpose(ctx, g, revision, m, source, target, false)
}

// ApplyOwnedModelReceiptEvidence preserves first-party verified original model
// response evidence without resolving unknown Host work or authorizing send.
func (r *Runtime) ApplyOwnedModelReceiptEvidence(ctx context.Context, g ChildExecutionGrant, revision uint64, m Mutation, source, target *featurecontrol.Proof) (Snapshot, error) {
	return r.applyOwnedPhysicalPurpose(ctx, g, revision, m, source, target, true)
}
func (r *Runtime) applyOwnedPhysicalPurpose(ctx context.Context, g ChildExecutionGrant, revision uint64, m Mutation, source, target *featurecontrol.Proof, evidence bool) (Snapshot, error) {

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
	p := OwnedPhysicalRecovery{source: source, target: target, modelEvidence: evidence}
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
