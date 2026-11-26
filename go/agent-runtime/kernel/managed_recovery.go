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

// ManagedDispatchOperation maps the three independently admitted native
// root dispatches to their existing Kernel operation. Retry generations,
// descendant controls and external handoffs have distinct authorities.
func ManagedDispatchOperation(phase string) (string, bool) {
	switch phase {
	case "start", "resume":
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
	return ok && approvalValid && b.KernelAuthority.Valid() && b.HostAuthority.Valid() && b.TurnID != "" && b.InvocationID != "" && b.Attempt > 0 && b.RunID != "" && b.DispatchID != "" && json.Valid(b.Source)
}

// MatchesOriginalGrant prevents a later same-Run dispatch from borrowing
// the old recovery identity. Both operation and deterministic original
// dispatch Grant ID are required, in addition to the native bearer proof.
func (b ManagedHostBinding) MatchesOriginalGrant(g ChildExecutionGrant) bool {
	op, ok := ManagedDispatchOperation(b.Phase)
	if !ok || g.ChildRunID != b.RunID || g.AdmissionOperation != op || !g.IsOwned() {
		return false
	}
	id, err := OwnedPhysicalDispatchGrantID(b.RunID, op, b.DispatchID)
	return err == nil && id == g.ID
}

// ManagedCancellation is the entire ORIGINAL hcancel identity. Payload is
// retained as well as its digest so persistence/reconstruction cannot discard
// a consequential field. RESUME requires the zero value.
type ManagedCancellation struct {
	IntentID      string
	Seq           uint64
	Payload       json.RawMessage `json:",omitempty"`
	PayloadDigest string
	Reason        string
	Native        bool `json:",omitempty"`
}

// MarshalJSON omits ONLY nil payload. A nonnil empty RawMessage is malformed,
// not an absent cancellation field, and must fail encoding rather than become
// valid RESUME authority after a persistent round trip.
func (c ManagedCancellation) MarshalJSON() ([]byte, error) {
	var payload *json.RawMessage
	if c.Payload != nil {
		value := c.Payload
		payload = &value
	}
	return json.Marshal(struct {
		IntentID      string
		Seq           uint64
		Payload       *json.RawMessage `json:",omitempty"`
		PayloadDigest string
		Reason        string
		Native        bool `json:",omitempty"`
	}{c.IntentID, c.Seq, payload, c.PayloadDigest, c.Reason, c.Native})
}

// Direct represents a cancellation authorized by the native command itself,
// without electing or persisting a second Host cancellation operation.
func (c ManagedCancellation) Direct() bool {
	return c.Native && c.IntentID == "" && c.Seq == 0 && c.Payload == nil && c.PayloadDigest == ""
}

func (c ManagedCancellation) Valid() bool {
	return !c.Native && c.IntentID != "" && c.Seq > 0 && json.Valid(c.Payload) && c.PayloadDigest == ManagedDigest(c.Payload)
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
		p.Host.Attempt != 1 || (o.InputBinding == "" && o.InputCursor != 0) {
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

// ManagedTransfer retains the original recovery facts. The Run revision and
// native Grant/Lease/Epoch arbitrate admission. The old generation, claimant
// and activation fields (including the old wire State) are retained only
// for historical receipt identity. New records never advance a managed state.
type ManagedActivation struct {
	Generation uint64
	ClaimantID string
	Grant      ChildExecutionGrant
	Lease      ExecutionLease
	Selection  ManagedRecoveryAuthorization
}

type ManagedTransfer struct {
	SafePoint      ManagedSafePoint
	LegacyState    string `json:"State"`
	Revision       uint64
	Generation     uint64
	Selection      ManagedRecoveryAuthorization
	ClaimantID     string
	SuccessorGrant ChildExecutionGrant
	SuccessorLease ExecutionLease
	Activations    []ManagedActivation
	Terminal       *Snapshot
}

// Resolved is an observed native result, not a lifecycle state. Legacy
// completed wire records retain their exact original receipt digest.
func (t ManagedTransfer) Resolved() bool {
	if t.Terminal == nil || !t.Selection.matchesPoint(t.SafePoint) {
		return false
	}
	switch t.Selection.Mode {
	case ManagedResume:
		return t.LegacyState == "" || t.LegacyState == "CLOSED"
	case ManagedCancel:
		return t.LegacyState == "" || t.LegacyState == "CANCELLED"
	}
	return false
}

type ManagedRecoveryAuthorization struct {
	ID           string
	RecoveryID   string
	SuspensionID string
	Binding      ManagedHostBinding
	Mode         ManagedRecoveryMode
	Cancellation ManagedCancellation
	Revision     uint64
}

func (r ManagedRecoveryAuthorization) Valid() bool {
	// New commands carry only the mode/reason. The remaining fields are
	// retained solely to verify previously committed historical receipts.
	if r.ID == "" {
		if r.RecoveryID != "" || r.SuspensionID != "" || r.Revision != 0 || !reflect.DeepEqual(r.Binding, ManagedHostBinding{}) {
			return false
		}
	} else if r.RecoveryID == "" || r.SuspensionID == "" || !r.Binding.Valid() || r.Revision == 0 {
		return false
	}
	switch r.Mode {
	case ManagedResume:
		return reflect.DeepEqual(r.Cancellation, ManagedCancellation{})
	case ManagedCancel:
		return r.Cancellation.Direct() || (r.ID != "" && r.Cancellation.Valid())
	}
	return false
}

func (r ManagedRecoveryAuthorization) matchesPoint(p ManagedSafePoint) bool {
	return r.Valid() && (r.ID == "" || (r.RecoveryID == p.RecoveryID && r.SuspensionID == p.ID && reflect.DeepEqual(r.Binding, p.Host)))
}

// ManagedRecoveryHostStore is a STORAGE authority port wired by the concrete
// first-party Harness bridge. It receives no Next/callback and cannot certify
// lifecycle completion. Stores are trusted persistence, as Kernel.Store is.
// Authorization is read-only under the original Turn lock. The Kernel alone
// chooses the winning RESUME or CANCEL in its native commit.
type ManagedRecoveryHostStore interface {
	ManagedRecoveryAuthority(context.Context) (ManagedAuthority, error)
	VerifyManagedOriginalDispatch(context.Context, ManagedHostBinding, bool) error
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
		t.LegacyState == "" && t.Terminal == nil && t.Selection.Mode == "" && t.SuccessorGrant.ID == "" && s.Run.Revision > t.SafePoint.Source.Run.Revision &&
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
type ManagedCancelRequest struct {
	RunID, SuspensionID string
	Selection           ManagedAdmissionProof
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
	CommitManagedCancellation(context.Context, ManagedCancelRequest) (ManagedTransfer, error)
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
	return managedClone(p.reservation)
}
func (p ManagedAdmissionProof) Matches(t ManagedTransfer, mode ManagedRecoveryMode) bool {
	r := p.reservation
	if !p.valid || !r.Valid() || r.Mode != mode || t.LegacyState != "" || t.Terminal != nil || ManagedDigest(t) != ManagedDigest(p.transfer) || r.ID != "" {
		return false
	}
	return t.Selection.Mode == "" || reflect.DeepEqual(t.Selection, r)
}

// PrepareManagedAdmission rechecks the exact observed source, expiry, and
// old execution segment under the same native transaction that commits the
// successor or cancellation. The preliminary preparation was read-only.
func PrepareManagedAdmission(t ManagedTransfer, p ManagedAdmissionProof, s Snapshot, l ExecutionLease, g ChildExecutionGrant, a ManagedAuthority, physicalEpoch uint64, now time.Time) (ManagedTransfer, error) {
	if !p.valid || p.request.ExpectedTransferDigest == "" {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	next, err := ValidateManagedRecoverySource(t, p.request, s, l, g, a, physicalEpoch, now)
	if err != nil || !p.Matches(next, p.reservation.Mode) {
		return ManagedTransfer{}, ErrExecutionConflict
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
	return t.Selection.Mode == ManagedResume && t.Selection.matchesPoint(t.SafePoint)
}

// OwnedAdmission turns an already authorized recovery source into the same
// native admission permit as ordinary execution. The Lease token supplies
// the private attempt commitment; there is no separate claimant identity.
func (p ManagedAdmissionProof) OwnedAdmission(q ExecutionAcquireRequest, leaseID, grantID string) OwnedPhysicalAdmission {
	t := p.transfer
	if !p.Matches(t, ManagedResume) || q.RunID != t.SafePoint.Host.RunID || !validExecutionAcquireRequest(q) || strings.TrimSpace(q.OwnerID) != q.OwnerID || leaseID == "" || !(GrantIdentity{GrantID: grantID, RunID: q.RunID}).Valid() {
		return OwnedPhysicalAdmission{}
	}
	l := ExecutionLease{RunID: q.RunID, OwnerID: q.OwnerID, LeaseID: leaseID}
	g := ChildExecutionGrant{ID: grantID, ChildRunID: q.RunID, ExpectedRevision: t.SafePoint.Source.Run.Revision, ExecutionOwnerID: q.OwnerID, ExecutionLeaseID: leaseID, AdmissionOperation: "managed_resume"}
	return OwnedPhysicalAdmission{acquire: q, attempt: sha256.Sum256([]byte(leaseID)), candidate: OwnedPhysicalAdmissionResult{Grant: g, Lease: l}, recovery: p}
}

// ResolveManagedPhysicalAdmission qualifies the sole expired predecessor,
// then delegates the new owner to the ordinary native admission reducer.
// The returned recovery facts and native segment commit together.
func ResolveManagedPhysicalAdmission(ctx context.Context, t ManagedTransfer, p OwnedPhysicalAdmission, s Snapshot, l ExecutionLease, g ChildExecutionGrant, a ManagedAuthority, physicalEpoch uint64, now time.Time, ledger budget.LedgerStore) (OwnedPhysicalAdmissionResult, ManagedTransfer, error) {
	var zero OwnedPhysicalAdmissionResult
	t, err := PrepareManagedAdmission(t, p.recovery, s, l, g, a, physicalEpoch, now)
	if err != nil {
		return zero, ManagedTransfer{}, err
	}
	if _, err = managedRecoveryLedger(ctx, t, ledger); err != nil {
		return zero, ManagedTransfer{}, err
	}
	v, err := ResolveOwnedPhysicalAdmission(s, l, now, false, false, p)
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
func QuiescentManagedLedger(l budget.Ledger, runID string) bool {
	binding, ok := l.Runs[runID]
	if !ok || binding.ParentRunID != "" || l.ID == "" || l.Revision == 0 || len(l.RootRetries) != 0 {
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
	l.Cancelled = false
	delete(l.FinishedRuns, runID)
	return ManagedDigest(l)
}

// ManagedCancellationLedger plans the accounting part under the existing
// managed cancellation Store transaction. No callback or Host ledger writes.
func managedRecoveryLedger(ctx context.Context, t ManagedTransfer, store budget.LedgerStore) (budget.Ledger, error) {
	p, ok := t.SafePoint.AgentObligations()
	if !ok || p.LedgerID != t.SafePoint.Source.Run.BudgetScopeID {
		return budget.Ledger{}, ErrInvalidInput
	}
	if p.LedgerID == "" {
		return budget.Ledger{}, nil
	}
	runID := t.SafePoint.Host.RunID
	l, err := LoadRunBudget(ctx, store, t.SafePoint.Source.Run)
	if err != nil {
		return l, err
	}
	if l.ID != p.LedgerID || l.Revision < p.LedgerRevision || !QuiescentManagedLedger(l, runID) || ManagedLedgerDigest(l, runID) != p.LedgerDigest || l.RunCancelled(runID) || l.FinishedRuns[runID] {
		return l, budget.ErrLedgerConflict
	}
	return l, nil
}

func ManagedCancellationLedger(ctx context.Context, t ManagedTransfer, store budget.LedgerStore) (*budget.LedgerMutation, error) {
	l, err := managedRecoveryLedger(ctx, t, store)
	if err != nil || l.ID == "" {
		return nil, err
	}
	return budget.PlanRunCancellation(ctx, l, t.SafePoint.Host.RunID, "")
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
			!QuiescentManagedLedger(l, runID) || ManagedLedgerDigest(l, runID) != p.LedgerDigest {
			return budget.ErrLedgerConflict
		}
	} else if terminal.Run.Status == RunStatusCompleted || terminal.Run.Status == RunStatusFailed || terminal.Run.Status == RunStatusCancelled {
		if !l.FinishedRuns[runID] {
			return budget.ErrLedgerConflict
		}
	}
	return nil
}

// ManagedCancellationMutation is the qualified native cancellation reducer.
// It preserves feature state and prior receipts exactly and performs the native
// Cancel transformation, not arbitrary caller-provided ApplyControl content.
func ManagedCancellationMutation(t ManagedTransfer, q ManagedCancelRequest, s Snapshot, now time.Time) (ManagedTransfer, StoreMutation, error) {
	if q.RunID != s.Run.ID || q.SuspensionID != t.SafePoint.ID || !ManagedSourceMatches(t.SafePoint, s) || !q.Selection.Matches(t, ManagedCancel) {
		return ManagedTransfer{}, StoreMutation{}, ErrExecutionConflict
	}
	r := s.Run
	r.Status = RunStatusCancelled
	r.Revision++
	r.ErrorCode = "run.cancelled"
	r.ErrorDetail = q.Selection.Authorization().Cancellation.Reason
	r.UpdatedAt = now.UTC()
	ended := now.UTC()
	r.EndedAt = &ended
	m := StoreMutation{Record: Record{Run: r, State: cloneJSON(s.State)}, Events: []EventDraft{{Type: "run.cancelled", Message: r.ErrorDetail}}}
	t.Revision = r.Revision
	t.Selection = q.Selection.Authorization()
	terminal := Snapshot{Run: r, State: cloneJSON(s.State), EventHead: s.EventHead + 1}
	t.Terminal = &terminal
	return t, m, nil
}

// ValidateManagedRecoverySource is a pure reducer; caller holds the Run/lease/grant
// transaction. Store-time expiry and identity are checked together, not by a
// preliminary read. No durable PARKED state is produced.
func ValidateManagedRecoverySource(t ManagedTransfer, q ManagedRecoverySourceRequest, s Snapshot, l ExecutionLease, g ChildExecutionGrant, a ManagedAuthority, physicalEpoch uint64, now time.Time) (ManagedTransfer, error) {
	if q.RunID != s.Run.ID || q.SuspensionID != t.SafePoint.ID || q.ExpectedRevision != s.Run.Revision || t.LegacyState != "" || t.Terminal != nil || (q.ExpectedTransferDigest != "" && q.ExpectedTransferDigest != ManagedDigest(t)) || !t.SafePoint.Valid() || a != t.SafePoint.Host.KernelAuthority {
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
	// Ordinary predecessor admission is conservatively considered entered.
	// A recovered segment can be taken over only before native entry.
	if g.AdmissionOperation == "managed_resume" {
		if physicalEpoch >= g.ExecutionEpoch {
			return ManagedTransfer{}, ErrExecutionConflict
		}
	} else if physicalEpoch != g.ExecutionEpoch {
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

// ManagedClosureProof is reconstructed only by authoritative Kernel reads.
// Host may use it to close the SAME original dispatch/selection, no new effect.
type ManagedClosureProof struct {
	transfer ManagedTransfer
	snapshot Snapshot
	valid    bool
}

func (p ManagedClosureProof) Transfer() ManagedTransfer { return managedClone(p.transfer) }
func (p ManagedClosureProof) Snapshot() Snapshot        { return managedClone(p.snapshot) }
func (p ManagedClosureProof) Matches(r ManagedRecoveryAuthorization) bool {
	return p.valid && r.Valid() && reflect.DeepEqual(p.transfer.Selection, r) && p.transfer.Resolved() && ManagedDigest(*p.transfer.Terminal) == ManagedDigest(p.snapshot)
}

func ManagedCancelReplay(t ManagedTransfer, q ManagedCancelRequest, s Snapshot) bool {
	original := q.Selection.transfer
	return q.Selection.Matches(original, ManagedCancel) && t.Resolved() && t.Selection.Mode == ManagedCancel &&
		t.SafePoint.ID == q.SuspensionID && s.Run.ID == q.RunID && ManagedDigest(t.SafePoint) == ManagedDigest(original.SafePoint) &&
		t.Terminal != nil && s.Run.Revision == original.SafePoint.Source.Run.Revision+1 &&
		ManagedDigest(*t.Terminal) == ManagedDigest(s) && reflect.DeepEqual(t.Selection, q.Selection.reservation)
}
