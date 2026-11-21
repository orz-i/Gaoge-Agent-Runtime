package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

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
	OwnedToolBudgetReturn OwnedCallbackCustody = "tool-budget-return-v1"
)

func (c OwnedCallbackCustody) Valid() bool {
	return c == "" || c.permitsControl() || c == OwnedToolResultReturn || c == OwnedToolBudgetReturn
}

func (c OwnedCallbackCustody) permitsControl() bool {
	return c == OwnedCustodyPrepared || c == OwnedCustodyScoped
}

type OwnedPhysicalCallbackOutcome struct {
	// The original return hash seals the ledger identity while the Tool is unresolved.
	ToolBudgetScope string `json:",omitempty"`
	HostCompleted   bool
	LeaseOwned      bool
	ControlCustody  OwnedCallbackCustody `json:",omitempty"`
}
type OwnedPhysicalCallbackCompletion struct {
	GrantID          string
	ReturnRevision   uint64
	ReturnSourceHash string
	HostCompleted    bool
	LeaseOwned       bool
	ControlCustody   OwnedCallbackCustody `json:",omitempty"`
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

func (p OwnedPhysicalCallbackReturn) Matches(g ChildExecutionGrant, s Snapshot, o OwnedPhysicalCallbackOutcome) bool {
	return o.ControlCustody.Valid() && (o.ControlCustody == OwnedToolBudgetReturn) == (o.ToolBudgetScope != "") &&
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
	return OwnedPhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: s.Run.Revision, ReturnSourceHash: hex.EncodeToString(hash[:]), HostCompleted: o.HostCompleted, LeaseOwned: o.LeaseOwned, ControlCustody: o.ControlCustody}
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
