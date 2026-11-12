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
	ID         string
	ChildRunID string
}

// ChildExecutionSettlement is the opaque first-party Agent-issued physical
// callback completion permit. Only the Kernel/Agent internal Feature control
// boundary can populate it. Store adapters MUST validate Matches against the
// authoritative child Snapshot INSIDE the SAME transaction as setting the
// durable settlement revision; a direct Store call with a zero/forged permit
// can never release an unsettled Grant.
type ChildExecutionSettlement struct{ proof *featurecontrol.Proof }

func (settlement ChildExecutionSettlement) Matches(grant ChildExecutionGrant, snapshot Snapshot) bool {
	return grant.ID != "" && grant.ChildRunID != "" &&
		snapshot.Run.ID == grant.ChildRunID && snapshot.Run.ParentRunID != "" &&
		settlement.proof.Matches(grant.ChildRunID, snapshot.Run.Revision,
			"agent_child_physical_settled", grant.ID, snapshot.State, snapshot.Checkpoint)
}

// ChildExecutionStore is an optional atomic store port for immutable parented
// Runs. Each implementation MUST serialize BeginChildExecution and every
// terminal ancestor transition in the SAME persistent Store, covering all
// ancestor Runs; no in-process lock or read-then-check fallback suffices.
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
func (runtime *Runtime) BeginChildExecution(ctx context.Context, childRunID string) (ChildExecutionGrant, error) {
	if runtime == nil || ctx == nil || strings.TrimSpace(childRunID) == "" {
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
	grant := ChildExecutionGrant{ID: id, ChildRunID: childRunID}
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
	if runtime == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" {
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
	permit := ChildExecutionSettlement{proof: proof}
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
	if runtime == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" {
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
	if snapshot.Run.ParentRunID == "" ||
		!proof.Matches(grant.ChildRunID, snapshot.Run.Revision, "agent_child_physical_settled", grant.ID, snapshot.State, snapshot.Checkpoint) {
		return ErrExecutionConflict
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
