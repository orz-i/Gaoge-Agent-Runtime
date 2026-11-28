package kernel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
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

// PhysicalExecutionStore is the shared admission, return, consumption and
// receipt-only recovery contract for all first-party physical operations. Begin returns nil only for the original committed
// admission; a definite rejection cannot grant execution, even if the same
// public Grant ID is already present. Only an actual COMMIT failure may be
// returned through this attempt's PhysicalAdmission.CommitOutcome.
// Observations and receipt-only recovery never grant fresh dispatch authority.
// CheckOwnedPhysicalEntry atomically consumes the original Grant entry epoch
// before acknowledging callback entry, including Managed successors; no replay.
type PhysicalExecutionStore interface {
	LoadOwnedRetirement(context.Context, string) (OwnedRetirementReceipt, error)
	LookupOwnedRetirementForRun(context.Context, string) (OwnedRetirementReceipt, error)
	LookupChildExecution(context.Context, string) (ChildExecutionGrant, error)
	FinishChildExecution(context.Context, ChildExecutionGrant, ChildExecutionSettlement) error
	RecordPhysicalCallbackReturn(context.Context, ChildExecutionGrant, uint64, PhysicalCallbackOutcome, PhysicalCallbackReturn) error
	LookupPhysicalCallbackReturn(context.Context, ChildExecutionGrant) (PhysicalCallbackCompletion, error)
	BeginPhysicalExecution(context.Context, PhysicalAdmission) (PhysicalAdmissionResult, error)
	CheckOwnedPhysicalEntry(context.Context, ChildExecutionGrant, ExecutionLease, Snapshot) (OwnedPhysicalEntryWindow, error)
}

// PhysicalAdmission binds the original source proof to one private
// invocation, not merely to a caller-configurable public Grant ID. The
// commitment is retained in the existing AdmissionSourceHash field; the
// attempt nonce is never exported or reconstructed from a stored Grant.
type PhysicalAdmission struct {
	proof      *featurecontrol.Proof
	attempt    [32]byte
	dispatchID string
	acquire    ExecutionAcquireRequest
	candidate  PhysicalAdmissionResult
	recovery   ManagedAdmissionProof
}

// PhysicalAdmissionResult is the single admitted native segment.
type PhysicalAdmissionResult struct {
	Grant ChildExecutionGrant
	Lease ExecutionLease
}

func (p PhysicalAdmission) AcquiresLease() bool     { return p.acquire.RunID != "" }
func (p PhysicalAdmission) IsManagedRecovery() bool { return p.recovery.valid }

// Candidate exposes only the immutable request identity, not dispatch
// authority. Fresh candidates have no epoch or authoritative expiration.
func (p PhysicalAdmission) Candidate() PhysicalAdmissionResult { return p.candidate }

// PhysicalAdmissionSubject seals the same intent for fresh and borrowed
// owners, including the operation and original Host dispatch correlation.
func PhysicalAdmissionSubject(q ExecutionAcquireRequest, l ExecutionLease, operation, dispatchID string) string {
	l.ExpiresAt = time.Time{}
	return ManagedDigest(struct {
		Request               ExecutionAcquireRequest
		Lease                 ExecutionLease
		Operation, DispatchID string
	}{q, l, operation, dispatchID})
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
func (p PhysicalAdmission) CommitOutcome(err error) error {
	if err == nil || p.attempt == ([32]byte{}) {
		return err
	}
	for _, denial := range [...]error{
		ErrInvalidInput, ErrNotFound, ErrAlreadyExists, ErrConflict, ErrTerminal, ErrDeadline,
		ErrExecutionConflict, ErrExecutionRequired, ErrExecutionQuarantined, ErrChildExecutionBusy,
	} {
		if errors.Is(err, denial) {
			return err
		}
	}
	return &ownedAdmissionCommitUnknown{attempt: p.attempt, cause: err}
}

func (p PhysicalAdmission) commitUncertain(err error) bool {
	outcome, ok := err.(*ownedAdmissionCommitUnknown) //nolint:errorlint // Direct attempt-specific Store outcome only.
	return ok && outcome.attempt == p.attempt && p.attempt != ([32]byte{})
}

func (p PhysicalAdmission) Matches(g ChildExecutionGrant, s Snapshot, l ExecutionLease) bool {
	if !p.candidate.Grant.IsOwned() {
		return g == p.candidate.Grant && g.Identity().Valid() && g.ExpectedRevision != 0 &&
			l == (ExecutionLease{}) && s.Run.ID == g.ChildRunID && s.Run.Revision == g.ExpectedRevision &&
			s.Run.Kind == "agent" && s.Run.ParentRunID != "" &&
			(s.Run.Status == RunStatusRunning || s.Run.Status == RunStatusWaitingInput) &&
			p.proof.Matches(s.Run.ID, s.Run.Revision, "agent_owned_physical_admission", PhysicalAdmissionSubject(ExecutionAcquireRequest{}, ExecutionLease{}, "", ""), s.State, s)
	}

	candidate := p.candidate
	if p.attempt == ([32]byte{}) || !g.Identity().Valid() ||
		g.ID != candidate.Grant.ID || g.ChildRunID != candidate.Grant.ChildRunID || g.ExpectedRevision != candidate.Grant.ExpectedRevision ||
		g.AdmissionOperation != candidate.Grant.AdmissionOperation || l.OwnerID != candidate.Lease.OwnerID || l.LeaseID != candidate.Lease.LeaseID ||
		(!p.AcquiresLease() && l.Epoch != candidate.Lease.Epoch) || !g.MatchesExecution(l) {
		return false
	}
	if p.IsManagedRecovery() {
		return p.AcquiresLease() && g.AdmissionOperation == "managed_resume" && !strings.HasPrefix(g.ID, ownedDispatchGrantPrefix) &&
			p.recovery.Matches(p.recovery.transfer, ManagedResume) && ManagedSourceMatches(p.recovery.transfer.SafePoint, s) &&
			g.AdmissionSourceHash == ownedAdmissionAttemptHash(s, l, g.AdmissionOperation, g.ID, p.attempt)
	}
	if !ordinaryOwnedOperation(g.AdmissionOperation) {
		return false
	}
	if p.dispatchID != "" {
		id, e := OwnedPhysicalDispatchGrantID(g.ChildRunID, g.AdmissionOperation, p.dispatchID)
		if e != nil || g.ID != id {
			return false
		}
	} else if strings.HasPrefix(g.ID, ownedDispatchGrantPrefix) {
		return false
	}
	borrowed := candidate.Lease
	if p.AcquiresLease() {
		borrowed = ExecutionLease{}
	}
	return s.Run.Kind == "agent" && s.Run.ParentRunID == "" && s.Run.Revision == g.ExpectedRevision &&
		(s.Run.Status == RunStatusRunning || s.Run.Status == RunStatusWaitingInput) &&
		g.AdmissionSourceHash == ownedAdmissionAttemptHash(s, l, g.AdmissionOperation, g.ID, p.attempt) &&
		p.proof.Matches(s.Run.ID, s.Run.Revision, "agent_owned_physical_admission", PhysicalAdmissionSubject(p.acquire, borrowed, g.AdmissionOperation, p.dispatchID), s.State, s)
}

func ownedAdmissionAttemptHash(s Snapshot, l ExecutionLease, operation, grantID string, attempt [32]byte) string {
	if attempt == ([32]byte{}) {
		return ""
	}
	l.ExpiresAt = time.Time{}
	return ManagedDigest(struct {
		Domain             string
		Source             Snapshot
		Lease              ExecutionLease
		Operation, GrantID string
		Attempt            [32]byte
	}{"owned-physical-admission-attempt/v2", s, l, operation, grantID, attempt})
}

// BoundExecutionLease returns a copy; it cannot renew, acquire or transfer it.
func (r *Runtime) BoundExecutionLease() (ExecutionLease, bool) {
	if r == nil || r.execution == nil {
		return ExecutionLease{}, false
	}
	return *r.execution, true
}
func (r *Runtime) SupportsPhysicalExecution() bool {
	if r == nil {
		return false
	}
	_, ok := r.store.(PhysicalExecutionStore)
	return ok
}

const ownedDispatchGrantPrefix = "owneddispatch_"
const ownedNativeCommandGrantPrefix = ownedDispatchGrantPrefix + "native_"

// NativeCommandCorrelation identifies a native operation, not a Host Item.
func NativeCommandCorrelation(id string) bool { return strings.HasPrefix(id, "nativecmd_") }

// OwnedPhysicalDispatchGrantID links an existing Host dispatch to its one
// native Grant identity. The reserved namespace prevents ordinary IDSource
// output from impersonating this linkage. The identity is not permission:
// private attempt commitment, source proof and native lease still arbitrate.
func OwnedPhysicalDispatchGrantID(runID, operation, dispatchID string) (string, error) {
	if runID == "" || strings.TrimSpace(runID) != runID || !ordinaryOwnedOperation(operation) ||
		dispatchID == "" || strings.TrimSpace(dispatchID) != dispatchID || len(dispatchID) > 256 {
		return "", ErrInvalidInput
	}
	prefix := ownedDispatchGrantPrefix
	if NativeCommandCorrelation(dispatchID) {
		prefix = ownedNativeCommandGrantPrefix
	}
	return prefix + ManagedDigest(struct{ RunID, Operation, DispatchID string }{runID, operation, dispatchID}), nil
}

// AdmitPhysicalExecution is the sole ordinary native admission. The original
// source determines ancestor fencing or a leased owner; both use the same
// Store transaction and the exact returned binding.
func (r *Runtime) AdmitPhysicalExecution(ctx context.Context, s Snapshot, q ExecutionAcquireRequest, proof *featurecontrol.Proof, operation, dispatchID string) (*Runtime, PhysicalAdmissionResult, error) {
	var zero PhysicalAdmissionResult
	id, rev := s.Run.ID, s.Run.Revision
	if r == nil || ctx == nil || id == "" || rev == 0 || !ordinaryOwnedOperation(operation) {
		return nil, zero, ErrInvalidInput
	}
	if r.physicalGrant != nil {
		return nil, zero, ErrExecutionConflict
	}
	if s.Run.Kind != "agent" {
		return nil, zero, ErrExecutionRequired
	}
	if s.Run.ParentRunID != "" {
		if q != (ExecutionAcquireRequest{}) || dispatchID != "" ||
			!proof.Matches(id, rev, "agent_owned_physical_admission", PhysicalAdmissionSubject(q, ExecutionLease{}, "", ""), s.State, s) {
			return nil, zero, ErrExecutionConflict
		}
		gid, e := r.NewID("childgrant")
		if e != nil {
			return nil, zero, e
		}
		p := PhysicalAdmission{proof: proof, candidate: PhysicalAdmissionResult{Grant: ChildExecutionGrant{ID: gid, ChildRunID: id, ExpectedRevision: rev}}}
		return r.admitPhysical(ctx, s, p)
	}

	_, ok := r.store.(PhysicalExecutionStore)
	if !ok || !r.SupportsExecution() {
		return nil, zero, ErrExecutionRequired
	}
	l, bound := r.BoundExecutionLease()
	if bound {
		if l.RunID != id || q != (ExecutionAcquireRequest{}) {
			return nil, zero, ErrExecutionConflict
		}
	} else if q.RunID != id || !validExecutionAcquireRequest(q) || strings.TrimSpace(q.OwnerID) != q.OwnerID {
		return nil, zero, ErrInvalidInput
	}
	var gid string
	var e error
	if dispatchID != "" {
		gid, e = OwnedPhysicalDispatchGrantID(id, operation, dispatchID)
	} else {
		gid, e = r.NewID("ownedgrant")
		if e == nil && strings.HasPrefix(gid, ownedDispatchGrantPrefix) {
			return nil, zero, ErrInvalidInput
		}
	}
	if e != nil {
		return nil, zero, e
	}
	p := PhysicalAdmission{proof: proof, dispatchID: dispatchID, acquire: q}
	if _, e = rand.Read(p.attempt[:]); e != nil {
		return nil, zero, e
	}
	if !bound {
		// The private invocation generates an unpredictable lease token, while
		// the Store alone chooses epoch and expiry at its locked transaction.
		var token [16]byte
		if _, e = rand.Read(token[:]); e != nil {
			return nil, zero, e
		}
		l = ExecutionLease{RunID: id, OwnerID: q.OwnerID, LeaseID: hex.EncodeToString(token[:])}
	}
	g := ChildExecutionGrant{ID: gid, ChildRunID: id, ExpectedRevision: rev, ExecutionOwnerID: l.OwnerID, ExecutionLeaseID: l.LeaseID, ExecutionEpoch: l.Epoch, AdmissionOperation: operation}
	if bound {
		g.AdmissionSourceHash = ownedAdmissionAttemptHash(s, l, operation, gid, p.attempt)
	}
	p.candidate = PhysicalAdmissionResult{Grant: g, Lease: l}
	borrowed := l
	if !bound {
		borrowed = ExecutionLease{}
	}
	if !proof.Matches(id, rev, "agent_owned_physical_admission", PhysicalAdmissionSubject(q, borrowed, operation, dispatchID), s.State, s) {
		return nil, zero, ErrExecutionConflict
	}
	return r.admitPhysical(ctx, s, p)
}

// Both ordinary and recovered segments use the same commit outcome and
// scoped native bearer. A separate recovery ACK cannot elect an owner.
func (r *Runtime) admitPhysical(ctx context.Context, s Snapshot, p PhysicalAdmission) (*Runtime, PhysicalAdmissionResult, error) {
	var zero PhysicalAdmissionResult
	if r == nil || ctx == nil {
		return nil, zero, ErrInvalidInput
	}
	store, ok := r.store.(PhysicalExecutionStore)
	if !ok {
		return nil, zero, ErrExecutionRequired
	}
	id, gid := s.Run.ID, p.Candidate().Grant.ID
	v, e := store.BeginPhysicalExecution(ctx, p)
	if e != nil {
		if !p.commitUncertain(e) {
			return nil, zero, e
		}
		// A commit error is not a grant. Compare the exact candidate, private
		// attempt and durable original row before returning a bearer.
		if ctx.Err() != nil {
			return nil, zero, errors.Join(e, ctx.Err())
		}
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		stored, readErr := store.LookupChildExecution(readCtx, id)
		if readErr != nil || stored != v.Grant || readCtx.Err() != nil {
			return nil, zero, errors.Join(e, readErr, readCtx.Err())
		}
	}
	if v.Grant.ID != gid || (v.Grant.IsOwned() && !validExecutionLease(v.Lease)) || !p.Matches(v.Grant, s, v.Lease) {
		return nil, zero, errors.Join(ErrPhysicalAdmissionCommitUnknown, e)
	}
	// Bind both capabilities from the one committed result. No second
	// read/owner proof is required. Callback entry revalidates the native
	// identity, current Snapshot and Store time before any effect.
	scoped := *r
	scoped.physicalGrant = &v.Grant
	if v.Grant.IsOwned() {
		scoped.execution = &v.Lease
	}
	return &scoped, v, nil
}

// ReceiptRecoveryTarget includes the optional accounting CAS in the same
// existing opaque target seal. A receipt-only permit never authorizes send.
func ReceiptRecoveryTarget(checkpoint *Checkpoint, ledger *budget.LedgerMutation) any {
	if ledger == nil {
		return checkpoint
	}
	return struct {
		Checkpoint *Checkpoint
		Ledger     *budget.LedgerMutation
	}{checkpoint, ledger}
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
	if m.Ledger != nil {
		if p.modelEvidence || len(m.Events) != 1 {
			return false
		}
		switch m.Events[0].Type {
		case "agent.model_invocation.completed", "tool.receipt_recovered", "tool.admission_unsent", "tool.external_no_effect":
		default:
			return false
		}
	}
	if p.modelEvidence {
		if len(m.Events) != 0 || m.Record.Run.ErrorCode != current.Run.ErrorCode || m.Record.Run.ErrorDetail != current.Run.ErrorDetail || !reflect.DeepEqual(m.Record.Checkpoint, current.Checkpoint) {
			return false
		}
		return p.source.Matches(current.Run.ID, current.Run.Revision, "agent_owned_model_receipt_evidence_source", g.ID, current.State, current.Checkpoint) && p.target.Matches(current.Run.ID, current.Run.Revision, "agent_owned_model_receipt_evidence_target", g.ID, m.Record.State, m.Record.Checkpoint)
	}
	return p.source.Matches(current.Run.ID, current.Run.Revision, "agent_owned_receipt_recovery_source", g.ID, current.State, current.Checkpoint) && p.target.Matches(current.Run.ID, current.Run.Revision, "agent_owned_receipt_recovery_target", g.ID, m.Record.State, ReceiptRecoveryTarget(m.Record.Checkpoint, m.Ledger))
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

	if r == nil || ctx == nil || revision == 0 || (evidence && m.Ledger != nil) || hasReservedApplyEvidence(m.Events) {
		return Snapshot{}, ErrInvalidInput
	}
	if !r.SupportsPhysicalExecution() {
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
	if m.Ledger != nil {
		if _, ok := r.store.(budget.LedgerStore); !ok {
			return Snapshot{}, ErrExecutionRequired
		}
	}
	proposed := r.makeStoreMutation(s, m)
	p := OwnedPhysicalRecovery{source: source, target: target, modelEvidence: evidence}
	if !p.Matches(g, s, proposed) {
		return Snapshot{}, ErrExecutionConflict
	}
	out, _, err := r.store.Commit(ctx, g.ChildRunID, revision, proposed, ReceiptCommit(g, p))
	return out, err
}
