package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

// Outcome is sealed together with the exact callback-return snapshot. It is
// not authority on its own and cannot be inferred from terminal/clean state.
// OwnedCallbackCustody describes the contract of the ACTUAL executing callback,
// not the composition of a later observer. Empty legacy/opaque custody never
// authorizes deferred control, even when the synchronous callback returned.
type OwnedCallbackCustody string

const (
	OwnedCustodyPrepared OwnedCallbackCustody = "prepared-v1"
	OwnedCustodyScoped   OwnedCallbackCustody = "scoped-v1"
	// OwnedCustodyNotEntered is original no-entry evidence, not Host completion
	// or control/Tool custody. It only permits retirement of that admission.
	OwnedCustodyNotEntered OwnedCallbackCustody = "not-entered-v1"
	// OwnedToolResultReturn binds committed Tool results for observation only.
	OwnedToolResultReturn      OwnedCallbackCustody = "tool-result-v1"
	OwnedBoundedApprovalReturn OwnedCallbackCustody = "bounded-approval-v1"
)

func (c OwnedCallbackCustody) Valid() bool {
	return c == "" || c == OwnedCustodyNotEntered || c.permitsControl() || c == OwnedToolResultReturn || c == OwnedBoundedApprovalReturn
}

func (c OwnedCallbackCustody) permitsControl() bool {
	return c == OwnedCustodyPrepared || c == OwnedCustodyScoped
}

type PhysicalCallbackOutcome struct {
	// ToolReturns identifies the outstanding Call obligations of this
	// original callback independently of its control/approval custody.
	ToolReturns    OwnedToolReturns `json:",omitempty"`
	HostCompleted  bool
	LeaseOwned     bool
	ControlCustody OwnedCallbackCustody `json:",omitempty"`
}
type PhysicalCallbackCompletion struct {
	GrantID          string
	ReturnRevision   uint64
	ReturnSourceHash string
	HostCompleted    bool
	LeaseOwned       bool
	ControlCustody   OwnedCallbackCustody `json:",omitempty"`
	ToolReturns      OwnedToolReturns     `json:",omitempty"`
}

// PhysicalCallbackReturnPayload is a pure canonical proof payload.
// Only the first-party callback boundary may seal it after the
// original executing stages return. No Runtime state or execution permission is changed.
func PhysicalCallbackReturnPayload(s Snapshot, o PhysicalCallbackOutcome) json.RawMessage {
	data, err := json.Marshal(struct {
		Snapshot Snapshot
		Outcome  PhysicalCallbackOutcome
	}{s, o})
	if err != nil {
		return nil
	}
	return data
}

type PhysicalCallbackReturn struct{ proof *featurecontrol.Proof }

// OwnedToolReturn binds one outstanding Call to its original reservation
// and/or approval. ReceiptDigest identifies a settled ledger outcome;
// OwnerResultDigest identifies a qualified committed owner result from the
// original callback, independently of whether a shared ledger was selected.
type OwnedToolReturn struct {
	CallID            string
	RequestDigest     string
	LedgerID          string
	ReceiptDigest     string `json:",omitempty"`
	ApprovalDigest    string `json:",omitempty"`
	OwnerResultDigest string `json:",omitempty"`
	// PendingKind records an actual returned pending disposition, not an
	// inferred external outcome. Empty means no qualified pending return.
	PendingKind string `json:",omitempty"`
	// Unsent attests that the original native callback proved no physical
	// Tool dispatch: either no chain entry or an exact returned first-party
	// dispatch denial before Next. It is not a Tool result receipt.
	Unsent bool `json:",omitempty"`
}

// OwnedToolReturns is a canonical, comparable encoding of a bounded ordered
// collection. It travels with the original callback return and retirement,
// not a new execution authority or a second ledger of business results.
type OwnedToolReturns string

func validToolReceiptDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == value
}

func NewOwnedToolReturns(bindings []OwnedToolReturn) (OwnedToolReturns, error) {
	if len(bindings) == 0 {
		return "", nil
	}
	bindings = append([]OwnedToolReturn(nil), bindings...)
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].CallID < bindings[j].CallID })
	for i, b := range bindings {
		if strings.TrimSpace(b.CallID) == "" || b.CallID != strings.TrimSpace(b.CallID) ||
			b.LedgerID != strings.TrimSpace(b.LedgerID) || (b.LedgerID == "" && b.ApprovalDigest == "" && !b.Unsent) ||
			!validToolReceiptDigest(b.RequestDigest) ||
			(b.ReceiptDigest != "" && (b.LedgerID == "" || !validToolReceiptDigest(b.ReceiptDigest))) ||
			(b.ApprovalDigest != "" && !validToolReceiptDigest(b.ApprovalDigest)) ||
			(b.OwnerResultDigest != "" && (b.ApprovalDigest == "" || !validToolReceiptDigest(b.OwnerResultDigest))) ||
			(b.Unsent && (b.ReceiptDigest != "" || b.OwnerResultDigest != "" || b.PendingKind != "")) ||
			(b.PendingKind != "" && (b.ReceiptDigest != "" || b.OwnerResultDigest != "" || (b.PendingKind != "pending" && b.PendingKind != "scheduled" && b.PendingKind != "child_terminal" && b.PendingKind != "external_result"))) ||
			(i > 0 && bindings[i-1].CallID == b.CallID) {
			return "", ErrInvalidInput
		}
	}
	raw, err := json.Marshal(bindings)
	return OwnedToolReturns(raw), err
}

func (r OwnedToolReturns) Bindings() ([]OwnedToolReturn, error) {
	if r == "" {
		return nil, nil
	}
	var bindings []OwnedToolReturn
	if err := json.Unmarshal([]byte(r), &bindings); err != nil {
		return nil, err
	}
	canonical, err := NewOwnedToolReturns(bindings)
	if err != nil || canonical != r {
		return nil, ErrInvalidInput
	}
	return bindings, nil
}

func (r OwnedToolReturns) Valid() bool {
	_, err := r.Bindings()
	return err == nil
}

func (r OwnedToolReturns) Find(callID string) (OwnedToolReturn, bool, error) {
	bindings, err := r.Bindings()
	if err != nil {
		return OwnedToolReturn{}, false, err
	}
	for _, b := range bindings {
		if b.CallID == callID {
			return b, true, nil
		}
	}
	return OwnedToolReturn{}, false, nil
}

// MatchesReturnSnapshot verifies the immutable whole-return evidence at its
// original revision. Later recovery CASes retain the same Store-owned
// completion, with independently matched Call bindings.
func (c PhysicalCallbackCompletion) MatchesReturnSnapshot(s Snapshot) bool {
	if !c.HostCompleted || c.ReturnRevision != s.Run.Revision || !c.ToolReturns.Valid() || !c.ControlCustody.Valid() {
		return false
	}
	o := PhysicalCallbackOutcome{HostCompleted: c.HostCompleted, LeaseOwned: c.LeaseOwned, ControlCustody: c.ControlCustody, ToolReturns: c.ToolReturns}
	return ManagedDigest(PhysicalCallbackReturnPayload(s, o)) == c.ReturnSourceHash
}

// NotEntered identifies a distinct original no-callback fact. It must not
// authorize any receipt consumer that needs an actual Host return.
func (c PhysicalCallbackCompletion) NotEntered() bool {
	return c.ControlCustody == OwnedCustodyNotEntered
}

// MatchesNotEnteredSnapshot binds the retirement-only evidence to the exact
// original admission revision and unchanged source. Managed activations have
// their own transfer lifecycle and are not ordinary no-entry retirements.
func (c PhysicalCallbackCompletion) MatchesNotEnteredSnapshot(g ChildExecutionGrant, s Snapshot) bool {
	if !c.NotEntered() || ValidatePhysicalCompletion(g, c) != nil || s.Run.ID != g.ChildRunID ||
		s.Run.Kind != "agent" || s.Run.ParentRunID != "" || s.Run.Revision != g.ExpectedRevision {
		return false
	}
	o := PhysicalCallbackOutcome{LeaseOwned: c.LeaseOwned, ControlCustody: OwnedCustodyNotEntered}
	return ManagedDigest(PhysicalCallbackReturnPayload(s, o)) == c.ReturnSourceHash
}

func ordinaryOwnedOperation(op string) bool {
	switch op {
	case "start", "prepare", "resume", "approval":
		return true
	default:
		return false
	}
}

func (p PhysicalCallbackReturn) Matches(g ChildExecutionGrant, s Snapshot, o PhysicalCallbackOutcome) bool {
	if g.ID == "" || g.ChildRunID != s.Run.ID || s.Run.Kind != "agent" || s.Run.Revision < g.ExpectedRevision {
		return false
	}
	if o.ControlCustody == OwnedCustodyNotEntered {
		return g.IsOwned() && s.Run.ParentRunID == "" && ordinaryOwnedOperation(g.AdmissionOperation) && !o.HostCompleted && o.ToolReturns == "" &&
			s.Run.Revision == g.ExpectedRevision && p.proof.Matches(s.Run.ID, s.Run.Revision, "agent_owned_callback_not_entered", g.ID, PhysicalCallbackReturnPayload(s, o), nil)
	}
	// A borrowed operation still requires its exact complete return;
	// it has no lease ownership or deferred-control custody to transfer.
	if g.IsOwned() {
		if s.Run.ParentRunID != "" || !o.ControlCustody.Valid() || !o.ToolReturns.Valid() ||
			(o.ToolReturns != "" && (!o.HostCompleted || o.ControlCustody == OwnedCustodyPrepared)) ||
			(o.ControlCustody == OwnedCustodyPrepared && g.AdmissionOperation != "prepare") {
			return false
		}
	} else if s.Run.ParentRunID == "" || o != (PhysicalCallbackOutcome{HostCompleted: true}) {
		return false
	}
	return p.proof.Matches(s.Run.ID, s.Run.Revision, "agent_physical_callback_return", g.ID, PhysicalCallbackReturnPayload(s, o), nil)
}
func (p PhysicalCallbackReturn) Completion(g ChildExecutionGrant, s Snapshot, o PhysicalCallbackOutcome) PhysicalCallbackCompletion {
	if !p.Matches(g, s, o) {
		return PhysicalCallbackCompletion{}
	}
	hash := sha256.Sum256(PhysicalCallbackReturnPayload(s, o))
	return PhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: s.Run.Revision, ReturnSourceHash: hex.EncodeToString(hash[:]), HostCompleted: o.HostCompleted, LeaseOwned: o.LeaseOwned, ControlCustody: o.ControlCustody, ToolReturns: o.ToolReturns}
}
func (r *Runtime) RecordPhysicalCallbackReturn(ctx context.Context, g ChildExecutionGrant, revision uint64, o PhysicalCallbackOutcome, proof *featurecontrol.Proof) error {
	if r == nil || ctx == nil || !g.Identity().Valid() || g.ExpectedRevision == 0 || revision == 0 {
		return ErrInvalidInput
	}
	store, ok := r.store.(PhysicalExecutionStore)
	if !ok {
		return ErrExecutionRequired
	}
	return store.RecordPhysicalCallbackReturn(ctx, g, revision, o, PhysicalCallbackReturn{proof: proof})
}
func (r *Runtime) LookupPhysicalCallbackReturn(ctx context.Context, g ChildExecutionGrant) (PhysicalCallbackCompletion, error) {
	if r == nil || ctx == nil || !g.Identity().Valid() || g.ExpectedRevision == 0 {
		return PhysicalCallbackCompletion{}, ErrInvalidInput
	}
	store, ok := r.store.(PhysicalExecutionStore)
	if !ok {
		return PhysicalCallbackCompletion{}, ErrExecutionRequired
	}
	c, err := store.LookupPhysicalCallbackReturn(ctx, g)
	if err != nil {
		return PhysicalCallbackCompletion{}, err
	}
	if err = ValidatePhysicalCompletion(g, c); err != nil {
		return PhysicalCallbackCompletion{}, ErrExecutionConflict
	}
	return c, nil
}
