package memory

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	stateprojection "github.com/orz-i/Gaoge/sdk/go/agent-runtime/projection"
)

type state struct {
	Runs          map[string]domain.Run                    `json:"runs"`
	Steps         map[string][]domain.Step                 `json:"steps"`
	Events        map[string][]domain.Event                `json:"events"`
	Plans         map[string][]domain.Plan                 `json:"plans"`
	Interactions  map[string]map[string]domain.Interaction `json:"interactions"`
	Checkpoints   map[string]map[string]domain.Checkpoint  `json:"checkpoints"`
	Contexts      map[string]domain.ContextSnapshot        `json:"contexts"`
	Artifacts     map[string]domain.ContextArtifact        `json:"artifacts"`
	Outputs       map[string][]domain.OutputRef            `json:"outputs"`
	Evidence      map[string]domain.Evidence               `json:"evidence"`
	Queue         map[string]domain.QueueItem              `json:"queue"`
	Continuations map[string]domain.ContinuationJob        `json:"continuations"`
	Manifests     map[string][]domain.AgentManifest        `json:"manifests"`
	Workflows     map[string][]domain.WorkflowDefinition   `json:"workflows"`
	Executions    map[string]domain.WorkflowExecution      `json:"executions"`
	Results       map[string]domain.RunResult              `json:"results"`
	WorkflowCache map[string]domain.WorkflowCacheEntry     `json:"workflowCache"`
	Handoffs      map[string]domain.RunHandoff             `json:"handoffs"`
	HandoffJoins  map[string]domain.RunHandoffJoin         `json:"handoffJoins"`
	Workbench     map[string]domain.WorkbenchProjection    `json:"workbench"`
	Phases        map[string][]domain.PhaseProjection      `json:"phases"`
}

type Store struct {
	mu    sync.RWMutex
	state state
}

var _ agentruntime.Store = (*Store)(nil)

func NewStore() *Store { return &Store{state: newState()} }

func newState() state {
	return state{
		Runs: make(map[string]domain.Run), Steps: make(map[string][]domain.Step), Events: make(map[string][]domain.Event),
		Plans: make(map[string][]domain.Plan), Interactions: make(map[string]map[string]domain.Interaction),
		Checkpoints: make(map[string]map[string]domain.Checkpoint), Contexts: make(map[string]domain.ContextSnapshot),
		Artifacts: make(map[string]domain.ContextArtifact), Outputs: make(map[string][]domain.OutputRef),
		Evidence: make(map[string]domain.Evidence), Queue: make(map[string]domain.QueueItem), Continuations: make(map[string]domain.ContinuationJob),
		Manifests: make(map[string][]domain.AgentManifest), Workflows: make(map[string][]domain.WorkflowDefinition),
		Executions: make(map[string]domain.WorkflowExecution), Results: make(map[string]domain.RunResult), WorkflowCache: make(map[string]domain.WorkflowCacheEntry),
		Handoffs: make(map[string]domain.RunHandoff), HandoffJoins: make(map[string]domain.RunHandoffJoin),
		Workbench: make(map[string]domain.WorkbenchProjection), Phases: make(map[string][]domain.PhaseProjection),
	}
}

func (s *Store) write(work func(*state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	backup := clone(s.state)
	if err := work(&s.state); err != nil {
		s.state = backup
		return err
	}
	return nil
}

func clone[T any](value T) T {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var copied T
	if err = json.Unmarshal(raw, &copied); err != nil {
		panic(err)
	}
	return copied
}

func owned(run domain.Run, actor domain.ActorRef) bool {
	return run.Actor == actor
}

func terminal(status string) bool {
	return status == domain.RunStatusCompleted || status == domain.RunStatusFailed || status == domain.RunStatusCancelled
}

func appendEvent(st *state, item domain.Event) (domain.Event, bool, error) {
	run, ok := st.Runs[item.RunID]
	if !ok {
		return domain.Event{}, false, agentruntime.ErrNotFound
	}
	for _, existing := range st.Events[item.RunID] {
		if existing.EventID == item.EventID {
			return clone(existing), false, nil
		}
	}
	if strings.TrimSpace(item.EventID) == "" {
		return domain.Event{}, false, agentruntime.ErrInvalidInput
	}
	item.Seq = run.LastEventSeq + 1
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	st.Events[item.RunID] = append(st.Events[item.RunID], clone(item))
	var stepProjection *domain.Step
	var stepIndex int
	for index := range st.Steps[item.RunID] {
		if st.Steps[item.RunID][index].StepID == item.StepID {
			value := clone(st.Steps[item.RunID][index])
			stepProjection, stepIndex = &value, index
			break
		}
	}
	if err := stateprojection.ApplyEvent(&run, stepProjection, item); err != nil {
		return domain.Event{}, false, err
	}
	run.LastEventSeq = item.Seq
	if item.EventType != "message.delta" {
		run.LastPresentationEventSeq = item.Seq
	}
	run.UpdatedAt = time.Now()
	if stepProjection != nil {
		stepProjection.UpdatedAt = run.UpdatedAt
		st.Steps[item.RunID][stepIndex] = *stepProjection
	}
	st.Runs[item.RunID] = run
	return clone(item), true, nil
}

func appendEvents(st *state, items []domain.Event) ([]domain.Event, error) {
	saved := make([]domain.Event, 0, len(items))
	for _, item := range items {
		row, _, err := appendEvent(st, item)
		if err != nil {
			return nil, err
		}
		saved = append(saved, row)
	}
	return saved, nil
}

func (s *Store) CreateRunStartBundle(_ context.Context, run *domain.Run, step *domain.Step, snapshot *domain.ContextSnapshot, artifacts []domain.ContextArtifact, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	if run == nil || step == nil || snapshot == nil || checkpoint == nil || run.RunID == "" || len(events) == 0 {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := s.write(func(st *state) error {
		if _, exists := st.Runs[run.RunID]; exists {
			return agentruntime.ErrDuplicate
		}
		now := time.Now()
		if run.CreatedAt.IsZero() {
			run.CreatedAt = now
		}
		if run.UpdatedAt.IsZero() {
			run.UpdatedAt = now
		}
		run.RuntimeKind = domain.NormalizeRuntimeKind(run.RuntimeKind)
		st.Runs[run.RunID] = clone(*run)
		st.Steps[run.RunID] = []domain.Step{clone(*step)}
		st.Contexts[run.RunID] = clone(*snapshot)
		for _, artifact := range artifacts {
			st.Artifacts[artifact.ArtifactID] = clone(artifact)
		}
		checkpoint.ContextSnapshotID = snapshot.SnapshotID
		st.Checkpoints[run.RunID] = map[string]domain.Checkpoint{checkpoint.CheckpointID: clone(*checkpoint)}
		var err error
		saved, err = appendEvents(st, events)
		return err
	})
	return clone(saved), err
}

func (s *Store) GetRun(_ context.Context, actor domain.ActorRef, runID string) (*domain.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[strings.TrimSpace(runID)]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(run)
	result.RuntimeKind = domain.NormalizeRuntimeKind(result.RuntimeKind)
	return &result, nil
}

func (s *Store) GetRunsByIDs(_ context.Context, actor domain.ActorRef, runIDs []string) ([]domain.Run, error) {
	ids, err := normalizeMemoryRunIDs(runIDs)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Run, 0, len(ids))
	for _, runID := range ids {
		run, ok := s.state.Runs[runID]
		if ok && owned(run, actor) {
			result = append(result, clone(run))
		}
	}
	return result, nil
}

func normalizeMemoryRunIDs(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, agentruntime.ErrInvalidInput
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) GetActiveRun(_ context.Context, actor domain.ActorRef, thread domain.ThreadRef) (*domain.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var candidate *domain.Run
	for _, run := range s.state.Runs {
		if owned(run, actor) && run.Thread == thread && run.EndedAt == nil {
			item := run
			if candidate == nil || item.CreatedAt.After(candidate.CreatedAt) {
				candidate = &item
			}
		}
	}
	if candidate == nil {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(*candidate)
	return &result, nil
}

func (s *Store) ListRuns(_ context.Context, actor domain.ActorRef, thread *domain.ThreadRef, offset, limit int) ([]domain.Run, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Run, 0)
	for _, run := range s.state.Runs {
		if owned(run, actor) && (thread == nil || run.Thread == *thread) {
			items = append(items, clone(run))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	total := int64(len(items))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if offset >= len(items) {
		return []domain.Run{}, total, nil
	}
	end := min(offset+limit, len(items))
	return items[offset:end], total, nil
}

func (s *Store) ListNonterminalRuns(_ context.Context, olderThan time.Time) ([]domain.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Run, 0)
	for _, run := range s.state.Runs {
		if run.EndedAt == nil && run.UpdatedAt.Before(olderThan) {
			items = append(items, clone(run))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items, nil
}

func (s *Store) GetRunCursor(ctx context.Context, actor domain.ActorRef, runID string) (*domain.RunCursor, error) {
	run, err := s.GetRun(ctx, actor, runID)
	if err != nil {
		return nil, err
	}
	return &domain.RunCursor{RuntimeKind: run.RuntimeKind, Status: run.Status, LastEventSeq: run.LastEventSeq, LastPresentationEventSeq: run.LastPresentationEventSeq, CurrentStepID: run.CurrentStepID, PendingInteractionID: run.PendingInteractionID}, nil
}

func (s *Store) ListRunSteps(_ context.Context, runID string) ([]domain.Step, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, ok := s.state.Steps[runID]
	if !ok {
		return []domain.Step{}, nil
	}
	result := clone(items)
	sort.Slice(result, func(i, j int) bool { return result[i].StepIndex < result[j].StepIndex })
	return result, nil
}

func (s *Store) AppendRunEvent(_ context.Context, item *domain.Event) (*domain.Event, bool, error) {
	if item == nil || item.RunID == "" || item.EventID == "" {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var saved domain.Event
	var created bool
	err := s.write(func(st *state) error {
		var err error
		saved, created, err = appendEvent(st, *item)
		return err
	})
	if err == nil {
		*item = clone(saved)
	}
	return item, created, err
}

func (s *Store) AppendRunEvents(_ context.Context, items []domain.Event) ([]domain.Event, error) {
	var saved []domain.Event
	err := s.write(func(st *state) error {
		var err error
		saved, err = appendEvents(st, items)
		return err
	})
	return clone(saved), err
}

func (s *Store) AppendRunEventsIfCurrent(_ context.Context, runID, expectedStatus string, expectedLastEventSeq int64, items []domain.Event) ([]domain.Event, bool, error) {
	var saved []domain.Event
	applied := false
	err := s.write(func(st *state) error {
		run, ok := st.Runs[runID]
		if !ok {
			return agentruntime.ErrNotFound
		}
		if run.Status != expectedStatus || run.LastEventSeq != expectedLastEventSeq {
			return nil
		}
		var err error
		saved, err = appendEvents(st, items)
		applied = err == nil
		return err
	})
	return clone(saved), applied, err
}

func (s *Store) ListRunEventsAfter(ctx context.Context, actor domain.ActorRef, runID string, afterSeq int64, limit int) ([]domain.Event, error) {
	return s.listEvents(ctx, actor, runID, func(seq int64) bool { return seq > afterSeq }, false, limit)
}

func (s *Store) ListRunEventsBefore(ctx context.Context, actor domain.ActorRef, runID string, beforeSeq int64, limit int) ([]domain.Event, error) {
	return s.listEvents(ctx, actor, runID, func(seq int64) bool { return beforeSeq <= 0 || seq < beforeSeq }, true, limit)
}

func (s *Store) listEvents(ctx context.Context, actor domain.ActorRef, runID string, include func(int64) bool, reverse bool, limit int) ([]domain.Event, error) {
	if _, err := s.GetRun(ctx, actor, runID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Event, 0)
	for _, event := range s.state.Events[runID] {
		if include(event.Seq) {
			items = append(items, clone(event))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if reverse {
			return items[i].Seq > items[j].Seq
		}
		return items[i].Seq < items[j].Seq
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Store) GetRunEvent(ctx context.Context, actor domain.ActorRef, runID, eventID string) (*domain.Event, error) {
	items, err := s.ListRunEventsAfter(ctx, actor, runID, -1, 0)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.EventID == eventID {
			result := clone(item)
			return &result, nil
		}
	}
	return nil, agentruntime.ErrNotFound
}

func (s *Store) GetRunToolResult(ctx context.Context, actor domain.ActorRef, runID, toolCallID string) (*domain.Event, error) {
	items, err := s.ListRunEventsAfter(ctx, actor, runID, -1, 0)
	if err != nil {
		return nil, err
	}
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].ToolCallID == toolCallID && (items[index].EventType == "tool.completed" || items[index].EventType == "tool.failed") {
			result := clone(items[index])
			return &result, nil
		}
	}
	return nil, agentruntime.ErrNotFound
}

func (s *Store) CountRunEventsByType(ctx context.Context, actor domain.ActorRef, runID string, eventTypes []string) (map[string]int, error) {
	items, err := s.ListRunEventsAfter(ctx, actor, runID, -1, 0)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(eventTypes))
	for _, kind := range eventTypes {
		allowed[kind] = struct{}{}
	}
	counts := make(map[string]int)
	for _, event := range items {
		if _, ok := allowed[event.EventType]; ok {
			counts[event.EventType]++
		}
	}
	return counts, nil
}

func (s *Store) DeleteRunEventsBefore(_ context.Context, before time.Time) (int64, error) {
	var deleted int64
	err := s.write(func(st *state) error {
		for runID, events := range st.Events {
			kept := events[:0]
			for _, event := range events {
				if event.CreatedAt.Before(before) {
					deleted++
					continue
				}
				kept = append(kept, event)
			}
			st.Events[runID] = kept
		}
		return nil
	})
	return deleted, err
}
