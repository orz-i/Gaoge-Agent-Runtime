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

type deniedError struct {
	cause     error
	dimension Dimension
}

func (err deniedError) Error() string {
	if err.dimension == "" {
		return err.cause.Error()
	}
	return fmt.Sprintf("%s: %s", err.cause, err.dimension)
}

func (err deniedError) Unwrap() error { return err.cause }

// DeniedDimension returns the budget dimension responsible for a structured
// admission denial. Callers must not parse Error() text to diagnose exhaustion
// or contention.
func DeniedDimension(err error) (Dimension, bool) {
	var denied deniedError
	if !errors.As(err, &denied) || denied.dimension == "" {
		return "", false
	}
	return denied.dimension, true
}

// ExhaustedAt reconstructs the same structured native quota denial when
// consuming an already committed, exact admission decision.
func ExhaustedAt(dimension Dimension) error { return deny(ErrExhausted, dimension) }

func deny(cause error, dimension Dimension) error {
	return deniedError{cause: cause, dimension: dimension}
}

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

// RootRetryGrant is the durable result of ONE original Harness Store Retry
// CAS, which atomically rotates Invocation Attempt and shared-budget rights.
// The Budget planner is pure and cannot activate a different future Run on
// its own. OriginalRunID preserves the complete cross-Attempt lineage and
// every historical Model/Tool reservation and receipt remains accounted for.
type RootRetryGrant struct {
	InvocationID  string `json:"invocationID"`
	OriginalRunID string `json:"originalRunID"`
	Attempt       int    `json:"attempt"`
	Active        bool   `json:"active,omitempty"`
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
	ID          string `json:"id"`
	RunID       string `json:"runID"`
	RequestHash string `json:"requestHash"`
	// PhysicalRequestHash binds a Model reservation to its exact dispatch shape.
	PhysicalRequestHash string            `json:"physicalRequestHash,omitempty"`
	Requested           Usage             `json:"requested"`
	Usage               Usage             `json:"usage"`
	Status              ReservationStatus `json:"status"`
	Active              bool              `json:"active,omitempty"`
	Dispatched          bool              `json:"dispatched,omitempty"`
	Receipt             json.RawMessage   `json:"receipt,omitempty"`
	Dimension           Dimension         `json:"dimension,omitempty"`
}

// Ledger is the authoritative persistent accounting state for one Harness Turn.
// Existing Turns without a ledger retain their original feature-local semantics.
type Ledger struct {
	ID            string                    `json:"id"`
	Revision      uint64                    `json:"revision"`
	Limits        Limits                    `json:"limits"`
	Runs          map[string]RunBudget      `json:"runs"`
	Reservations  map[string]Reservation    `json:"reservations"`
	Cancelled     bool                      `json:"cancelled,omitempty"`
	CancelledRuns map[string]bool           `json:"cancelledRuns,omitempty"`
	FinishedRuns  map[string]bool           `json:"finishedRuns,omitempty"`
	RootRetries   map[string]RootRetryGrant `json:"rootRetries,omitempty"`
}

// LedgerView contains authoritative aggregate and per-subtree accounting. Zero
// remaining only denotes exhaustion where the matching ceiling is nonzero.
type LedgerView struct {
	ScopeID               string `json:"scopeID"`
	Revision              uint64 `json:"revision"`
	Limits                Limits `json:"limits"`
	Usage                 Usage  `json:"usage"`
	Reserved              Usage  `json:"reserved"`
	Remaining             Usage  `json:"remaining"`
	ActiveRuns            int    `json:"activeRuns"`
	WaitingRuns           int    `json:"waitingRuns"`
	UnfinishedDescendants int    `json:"unfinishedDescendants"`
	UnknownUsage          bool   `json:"unknownUsage,omitempty"`
	Cancelled             bool   `json:"cancelled,omitempty"`
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
		if binding.ParentRunID != "" {
			if _, ok := ledger.Runs[binding.ParentRunID]; !ok {
				return ErrLedgerConflict
			}
		}
		// Validate the prospective immutable ancestry BEFORE registering or
		// charging it. An unregistered child cannot discover its Retry grant
		// by following the old Runs map, nor may it skip its own revocation.
		if ledger.runCancelled(runID, &binding) {
			return ErrExhausted
		}
		if binding.ParentRunID != "" {
			if ledger.FinishedRuns[binding.ParentRunID] {
				return ErrExhausted
			}
			for current := binding.ParentRunID; ; current = ledger.Runs[current].ParentRunID {
				view := ledger.View(current)
				if view.Limits.MaxConcurrentDescendants > 0 && view.UnfinishedDescendants >= view.Limits.MaxConcurrentDescendants {
					return deny(ErrWaiting, DimensionConcurrentDescendants)
				}
				if current == "" {
					break
				}
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

// FinishRuns releases lifetime capacity only after the host observes committed
// terminal Run facts. Model/Tool reservations, including unknown charges, remain.
// Repeating reconciliation after a crash is safe.
func (coordinator Coordinator) FinishRuns(ctx context.Context, scopeID string, runIDs ...string) (Ledger, error) {
	return coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		for _, runID := range runIDs {
			if _, ok := ledger.Runs[runID]; !ok {
				return ErrLedgerNotFound
			}
		}
		if len(runIDs) > 0 && ledger.FinishedRuns == nil {
			ledger.FinishedRuns = map[string]bool{}
		}
		for _, runID := range runIDs {
			ledger.FinishedRuns[runID] = true
		}
		return nil
	})
}

// Reserve also acquires an execution slot when active is true. Waiting attempts
// are persisted without consuming quota. Replays retain the original estimate.
func (coordinator Coordinator) Reserve(ctx context.Context, scopeID string, request Reservation, active bool) (Ledger, error) {
	return coordinator.reserve(ctx, scopeID, request, active, 0)
}

// ReserveAtRevision commits a sampling plan only against the ledger revision
// from which its request restrictions were derived. It cannot silently replan.
func (coordinator Coordinator) ReserveAtRevision(ctx context.Context, scopeID string, request Reservation, active bool, revision uint64) (Ledger, error) {
	if revision == 0 {
		return Ledger{}, ErrLedgerConflict
	}
	return coordinator.reserve(ctx, scopeID, request, active, revision)
}

// PreviewReservation evaluates the exact same reservation transition as Reserve
// against an isolated snapshot. It never writes, acquires capacity or grants
// dispatch authority; live admission still requires the revision CAS.
func (ledger Ledger) PreviewReservation(request Reservation, active bool) error {
	if !validReservationRequest(request) {
		return ErrInvalidUsage
	}
	next := CloneLedger(ledger)
	admissionErr, err := next.applyReservation(request, active)
	return errors.Join(err, admissionErr)
}

func validReservationRequest(request Reservation) bool {
	return request.ID != "" && request.RunID != "" && request.RequestHash != "" && ValidUsage(request.Requested)
}

func (coordinator Coordinator) reserve(ctx context.Context, scopeID string, request Reservation, active bool, revision uint64) (Ledger, error) {
	if !validReservationRequest(request) {
		return Ledger{}, ErrInvalidUsage
	}
	var admissionErr error
	ledger, err := coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		admissionErr = nil
		if revision != 0 && ledger.Revision != revision {
			return ErrLedgerConflict
		}
		var changeErr error
		admissionErr, changeErr = ledger.applyReservation(request, active)
		return changeErr
	})
	return ledger, errors.Join(err, admissionErr)
}

// The same pure transition serves preview and durable CAS. The first error
// is a disposition which can retain a waiting/held record; the second aborts
// the transition entirely. The caller owns an isolated ledger copy.
func (ledger *Ledger) applyReservation(request Reservation, active bool) (error, error) {
	if _, ok := ledger.Runs[request.RunID]; !ok {
		return nil, ErrLedgerConflict
	}
	existing, found := ledger.Reservations[request.ID]
	if found {
		if existing.RunID != request.RunID || existing.RequestHash != request.RequestHash || existing.PhysicalRequestHash != request.PhysicalRequestHash {
			return nil, ErrLedgerConflict
		}
		if existing.Status == ReservationSettled {
			return nil, nil
		}
		if existing.Status == ReservationReleased {
			return nil, ErrLedgerConflict
		}
		request = existing
	}
	if ledger.RunCancelled(request.RunID) {
		return nil, ErrExhausted
	}
	delete(ledger.Reservations, request.ID)
	dimension, denied := ledger.admit(request.RunID, request.Requested, active)
	if denied != nil {
		if found && existing.Status == ReservationHeld {
			ledger.Reservations[request.ID] = existing
			return denied, nil
		}
		if !errors.Is(denied, ErrWaiting) {
			return nil, denied
		}
		request.Status, request.Active, request.Dimension = ReservationWaiting, false, dimension
		ledger.Reservations[request.ID] = request
		return denied, nil
	}
	request.Status, request.Active, request.Dimension = ReservationHeld, active, ""
	ledger.Reservations[request.ID] = request
	return nil, nil
}

// Dispatch marks the point after which timeouts and cancellation cannot refund
// the reservation. It is persisted before the external call is made.
func (coordinator Coordinator) Dispatch(ctx context.Context, scopeID, id string) (Ledger, error) {
	return coordinator.changeReservation(ctx, scopeID, id, dispatchReservation)
}

func dispatchReservation(value *Reservation) error {
	if value.Status != ReservationHeld {
		return ErrLedgerConflict
	}
	if value.Dispatched {
		return ErrWaiting
	}
	value.Dispatched = true
	return nil
}

// DispatchTool applies the same dispatch transition, but at the actual
// ledger CAS checks the original Tool reservation and elected Run closure.
// On definitive cancellation it returns the observed, unchanged ledger.
// A Store write error may instead return a speculative dispatched candidate;
// that candidate is never a qualified unsent denial.
func (coordinator Coordinator) DispatchTool(ctx context.Context, scopeID string, expected Reservation) (Ledger, error) {
	if !validToolReservationIdentity(expected) {
		return Ledger{}, ErrInvalidUsage
	}
	return coordinator.update(ctx, scopeID, func(l *Ledger) error {
		res, ok := l.Reservations[expected.ID]
		if _, registered := l.Runs[expected.RunID]; !registered || !ok || !sameToolReservationIdentity(res, expected) {
			return ErrLedgerConflict
		}
		if res.Status == ReservationHeld && res.Dispatched {
			return ErrWaiting
		}
		if !sameUnsentToolReservation(res, expected) {
			return ErrLedgerConflict
		}
		if l.RunCancelled(expected.RunID) {
			return ErrExhausted
		}
		if err := dispatchReservation(&res); err != nil {
			return err
		}
		l.Reservations[expected.ID] = res
		return nil
	})
}

func validToolReservationIdentity(expected Reservation) bool {
	prefix := "tool:" + expected.RunID + ":"
	return validReservationRequest(expected) && strings.HasPrefix(expected.ID, prefix) && len(expected.ID) > len(prefix) && expected.Requested == (Usage{ToolCalls: 1}) && expected.PhysicalRequestHash == ""
}

func sameToolReservationIdentity(res, expected Reservation) bool {
	return res.ID == expected.ID && res.RunID == expected.RunID && res.RequestHash == expected.RequestHash &&
		res.PhysicalRequestHash == "" && res.Requested == expected.Requested
}
func sameUnsentToolReservation(res, expected Reservation) bool {
	return sameToolReservationIdentity(res, expected) && !res.Dispatched && res.Usage == (Usage{}) && len(res.Receipt) == 0
}

// RequeueProvenUnsent revokes a speculative dispatch marker ONLY when the
// trusted caller has proof that no external Model request was physically sent.
// The exact ledger revision returned by Dispatch is required: a stale worker
// cannot refund a reservation after any intervening ledger transition or a
// later worker's new Dispatch. Without that proof, use Suspend and keep quota
// held as unknown usage. Requeue preserves the immutable logical request hash
// and original quota estimate, allowing the same Invocation ID to be admitted
// again after the Agent obtains a fresh execution claim.
func (coordinator Coordinator) RequeueProvenUnsent(ctx context.Context, scopeID, id string, dispatchRevision uint64) (Ledger, error) {
	if dispatchRevision == 0 || !strings.HasPrefix(id, "model:") || len(id) <= len("model:") {
		return Ledger{}, ErrLedgerConflict
	}
	return coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		if ledger.Revision != dispatchRevision {
			return ErrLedgerConflict
		}
		value, found := ledger.Reservations[id]
		if !found || value.Status != ReservationHeld || !value.Active || !value.Dispatched ||
			len(value.Receipt) != 0 || value.Usage != (Usage{}) {
			return ErrLedgerConflict
		}
		value.Status, value.Active, value.Dispatched, value.Dimension = ReservationWaiting, false, false, ""
		ledger.Reservations[id] = value
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

// ResolveUnsentTool changes only an exact, never-dispatched Tool reservation.
// The caller must have committed its native no-send disposition first. Release
// is terminal; park permits a later fresh capacity admission of the same Call.
// Neither can refund dispatched or settled usage. Repeating the same action
// after a lost acknowledgement requires the current exact fact. A stale
// receipt observer may not park a successor's newly acquired reservation.
func (coordinator Coordinator) ResolveUnsentTool(ctx context.Context, scopeID string, expected Reservation, release bool, revision uint64) (Ledger, error) {
	if revision == 0 || !validToolReservationIdentity(expected) {
		return Ledger{}, ErrInvalidUsage
	}
	return coordinator.update(ctx, scopeID, func(l *Ledger) error {
		if l.Revision != revision {
			return ErrLedgerConflict
		}
		if _, ok := l.Runs[expected.RunID]; !ok {
			return ErrLedgerConflict
		}
		terminal := release || l.RunCancelled(expected.RunID)
		res, ok := l.Reservations[expected.ID]
		if !ok || !sameUnsentToolReservation(res, expected) ||
			(res.Status != ReservationWaiting && res.Status != ReservationHeld && (!terminal || res.Status != ReservationReleased)) {
			return ErrLedgerConflict
		}
		res.Active = false
		if terminal {
			res.Status = ReservationReleased
		} else {
			res.Status = ReservationWaiting
			res.Dimension = ""
		}
		l.Reservations[expected.ID] = res
		return nil
	})
}

func (coordinator Coordinator) Cancel(ctx context.Context, scopeID string) (Ledger, error) {
	return coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		ledger.Cancelled = true
		// The immutable global closure predates Root Retry generations. A
		// scope Cancel must also revoke the CURRENT admitted generation.
		for runID, grant := range ledger.RootRetries {
			if grant.Active {
				if ledger.CancelledRuns == nil {
					ledger.CancelledRuns = map[string]bool{}
				}
				ledger.CancelledRuns[runID] = true
			}
		}
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

// RunCancelled is the single cancellation predicate for admission, cleanup,
// recovery and projection. The empty ID observes the active Root generation;
// Cancelled itself remains the immutable historical first-generation closure.
func (ledger Ledger) RunCancelled(runID string) bool {
	if runID == "" {
		root, valid := ledger.activeRetryRoot()
		if !valid {
			return true
		}
		if root == "" {
			return ledger.Cancelled
		}
		runID = root
	}
	return ledger.runCancelled(runID, nil)
}

func (ledger Ledger) activeRetryRoot() (string, bool) {
	root := ""
	for id, grant := range ledger.RootRetries {
		if !grant.Active {
			continue
		}
		if root != "" || id == "" || grant.InvocationID == "" || grant.Attempt < 2 {
			return "", false
		}
		root = id
	}
	return root, true
}

func (ledger Ledger) runCancelled(runID string, proposed *RunBudget) bool {
	activeRoot, valid := ledger.activeRetryRoot()
	if !valid {
		return true
	}
	authorized := false
	seen := map[string]bool{}
	for current := runID; current != ""; {
		if seen[current] || ledger.CancelledRuns[current] {
			return true
		}
		seen[current] = true
		if current == activeRoot {
			authorized = true
		}
		binding := ledger.Runs[current]
		if current == runID && proposed != nil {
			binding = *proposed
		}
		current = binding.ParentRunID
	}
	return ledger.Cancelled && !authorized
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
	var waiting Dimension
	for current := runID; ; current = ledger.Runs[current].ParentRunID {
		view := ledger.View(current)
		used, err := AddUsage(view.Usage, requested)
		if err != nil {
			return "", err
		}
		if dimension := Exceeded(view.Limits, used); dimension != "" {
			return dimension, deny(ErrExhausted, dimension)
		}
		occupied, err := AddUsage(used, view.Reserved)
		if err != nil {
			return "", err
		}
		if waiting == "" {
			waiting = Exceeded(view.Limits, occupied)
			if waiting == "" && active && view.Limits.MaxConcurrentRuns > 0 && !ledger.active(runID, current) && view.ActiveRuns >= view.Limits.MaxConcurrentRuns {
				waiting = DimensionConcurrentRuns
			}
		}
		if current == "" {
			if waiting != "" {
				return waiting, deny(ErrWaiting, waiting)
			}
			return "", nil
		}
	}
}

// View computes a projection from durable reservations, optionally restricted
// to a role subtree. It never uses browser- or process-local counters.
func (ledger Ledger) View(runID string) LedgerView {
	view := LedgerView{ScopeID: ledger.ID, Revision: ledger.Revision, Limits: ledger.Limits, Cancelled: ledger.RunCancelled(runID)}
	if runID != "" {
		view.Limits = ledger.Runs[runID].Limits
	}
	for id, binding := range ledger.Runs {
		if binding.ParentRunID != "" && id != runID && !ledger.FinishedRuns[id] && ledger.contains(runID, id) {
			view.UnfinishedDescendants++
		}
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
	if value.FinishedRuns != nil {
		finished := make(map[string]bool, len(value.FinishedRuns))
		for id, flag := range value.FinishedRuns {
			finished[id] = flag
		}
		value.FinishedRuns = finished
	}
	cancelled := make(map[string]bool, len(value.CancelledRuns))
	for id, flag := range value.CancelledRuns {
		cancelled[id] = flag
	}
	if value.CancelledRuns != nil {
		value.CancelledRuns = cancelled
	}
	retries := make(map[string]RootRetryGrant, len(value.RootRetries))
	for runID, grant := range value.RootRetries {
		retries[runID] = grant
	}
	if value.RootRetries != nil {
		value.RootRetries = retries
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
