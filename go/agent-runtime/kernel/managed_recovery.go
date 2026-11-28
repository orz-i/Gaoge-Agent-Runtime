package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

// ManagedAuthority is persistent Store identity, not a credential. A fresh
// Memory Store gets a new identity; reopening a durable Store preserves it.
// Initialization precedes Run admission, never a rejected recovery request.
type ManagedAuthority struct {
	Namespace  string
	Generation uint64
	Version    uint32
}

func (a ManagedAuthority) Valid() bool {
	b, e := hex.DecodeString(a.Namespace)
	return e == nil && len(b) == 32 && a.Generation > 0 && a.Version == 1
}

type ManagedRecoveryMode string

const (
	ManagedResume ManagedRecoveryMode = "RESUME"
	ManagedCancel ManagedRecoveryMode = "CANCEL"
)

// ManagedLifecycleAgentObligations is the one native transfer payload.
// Context, Summary, Inputs and Budget are optional original obligations.
const ManagedLifecycleAgentObligations = "managed-agent-obligations-v2"

// ManagedHostBinding is the actual original native Harness dispatch source.
// Source contains the canonical frozen Invocation/Turn ownership projection,
// not mutable status/timestamp fields. IDs alone never establish authority.
type ManagedHostBinding struct {
	KernelAuthority ManagedAuthority
	HostAuthority   ManagedAuthority
	TurnID          string
	InvocationID    string
	Attempt         uint32
	RunID           string
	DispatchID      string
	Phase           string
	Source          json.RawMessage          `json:",omitempty"`
	Approval        *ManagedApprovalDecision `json:",omitempty"`
}

// ManagedApprovalDecision freezes the Host's original control request in the
// dispatch itself. A recovered approval must not invent a decision/comment
// after its native checkpoint was consumed.
type ManagedApprovalDecision struct {
	CheckpointID string
	Decision     string
	Comment      string
}

func (a ManagedApprovalDecision) Valid() bool {
	return a.CheckpointID != "" && len(a.CheckpointID) <= 128 && strings.TrimSpace(a.CheckpointID) == a.CheckpointID &&
		(a.Decision == "approve" || a.Decision == "reject") && strings.TrimSpace(a.Comment) == a.Comment
}

// ManagedDispatchOperation maps the original native command to its Kernel operation.
func ManagedDispatchOperation(phase string) (string, bool) {
	switch phase {
	case "start", "start-retry":
		return "start", true
	case "resume":
		return phase, true
	case "resolve-approval":
		return "approval", true
	default:
		return "", false
	}
}

// DirectNativeCommand has no independently admitted Host execution Item.
func (b ManagedHostBinding) DirectNativeCommand() bool { return NativeCommandCorrelation(b.DispatchID) }

func (b ManagedHostBinding) Valid() bool {
	_, ok := ManagedDispatchOperation(b.Phase)
	approvalValid := (b.Phase == "resolve-approval" && b.Approval != nil && b.Approval.Valid()) || (b.Phase != "resolve-approval" && b.Approval == nil)
	return ok && approvalValid && (b.Phase != "start-retry" || b.Attempt > 1) && (b.Phase != "start" || b.Attempt == 1) && b.DirectNativeCommand() && b.KernelAuthority.Valid() && b.HostAuthority.Valid() && b.TurnID != "" && b.InvocationID != "" && b.Attempt > 0 && b.RunID != "" && b.DispatchID != "" && json.Valid(b.Source)
}

// MatchesOriginalGrant prevents a later same-Run dispatch from borrowing
// the old recovery identity. Both operation and deterministic original
// dispatch Grant ID are required, in addition to the native bearer proof.
func (b ManagedHostBinding) MatchesOriginalGrant(g ChildExecutionGrant) bool {
	op, ok := ManagedDispatchOperation(b.Phase)
	if !ok || !b.DirectNativeCommand() || g.ChildRunID != b.RunID || g.AdmissionOperation != op || !g.IsOwned() {
		return false
	}
	id, err := OwnedPhysicalDispatchGrantID(b.RunID, op, b.DispatchID)
	return err == nil && id == g.ID
}

// ManagedSafePoint is stored IN the existing native ApplyExecution
// transaction. Source is that committed snapshot, including the whole Run,
// state/checkpoint/result. Its revision is not a later bookkeeping revision.
type ManagedSafePoint struct {
	ID                string
	RecoveryID        string
	Host              ManagedHostBinding
	OriginalGrant     ChildExecutionGrant
	OriginalLease     ExecutionLease
	Source            Snapshot
	ModelInvocationID string
	ModelRequestHash  string
	LifecycleVersion  string
	LifecycleSource   json.RawMessage
}

// ManagedPreparationContext identifies the original Host-owned Context
// checkpoint before a physical Model intent exists. The concrete Host bridge
// verifies its immutable source and any exact native rollover on recovery.
type ManagedPreparationContext struct {
	CheckpointID     string `json:"checkpointID"`
	CheckpointDigest string `json:"checkpointDigest"`
}

// ManagedAgentObligations is the exact combined safe-point payload. Kernel
// validates its closed event/source shape; Agent validates feature state and
// the original Budget Store before ownership transfer. Host closure consumes
// the same representation, not another interpretation of the ledger identity.
type ManagedAgentObligations struct {
	Phase              string                     `json:"phase"`
	Purpose            string                     `json:"purpose,omitempty"`
	SourceRevision     uint64                     `json:"sourceRevision"`
	InvocationID       string                     `json:"invocationID"`
	RequestHash        string                     `json:"requestHash"`
	BindingDigest      string                     `json:"bindingDigest,omitempty"`
	SamplingDigest     string                     `json:"samplingDigest"`
	InputBinding       string                     `json:"inputBinding,omitempty"`
	InputCursor        uint64                     `json:"inputCursor,omitempty"`
	LedgerID           string                     `json:"ledgerID,omitempty"`
	LedgerRevision     uint64                     `json:"ledgerRevision,omitempty"`
	LedgerDigest       string                     `json:"ledgerDigest,omitempty"`
	Journal            json.RawMessage            `json:"journal"`
	PreparationContext *ManagedPreparationContext `json:"preparationContext,omitempty"`
	// New preparation proofs bind the exact reducer event, not merely a
	// recognized event name. Older ready/input points retain their profile.
	PreparationTransitionDigest string `json:"preparationTransitionDigest,omitempty"`
	// RequalifiedFrom binds an expired first-owner frontier to its exact
	// preceding immutable point. No callback return is inferred.
	RequalifiedFrom string `json:"requalifiedFrom,omitempty"`
}

func (p ManagedSafePoint) AgentObligations() (ManagedAgentObligations, bool) {
	var o ManagedAgentObligations
	decoder := json.NewDecoder(bytes.NewReader(p.LifecycleSource))
	decoder.DisallowUnknownFields()
	if p.LifecycleVersion != ManagedLifecycleAgentObligations || !json.Valid(p.LifecycleSource) || decoder.Decode(&o) != nil ||
		o.SourceRevision != p.Source.Run.Revision || o.InvocationID != p.ModelInvocationID || o.RequestHash != p.ModelRequestHash ||
		p.Host.Attempt == 0 || (o.InputBinding == "" && o.InputCursor != 0) {
		return o, false
	}
	var journal map[string]json.RawMessage
	if json.Unmarshal(o.Journal, &journal) != nil || len(journal) == 0 {
		return o, false
	}
	if o.LedgerID == "" {
		if o.LedgerRevision != 0 || o.LedgerDigest != "" {
			return o, false
		}
	} else if o.LedgerID != p.Host.TurnID || o.LedgerRevision == 0 || !validToolReceiptDigest(o.LedgerDigest) {
		return o, false
	}
	// Preparation has no Model Invocation. It is a separately tagged
	// phase of the same native obligation proof, never a fake sampling ID.
	if o.Phase == "prepare" {
		c := o.PreparationContext
		return o, c != nil && c.CheckpointID != "" && validToolReceiptDigest(c.CheckpointDigest) &&
			o.InvocationID == "" && o.RequestHash == "" && o.SamplingDigest == "" && o.Purpose == "" &&
			o.BindingDigest == "" && o.RequalifiedFrom == "" &&
			(o.PreparationTransitionDigest == "" || validToolReceiptDigest(o.PreparationTransitionDigest))
	}
	if o.PreparationContext != nil || o.PreparationTransitionDigest != "" || o.InvocationID == "" || len(o.RequestHash) != 64 || !validToolReceiptDigest(o.SamplingDigest) {
		return o, false
	}
	if o.RequalifiedFrom != "" && (o.RequalifiedFrom == p.ID || o.Phase == "consumed") {
		return o, false
	}
	if o.Purpose == "" {
		return o, o.BindingDigest == "" && (o.Phase == "pending" || o.Phase == "completed" ||
			(o.Phase == "consumed" && o.InputBinding != "" && o.InputCursor > 0))
	}
	return o, o.Purpose == "context_summary" && validToolReceiptDigest(o.BindingDigest) &&
		(o.Phase == "pending" || o.Phase == "completed" || o.Phase == "consumed")
}

func (p ManagedSafePoint) Valid() bool {
	o, ok := p.AgentObligations()
	return ok && p.ID != "" && p.RecoveryID == ManagedRecoveryID(p.ID, p.Host) && p.Host.Valid() && p.Source.Run.ID == p.Host.RunID && p.Source.Run.Kind == "agent" && p.Source.Run.ParentRunID == "" && p.Source.Run.Status == RunStatusRunning && p.Source.Run.Revision > 0 && json.Valid(p.Source.State) && p.Host.MatchesOriginalGrant(p.OriginalGrant) && p.OriginalGrant.MatchesExecution(p.OriginalLease) && (o.Phase == "prepare" || (p.ModelInvocationID != "" && len(p.ModelRequestHash) == 64))
}

// ManagedTransfer records only the native source and its actual successor
// or cancellation. There is no Host reservation or activation state.
type ManagedTransfer struct {
	SafePoint      ManagedSafePoint
	Revision       uint64
	Selection      ManagedRecoveryAuthorization
	SuccessorGrant ChildExecutionGrant
	SuccessorLease ExecutionLease
	Terminal       *Snapshot
}

// Reject retired wire fields, including nested Host reservation tuples.
// Stopped-writer history is not interpreted as a new native qualification.
func (t *ManagedTransfer) UnmarshalJSON(raw []byte) error {
	type native ManagedTransfer
	var value native
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return err
	}
	*t = ManagedTransfer(value)
	return nil
}

func (t ManagedTransfer) Valid() bool {
	return t.SafePoint.Valid() && (t.Selection == (ManagedRecoveryAuthorization{}) || t.Selection.Valid())
}

func (t ManagedTransfer) Resolved() bool {
	return t.Valid() && t.Terminal != nil && t.Selection.Valid()
}

type ManagedRecoveryAuthorization struct {
	Mode               ManagedRecoveryMode
	CancellationReason string
}

func (r ManagedRecoveryAuthorization) Valid() bool {
	return r.Mode == ManagedResume && r.CancellationReason == "" ||
		r.Mode == ManagedCancel && strings.TrimSpace(r.CancellationReason) == r.CancellationReason &&
			utf8.ValidString(r.CancellationReason)
}

// ManagedRecoveryHostStore is a STORAGE authority port wired by the concrete
// first-party Harness bridge. It receives no Next/callback and cannot certify
// lifecycle completion. Stores are trusted persistence, as Kernel.Store is.
// Authorization is read-only under the original Turn lock. The Kernel alone
// chooses the winning RESUME or CANCEL in its native commit.
type ManagedRecoveryHostStore interface {
	ManagedRecoveryAuthority(context.Context) (ManagedAuthority, error)
	VerifyManagedOriginalDispatch(context.Context, ManagedHostBinding) error
}

// ManagedSafePointProof is not externally constructible. Stores must validate
// Matches within the SAME transaction as the qualified source mutation.
type ManagedSafePointProof struct {
	point ManagedSafePoint
	proof *featurecontrol.Proof
}

func (p ManagedSafePointProof) Present() bool           { return p.proof != nil }
func (p ManagedSafePointProof) Point() ManagedSafePoint { return managedClone(p.point) }
func (p ManagedSafePointProof) Matches(previous Snapshot, next Record, lease ExecutionLease, grant ChildExecutionGrant) bool {
	return p.point.Valid() && p.point.Source.EventHead == previous.EventHead+1 && reflect.DeepEqual(p.point.OriginalGrant, grant) && sameManagedLease(p.point.OriginalLease, lease) && reflect.DeepEqual(p.point.Source.Run, next.Run) && reflect.DeepEqual(p.point.Source.State, next.State) && reflect.DeepEqual(p.point.Source.Checkpoint, next.Checkpoint) && reflect.DeepEqual(p.point.Source.Result, next.Result) && p.proof.Matches(previous.Run.ID, previous.Run.Revision, "managed_safe_point", grant.ID, previous.State, p.point)
}

// MatchesMutation adds the closed lifecycle/event profile to the opaque native
// source proof. Stores share this check instead of independently widening T0.
func (p ManagedSafePointProof) MatchesMutation(previous Snapshot, next Record, lease ExecutionLease, grant ChildExecutionGrant, events []EventDraft) bool {
	return p.Matches(previous, next, lease, grant) && managedSafePointEventMatches(p.point, events)
}

// ManagedPreparationTransition is the closed event class for ordinary
// native reducers that may return to model preparation. It is NOT evidence
// of quiescence: Agent validates the encoded target, original receipts and
// actual frame before minting a proof, and Store verifies that proof with
// the exact mutation. New reducers must not silently leave an eligible gap.
func ManagedPreparationTransition(events []EventDraft) bool {
	if len(events) != 1 {
		return false
	}
	switch events[0].Type {
	case "agent.native_preparation.ready", "agent.input_applied",
		"agent.model_continued", "agent.completion_corrected",
		"tool.completed", "tool.correction_requested",
		"agent.allowance_auto_granted":
		return true
	default:
		return false
	}
}

func managedSafePointEventMatches(point ManagedSafePoint, events []EventDraft) bool {
	if len(events) != 1 {
		return false
	}
	o, ok := point.AgentObligations()
	if !ok || o.RequalifiedFrom != "" {
		return false
	}
	switch o.Phase {
	case "prepare":
		if o.PreparationTransitionDigest == "" {
			return events[0].Type == "agent.native_preparation.ready" || events[0].Type == "agent.input_applied"
		}
		return ManagedPreparationTransition(events) && o.PreparationTransitionDigest == ManagedDigest(events)
	case "pending":
		return events[0].Type == "agent.model_invocation.pending"
	case "completed":
		return events[0].Type == "agent.model_invocation.completed"
	case "consumed":
		if o.Purpose == "" {
			return events[0].Type == "agent.input_applied"
		}
		return events[0].Type == "agent.context_summary.consumed"
	}
	return false
}

// ManagedFrontierQualification is an internal Agent proof of a new safe frontier
// after the original point was left. It replaces no history and grants no
// callback-return or dispatch fact. Store consumes it inside the same native admission CAS.
type ManagedFrontierQualification struct {
	prior ManagedTransfer
	point ManagedSafePoint
	proof *featurecontrol.Proof
}

func (q ManagedFrontierQualification) matches(t ManagedTransfer, s Snapshot) bool {
	o, ok := q.point.AgentObligations()
	return q.proof != nil && ok && t.SafePoint.LifecycleVersion == ManagedLifecycleAgentObligations &&
		t.Valid() && t.Terminal == nil && t.Selection.Mode == "" && t.SuccessorGrant.ID == "" && s.Run.Revision > t.SafePoint.Source.Run.Revision &&
		ManagedDigest(q.prior) == ManagedDigest(t) && o.RequalifiedFrom == t.SafePoint.ID &&
		q.point.Valid() && q.point.Host.RunID == s.Run.ID && reflect.DeepEqual(q.point.Host, t.SafePoint.Host) &&
		q.point.OriginalGrant == t.SafePoint.OriginalGrant && sameManagedLease(q.point.OriginalLease, t.SafePoint.OriginalLease) &&
		q.point.ModelInvocationID == t.SafePoint.ModelInvocationID && q.point.ModelRequestHash == t.SafePoint.ModelRequestHash &&
		ManagedSourceMatches(q.point, s) &&
		q.proof.Matches(s.Run.ID, s.Run.Revision, "managed_qualified_recovery", t.SafePoint.ID, s.State, q.point)
}

type ManagedRecoverySourceRequest struct {
	RunID, SuspensionID    string
	ExpectedRevision       uint64
	ExpectedTransferDigest string
	Qualification          ManagedFrontierQualification
}

// ManagedRecoveryStore operations use the existing Run/native-lease/grant
// transaction (Memory mutex, SQLite write transaction, PG row locks).
// Ordinary execution observes only the active native Grant and entry epoch.
// T0 is carried by StoreMutation.ManagedSafePoint, not a separate commit.
// A changed Run revision makes the recorded safe point stale without a write.
type ManagedRecoveryStore interface {
	ManagedRecoveryAuthority(context.Context) (ManagedAuthority, error)
	LoadManagedRecovery(context.Context, string) (ManagedTransfer, error)
	LoadManagedRecoveryByID(context.Context, string) (ManagedTransfer, error)
}

// ManagedAdmissionProof is minted from the configured Host's read-only
// authorization on first admission, or the native selection on re-admission.
// It is not deserializable caller authority.
type ManagedAdmissionProof struct {
	reservation ManagedRecoveryAuthorization
	transfer    ManagedTransfer
	request     ManagedRecoverySourceRequest
	valid       bool
}

func (p ManagedAdmissionProof) Point() ManagedSafePoint { return managedClone(p.transfer.SafePoint) }

func (p ManagedAdmissionProof) Authorization() ManagedRecoveryAuthorization {
	return p.reservation
}
func (p ManagedAdmissionProof) Matches(t ManagedTransfer, mode ManagedRecoveryMode) bool {
	r := p.reservation
	if !p.valid || !r.Valid() || r.Mode != mode || !t.Valid() || t.Terminal != nil || ManagedDigest(t) != ManagedDigest(p.transfer) {
		return false
	}
	return t.Selection.Mode == "" || t.Selection == r
}

// qualifyManagedSource rechecks the exact observed source, expiry, and
// old execution segment under the same native transaction that commits the
// successor or cancellation. The preliminary preparation was read-only.
func qualifyManagedSource(ctx context.Context, t ManagedTransfer, p ManagedAdmissionProof, s Snapshot, l ExecutionLease, g ChildExecutionGrant, a ManagedAuthority, physicalEpoch uint64, now time.Time, ledger budget.LedgerStore) (ManagedTransfer, error) {
	if !p.valid || p.request.ExpectedTransferDigest == "" {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	next, err := ValidateManagedRecoverySource(t, p.request, s, l, g, a, physicalEpoch, now)
	if err != nil || !p.Matches(next, p.reservation.Mode) {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	if _, err = managedRecoveryLedger(ctx, next, ledger); err != nil {
		return ManagedTransfer{}, err
	}
	return next, nil
}

func ManagedDigest(value any) string {
	b, e := json.Marshal(value)
	if e != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ManagedRecoveryID(id string, b ManagedHostBinding) string {
	return "managed-recovery-" + ManagedDigest(struct {
		ID      string
		Binding ManagedHostBinding
	}{id, b})
}
func sameManagedLease(a, b ExecutionLease) bool {
	return a.RunID == b.RunID && a.OwnerID == b.OwnerID && a.LeaseID == b.LeaseID && a.Epoch == b.Epoch
}

// ManagedCurrentGrant identifies the predecessor execution segment. Only
// the native Store lease, not a historical copy in the safe point, owns time.
func ManagedCurrentGrant(t ManagedTransfer) ChildExecutionGrant {
	if t.SuccessorGrant.ID != "" {
		return t.SuccessorGrant
	}
	return t.SafePoint.OriginalGrant
}

// ManagedSourceMatches compares the full persisted aggregate including EventHead.
func ManagedSourceMatches(p ManagedSafePoint, s Snapshot) bool {
	return ManagedDigest(p.Source) == ManagedDigest(s)
}

// The original selection must retain its exact persisted source identity
// when the entered successor is later retired. Neither entry nor closure
// may substitute another valid reservation for the same Run.
func managedResumeSelectionMatches(t ManagedTransfer) bool {
	return t.Selection.Mode == ManagedResume && t.Selection.Valid()
}

// OwnedAdmission turns an already authorized recovery source into the same
// native admission permit as ordinary execution. The Lease token supplies
// the private attempt commitment; there is no separate claimant identity.
func (p ManagedAdmissionProof) OwnedAdmission(q ExecutionAcquireRequest, leaseID, grantID string) PhysicalAdmission {
	t := p.transfer
	if !p.Matches(t, ManagedResume) || q.RunID != t.SafePoint.Host.RunID || !validExecutionAcquireRequest(q) || strings.TrimSpace(q.OwnerID) != q.OwnerID || leaseID == "" || !(GrantIdentity{GrantID: grantID, RunID: q.RunID}).Valid() {
		return PhysicalAdmission{}
	}
	l := ExecutionLease{RunID: q.RunID, OwnerID: q.OwnerID, LeaseID: leaseID}
	g := ChildExecutionGrant{ID: grantID, ChildRunID: q.RunID, ExpectedRevision: t.SafePoint.Source.Run.Revision, ExecutionOwnerID: q.OwnerID, ExecutionLeaseID: leaseID, AdmissionOperation: "managed_resume"}
	return PhysicalAdmission{acquire: q, attempt: sha256.Sum256([]byte(leaseID)), candidate: PhysicalAdmissionResult{Grant: g, Lease: l}, recovery: p}
}

// ResolveManagedPhysicalAdmission qualifies the sole expired predecessor,
// then delegates the new owner to the ordinary native admission reducer.
// The returned recovery facts and native segment commit together.
func ResolveManagedPhysicalAdmission(ctx context.Context, t ManagedTransfer, p PhysicalAdmission, s Snapshot, l ExecutionLease, g ChildExecutionGrant, a ManagedAuthority, physicalEpoch uint64, now time.Time, ledger budget.LedgerStore) (PhysicalAdmissionResult, ManagedTransfer, error) {
	var zero PhysicalAdmissionResult
	t, err := qualifyManagedSource(ctx, t, p.recovery, s, l, g, a, physicalEpoch, now, ledger)
	if err != nil {
		return zero, ManagedTransfer{}, err
	}
	v, err := ResolvePhysicalAdmission(s, l, now, false, false, p)
	if err != nil {
		return zero, ManagedTransfer{}, err
	}
	t.Revision, t.Selection = s.Run.Revision, p.recovery.Authorization()
	t.SuccessorGrant, t.SuccessorLease = v.Grant, v.Lease
	return v, t, nil
}

// Quiescence is deliberately narrower than ordinary receipt recovery. Never
// transfer an active child or held/waiting reservation (even a known unsent
// hold). No quota is reserved at this pre-dispatch point. Prior settled and
// released reservations remain the original accounting authority.
func QuiescentManagedLedger(l budget.Ledger, host ManagedHostBinding) bool {
	runID := host.RunID
	binding, ok := l.Runs[runID]
	if !ok || binding.ParentRunID != "" || l.ID == "" || l.ID != host.TurnID || l.Revision == 0 ||
		uint64(len(l.RootRetries))+1 != uint64(host.Attempt) {
		return false
	}
	id := runID
	for attempt := host.Attempt; attempt > 1; attempt-- {
		g, ok := l.RootRetries[id]
		if !ok || g.InvocationID != host.InvocationID || uint64(g.Attempt) != uint64(attempt) ||
			g.Active != (id == runID) || g.OriginalRunID == "" || !l.CancelledRuns[g.OriginalRunID] ||
			l.Runs[g.OriginalRunID].ParentRunID != "" {
			return false
		}
		id = g.OriginalRunID
	}
	if _, retry := l.RootRetries[id]; retry {
		return false
	}
	for id := range l.Runs {
		if id != runID && !l.FinishedRuns[id] {
			return false
		}
	}
	for _, res := range l.Reservations {
		if res.Status != budget.ReservationSettled && res.Status != budget.ReservationReleased {
			return false
		}
	}
	return true
}

// Allow only the original Root cancellation and terminal lifetime closure to
// change the ledger during CANCEL. All charges/receipts, limits, ancestry,
// prior child closures and other reservations remain exactly the frozen facts.
func ManagedLedgerDigest(l budget.Ledger, runID string) string {
	l = budget.CloneLedger(l)
	l.Revision = 0
	if _, retry := l.RootRetries[runID]; retry {
		delete(l.CancelledRuns, runID)
	} else {
		l.Cancelled = false
	}
	delete(l.FinishedRuns, runID)
	return ManagedDigest(l)
}

// MatchesLiveLedger is the common check before sealing, admitting or recovering
// a live original obligation. Cancellation closure uses its separate terminal predicate.
func (o ManagedAgentObligations) MatchesLiveLedger(l budget.Ledger, host ManagedHostBinding) bool {
	return l.ID == o.LedgerID && l.Revision >= o.LedgerRevision && QuiescentManagedLedger(l, host) &&
		ManagedLedgerDigest(l, host.RunID) == o.LedgerDigest && !l.RunCancelled(host.RunID) && !l.FinishedRuns[host.RunID]
}

// managedRecoveryLedger verifies the original quiescent accounting source.
func managedRecoveryLedger(ctx context.Context, t ManagedTransfer, store budget.LedgerStore) (budget.Ledger, error) {
	p, ok := t.SafePoint.AgentObligations()
	if !ok || p.LedgerID != t.SafePoint.Source.Run.BudgetScopeID {
		return budget.Ledger{}, ErrInvalidInput
	}
	if p.LedgerID == "" {
		return budget.Ledger{}, nil
	}
	l, err := LoadRunBudget(ctx, store, t.SafePoint.Source.Run)
	if err != nil {
		return l, err
	}
	if !p.MatchesLiveLedger(l, t.SafePoint.Host) {
		return l, budget.ErrLedgerConflict
	}
	return l, nil
}

// ValidateManagedBudgetClosure verifies native accounting before issuing a
// closure proof to the Host projection.
func ValidateManagedBudgetClosure(point ManagedSafePoint, terminal Snapshot, mode ManagedRecoveryMode, l budget.Ledger) error {
	p, ok := point.AgentObligations()
	if !ok {
		return ErrInvalidInput
	}
	if p.LedgerID == "" {
		return nil
	}
	if l.ID != point.Host.TurnID || l.ID != p.LedgerID || l.Revision < p.LedgerRevision {
		return budget.ErrLedgerConflict
	}
	runID := point.Host.RunID
	if mode == ManagedCancel {
		if terminal.Run.Status != RunStatusCancelled || !l.RunCancelled(runID) || !l.FinishedRuns[runID] ||
			!QuiescentManagedLedger(l, point.Host) || ManagedLedgerDigest(l, runID) != p.LedgerDigest {
			return budget.ErrLedgerConflict
		}
	} else if terminal.Run.Status == RunStatusCompleted || terminal.Run.Status == RunStatusFailed || terminal.Run.Status == RunStatusCancelled {
		if !l.FinishedRuns[runID] {
			return budget.ErrLedgerConflict
		}
	}
	return nil
}

// PlanManagedCancellation consumes no facts itself. The ordinary control
// transaction qualifies its predecessor, verifies the original ledger and
// commits the returned native mutation, retirement and selection together.
func PlanManagedCancellation(ctx context.Context, t ManagedTransfer, p ManagedAdmissionProof, s Snapshot, l ExecutionLease, g ChildExecutionGrant, a ManagedAuthority, physicalEpoch uint64, now time.Time, store budget.LedgerStore) (ManagedTransfer, StoreMutation, error) {
	t, e := qualifyManagedSource(ctx, t, p, s, l, g, a, physicalEpoch, now, store)
	if e != nil {
		return ManagedTransfer{}, StoreMutation{}, e
	}
	return managedCancellationMutation(t, p, s, now)
}

// managedCancellationMutation is the qualified native cancellation reducer.
// It preserves feature state and prior receipts exactly and performs the native
// Cancel transformation, not arbitrary caller-provided ApplyControl content.
func managedCancellationMutation(t ManagedTransfer, p ManagedAdmissionProof, s Snapshot, now time.Time) (ManagedTransfer, StoreMutation, error) {
	if p.transfer.SafePoint.Host.RunID != s.Run.ID || p.transfer.SafePoint.ID != t.SafePoint.ID || !ManagedSourceMatches(t.SafePoint, s) || !p.Matches(t, ManagedCancel) {
		return ManagedTransfer{}, StoreMutation{}, ErrExecutionConflict
	}
	m := storeMutationAt(s, cancelMutation(s, p.Authorization().CancellationReason), now.UTC())
	t.Revision = m.Record.Run.Revision
	t.Selection = p.Authorization()
	// The actual committed Snapshot, not a speculative mutation result,
	// is attached by the control transaction after its CAS.

	return t, m, nil
}

// ValidateManagedRecoverySource is a pure reducer; caller holds the Run/lease/grant
// transaction. Store-time expiry and identity are checked together, not by a
// preliminary read. No durable PARKED state is produced.
func ValidateManagedRecoverySource(t ManagedTransfer, q ManagedRecoverySourceRequest, s Snapshot, l ExecutionLease, g ChildExecutionGrant, a ManagedAuthority, physicalEpoch uint64, now time.Time) (ManagedTransfer, error) {
	if q.RunID != s.Run.ID || q.SuspensionID != t.SafePoint.ID || q.ExpectedRevision != s.Run.Revision || !t.Valid() || t.Terminal != nil || (q.ExpectedTransferDigest != "" && q.ExpectedTransferDigest != ManagedDigest(t)) || a != t.SafePoint.Host.KernelAuthority {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	if q.Qualification.proof != nil {
		if !q.Qualification.matches(t, s) {
			return ManagedTransfer{}, ErrExecutionConflict
		}
		// A stale source stays historical; only its exact requalification
		// can be admitted at the new Run revision.
		t.SafePoint = managedClone(q.Qualification.point)
		t.Revision = s.Run.Revision
	}
	if !ManagedSourceMatches(t.SafePoint, s) {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	// The original safe point proves its ordinary callback crossed entry.
	// A successor may carry that same frontier only while still unentered.
	entered := physicalEpoch == g.ExecutionEpoch
	if physicalEpoch > g.ExecutionEpoch || entered == (g.AdmissionOperation == "managed_resume") {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	if g != ManagedCurrentGrant(t) || !g.MatchesExecution(l) || l.ExpiresAt.IsZero() || now.Before(l.ExpiresAt) {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	return t, nil
}

// ManagedInitialTransfer records the original immutable qualification.
// Its freshness is the Run revision and its recovery owner is the Grant.
func ManagedInitialTransfer(p ManagedSafePoint) ManagedTransfer {
	return ManagedTransfer{SafePoint: managedClone(p), Revision: p.Source.Run.Revision}
}

// ManagedClosureProof is an observation of the exact native result.
// Its single private transfer already includes the verified terminal snapshot.
type ManagedClosureProof struct{ transfer ManagedTransfer }

func (p ManagedClosureProof) Transfer() ManagedTransfer { return managedClone(p.transfer) }
func (p ManagedClosureProof) Snapshot() Snapshot {
	if p.transfer.Terminal == nil {
		return Snapshot{}
	}
	return managedClone(*p.transfer.Terminal)
}
func (p ManagedClosureProof) Resolved() bool { return p.transfer.Resolved() }
func (p ManagedClosureProof) Matches(r ManagedRecoveryAuthorization) bool {
	return p.Resolved() && p.transfer.Selection == r
}

func ManagedCancelReplay(t ManagedTransfer, p ManagedAdmissionProof, s Snapshot) bool {
	original := p.transfer
	return p.Matches(original, ManagedCancel) && t.Resolved() && t.Selection.Mode == ManagedCancel &&
		t.SafePoint.ID == original.SafePoint.ID && s.Run.ID == original.SafePoint.Host.RunID && ManagedDigest(t.SafePoint) == ManagedDigest(original.SafePoint) &&
		t.Terminal != nil && s.Run.Revision == original.SafePoint.Source.Run.Revision+1 &&
		ManagedDigest(*t.Terminal) == ManagedDigest(s) && t.Selection == p.reservation
}
