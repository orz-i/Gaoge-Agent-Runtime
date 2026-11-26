package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"

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

type ManagedRecoveryState string

// ManagedLifecycleWorkingStateSummary admits only the dedicated native Agent
// summary phases. Feature-owned validation is required before any PARK.
const ManagedLifecycleWorkingStateSummary = "working-state-summary-v3"

// ManagedLifecycleAgentObligations is the native combined proof profile.
// Older readers must not reinterpret it as a Context-only continuation.
const ManagedLifecycleAgentObligations = "managed-agent-obligations-v1"

const (
	ManagedAvailable   ManagedRecoveryState = "AVAILABLE"
	ManagedInvalidated ManagedRecoveryState = "INVALIDATED"
	ManagedParked      ManagedRecoveryState = "PARKED"
	ManagedEnabled     ManagedRecoveryState = "ENABLED"
	ManagedCancelled   ManagedRecoveryState = "CANCELLED"
	ManagedClosed      ManagedRecoveryState = "CLOSED"
)

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
	}{c.IntentID, c.Seq, payload, c.PayloadDigest, c.Reason})
}

func (c ManagedCancellation) Valid() bool {
	return c.IntentID != "" && c.Seq > 0 && json.Valid(c.Payload) && c.PayloadDigest == ManagedDigest(c.Payload)
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
	RequalifiedFrom      string `json:"requalifiedFrom,omitempty"`
	SettledReceiptDigest string `json:"settledReceiptDigest,omitempty"`
}

func (p ManagedSafePoint) AgentObligations() (ManagedAgentObligations, bool) {
	var o ManagedAgentObligations
	if p.LifecycleVersion != ManagedLifecycleAgentObligations || json.Unmarshal(p.LifecycleSource, &o) != nil ||
		o.SourceRevision != p.Source.Run.Revision || o.InvocationID != p.ModelInvocationID || o.RequestHash != p.ModelRequestHash ||
		p.Host.Attempt != 1 || (o.InputBinding == "" && o.InputCursor != 0) ||
		(o.LedgerID == "" && o.InputBinding == "") {
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
			o.BindingDigest == "" && o.RequalifiedFrom == "" && o.SettledReceiptDigest == "" &&
			(o.PreparationTransitionDigest == "" || validToolReceiptDigest(o.PreparationTransitionDigest))
	}
	if o.PreparationContext != nil || o.PreparationTransitionDigest != "" || o.InvocationID == "" || len(o.RequestHash) != 64 || !validToolReceiptDigest(o.SamplingDigest) {
		return o, false
	}
	if o.SettledReceiptDigest != "" && (o.RequalifiedFrom == "" || o.Phase != "pending" || o.LedgerID == "" || !validToolReceiptDigest(o.SettledReceiptDigest)) {
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
	modelIdentity := p.ModelInvocationID != "" && len(p.ModelRequestHash) == 64
	if p.LifecycleVersion == ManagedLifecycleAgentObligations {
		o, ok := p.AgentObligations()
		if !ok {
			return false
		}
		modelIdentity = o.Phase == "prepare" || modelIdentity
	}
	return p.ID != "" && p.RecoveryID == ManagedRecoveryID(p.ID, p.Host) && p.Host.Valid() && p.Source.Run.ID == p.Host.RunID && p.Source.Run.Kind == "agent" && p.Source.Run.ParentRunID == "" && p.Source.Run.Status == RunStatusRunning && p.Source.Run.Revision > 0 && json.Valid(p.Source.State) && p.Host.MatchesOriginalGrant(p.OriginalGrant) && p.OriginalGrant.MatchesExecution(p.OriginalLease) && modelIdentity && (p.LifecycleVersion == "managed-context-v1" || p.LifecycleVersion == ManagedLifecycleWorkingStateSummary || p.LifecycleVersion == ManagedLifecycleAgentObligations) && json.Valid(p.LifecycleSource)
}

// ManagedTransfer retains one immutable original recovery identity. Selection
// is initially empty; its first nonempty mode is immutable across generations.
// PARKED has no executable owner. ENABLED grants only its winning claimant.
type ManagedActivation struct {
	Generation uint64
	ClaimantID string
	Grant      ChildExecutionGrant
	Lease      ExecutionLease
	Selection  ManagedHostReservation
}

type ManagedTransfer struct {
	SafePoint      ManagedSafePoint
	State          ManagedRecoveryState
	Revision       uint64
	Generation     uint64
	Selection      ManagedHostReservation
	ClaimantID     string
	SuccessorGrant ChildExecutionGrant
	SuccessorLease ExecutionLease
	Activations    []ManagedActivation
	Terminal       *Snapshot
}

type ManagedHostReservation struct {
	ID           string
	RecoveryID   string
	SuspensionID string
	Binding      ManagedHostBinding
	Mode         ManagedRecoveryMode
	Cancellation ManagedCancellation
	Revision     uint64
}

func (r ManagedHostReservation) Valid() bool {
	if r.ID == "" || r.RecoveryID == "" || r.SuspensionID == "" || !r.Binding.Valid() || r.Revision == 0 {
		return false
	}
	switch r.Mode {
	case ManagedResume:
		return reflect.DeepEqual(r.Cancellation, ManagedCancellation{})
	case ManagedCancel:
		return r.Cancellation.Valid()
	default:
		return false
	}
}

// ManagedRecoveryHostStore is a STORAGE authority port wired by the concrete
// first-party Harness bridge. It receives no Next/callback and cannot certify
// lifecycle completion. Stores are trusted persistence, as Kernel.Store is.
// Reserve takes the original Turn lock; it chooses exactly RESUME or CANCEL
// from real records. Identical replay returns the same immutable selection.
// A RESERVED selection cannot be revoked while its Kernel transfer may run.
type ManagedRecoveryHostStore interface {
	ManagedRecoveryAuthority(context.Context) (ManagedAuthority, error)
	ReadManagedOriginalDispatch(context.Context, ManagedHostBinding) (ManagedHostBinding, error)
	ReserveManagedRecovery(context.Context, ManagedParkedProof) (ManagedHostReservation, error)
	ReadManagedRecovery(context.Context, ManagedHostBinding, string) (ManagedHostReservation, error)
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
	if point.LifecycleVersion == "managed-context-v1" {
		return events[0].Type == "agent.model_invocation.pending"
	}
	var phase string
	switch point.LifecycleVersion {
	case ManagedLifecycleAgentObligations:
		o, ok := point.AgentObligations()
		if !ok {
			return false
		}
		if o.RequalifiedFrom != "" {
			return false
		}
		if o.Phase == "prepare" {
			if o.PreparationTransitionDigest == "" {
				// The preceding prepare profile admitted only these two events.
				// Do not use a historical proof for a newly admitted transition.
				return events[0].Type == "agent.native_preparation.ready" || events[0].Type == "agent.input_applied"
			}
			return ManagedPreparationTransition(events) && o.PreparationTransitionDigest == ManagedDigest(events)
		}
		if o.Purpose == "" && o.Phase == "consumed" {
			return events[0].Type == "agent.input_applied"
		}
		phase = o.Phase
	case ManagedLifecycleWorkingStateSummary:
		var source struct {
			Phase          string `json:"phase"`
			SourceRevision uint64 `json:"sourceRevision"`
			InvocationID   string `json:"invocationID"`
			RequestHash    string `json:"requestHash"`
			BindingDigest  string `json:"bindingDigest"`
		}
		if json.Unmarshal(point.LifecycleSource, &source) != nil || source.SourceRevision != point.Source.Run.Revision ||
			source.InvocationID != point.ModelInvocationID || source.RequestHash != point.ModelRequestHash || !validToolReceiptDigest(source.BindingDigest) {
			return false
		}
		phase = source.Phase
	default:
		return false
	}
	switch phase {
	case "pending":
		return events[0].Type == "agent.model_invocation.pending"
	case "completed":
		return events[0].Type == "agent.model_invocation.completed"
	case "consumed":
		return events[0].Type == "agent.context_summary.consumed"
	}
	return false
}

// ManagedParkQualification is an internal Agent proof of a new safe frontier
// after the original point was left. It replaces no history and grants no
// callback-return or dispatch fact. Store consumes it inside the same PARK CAS.
type ManagedParkQualification struct {
	prior ManagedTransfer
	point ManagedSafePoint
	proof *featurecontrol.Proof
}

func (q ManagedParkQualification) matches(t ManagedTransfer, s Snapshot) bool {
	o, ok := q.point.AgentObligations()
	return q.proof != nil && ok && t.SafePoint.LifecycleVersion == ManagedLifecycleAgentObligations &&
		t.State == ManagedInvalidated && t.Selection.ID == "" && len(t.Activations) == 0 &&
		ManagedDigest(q.prior) == ManagedDigest(t) && o.RequalifiedFrom == t.SafePoint.ID &&
		q.point.Valid() && q.point.Host.RunID == s.Run.ID && reflect.DeepEqual(q.point.Host, t.SafePoint.Host) &&
		q.point.OriginalGrant == t.SafePoint.OriginalGrant && sameManagedLease(q.point.OriginalLease, t.SafePoint.OriginalLease) &&
		q.point.ModelInvocationID == t.SafePoint.ModelInvocationID && q.point.ModelRequestHash == t.SafePoint.ModelRequestHash &&
		ManagedSourceMatches(q.point, s) &&
		q.proof.Matches(s.Run.ID, s.Run.Revision, "managed_qualified_park", t.SafePoint.ID, s.State, q.point)
}

type ManagedParkRequest struct {
	RunID, SuspensionID string
	ExpectedRevision    uint64
	Qualification       ManagedParkQualification
}
type ManagedLeaveRequest struct {
	RunID, SuspensionID                                    string
	ExpectedRevision, ExpectedTransferRevision, Generation uint64
	Lease                                                  ExecutionLease
}

// Caller must stop on any Leave error, including an ambiguous committed ACK.
// No configurable callback may run before Leave returns successfully.
type ManagedEnableRequest struct {
	RunID, SuspensionID, ClaimantID, OwnerID, LeaseID, GrantID string
	ExpectedTransferRevision, Generation                       uint64
	LeaseDuration                                              time.Duration
	Selection                                                  ManagedSelectionProof
}
type ManagedCancelRequest struct {
	RunID, SuspensionID                  string
	ExpectedTransferRevision, Generation uint64
	Selection                            ManagedSelectionProof
}

// ManagedRecoveryStore operations use the existing Run/native-lease/grant
// transaction (Memory mutex, SQLite write transaction, PG row locks).
// All ordinary Apply/ApplyExecution/Control/Acquire/Renew/Check paths reject
// PARKED, and cannot substitute an expired native token for recovery authority.
// T0 is carried by StoreMutation.ManagedSafePoint, not a separate commit.
// Any subsequent ordinary mutation invalidates AVAILABLE atomically.
type ManagedRecoveryStore interface {
	ManagedRecoveryAuthority(context.Context) (ManagedAuthority, error)
	LoadManagedRecovery(context.Context, string) (ManagedTransfer, error)
	LoadManagedRecoveryByID(context.Context, string) (ManagedTransfer, error)
	CompleteManagedRecovery(context.Context, ChildExecutionGrant, Snapshot, ManagedCompletionProof) error
	ParkManagedRecovery(context.Context, ManagedParkRequest) (ManagedTransfer, error)
	LeaveManagedSafePoint(context.Context, ManagedLeaveRequest) (ManagedTransfer, error)
	EnableManagedRecovery(context.Context, ManagedEnableRequest) (ManagedTransfer, error)
	CancelParkedManagedContinuation(context.Context, ManagedCancelRequest) (ManagedTransfer, error)
}

// ManagedParkedProof can only be reconstructed by Runtime reading its actual
// configured Store. Host compares this binding against its own original rows.
type ManagedParkedProof struct {
	transfer  ManagedTransfer
	authority ManagedAuthority
	valid     bool
}

func (p ManagedParkedProof) Transfer() ManagedTransfer { return managedClone(p.transfer) }
func (p ManagedParkedProof) Matches(binding ManagedHostBinding) bool {
	return p.valid && p.authority == binding.KernelAuthority && p.transfer.State == ManagedParked && p.transfer.SafePoint.Valid() && reflect.DeepEqual(p.transfer.SafePoint.Host, binding)
}

// ManagedSelectionProof is minted only after Runtime's configured Host port
// reads the committed reservation; it is not deserializable caller authority.
type ManagedSelectionProof struct {
	reservation ManagedHostReservation
	transfer    ManagedTransfer
	valid       bool
}

func (p ManagedSelectionProof) Reservation() ManagedHostReservation {
	return managedClone(p.reservation)
}
func (p ManagedSelectionProof) Matches(t ManagedTransfer, mode ManagedRecoveryMode) bool {
	r := p.reservation
	if !p.valid || !r.Valid() || r.Mode != mode || t.State != ManagedParked || t.Revision != p.transfer.Revision || t.Generation != p.transfer.Generation || !reflect.DeepEqual(t.SafePoint, p.transfer.SafePoint) || r.RecoveryID != t.SafePoint.RecoveryID || r.SuspensionID != t.SafePoint.ID || !reflect.DeepEqual(r.Binding, t.SafePoint.Host) {
		return false
	}
	return t.Selection.ID == "" || reflect.DeepEqual(t.Selection, r)
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

// ManagedCurrentAuthority selects the only active native identity. Original
// authority remains immutable historical evidence after a successor is enabled.
func ManagedCurrentAuthority(t ManagedTransfer) (ChildExecutionGrant, ExecutionLease) {
	if t.SuccessorGrant.ID != "" {
		return t.SuccessorGrant, t.SuccessorLease
	}
	return t.SafePoint.OriginalGrant, t.SafePoint.OriginalLease
}

// ManagedSourceMatches compares the full persisted aggregate including EventHead.
func ManagedSourceMatches(p ManagedSafePoint, s Snapshot) bool {
	return ManagedDigest(p.Source) == ManagedDigest(s)
}

// ManagedEnabledTransfer is the shared exact T3 builder. Store supplies current
// authoritative native epoch and time INSIDE its lock/transaction. Persist its
// lease and active child-grant index together with the returned transfer.
func ManagedEnabledTransfer(t ManagedTransfer, q ManagedEnableRequest, s Snapshot, epoch uint64, now time.Time) (ManagedTransfer, error) {
	if q.RunID != s.Run.ID || q.SuspensionID != t.SafePoint.ID || q.ExpectedTransferRevision != t.Revision || q.Generation != t.Generation || q.ClaimantID == "" || q.OwnerID == "" || q.LeaseID == "" || !(GrantIdentity{GrantID: q.GrantID, RunID: q.RunID}).Valid() || !validExecutionDuration(q.LeaseDuration) || epoch == ^uint64(0) || !ManagedSourceMatches(t.SafePoint, s) || !q.Selection.Matches(t, ManagedResume) {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	lease := ExecutionLease{RunID: q.RunID, OwnerID: q.OwnerID, LeaseID: q.LeaseID, Epoch: epoch + 1, ExpiresAt: now.UTC().Add(q.LeaseDuration)}
	grant := ChildExecutionGrant{ID: q.GrantID, ChildRunID: q.RunID, ExpectedRevision: s.Run.Revision, ExecutionOwnerID: lease.OwnerID, ExecutionLeaseID: lease.LeaseID, ExecutionEpoch: lease.Epoch, AdmissionOperation: "managed_resume"}
	grant.AdmissionSourceHash = ownedAdmissionHash(s, lease, grant.AdmissionOperation)
	t.State = ManagedEnabled
	t.Revision++
	t.Selection = q.Selection.Reservation()
	t.ClaimantID = q.ClaimantID
	t.SuccessorLease = lease
	t.SuccessorGrant = grant
	t.Activations = append(t.Activations, ManagedActivation{Generation: t.Generation, ClaimantID: q.ClaimantID, Grant: grant, Lease: lease, Selection: t.Selection})
	return t, nil
}

// ManagedCancellationMutation is the ONLY allowed PARKED terminal mutation.
// It preserves feature state and prior receipts exactly and performs the native
// Cancel transformation, not arbitrary caller-provided ApplyControl content.
func ManagedCancellationMutation(t ManagedTransfer, q ManagedCancelRequest, s Snapshot, now time.Time) (ManagedTransfer, StoreMutation, error) {
	if q.RunID != s.Run.ID || q.SuspensionID != t.SafePoint.ID || q.ExpectedTransferRevision != t.Revision || q.Generation != t.Generation || !ManagedSourceMatches(t.SafePoint, s) || !q.Selection.Matches(t, ManagedCancel) {
		return ManagedTransfer{}, StoreMutation{}, ErrExecutionConflict
	}
	if o, ok := t.SafePoint.AgentObligations(); ok && o.SettledReceiptDigest != "" {
		return ManagedTransfer{}, StoreMutation{}, ErrExecutionConflict
	}
	r := s.Run
	r.Status = RunStatusCancelled
	r.Revision++
	r.ErrorCode = "run.cancelled"
	r.ErrorDetail = q.Selection.Reservation().Cancellation.Reason
	r.UpdatedAt = now.UTC()
	ended := now.UTC()
	r.EndedAt = &ended
	m := StoreMutation{Record: Record{Run: r, State: cloneJSON(s.State)}, Events: []EventDraft{{Type: "run.cancelled", Message: r.ErrorDetail}}}
	t.State = ManagedCancelled
	t.Revision++
	t.Selection = q.Selection.Reservation()
	terminal := Snapshot{Run: r, State: cloneJSON(s.State), EventHead: s.EventHead + 1}
	t.Terminal = &terminal
	return t, m, nil
}

// ManagedParkTransfer is a pure reducer; caller holds the Run/lease/grant
// transaction. Store-time expiry and identity are checked together, not by a
// preliminary read. Existing PARKED is an idempotent read, not a second fence.
func ManagedParkTransfer(t ManagedTransfer, q ManagedParkRequest, s Snapshot, l ExecutionLease, g ChildExecutionGrant, a ManagedAuthority, now time.Time) (ManagedTransfer, error) {
	if q.RunID != s.Run.ID || q.SuspensionID != t.SafePoint.ID || q.ExpectedRevision != s.Run.Revision || !t.SafePoint.Valid() || a != t.SafePoint.Host.KernelAuthority {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	if q.Qualification.proof != nil {
		if !q.Qualification.matches(t, s) {
			return ManagedTransfer{}, ErrExecutionConflict
		}
		// Only the freshly qualified current source may be parked. The prior
		// historical point remains INVALIDATED in every Store.
		t.SafePoint = managedClone(q.Qualification.point)
		t.State = ManagedAvailable
	}
	if !ManagedSourceMatches(t.SafePoint, s) {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	if t.State == ManagedParked {
		return t, nil
	}
	if t.State != ManagedAvailable && t.State != ManagedEnabled {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	wantG, wantL := ManagedCurrentAuthority(t)
	if g != wantG || !sameManagedLease(l, wantL) || !g.MatchesExecution(l) || l.ExpiresAt.IsZero() || now.Before(l.ExpiresAt) {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	t.State = ManagedParked
	t.Revision++
	t.Generation++
	return t, nil
}
func ManagedLeaveTransfer(t ManagedTransfer, q ManagedLeaveRequest, s Snapshot, l ExecutionLease, now time.Time) (ManagedTransfer, error) {
	if q.RunID != s.Run.ID || q.SuspensionID != t.SafePoint.ID || q.ExpectedRevision != s.Run.Revision || q.ExpectedTransferRevision != t.Revision || q.Generation != t.Generation || !ManagedSourceMatches(t.SafePoint, s) || (t.State != ManagedAvailable && t.State != ManagedEnabled) {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	_, expected := ManagedCurrentAuthority(t)
	if !sameManagedLease(expected, l) || !sameManagedLease(q.Lease, l) || !now.Before(l.ExpiresAt) {
		return ManagedTransfer{}, ErrExecutionConflict
	}
	t.State = ManagedInvalidated
	t.Revision++
	return t, nil
}

// ManagedOrdinaryGate is mandatory before ordinary owner/control mutations.
// AVAILABLE renewal remains legal and competes atomically with PARK.
// Source mutations invalidate AVAILABLE/ENABLED in the same transaction.
// Read-only loads and exact recovery reducers do not call this helper.
func ManagedOrdinaryGate(t ManagedTransfer) error {
	switch t.State {
	case ManagedParked:
		return ErrExecutionConflict
	case ManagedAvailable, ManagedInvalidated, ManagedEnabled, ManagedCancelled, ManagedClosed:
		return nil
	}
	return nil
}

// ManagedInitialTransfer stores T0 metadata only after proof.Matches passed in
// the same transaction. A new safe point must not destroy historical lineage;
// keep prior transfers indexed by suspension ID, plus a current Run pointer.
func ManagedInitialTransfer(p ManagedSafePoint) ManagedTransfer {
	return ManagedTransfer{SafePoint: managedClone(p), State: ManagedAvailable, Revision: 1, Generation: 1}
}

// ManagedInvalidateMutation must be persisted with a successful ordinary Run
// mutation; rejected mutations leave it unchanged. It never grants dispatch.
func ManagedInvalidateMutation(t ManagedTransfer) ManagedTransfer {
	if t.State == ManagedAvailable || t.State == ManagedEnabled {
		t.State = ManagedInvalidated
		t.Revision++
	}
	return t
}

// ManagedCompletionProof records an actual returned, settled successor. It is
// issued by the Root owner only after real lifecycle completion, never PARK.
type ManagedCompletionProof struct{ proof *featurecontrol.Proof }

func (p ManagedCompletionProof) Matches(g ChildExecutionGrant, s Snapshot) bool {
	return p.proof.Matches(s.Run.ID, s.Run.Revision, "managed_successor_completed", g.ID, s.State, s)
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
func (p ManagedClosureProof) Matches(r ManagedHostReservation) bool {
	return p.valid && r.Valid() && reflect.DeepEqual(p.transfer.Selection, r) && (p.transfer.State == ManagedClosed || p.transfer.State == ManagedCancelled) && p.transfer.Terminal != nil && ManagedDigest(*p.transfer.Terminal) == ManagedDigest(p.snapshot)
}

func ManagedEnableReplay(t ManagedTransfer, q ManagedEnableRequest, s Snapshot, l ExecutionLease, now time.Time) bool {
	original := q.Selection.transfer
	return q.Selection.Matches(original, ManagedResume) && t.State == ManagedEnabled && t.SafePoint.ID == q.SuspensionID && t.SafePoint.Host.RunID == q.RunID && t.Revision == q.ExpectedTransferRevision+1 && t.Generation == q.Generation && t.ClaimantID == q.ClaimantID && t.SuccessorLease.OwnerID == q.OwnerID && t.SuccessorLease.LeaseID == q.LeaseID && t.SuccessorGrant.ID == q.GrantID && sameManagedLease(l, t.SuccessorLease) && now.Before(l.ExpiresAt) && ManagedSourceMatches(t.SafePoint, s) && reflect.DeepEqual(t.Selection, q.Selection.reservation)
}
func ManagedCancelReplay(t ManagedTransfer, q ManagedCancelRequest, s Snapshot) bool {
	original := q.Selection.transfer
	return q.Selection.Matches(original, ManagedCancel) && t.State == ManagedCancelled && t.SafePoint.ID == q.SuspensionID && t.SafePoint.Host.RunID == q.RunID && t.Revision == q.ExpectedTransferRevision+1 && t.Generation == q.Generation && t.Terminal != nil && ManagedDigest(*t.Terminal) == ManagedDigest(s) && reflect.DeepEqual(t.Selection, q.Selection.reservation)
}
