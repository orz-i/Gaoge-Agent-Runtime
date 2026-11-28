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
	ID               string
	ChildRunID       string
	ExpectedRevision uint64 // Original physical caller's immutable Run revision.
	ExecutionOwnerID string
	ExecutionLeaseID string
	ExecutionEpoch   uint64
	// New ordinary owned grants bind the frozen source AND original private
	// admission attempt. Retained older and managed hashes remain opaque.
	AdmissionSourceHash string
	AdmissionOperation  string
}

// ChildExecutionSettlement is the opaque first-party physical-return
// qualification. Store adapters validate it under the Run transaction;
// a visible Grant ID or a clean Run cannot manufacture this permit.
type ChildExecutionSettlement struct {
	proof *featurecontrol.Proof
}

func (settlement ChildExecutionSettlement) Matches(grant ChildExecutionGrant, snapshot Snapshot) bool {
	if grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 ||
		snapshot.Run.ID != grant.ChildRunID || snapshot.Run.Revision < grant.ExpectedRevision {
		return false
	}
	var proofKind string
	var extra any = snapshot.Checkpoint
	switch snapshot.Run.Kind {
	case "agent":
		proofKind = "agent_child_physical_settled"
	default:
		return false
	}
	if grant.IsOwned() {
		if snapshot.Run.Kind != "agent" || snapshot.Run.ParentRunID != "" {
			return false
		}
		proofKind = "agent_owned_physical_settled"
		extra = snapshot
	}
	return settlement.proof.Matches(grant.ChildRunID, snapshot.Run.Revision,
		proofKind, grant.ID, snapshot.State, extra)
}

// PhysicalApplyPermit is an opaque per-Run write proof for a Root or child
// Agent while its own durable physical Grant is active. It is minted
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
	case "agent":
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
// active per-Revision write capability. Any nested Child executor must use
// its OWN child Run and immutable ParentRunID. A foreign or child Run must
// not inherit the root's Store-mutation permit from the outer interpreter.
func (runtime *Runtime) WithoutPhysicalGrant() *Runtime {
	if runtime == nil {
		return nil
	}
	unbound := *runtime
	unbound.physicalGrant = nil
	return &unbound
}

// FinishChildExecution releases only its exact original Grant. A timed-out
// callback, lost ACK, unresolved Model/Tool dispatch or detached process does
// not authorize Finish; the persisted Grant deliberately blocks terminal
// parent state until its outcome has been reconciled.
func (runtime *Runtime) FinishChildExecution(ctx context.Context, grant ChildExecutionGrant, proof *featurecontrol.Proof) error {

	if runtime == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 {
		return ErrInvalidInput
	}
	store, ok := runtime.store.(PhysicalExecutionStore)
	if !ok {
		return ErrExecutionRequired
	}
	snapshot, err := runtime.Load(ctx, grant.ChildRunID)
	if err != nil {
		return err
	}
	permit := ChildExecutionSettlement{proof: proof}
	if !permit.Matches(grant, snapshot) {
		return ErrExecutionConflict
	}
	return store.FinishChildExecution(ctx, grant, permit)
}

// LookupChildExecution returns the exact original Store-backed unresolved
// Grant after a crash/restart. This is observation only: it cannot authorize
// retrying the physical effect or finishing its grant without trusted proof.
func (runtime *Runtime) LookupChildExecution(ctx context.Context, childID string) (ChildExecutionGrant, error) {
	if runtime == nil || ctx == nil || strings.TrimSpace(childID) == "" {
		return ChildExecutionGrant{}, ErrInvalidInput
	}
	store, ok := runtime.store.(PhysicalExecutionStore)
	if !ok {
		return ChildExecutionGrant{}, ErrExecutionRequired
	}
	return store.LookupChildExecution(ctx, childID)
}
