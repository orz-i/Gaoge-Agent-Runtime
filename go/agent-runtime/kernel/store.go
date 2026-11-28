package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

var (
	ErrInvalidInput  = errors.New("invalid kernel input")
	ErrNotFound      = errors.New("kernel run not found")
	ErrAlreadyExists = errors.New("kernel run already exists")
	ErrConflict      = errors.New("kernel run revision conflict")
	ErrTerminal      = errors.New("kernel run is terminal")
	ErrDeadline      = errors.New("kernel run deadline exceeded")
)

// Record is the feature-neutral data persisted atomically with a Run.
type Record struct {
	// ExecutionRequired is a creation-only Store hint: reserve Epoch 1 in
	// the SAME Create transaction before any legacy executor can act.
	// Existing/legacy Runs keep Epoch 0 and cannot switch modes implicitly.
	// This flag is NOT another persisted status or mutable Run field.
	ExecutionRequired bool
	BudgetEnrollment  *budget.RunEnrollment // creation-only, never a mutable binding
	Run               Run
	State             json.RawMessage
	Checkpoint        *Checkpoint
	Result            *Result
}

// NeedsTransitionProjection reports whether one committed transition must be
// retained for a future projector. Feature-owned events opt in explicitly;
// terminal transitions are always retained because they may wake an owning
// parent Run through a durable RunRelation.
func NeedsTransitionProjection(status RunStatus, events []EventDraft) bool {
	if terminalStatus(status) {
		return true
	}
	for _, event := range events {
		if event.Wakeup {
			return true
		}
	}
	return false
}

// StoreMutation is the fully validated replacement record for one CAS transition.
type StoreMutation struct {
	Record Record
	Events []EventDraft
	// Ledger is the original accounting CAS, committed or rolled back with the Run.
	// Only the existing fenced execution/control write paths may accept it.
	Ledger *budget.LedgerMutation
	// PhysicalPermit proves an Agent's original active execution
	// segment for precisely the current Run state/revision. Only a first-party
	// Kernel scoped Runtime can populate this nonforgeable field.
	PhysicalPermit PhysicalApplyPermit
	// ManagedSafePoint is a private first-party T0 proof, committed with this mutation.
	ManagedSafePoint ManagedSafePointProof
}

// CommitAuthority carries the original authority of one Run write. It is
// transient, never a second persisted execution identity. The zero value
// allows only the existing unclaimed/ancestor-fenced CAS.
type CommitAuthority struct {
	Lease    *ExecutionLease
	Control  *ControlGuard
	Recovery *ReceiptRecovery
}
type ReceiptRecovery struct {
	Grant  ChildExecutionGrant
	Permit OwnedPhysicalRecovery
}

func LeaseCommit(l ExecutionLease) CommitAuthority { return CommitAuthority{Lease: &l} }
func ControlCommit(g ControlGuard) CommitAuthority { return CommitAuthority{Control: &g} }
func ReceiptCommit(g ChildExecutionGrant, p OwnedPhysicalRecovery) CommitAuthority {
	return CommitAuthority{Recovery: &ReceiptRecovery{g, p}}
}

// Validate checks the command shape only. The original authority is always
// qualified against locked Store facts by the committing adapter.
func (a CommitAuthority) Validate(id string, rev uint64, m StoreMutation) error {
	if id == "" || rev == 0 {
		return ErrInvalidInput
	}
	if a.Lease != nil {
		if a.Control != nil || a.Recovery != nil || !validExecutionLease(*a.Lease) || a.Lease.RunID != id || m.Record.Run.ID != id || m.Record.Run.Revision != rev+1 {
			return ErrInvalidInput
		}
	}
	if a.Recovery != nil {
		if a.Control != nil || a.Recovery.Grant.ChildRunID != id {
			return ErrInvalidInput
		}
		return ValidateOwnedPhysicalRecoveryRequest(a.Recovery.Grant, rev, m)
	}
	if (a.Control == nil || a.Control.managed == nil) && (m.Record.Run.ID != id || m.Record.Run.Revision != rev+1) {
		return ErrConflict
	}
	return nil
}

// CommittedTransition is the feature-neutral durable projection of one
// successfully committed Run revision. Stores append it in the same atomic
// transaction as the aggregate CAS and Event journal append.
type CommittedTransition struct {
	ID          string
	RunID       string
	Kind        RunKind
	Status      RunStatus
	Revision    uint64
	Events      []EventDraft
	CommittedAt time.Time
	Attempts    uint32
}

// TransitionClaimRequest leases committed transitions to one projector. Now is
// caller supplied so deterministic stores/tests do not depend on wall clock.
type TransitionClaimRequest struct {
	WorkerID      string
	Limit         int
	LeaseDuration time.Duration
	Now           time.Time
}

// TransitionClaim is one leased committed transition.
type TransitionClaim struct {
	Transition CommittedTransition
	LeaseID    string
	WorkerID   string
	LeaseUntil time.Time
}

// TransitionLeaseRequest identifies the exact current delivery lease. The
// projector may ACK after the lease's nominal deadline if NO newer claim
// replaced its lease identity; this is a delivery acknowledgment, NOT Run
// execution authority. Superseded Worker IDs/lease IDs must fail closed.
type TransitionLeaseRequest struct {
	TransitionID string
	LeaseID      string
	WorkerID     string
}

// TransitionRetryRequest releases a failed projection for a later retry.
type TransitionRetryRequest struct {
	TransitionLeaseRequest
	AvailableAt time.Time
}

// TransitionOutbox is the feature-neutral durable committed-transition source.
// Consumers decide what, if any, feature action a transition implies.
type TransitionOutbox interface {
	ClaimTransitions(context.Context, TransitionClaimRequest) ([]TransitionClaim, error)
	AckTransition(context.Context, TransitionLeaseRequest) error
	RetryTransition(context.Context, TransitionRetryRequest) error
}

// EventJournal reads the append-only Run event stream independently of the
// hot aggregate Snapshot path. afterSeq is exclusive and limit is bounded.
type EventJournal interface {
	ListEvents(context.Context, string, int64, int) ([]Event, error)
}

// Store persists the minimal Run root, opaque feature state, Event journal, and
// committed-transition outbox. The outbox append is part of Create/Commit's
// transaction boundary; projector claim/ack is deliberately separate.
type Store interface {
	Create(context.Context, Record, []EventDraft) (Snapshot, error)
	DecideBeforeCreate(context.Context, RunCreationDecision) (RunCreationDecision, error)
	LoadCreationDecision(context.Context, string) (RunCreationDecision, error)
	Load(context.Context, string) (Snapshot, error)
	// Commit returns the committed snapshot and whether control revoked an owner.
	Commit(context.Context, string, uint64, StoreMutation, CommitAuthority) (Snapshot, bool, error)
	EventJournal
	TransitionOutbox
}
