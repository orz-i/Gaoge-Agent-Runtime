package kernel

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

// ErrChildExecutionBusy is authoritative evidence that a parent has at least
// one unsettled child physical execution Grant. A parent terminal mutation
// MUST fail rather than report that its external child effects are cancelled.
var ErrChildExecutionBusy = errors.New("kernel descendant has an unsettled physical execution Grant")

// ChildExecutionGrant is one Store-authoritative physical admission that
// competes with terminal parent mutations, including normal Apply, controls,
// and execution-lease commits. This identity is not a Model/Tool receipt.
// Unknown effects keep the Grant pending until explicit reconciliation.
type ChildExecutionGrant struct {
	ID                  string
	ChildRunID          string
	ExpectedRevision    uint64 // Original physical caller's immutable Run revision.
	ExecutionOwnerID    string
	ExecutionLeaseID    string
	ExecutionEpoch      uint64
	AdmissionSourceHash string
	AdmissionOperation  string
}

// ChildExecutionSettlement is the opaque first-party Agent/Workflow physical
// callback completion permit. Only the internal Feature control
// boundary can populate it. Store adapters MUST validate Matches against the
// authoritative child Snapshot INSIDE the SAME transaction as setting the
// durable settlement revision; a direct Store call with a zero/forged permit
// can never release an unsettled Grant.
type ChildExecutionSettlement struct {
	proof     *featurecontrol.Proof
	execution *ExecutionLease
	recovery  bool
}

// IsRecovery identifies a sealed first-party receipt-only settlement.
func (s ChildExecutionSettlement) IsRecovery() bool { return s.recovery }

// ExecutionLease returns an isolated original bearer, never new authority.
func (s ChildExecutionSettlement) ExecutionLease() (ExecutionLease, bool) {
	if s.execution == nil {
		return ExecutionLease{}, false
	}
	return *s.execution, true
}

func (settlement ChildExecutionSettlement) Matches(grant ChildExecutionGrant, snapshot Snapshot) bool {
	if grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 ||
		snapshot.Run.ID != grant.ChildRunID || snapshot.Run.Revision < grant.ExpectedRevision {
		return false
	}
	var proofKind string
	switch snapshot.Run.Kind {
	case "agent":
		proofKind = "agent_child_physical_settled"
	case "workflow":
		proofKind = "workflow_child_physical_settled"
	default:
		return false
	}
	if grant.IsOwned() {
		if snapshot.Run.Kind != "agent" || snapshot.Run.ParentRunID != "" {
			return false
		}
		proofKind = "agent_owned_physical_settled"
		if settlement.recovery {
			proofKind = "agent_owned_physical_recovered"
		}
	}
	return settlement.proof.Matches(grant.ChildRunID, snapshot.Run.Revision,
		proofKind, grant.ID, snapshot.State, snapshot.Checkpoint)
}

// PhysicalApplyPermit is an opaque per-Run write proof for a Root or child
// Agent/Workflow while its own durable physical Grant is active. It is minted
// exclusively by a first-party Feature-scoped Kernel Runtime. An external caller holding
// a visible Grant ID cannot forge this permit or mutate a root mid-dispatch.
type PhysicalApplyPermit struct {
	grant     ChildExecutionGrant
	proof     *featurecontrol.Proof
	execution *ExecutionLease
}

// ExecutionLease returns only the original first-party bound bearer.
func (p PhysicalApplyPermit) ExecutionLease() (ExecutionLease, bool) {
	if p.execution == nil {
		return ExecutionLease{}, false
	}
	return *p.execution, true
}

func (permit PhysicalApplyPermit) Matches(grant ChildExecutionGrant, snapshot Snapshot) bool {
	if grant.ID == "" || grant.ChildRunID != snapshot.Run.ID || grant.ExpectedRevision == 0 ||
		permit.grant != grant {
		return false
	}
	domain := physicalFeatureProofDomain(snapshot.Run.Kind, "apply")
	return domain != "" && permit.proof.Matches(snapshot.Run.ID, snapshot.Run.Revision,
		domain, grant.ID, snapshot.State, snapshot.Checkpoint)
}

func physicalFeatureProofDomain(kind RunKind, phase string) string {
	switch kind {
	case "agent", "workflow":
		return string(kind) + "_physical_" + phase
	default:
		return ""
	}
}

// IsPresent permits Store adapters to reject a sealed Root execution
// proof when no corresponding physical Grant exists in the same database.
func (permit PhysicalApplyPermit) IsPresent() bool {
	return permit.grant.ID != "" || permit.proof != nil
}

// WithoutPhysicalGrant drops, but never transfers or creates, a Root Run's
// active per-Revision write capability. Built-in nested Subworkflows must use
// their OWN child Run and immutable ParentRunID. A foreign or child Run must
// not inherit the root's Store-mutation permit from the outer interpreter.
func (runtime *Runtime) WithoutPhysicalGrant() *Runtime {
	if runtime == nil {
		return nil
	}
	unbound := *runtime
	unbound.physicalGrant = nil
	return &unbound
}

// WithPhysicalGrant binds a Kernel Runtime to the original Root/child
// Agent/Workflow physical Grant. Only the first-party Feature can mint a
// valid internal source proof, and the Store validates every subsequent per-revision write under
// its own atomic lock/transaction. It never bypasses terminal control CAS.
func (runtime *Runtime) WithPhysicalGrant(ctx context.Context, grant ChildExecutionGrant, proof *featurecontrol.Proof) (*Runtime, error) {
	if runtime == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 ||
		runtime.physicalGrant != nil {
		return nil, ErrInvalidInput
	}
	if grant.IsOwned() {
		lease, ok := runtime.BoundExecutionLease()
		if !ok || !grant.MatchesExecution(lease) {
			return nil, ErrExecutionConflict
		}
	}
	stored, err := runtime.LookupChildExecution(ctx, grant.ChildRunID)
	if err != nil {
		return nil, err
	}
	current, err := runtime.Load(ctx, grant.ChildRunID)
	if err != nil {
		return nil, err
	}
	if stored != grant || current.Run.Revision != grant.ExpectedRevision ||
		physicalFeatureProofDomain(current.Run.Kind, "owner") == "" ||
		!proof.Matches(current.Run.ID, current.Run.Revision,
			physicalFeatureProofDomain(current.Run.Kind, "owner"),
			grant.ID, current.State, current.Checkpoint) {
		return nil, ErrExecutionConflict
	}
	bound := *runtime
	bound.physicalGrant = &grant
	return &bound, nil
}

// ChildExecutionStore is the authoritative, persistent physical Feature
// execution port. Each implementation MUST serialize Grant admission with
// writes/terminal Control on the executing Run ITSELF and every immutable
// ancestor in the SAME Store transaction; a read-then-check fallback is
// insufficient.
// Finish may retire a Grant only after the owning Feature has certified the
// physical execution returned with no unresolved effects. ErrChildExecutionBusy
// from a parent ApplyControl MUST be an atomic, no-mutation rejection; the
// Kernel can mint a private control no-effect proof only under this contract.
type ChildExecutionStore interface {
	BeginChildExecution(context.Context, ChildExecutionGrant) error
	LookupChildExecution(context.Context, string) (ChildExecutionGrant, error)
	MarkChildExecutionSettled(context.Context, ChildExecutionGrant, ChildExecutionSettlement) error
	FinishChildExecution(context.Context, ChildExecutionGrant) error
}

func (runtime *Runtime) SupportsChildExecution() bool {
	if runtime == nil {
		return false
	}
	_, ok := runtime.store.(ChildExecutionStore)
	return ok
}

// BeginChildExecution is the only physical admission for a parent-bound Run.
// The Store rechecks the original persisted child.ParentRunID, every live
// ancestor and existing pending Grants under its transactional authority.
func (runtime *Runtime) BeginChildExecution(ctx context.Context, childRunID string, expectedRevision uint64) (ChildExecutionGrant, error) {
	if runtime == nil || ctx == nil || strings.TrimSpace(childRunID) == "" || expectedRevision == 0 {
		return ChildExecutionGrant{}, ErrInvalidInput
	}
	store, ok := runtime.store.(ChildExecutionStore)
	if !ok {
		return ChildExecutionGrant{}, ErrExecutionRequired
	}
	id, err := runtime.NewID("childgrant")
	if err != nil {
		return ChildExecutionGrant{}, err
	}
	grant := ChildExecutionGrant{ID: id, ChildRunID: childRunID, ExpectedRevision: expectedRevision}
	if err := store.BeginChildExecution(ctx, grant); err != nil {
		return ChildExecutionGrant{}, err
	}
	return grant, nil
}

// MarkChildExecutionSettled is issued only AFTER the entire original physical
// callback has returned. The Agent supplies a private Feature proof bound to
// the persisted child state and revision; the Store then persists that
// revision against the original Grant. A crashed worker cannot mint this
// marker merely because an unreturned callback has not yet staged a Tool ID.
func (runtime *Runtime) MarkChildExecutionSettled(ctx context.Context, grant ChildExecutionGrant, proof *featurecontrol.Proof) error {
	if runtime == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 {
		return ErrInvalidInput
	}
	store, ok := runtime.store.(ChildExecutionStore)
	if !ok {
		return ErrExecutionRequired
	}
	snapshot, err := runtime.Load(ctx, grant.ChildRunID)
	if err != nil {
		return err
	}
	permit := ChildExecutionSettlement{proof: proof, execution: runtime.execution}
	if !permit.Matches(grant, snapshot) {
		return ErrExecutionConflict
	}
	return store.MarkChildExecutionSettled(ctx, grant, permit)
}

// FinishChildExecution releases only its exact original Grant. A timed-out
// callback, lost ACK, unresolved Model/Tool dispatch or detached process does
// not authorize Finish; the persisted Grant deliberately blocks terminal
// parent state until its outcome has been reconciled.
func (runtime *Runtime) FinishChildExecution(ctx context.Context, grant ChildExecutionGrant, proof *featurecontrol.Proof) error {
	if runtime == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 {
		return ErrInvalidInput
	}
	store, ok := runtime.store.(ChildExecutionStore)
	if !ok {
		return ErrExecutionRequired
	}
	// A public Lookup reveals the original Grant, not permission to retire
	// an uncertain physical callback. The Store also MUST require a previously
	// committed, exact-revision settlement marker, minted after callback return.
	snapshot, err := runtime.Load(ctx, grant.ChildRunID)
	if err != nil {
		return err
	}
	// Agent and Workflow use different first-party Feature proof domains.
	// They share the same opaque Store settlement permit; a Workflow proof
	// must never be accepted as an Agent proof (or the inverse).
	permit := ChildExecutionSettlement{proof: proof, execution: runtime.execution}
	if !permit.Matches(grant, snapshot) {
		permit.recovery = true
		if !grant.IsOwned() || !permit.Matches(grant, snapshot) {
			return ErrExecutionConflict
		}
	}
	return store.FinishChildExecution(ctx, grant)
}

// LookupChildExecution returns the exact original Store-backed unresolved
// Grant after a crash/restart. This is observation only: it cannot authorize
// retrying the physical effect or finishing its grant without trusted proof.
func (runtime *Runtime) LookupChildExecution(ctx context.Context, childID string) (ChildExecutionGrant, error) {
	if runtime == nil || ctx == nil || strings.TrimSpace(childID) == "" {
		return ChildExecutionGrant{}, ErrInvalidInput
	}
	store, ok := runtime.store.(ChildExecutionStore)
	if !ok {
		return ChildExecutionGrant{}, ErrExecutionRequired
	}
	return store.LookupChildExecution(ctx, childID)
}
