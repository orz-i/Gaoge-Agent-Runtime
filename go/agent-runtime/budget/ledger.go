package budget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

var (
	ErrLedgerNotFound = errors.New("budget ledger not found")
	ErrLedgerConflict = errors.New("budget ledger conflict")
	ErrWaiting        = errors.New("budget temporarily reserved by another execution")
	ErrExhausted      = errors.New("shared budget exhausted")
	ErrUnmetered      = errors.New("model cannot enforce token admission")
)

// LedgerStore atomically compares the entire Turn ledger. Creation uses revision
// zero; successful writes increment the revision exactly once. It must never
// expose partially written reservations or release uncertain dispatched work.
type LedgerStore interface {
	LoadBudget(context.Context, string) (Ledger, error)
	SaveBudget(context.Context, Ledger, uint64) error
}

// RunBudget records durable ancestry and optional additional subtree ceilings.
type RunBudget struct {
	ParentRunID string `json:"parentRunID,omitempty"`
	Limits      Limits `json:"limits"`
}

type ReservationStatus string

const (
	ReservationWaiting  ReservationStatus = "waiting"
	ReservationHeld     ReservationStatus = "held"
	ReservationSettled  ReservationStatus = "settled"
	ReservationReleased ReservationStatus = "released"
)

// Reservation is identified by a durable logical invocation, never by a poll or
// worker attempt. Receipt allows a caller to recover after settlement but before
// its own state transition commits. An unknown dispatched result remains held.
type Reservation struct {
	ID          string            `json:"id"`
	RunID       string            `json:"runID"`
	RequestHash string            `json:"requestHash"`
	Requested   Usage             `json:"requested"`
	Usage       Usage             `json:"usage"`
	Status      ReservationStatus `json:"status"`
	Active      bool              `json:"active,omitempty"`
	Dispatched  bool              `json:"dispatched,omitempty"`
	Receipt     json.RawMessage   `json:"receipt,omitempty"`
	Dimension   Dimension         `json:"dimension,omitempty"`
}

// Ledger is the authoritative persistent accounting state for one Harness Turn.
// Existing Turns without a ledger retain their original feature-local semantics.
type Ledger struct {
	ID            string                 `json:"id"`
	Revision      uint64                 `json:"revision"`
	Limits        Limits                 `json:"limits"`
	Runs          map[string]RunBudget   `json:"runs"`
	Reservations  map[string]Reservation `json:"reservations"`
	Cancelled     bool                   `json:"cancelled,omitempty"`
	CancelledRuns map[string]bool        `json:"cancelledRuns,omitempty"`
}

// LedgerView contains authoritative aggregate and per-subtree accounting. Zero
// remaining only denotes exhaustion where the matching ceiling is nonzero.
type LedgerView struct {
	ScopeID      string `json:"scopeID"`
	Revision     uint64 `json:"revision"`
	Limits       Limits `json:"limits"`
	Usage        Usage  `json:"usage"`
	Reserved     Usage  `json:"reserved"`
	Remaining    Usage  `json:"remaining"`
	ActiveRuns   int    `json:"activeRuns"`
	WaitingRuns  int    `json:"waitingRuns"`
	UnknownUsage bool   `json:"unknownUsage,omitempty"`
	Cancelled    bool   `json:"cancelled,omitempty"`
}

// Coordinator applies common admission rules while the Harness owns scope and
// lifetime. Store implementations are shared by memory and PostgreSQL hosts.
type Coordinator struct{ Store LedgerStore }

func (coordinator Coordinator) Ensure(ctx context.Context, id string, limits Limits) (Ledger, error) {
	if coordinator.Store == nil || strings.TrimSpace(id) == "" || !ValidLimits(limits) {
		return Ledger{}, ErrInvalidUsage
	}
	ledger, err := coordinator.Store.LoadBudget(ctx, id)
	if err == nil {
		if ledger.Limits != limits {
			return Ledger{}, ErrLedgerConflict
		}
		return ledger, nil
	}
	if !errors.Is(err, ErrLedgerNotFound) {
		return Ledger{}, err
	}
	ledger = Ledger{ID: id, Revision: 1, Limits: limits, Runs: map[string]RunBudget{}, Reservations: map[string]Reservation{}}
	if err = coordinator.Store.SaveBudget(ctx, ledger, 0); errors.Is(err, ErrLedgerConflict) {
		return coordinator.Ensure(ctx, id, limits)
	}
	return ledger, err
}

// RegisterRun charges the child count before admitting a new descendant. The
// ancestry and local limits are immutable and replayed registrations are free.
func (coordinator Coordinator) RegisterRun(ctx context.Context, scopeID, runID string, binding RunBudget) (Ledger, error) {
	if runID == "" || runID == binding.ParentRunID || !ValidLimits(binding.Limits) {
		return Ledger{}, ErrInvalidUsage
	}
	return coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		if existing, ok := ledger.Runs[runID]; ok {
			if existing != binding {
				return ErrLedgerConflict
			}
			return nil
		}
		if ledger.cancelled(runID) || ledger.cancelled(binding.ParentRunID) {
			return ErrExhausted
		}
		if binding.ParentRunID != "" {
			if _, ok := ledger.Runs[binding.ParentRunID]; !ok {
				return ErrLedgerConflict
			}
			charge := Usage{ChildRuns: 1}
			if _, err := ledger.admit(binding.ParentRunID, charge, false); err != nil {
				return err
			}
			id := "child:" + runID
			ledger.Reservations[id] = Reservation{ID: id, RunID: binding.ParentRunID, Status: ReservationSettled, Usage: charge, Requested: charge}
		}
		ledger.Runs[runID] = binding
		return nil
	})
}

// Reserve also acquires an execution slot when active is true. Waiting attempts
// are persisted without consuming quota. Replays retain the original estimate.
func (coordinator Coordinator) Reserve(ctx context.Context, scopeID string, request Reservation, active bool) (Ledger, error) {
	if request.ID == "" || request.RunID == "" || request.RequestHash == "" || !ValidUsage(request.Requested) {
		return Ledger{}, ErrInvalidUsage
	}
	var admissionErr error
	ledger, err := coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		admissionErr = nil
		if _, ok := ledger.Runs[request.RunID]; !ok {
			return ErrLedgerConflict
		}
		existing, found := ledger.Reservations[request.ID]
		if found {
			if existing.RunID != request.RunID || existing.RequestHash != request.RequestHash {
				return ErrLedgerConflict
			}
			if existing.Status == ReservationSettled {
				return nil
			}
			if existing.Status == ReservationReleased {
				return ErrLedgerConflict
			}
			request = existing
		}
		if ledger.cancelled(request.RunID) {
			return ErrExhausted
		}
		delete(ledger.Reservations, request.ID)
		dimension, denied := ledger.admit(request.RunID, request.Requested, active)
		if denied != nil {
			if found && existing.Status == ReservationHeld {
				ledger.Reservations[request.ID] = existing
				admissionErr = denied
				return nil
			}
			if !errors.Is(denied, ErrWaiting) {
				return denied
			}
			request.Status, request.Active, request.Dimension = ReservationWaiting, false, dimension
			ledger.Reservations[request.ID] = request
			admissionErr = denied
			return nil
		}
		request.Status, request.Active, request.Dimension = ReservationHeld, active, ""
		ledger.Reservations[request.ID] = request
		return nil
	})
	return ledger, errors.Join(err, admissionErr)
}

// Dispatch marks the point after which timeouts and cancellation cannot refund
// the reservation. It is persisted before the external call is made.
func (coordinator Coordinator) Dispatch(ctx context.Context, scopeID, id string) (Ledger, error) {
	return coordinator.changeReservation(ctx, scopeID, id, func(value *Reservation) error {
		if value.Status != ReservationHeld {
			return ErrLedgerConflict
		}
		if value.Dispatched {
			return ErrWaiting
		}
		value.Dispatched = true
		return nil
	})
}

// Suspend releases only the execution slot; a pending logical call retains its
// quota. It is used for durable async tools and explicit safe provider retries.
func (coordinator Coordinator) Suspend(ctx context.Context, scopeID, id string) (Ledger, error) {
	return coordinator.changeReservation(ctx, scopeID, id, func(value *Reservation) error {
		value.Active = false
		return nil
	})
}

func (coordinator Coordinator) Settle(ctx context.Context, scopeID, id string, usage Usage, receipt json.RawMessage) (Ledger, error) {
	if !ValidUsage(usage) || (len(receipt) > 0 && !json.Valid(receipt)) {
		return Ledger{}, ErrInvalidUsage
	}
	return coordinator.changeReservation(ctx, scopeID, id, func(value *Reservation) error {
		if value.Status == ReservationSettled {
			if value.Usage != usage || string(value.Receipt) != string(receipt) {
				return ErrLedgerConflict
			}
			return nil
		}
		if value.Status != ReservationHeld {
			return ErrLedgerConflict
		}
		value.Status, value.Active = ReservationSettled, false
		value.Usage, value.Receipt = usage, append(json.RawMessage(nil), receipt...)
		return nil
	})
}

// Release is legal only before dispatch. Reconciliation of dispatched work must
// use Settle with an authoritative receipt (including a proven zero charge).
func (coordinator Coordinator) Release(ctx context.Context, scopeID, id string) (Ledger, error) {
	return coordinator.changeReservation(ctx, scopeID, id, func(value *Reservation) error {
		if value.Dispatched || value.Status == ReservationSettled {
			return ErrLedgerConflict
		}
		value.Status, value.Active = ReservationReleased, false
		return nil
	})
}

func (coordinator Coordinator) Cancel(ctx context.Context, scopeID string) (Ledger, error) {
	return coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		ledger.Cancelled = true
		for id, value := range ledger.Reservations {
			if !value.Dispatched && value.Status != ReservationSettled {
				value.Status, value.Active = ReservationReleased, false
				ledger.Reservations[id] = value
			}
		}
		return nil
	})
}

// CancelRun stops new admissions in a subtree without affecting siblings.
func (coordinator Coordinator) CancelRun(ctx context.Context, scopeID, runID string) (Ledger, error) {
	if strings.TrimSpace(runID) == "" {
		return Ledger{}, ErrInvalidUsage
	}
	return coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		if ledger.CancelledRuns == nil {
			ledger.CancelledRuns = map[string]bool{}
		}
		ledger.CancelledRuns[runID] = true
		for id, value := range ledger.Reservations {
			if ledger.contains(runID, value.RunID) && !value.Dispatched && value.Status != ReservationSettled {
				value.Status, value.Active = ReservationReleased, false
				ledger.Reservations[id] = value
			}
		}
		return nil
	})
}

func (ledger Ledger) cancelled(runID string) bool {
	if ledger.Cancelled {
		return true
	}
	for runID != "" {
		if ledger.CancelledRuns[runID] {
			return true
		}
		runID = ledger.Runs[runID].ParentRunID
	}
	return false
}

func (coordinator Coordinator) changeReservation(ctx context.Context, scopeID, id string, change func(*Reservation) error) (Ledger, error) {
	return coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		value, ok := ledger.Reservations[id]
		if !ok {
			return ErrLedgerNotFound
		}
		if err := change(&value); err != nil {
			return err
		}
		ledger.Reservations[id] = value
		return nil
	})
}

func (coordinator Coordinator) update(ctx context.Context, scopeID string, change func(*Ledger) error) (Ledger, error) {
	if coordinator.Store == nil {
		return Ledger{}, ErrInvalidUsage
	}
	for {
		if err := ctx.Err(); err != nil {
			return Ledger{}, err
		}
		ledger, err := coordinator.Store.LoadBudget(ctx, scopeID)
		if err != nil {
			return Ledger{}, err
		}
		next := CloneLedger(ledger)
		if err = change(&next); err != nil {
			return ledger, err
		}
		if reflect.DeepEqual(ledger, next) {
			return ledger, nil
		}
		next.Revision++
		err = coordinator.Store.SaveBudget(ctx, next, ledger.Revision)
		if errors.Is(err, ErrLedgerConflict) {
			continue
		}
		return next, err
	}
}

func (ledger Ledger) admit(runID string, requested Usage, active bool) (Dimension, error) {
	for current := runID; ; current = ledger.Runs[current].ParentRunID {
		view := ledger.View(current)
		used, err := AddUsage(view.Usage, requested)
		if err != nil {
			return "", err
		}
		if dimension := Exceeded(view.Limits, used); dimension != "" {
			return dimension, fmt.Errorf("%w: %s", ErrExhausted, dimension)
		}
		occupied, err := AddUsage(used, view.Reserved)
		if err != nil {
			return "", err
		}
		if dimension := Exceeded(view.Limits, occupied); dimension != "" {
			return dimension, fmt.Errorf("%w: %s", ErrWaiting, dimension)
		}
		if active && view.Limits.MaxConcurrentRuns > 0 && !ledger.active(runID, current) && view.ActiveRuns >= view.Limits.MaxConcurrentRuns {
			return DimensionConcurrentRuns, ErrWaiting
		}
		if current == "" {
			return "", nil
		}
	}
}

// View computes a projection from durable reservations, optionally restricted
// to a role subtree. It never uses browser- or process-local counters.
func (ledger Ledger) View(runID string) LedgerView {
	view := LedgerView{ScopeID: ledger.ID, Revision: ledger.Revision, Limits: ledger.Limits, Cancelled: ledger.cancelled(runID)}
	if runID != "" {
		view.Limits = ledger.Runs[runID].Limits
	}
	active, waiting := map[string]bool{}, map[string]bool{}
	for _, value := range ledger.Reservations {
		if !ledger.contains(runID, value.RunID) {
			continue
		}
		switch value.Status {
		case ReservationSettled:
			view.Usage, _ = AddUsage(view.Usage, value.Usage)
		case ReservationHeld:
			view.Reserved, _ = AddUsage(view.Reserved, value.Requested)
			if value.Active {
				active[value.RunID] = true
			}
			if value.Dispatched {
				view.UnknownUsage = true
			}
		case ReservationWaiting:
			waiting[value.RunID] = true
		case ReservationReleased:
		}
	}
	view.ActiveRuns, view.WaitingRuns = len(active), len(waiting)
	view.Remaining = remainingUsage(view.Limits, view.Usage, view.Reserved)
	return view
}

func (ledger Ledger) active(runID, subtree string) bool {
	for _, value := range ledger.Reservations {
		if value.RunID == runID && value.Active && ledger.contains(subtree, value.RunID) {
			return true
		}
	}
	return false
}

func (ledger Ledger) contains(parent, child string) bool {
	if parent == "" {
		return true
	}
	for child != "" {
		if child == parent {
			return true
		}
		child = ledger.Runs[child].ParentRunID
	}
	return false
}

func CloneLedger(value Ledger) Ledger {
	cancelled := make(map[string]bool, len(value.CancelledRuns))
	for id, flag := range value.CancelledRuns {
		cancelled[id] = flag
	}
	if value.CancelledRuns != nil {
		value.CancelledRuns = cancelled
	}
	runs := make(map[string]RunBudget, len(value.Runs))
	for id, run := range value.Runs {
		runs[id] = run
	}
	reservations := make(map[string]Reservation, len(value.Reservations))
	for id, reservation := range value.Reservations {
		reservation.Receipt = append(json.RawMessage(nil), reservation.Receipt...)
		reservations[id] = reservation
	}
	value.Runs, value.Reservations = runs, reservations
	return value
}
