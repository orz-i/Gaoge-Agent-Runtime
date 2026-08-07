package kernel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Clock supplies orchestration time.
type Clock interface {
	Now() time.Time
}

func normalizeDeadline(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}

// Cancel atomically terminates one non-terminal Run while preserving its last feature state.
func (runtime *Runtime) Cancel(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
	reason string,
) (Snapshot, error) {
	current, err := runtime.Load(ctx, runID)
	if err != nil {
		return Snapshot{}, err
	}
	if current.Run.Revision != expectedRevision {
		return Snapshot{}, ErrConflict
	}
	return runtime.Apply(ctx, runID, expectedRevision, Mutation{
		Status: RunStatusCancelled, State: current.State,
		ErrorCode: "run.cancelled", ErrorDetail: strings.TrimSpace(reason),
		Events: []EventDraft{{Type: "run.cancelled", Message: strings.TrimSpace(reason)}},
	})
}

// Expire fails one Run only when its frozen DeadlineAt is due.
func (runtime *Runtime) Expire(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
) (Snapshot, bool, error) {
	current, err := runtime.Load(ctx, runID)
	if err != nil {
		return Snapshot{}, false, err
	}
	if current.Run.Revision != expectedRevision {
		return Snapshot{}, false, ErrConflict
	}
	if current.Run.DeadlineAt == nil || current.Run.DeadlineAt.After(runtime.Now()) {
		return current, false, nil
	}
	expired, err := runtime.Apply(ctx, runID, expectedRevision, Mutation{
		Status: RunStatusFailed, State: current.State,
		ErrorCode: "run.deadline_exceeded", ErrorDetail: ErrDeadline.Error(),
		Events: []EventDraft{{Type: "run.deadline_exceeded", Message: ErrDeadline.Error()}},
	})
	return expired, true, err
}

// IDSource supplies non-deterministic public identifiers.
type IDSource interface {
	NewID(string) (string, error)
}

// Dependencies are the only requirements of the minimal Kernel.
type Dependencies struct {
	Store       Store
	Clock       Clock
	IDs         IDSource
	Transitions TransitionSink
}

// Runtime owns feature-neutral Run transitions and no background workers.
type Runtime struct {
	store       Store
	clock       Clock
	ids         IDSource
	transitions TransitionSink
}

// New creates a minimal Runtime with no optional feature dependencies.
func New(dependencies Dependencies) (*Runtime, error) {
	if dependencies.Store == nil {
		return nil, ErrInvalidInput
	}
	if dependencies.Clock == nil {
		dependencies.Clock = systemClock{}
	}
	if dependencies.IDs == nil {
		dependencies.IDs = randomIDSource{}
	}
	return &Runtime{
		store: dependencies.Store, clock: dependencies.Clock,
		ids: dependencies.IDs, transitions: dependencies.Transitions,
	}, nil
}

// Descriptor exposes only the minimal Kernel capability.
func (runtime *Runtime) Descriptor() FeatureDescriptor {
	return FeatureDescriptor{Name: "kernel", Provides: []Capability{CapabilityRuntime}}
}

// Create starts one explicitly selected Runtime Kind.
func (runtime *Runtime) Create(ctx context.Context, request CreateRequest) (Snapshot, error) {
	if runtime == nil || !validCreateRequest(request) {
		return Snapshot{}, ErrInvalidInput
	}
	runID := strings.TrimSpace(request.ID)
	if runID == "" {
		var err error
		runID, err = runtime.NewID("run")
		if err != nil {
			return Snapshot{}, err
		}
	}
	now := runtime.Now()
	deadlineAt := normalizeDeadline(request.DeadlineAt)
	if deadlineAt != nil && !deadlineAt.After(now) {
		return Snapshot{}, ErrDeadline
	}
	record := Record{
		Run: Run{
			ID: runID, Kind: request.Kind, Actor: request.Actor, Thread: request.Thread,
			RequestID: strings.TrimSpace(request.RequestID), Goal: strings.TrimSpace(request.Goal),
			Status: RunStatusRunning, Revision: 1, DeadlineAt: deadlineAt, CreatedAt: now, UpdatedAt: now,
		},
		State: cloneJSON(request.State),
	}
	events := append([]EventDraft{{Type: "run.created", Message: "Run created"}}, request.Events...)
	snapshot, err := runtime.store.Create(ctx, record, events)
	if err == nil {
		runtime.observe(ctx, Transition{Current: snapshot, Events: events})
	}
	return snapshot, err
}

// Load returns one atomic Run snapshot.
func (runtime *Runtime) Load(ctx context.Context, runID string) (Snapshot, error) {
	if runtime == nil || strings.TrimSpace(runID) == "" {
		return Snapshot{}, ErrInvalidInput
	}
	return runtime.store.Load(ctx, strings.TrimSpace(runID))
}

// Apply validates and atomically commits one feature-owned state transition.
func (runtime *Runtime) Apply(ctx context.Context, runID string, expectedRevision uint64, mutation Mutation) (Snapshot, error) {
	if runtime == nil || strings.TrimSpace(runID) == "" || expectedRevision == 0 {
		return Snapshot{}, ErrInvalidInput
	}
	current, err := runtime.store.Load(ctx, strings.TrimSpace(runID))
	if err != nil {
		return Snapshot{}, err
	}
	if current.Run.Revision != expectedRevision {
		return Snapshot{}, ErrConflict
	}
	if err = validateMutation(current, mutation); err != nil {
		return Snapshot{}, err
	}
	next := current.Run
	next.Status = mutation.Status
	next.Revision++
	next.ErrorCode = strings.TrimSpace(mutation.ErrorCode)
	next.ErrorDetail = strings.TrimSpace(mutation.ErrorDetail)
	now := runtime.Now()
	next.UpdatedAt = now
	if terminalStatus(next.Status) {
		endedAt := now
		next.EndedAt = &endedAt
	}
	record := Record{
		Run: next, State: cloneJSON(mutation.State),
		Checkpoint: cloneCheckpoint(mutation.Checkpoint), Result: cloneResult(mutation.Result),
	}
	updated, err := runtime.store.Apply(ctx, runID, expectedRevision, StoreMutation{Record: record, Events: mutation.Events})
	if err == nil {
		previous := cloneSnapshot(current)
		runtime.observe(ctx, Transition{Previous: &previous, Current: updated, Events: mutation.Events})
	}
	return updated, err
}

// NewID creates a public identifier through the configured entropy source.
func (runtime *Runtime) NewID(prefix string) (string, error) {
	if runtime == nil || runtime.ids == nil {
		return "", ErrInvalidInput
	}
	return runtime.ids.NewID(strings.TrimSpace(prefix))
}

// Now returns the configured orchestration time in UTC.
func (runtime *Runtime) Now() time.Time {
	if runtime == nil || runtime.clock == nil {
		return time.Time{}
	}
	return runtime.clock.Now().UTC()
}

func (runtime *Runtime) observe(ctx context.Context, transition Transition) {
	if runtime == nil || runtime.transitions == nil {
		return
	}
	transition.Current = cloneSnapshot(transition.Current)
	if transition.Previous != nil {
		previous := cloneSnapshot(*transition.Previous)
		transition.Previous = &previous
	}
	transition.Events = cloneEventDrafts(transition.Events)
	runtime.transitions.ObserveTransition(ctx, transition)
}

func validCreateRequest(request CreateRequest) bool {
	return validRunKind(request.Kind) && validActor(request.Actor) && validThread(request.Thread) &&
		strings.TrimSpace(request.Goal) != "" && validState(request.State)
}

func validateMutation(current Snapshot, mutation Mutation) error {
	if terminalStatus(current.Run.Status) {
		return ErrTerminal
	}
	if !validTransition(current.Run.Status, mutation.Status) || !validState(mutation.State) {
		return ErrInvalidInput
	}
	if !validMutationCheckpoint(mutation) || !validMutationResult(mutation) {
		return ErrInvalidInput
	}
	return nil
}

func validMutationCheckpoint(mutation Mutation) bool {
	if mutation.Status == RunStatusWaitingInput {
		return mutation.Checkpoint != nil && mutation.Checkpoint.Status == CheckpointPending &&
			validCheckpoint(*mutation.Checkpoint)
	}
	return mutation.Checkpoint == nil || validCheckpoint(*mutation.Checkpoint)
}

func validMutationResult(mutation Mutation) bool {
	if mutation.Status == RunStatusCompleted {
		return mutation.Result != nil && validResult(*mutation.Result)
	}
	return mutation.Result == nil
}

func validTransition(current, next RunStatus) bool {
	switch current {
	case RunStatusRunning:
		return next == RunStatusRunning || next == RunStatusWaitingInput || terminalStatus(next)
	case RunStatusWaitingInput:
		return next == RunStatusRunning || terminalStatus(next)
	default:
		return false
	}
}

func validRunKind(kind RunKind) bool {
	return kind == RunKindAgent || kind == RunKindPlanExecute || kind == RunKindWorkflow || kind == RunKindTeam
}

func validActor(actor ActorRef) bool {
	return strings.TrimSpace(actor.TenantID) != "" && strings.TrimSpace(actor.ActorID) != ""
}

func validThread(thread ThreadRef) bool {
	return strings.TrimSpace(thread.Kind) != "" && strings.TrimSpace(thread.ID) != ""
}

func validState(state json.RawMessage) bool {
	return len(state) > 0 && json.Valid(state)
}

func validCheckpoint(checkpoint Checkpoint) bool {
	if strings.TrimSpace(checkpoint.ID) == "" || strings.TrimSpace(checkpoint.Kind) == "" ||
		!json.Valid(checkpoint.Payload) || checkpoint.CreatedAt.IsZero() {
		return false
	}
	if checkpoint.Status == CheckpointPending {
		return checkpoint.ResolvedAt == nil && len(checkpoint.Response) == 0
	}
	return checkpoint.Status == CheckpointResolved && checkpoint.ResolvedAt != nil && json.Valid(checkpoint.Response)
}

func validResult(result Result) bool {
	return strings.TrimSpace(result.ContentType) != "" && len(result.Content) > 0 && json.Valid(result.Content)
}

func terminalStatus(status RunStatus) bool {
	return status == RunStatusCompleted || status == RunStatusFailed || status == RunStatusCancelled
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneCheckpoint(value *Checkpoint) *Checkpoint {
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

func cloneResult(value *Result) *Result {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Content = cloneJSON(value.Content)
	return &cloned
}

func cloneSnapshot(value Snapshot) Snapshot {
	value.State = cloneJSON(value.State)
	value.Checkpoint = cloneCheckpoint(value.Checkpoint)
	value.Result = cloneResult(value.Result)
	value.Events = append([]Event(nil), value.Events...)
	for index := range value.Events {
		value.Events[index].Data = cloneJSON(value.Events[index].Data)
	}
	return value
}

func cloneEventDrafts(values []EventDraft) []EventDraft {
	result := append([]EventDraft(nil), values...)
	for index := range result {
		result[index].Data = cloneJSON(result[index].Data)
	}
	return result
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type randomIDSource struct{}

func (randomIDSource) NewID(prefix string) (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", errors.Join(ErrInvalidInput, err)
	}
	encoded := hex.EncodeToString(bytes[:])
	if prefix == "" {
		return encoded, nil
	}
	return prefix + "_" + encoded, nil
}
