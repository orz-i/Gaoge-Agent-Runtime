package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Store is the single-process atomic Kernel adapter used by minimal hosts and tests.
type Store struct {
	mu                sync.RWMutex
	records           map[string]kernel.Snapshot
	creationDecisions map[string]kernel.RunCreationDecision
	ledgers           budget.LedgerMap
	events            map[string][]kernel.Event
	transitions       map[string]transitionState
	transitionOrder   []string
	execution         map[string]memoryExecutionState
	childGrants       map[string]memoryChildGrant
	grantIdentities   map[string]string
	ownedRetirements  map[string]kernel.OwnedRetirementReceipt
	childGrantOwners  map[string]map[string]struct{}
	executionClock    func() time.Time
	managedAuthority  kernel.ManagedAuthority
	managedTransfers  map[string]kernel.ManagedTransfer
	managedCurrent    map[string]string
}

type transitionState struct {
	transition  kernel.CommittedTransition
	availableAt time.Time
	leaseID     string
	workerID    string
	leaseUntil  time.Time
}

func cloneRun(run kernel.Run) kernel.Run {
	if run.DeadlineAt != nil {
		deadlineAt := *run.DeadlineAt
		run.DeadlineAt = &deadlineAt
	}
	if run.EndedAt != nil {
		endedAt := *run.EndedAt
		run.EndedAt = &endedAt
	}
	return run
}

// NewStore creates an empty in-memory Kernel Store.
func NewStore() *Store {
	return &Store{
		ledgers: make(budget.LedgerMap),
		records: make(map[string]kernel.Snapshot), events: make(map[string][]kernel.Event),
		creationDecisions: make(map[string]kernel.RunCreationDecision),
		transitions:       make(map[string]transitionState),
		execution:         make(map[string]memoryExecutionState),
		childGrants:       make(map[string]memoryChildGrant),
		grantIdentities:   make(map[string]string),
		childGrantOwners:  make(map[string]map[string]struct{}),
		executionClock:    time.Now,
		managedAuthority:  newMemoryManagedAuthority(),
		managedTransfers:  make(map[string]kernel.ManagedTransfer),
		managedCurrent:    make(map[string]string),
	}
}

// Create atomically inserts one Run record.
func (store *Store) Create(ctx context.Context, record kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.records[record.Run.ID]; exists {
		return kernel.Snapshot{}, kernel.ErrAlreadyExists
	}
	if decision, rejected := store.creationDecisions[record.Run.ID]; rejected {
		return kernel.Snapshot{}, decision.Rejection()
	}

	if _, err := store.validateLiveRunOwners(ctx, record.Run); err != nil {
		return kernel.Snapshot{}, err
	}
	if err := store.validateCreationRetry(ctx, record.BudgetEnrollment); err != nil {
		return kernel.Snapshot{}, err
	}
	if err := kernel.EnrollRunBudget(ctx, store.ledgers, record); err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot := snapshotFromRecord(record)
	journal := appendEvents(nil, events, record.Run.UpdatedAt)
	snapshot.EventHead = int64(len(journal))
	if record.ExecutionRequired {
		store.execution[record.Run.ID] = memoryExecutionState{epoch: 1}
	}
	store.records[record.Run.ID] = cloneSnapshot(snapshot)
	store.events[record.Run.ID] = cloneEvents(journal)
	store.appendCommittedTransition(record.Run, events)
	return cloneSnapshot(snapshot), nil
}

// DecideBeforeCreate and Create share the same native mutex and ledger CAS.
func (s *Store) DecideBeforeCreate(ctx context.Context, q kernel.RunCreationDecision) (kernel.RunCreationDecision, error) {
	var zero kernel.RunCreationDecision
	if ctx == nil || !q.Valid() {
		return zero, kernel.ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if _, ok := s.records[q.RunID]; ok {
		return zero, kernel.ErrAlreadyExists
	}
	if c, ok := s.creationDecisions[q.RunID]; ok {
		if !c.MatchesSelection(q) {
			return zero, kernel.ErrConflict
		}
		return c, nil
	}
	if err := s.validateCreationRetry(ctx, &q.Budget); err != nil {
		return zero, err
	}
	if err := budget.CloseBeforeEnrollment(ctx, s.ledgers, q.RunID, q.Budget); err != nil {
		return zero, err
	}
	s.creationDecisions[q.RunID] = q
	return q, nil
}

func (s *Store) loadCreationDecision(_ context.Context, id string) (kernel.RunCreationDecision, error) {
	if c, ok := s.creationDecisions[id]; ok {
		return c, nil
	}
	if _, ok := s.records[id]; ok {
		return kernel.RunCreationDecision{}, kernel.ErrAlreadyExists
	}
	return kernel.RunCreationDecision{}, kernel.ErrNotFound
}

func (s *Store) LoadCreationDecision(ctx context.Context, id string) (kernel.RunCreationDecision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadCreationDecision(ctx, id)
}

func (s *Store) validateCreationRetry(ctx context.Context, e *budget.RunEnrollment) error {
	return kernel.ValidateCreationRetry(ctx, e, func(_ context.Context, id string) (kernel.Snapshot, error) {
		if v, ok := s.records[id]; ok {
			return v, nil
		}
		return kernel.Snapshot{}, kernel.ErrNotFound
	}, s.loadCreationDecision)
}

// ListEvents returns one isolated page without reading or cloning the aggregate.
func (store *Store) ListEvents(_ context.Context, runID string, afterSeq int64, limit int) ([]kernel.Event, error) {
	if store == nil || strings.TrimSpace(runID) == "" || afterSeq < 0 || limit <= 0 || limit > 10_000 {
		return nil, kernel.ErrInvalidInput
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if _, exists := store.records[runID]; !exists {
		return nil, kernel.ErrNotFound
	}
	journal := store.events[runID]
	if afterSeq >= int64(len(journal)) {
		return []kernel.Event{}, nil
	}
	end := int64(len(journal))
	if int64(limit) < end-afterSeq {
		end = afterSeq + int64(limit)
	}
	return cloneEvents(journal[afterSeq:end]), nil
}

// Load returns one isolated snapshot copy.
func (store *Store) Load(_ context.Context, runID string) (kernel.Snapshot, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	snapshot, ok := store.records[runID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return cloneSnapshot(snapshot), nil
}

// Apply performs one revision-checked atomic replacement and event append.
func (store *Store) Apply(ctx context.Context, runID string, expectedRevision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.execution[runID].epoch > 0 {
		return kernel.Snapshot{}, kernel.ErrExecutionRequired
	}
	if mutation.Ledger != nil {
		current, ok := store.records[runID]
		if !ok || current.Run.ParentRunID == "" || !mutation.PhysicalPermit.IsPresent() {
			return kernel.Snapshot{}, kernel.ErrExecutionRequired
		}
		if _, err := store.validateLiveRunOwners(ctx, current.Run); err != nil {
			return kernel.Snapshot{}, err
		}
	}
	return store.applyLocked(ctx, runID, expectedRevision, mutation, false)
}

// applyLocked is reached only after ordinary or fenced eligibility checks.
func (store *Store) applyLocked(ctx context.Context, runID string, expectedRevision uint64, mutation kernel.StoreMutation, control bool) (kernel.Snapshot, error) {
	if err := store.nativeEntryGateLocked(runID); err != nil {
		return kernel.Snapshot{}, err
	}
	current, ok := store.records[runID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	if current.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if current.Run.ParentRunID != mutation.Record.Run.ParentRunID {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	// The original self-owned physical Grant fences every competing Run
	// write, not only terminal cancellation. Only a first-party scoped
	// Kernel Runtime carries its exact per-revision proof. Descendant
	// Grants independently fence all competing ancestor mutations.
	var rootGrantFound bool
	for id := range store.childGrantOwners[runID] {
		granted, ok := store.childGrants[id]
		if !ok {
			return kernel.Snapshot{}, kernel.ErrExecutionConflict
		}
		if granted.grant.ChildRunID == runID {
			rootGrantFound = true
			if !mutation.PhysicalPermit.Matches(granted.grant, current) {
				return kernel.Snapshot{}, kernel.ErrChildExecutionBusy
			}
		} else {
			return kernel.Snapshot{}, kernel.ErrChildExecutionBusy
		}
	}
	if !rootGrantFound && mutation.PhysicalPermit.IsPresent() {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	var safe kernel.ManagedSafePoint
	if mutation.ManagedSafePoint.Present() {
		g, ok := store.ownedGrantLocked(runID)
		lease := store.execution[runID].lease
		safe = mutation.ManagedSafePoint.Point()
		if !ok || g.settledRevision != 0 || g.completion.GrantID != "" || store.execution[runID].quarantineRev != 0 || !activeMemoryLease(store.execution[runID], lease, store.currentExecutionTime()) || safe.Host.KernelAuthority != store.managedAuthority || !mutation.ManagedSafePoint.MatchesMutation(current, mutation.Record, lease, g.grant, mutation.Events) {
			return kernel.Snapshot{}, kernel.ErrExecutionConflict
		}
		if _, exists := store.managedTransfers[safe.ID]; exists {
			return kernel.Snapshot{}, kernel.ErrExecutionConflict
		}
	}
	next := snapshotFromRecord(mutation.Record)
	journal := store.events[runID]
	nextEvents := appendEvents(journal, mutation.Events, mutation.Record.Run.UpdatedAt)
	next.EventHead = int64(len(nextEvents))
	if safe.ID != "" && !kernel.ManagedSourceMatches(safe, next) {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	ledger, err := kernel.PlanRunBudgetMutation(ctx, store.ledgers, current.Run, mutation, control)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if safe.ID != "" {
		store.saveManagedLocked(kernel.ManagedInitialTransfer(safe))
	}
	if ledger != nil {
		store.ledgers[ledger.ScopeID()] = ledger.Next()
	}
	store.records[runID] = cloneSnapshot(next)
	store.events[runID] = nextEvents
	store.appendCommittedTransition(mutation.Record.Run, mutation.Events)
	return cloneSnapshot(next), nil
}

// ClaimTransitions leases committed transitions for one projector.
func (store *Store) ClaimTransitions(
	_ context.Context,
	request kernel.TransitionClaimRequest,
) ([]kernel.TransitionClaim, error) {
	if store == nil || strings.TrimSpace(request.WorkerID) == "" || request.Limit <= 0 ||
		request.LeaseDuration <= 0 || request.Now.IsZero() {
		return nil, kernel.ErrInvalidInput
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	claims := make([]kernel.TransitionClaim, 0, request.Limit)
	for _, id := range store.transitionOrder {
		if len(claims) >= request.Limit {
			break
		}
		state, ok := store.transitions[id]
		if !ok || state.availableAt.After(request.Now) ||
			(!state.leaseUntil.IsZero() && state.leaseUntil.After(request.Now)) {
			continue
		}
		state.transition.Attempts++
		state.workerID = strings.TrimSpace(request.WorkerID)
		state.leaseID = fmt.Sprintf("%s:%s:%d", state.workerID, id, state.transition.Attempts)
		state.leaseUntil = request.Now.UTC().Add(request.LeaseDuration)
		store.transitions[id] = state
		claims = append(claims, kernel.TransitionClaim{
			Transition: cloneTransition(state.transition), LeaseID: state.leaseID,
			WorkerID: state.workerID, LeaseUntil: state.leaseUntil,
		})
	}
	return claims, nil
}

// AckTransition removes one successfully projected outbox record.
func (store *Store) AckTransition(_ context.Context, request kernel.TransitionLeaseRequest) error {
	if store == nil || !validTransitionLease(request) {
		return kernel.ErrInvalidInput
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, ok := store.transitions[request.TransitionID]
	if !ok {
		return kernel.ErrNotFound
	}
	if state.leaseID != request.LeaseID || state.workerID != request.WorkerID {
		return kernel.ErrConflict
	}
	delete(store.transitions, request.TransitionID)
	store.compactTransitionOrder()
	return nil
}

// RetryTransition releases one failed projection and optionally delays retry.
func (store *Store) RetryTransition(_ context.Context, request kernel.TransitionRetryRequest) error {
	if store == nil || !validTransitionLease(request.TransitionLeaseRequest) {
		return kernel.ErrInvalidInput
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, ok := store.transitions[request.TransitionID]
	if !ok {
		return kernel.ErrNotFound
	}
	if state.leaseID != request.LeaseID || state.workerID != request.WorkerID {
		return kernel.ErrConflict
	}
	state.availableAt = request.AvailableAt.UTC()
	state.leaseID = ""
	state.workerID = ""
	state.leaseUntil = time.Time{}
	store.transitions[request.TransitionID] = state
	return nil
}

func (store *Store) appendCommittedTransition(run kernel.Run, events []kernel.EventDraft) {
	if !kernel.NeedsTransitionProjection(run.Status, events) {
		return
	}
	id := committedTransitionID(run.ID, run.Revision)
	store.transitions[id] = transitionState{transition: kernel.CommittedTransition{
		ID: id, RunID: run.ID, Kind: run.Kind, Status: run.Status, Revision: run.Revision,
		Events: cloneEventDrafts(events), CommittedAt: run.UpdatedAt.UTC(),
	}}
	store.transitionOrder = append(store.transitionOrder, id)
}

func (store *Store) compactTransitionOrder() {
	for len(store.transitionOrder) > 0 {
		if _, exists := store.transitions[store.transitionOrder[0]]; exists {
			return
		}
		store.transitionOrder = store.transitionOrder[1:]
	}
}

func committedTransitionID(runID string, revision uint64) string {
	return runID + ":" + strconv.FormatUint(revision, 10)
}

func validTransitionLease(request kernel.TransitionLeaseRequest) bool {
	return strings.TrimSpace(request.TransitionID) != "" && strings.TrimSpace(request.LeaseID) != "" &&
		strings.TrimSpace(request.WorkerID) != ""
}

func cloneTransition(value kernel.CommittedTransition) kernel.CommittedTransition {
	value.Events = cloneEventDrafts(value.Events)
	return value
}

func cloneEventDrafts(values []kernel.EventDraft) []kernel.EventDraft {
	result := make([]kernel.EventDraft, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Data = cloneJSON(value.Data)
		if value.WakeupAt != nil {
			wakeupAt := value.WakeupAt.UTC()
			result[index].WakeupAt = &wakeupAt
		}
	}
	return result
}

func snapshotFromRecord(record kernel.Record) kernel.Snapshot {
	return kernel.Snapshot{
		Run: record.Run, State: cloneJSON(record.State),
		Checkpoint: cloneCheckpoint(record.Checkpoint), Result: cloneResult(record.Result),
	}
}

func appendEvents(current []kernel.Event, drafts []kernel.EventDraft, createdAt time.Time) []kernel.Event {
	result := current
	for _, draft := range drafts {
		result = append(result, kernel.Event{
			Seq: int64(len(result) + 1), Type: draft.Type, Message: draft.Message,
			Data: cloneJSON(draft.Data), CreatedAt: createdAt.UTC(),
		})
	}
	return result
}

func cloneSnapshot(snapshot kernel.Snapshot) kernel.Snapshot {
	snapshot.Run = cloneRun(snapshot.Run)
	snapshot.State = cloneJSON(snapshot.State)
	snapshot.Checkpoint = cloneCheckpoint(snapshot.Checkpoint)
	snapshot.Result = cloneResult(snapshot.Result)
	return snapshot
}

func cloneEvents(values []kernel.Event) []kernel.Event {
	result := make([]kernel.Event, len(values))
	for index, event := range values {
		result[index] = event
		result[index].Data = cloneJSON(event.Data)
	}
	return result
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneCheckpoint(value *kernel.Checkpoint) *kernel.Checkpoint {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Payload = cloneJSON(value.Payload)
	cloned.Response = cloneJSON(value.Response)
	if value.ResolvedAt != nil {
		resolvedAt := *value.ResolvedAt
		cloned.ResolvedAt = &resolvedAt
	}
	return &cloned
}

func cloneResult(value *kernel.Result) *kernel.Result {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Content = cloneJSON(value.Content)
	return &cloned
}
