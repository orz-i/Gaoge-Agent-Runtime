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
	// OwnedToolResultReturn binds committed Tool results for observation only.
	OwnedToolResultReturn OwnedCallbackCustody = "tool-result-v1"
	// OwnedToolBudgetReturn binds an unresolved Tool to its suspended reservation.
	OwnedToolBudgetReturn      OwnedCallbackCustody = "tool-budget-return-v1"
	OwnedBoundedApprovalReturn OwnedCallbackCustody = "bounded-approval-v1"
)

func (c OwnedCallbackCustody) Valid() bool {
	return c == "" || c.permitsControl() || c == OwnedToolResultReturn || c == OwnedToolBudgetReturn || c == OwnedBoundedApprovalReturn
}

func (c OwnedCallbackCustody) permitsControl() bool {
	return c == OwnedCustodyPrepared || c == OwnedCustodyScoped
}

type OwnedPhysicalCallbackOutcome struct {
	// The original return hash seals the ledger identity while the Tool is unresolved.
	ToolBudgetScope string `json:",omitempty"`
	// ToolReturns identifies the outstanding Call obligations of this
	// original callback independently of its control/approval custody.
	ToolReturns    OwnedToolReturns `json:",omitempty"`
	HostCompleted  bool
	LeaseOwned     bool
	ControlCustody OwnedCallbackCustody `json:",omitempty"`
}
type OwnedPhysicalCallbackCompletion struct {
	GrantID          string
	ReturnRevision   uint64
	ReturnSourceHash string
	HostCompleted    bool
	LeaseOwned       bool
	ControlCustody   OwnedCallbackCustody `json:",omitempty"`
	ToolReturns      OwnedToolReturns     `json:",omitempty"`
}

// OwnedPhysicalCallbackReturnPayload is a pure canonical proof payload.
// Only the first-party callback boundary may seal it after all entered Next
// calls return. No Runtime state or execution permission is changed.
func OwnedPhysicalCallbackReturnPayload(s Snapshot, o OwnedPhysicalCallbackOutcome) json.RawMessage {
	data, err := json.Marshal(struct {
		Snapshot Snapshot
		Outcome  OwnedPhysicalCallbackOutcome
	}{s, o})
	if err != nil {
		return nil
	}
	return data
}

type OwnedPhysicalCallbackReturn struct{ proof *featurecontrol.Proof }

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
			b.LedgerID != strings.TrimSpace(b.LedgerID) || (b.LedgerID == "" && b.ApprovalDigest == "") ||
			!validToolReceiptDigest(b.RequestDigest) ||
			(b.ReceiptDigest != "" && (b.LedgerID == "" || !validToolReceiptDigest(b.ReceiptDigest))) ||
			(b.ApprovalDigest != "" && !validToolReceiptDigest(b.ApprovalDigest)) ||
			(b.OwnerResultDigest != "" && (b.ApprovalDigest == "" || !validToolReceiptDigest(b.OwnerResultDigest))) ||
			(b.PendingKind != "" && (b.LedgerID == "" || b.ReceiptDigest != "" || b.OwnerResultDigest != "" || (b.PendingKind != "pending" && b.PendingKind != "scheduled" && b.PendingKind != "child_terminal" && b.PendingKind != "external_result"))) ||
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
func (c OwnedPhysicalCallbackCompletion) MatchesReturnSnapshot(s Snapshot) bool {
	if !c.HostCompleted || c.ReturnRevision != s.Run.Revision || !c.ToolReturns.Valid() ||
		c.ControlCustody == OwnedToolBudgetReturn {
		return false
	}
	o := OwnedPhysicalCallbackOutcome{HostCompleted: c.HostCompleted, LeaseOwned: c.LeaseOwned, ControlCustody: c.ControlCustody, ToolReturns: c.ToolReturns}
	return ManagedDigest(OwnedPhysicalCallbackReturnPayload(s, o)) == c.ReturnSourceHash
}

func (p OwnedPhysicalCallbackReturn) Matches(g ChildExecutionGrant, s Snapshot, o OwnedPhysicalCallbackOutcome) bool {
	return o.ControlCustody.Valid() && (o.ControlCustody == OwnedToolBudgetReturn) == (o.ToolBudgetScope != "") &&
		o.ToolReturns.Valid() && (o.ToolReturns == "" || (o.HostCompleted && o.ToolBudgetScope == "" && o.ControlCustody != OwnedCustodyPrepared)) &&
		(o.ControlCustody != OwnedCustodyPrepared || g.AdmissionOperation == "prepare") &&
		g.IsOwned() && g.ID != "" && g.ChildRunID == s.Run.ID &&
		s.Run.Kind == "agent" && s.Run.ParentRunID == "" && s.Run.Revision >= g.ExpectedRevision &&
		p.proof.Matches(s.Run.ID, s.Run.Revision, "agent_owned_callback_return", g.ID, OwnedPhysicalCallbackReturnPayload(s, o), nil)
}
func (p OwnedPhysicalCallbackReturn) Completion(g ChildExecutionGrant, s Snapshot, o OwnedPhysicalCallbackOutcome) OwnedPhysicalCallbackCompletion {
	if !p.Matches(g, s, o) {
		return OwnedPhysicalCallbackCompletion{}
	}
	hash := sha256.Sum256(OwnedPhysicalCallbackReturnPayload(s, o))
	return OwnedPhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: s.Run.Revision, ReturnSourceHash: hex.EncodeToString(hash[:]), HostCompleted: o.HostCompleted, LeaseOwned: o.LeaseOwned, ControlCustody: o.ControlCustody, ToolReturns: o.ToolReturns}
}
func (r *Runtime) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g ChildExecutionGrant, revision uint64, o OwnedPhysicalCallbackOutcome, proof *featurecontrol.Proof) error {
	if r == nil || ctx == nil || !g.IsOwned() || revision == 0 {
		return ErrInvalidInput
	}
	store, ok := r.store.(OwnedPhysicalExecutionStore)
	if !ok {
		return ErrExecutionRequired
	}
	return store.RecordOwnedPhysicalCallbackReturn(ctx, g, revision, o, OwnedPhysicalCallbackReturn{proof: proof})
}
func (r *Runtime) LookupOwnedPhysicalCallbackReturn(ctx context.Context, g ChildExecutionGrant) (OwnedPhysicalCallbackCompletion, error) {
	if r == nil || ctx == nil || !g.IsOwned() {
		return OwnedPhysicalCallbackCompletion{}, ErrInvalidInput
	}
	store, ok := r.store.(OwnedPhysicalExecutionStore)
	if !ok {
		return OwnedPhysicalCallbackCompletion{}, ErrExecutionRequired
	}
	return store.LookupOwnedPhysicalCallbackReturn(ctx, g)
}
