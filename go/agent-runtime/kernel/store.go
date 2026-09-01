package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"time"
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
	Run        Run
	State      json.RawMessage
	Checkpoint *Checkpoint
	Result     *Result
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

// TransitionLeaseRequest identifies the exact active lease being acknowledged.
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
// committed-transition outbox. The outbox append is part of Create/Apply's
// transaction boundary; projector claim/ack is deliberately separate.
type Store interface {
	Create(context.Context, Record, []EventDraft) (Snapshot, error)
	Load(context.Context, string) (Snapshot, error)
	Apply(context.Context, string, uint64, StoreMutation) (Snapshot, error)
	EventJournal
	TransitionOutbox
}
