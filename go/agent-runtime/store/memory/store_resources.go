package memory

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Store) CreateContextSnapshotBundle(_ context.Context, snapshot *domain.ContextSnapshot, artifacts []domain.ContextArtifact, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	if snapshot == nil || checkpoint == nil {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := s.write(func(st *state) error {
		run, ok := st.Runs[snapshot.RunID]
		if !ok {
			return agentruntime.ErrNotFound
		}
		if snapshot.Revision <= 0 {
			snapshot.Revision = len(st.Contexts[snapshot.RunID]) + 1
		}
		if snapshot.ManagementStatus == "" {
			snapshot.ManagementStatus = domain.ContextManagementStatusBaseline
		}
		for _, existing := range st.Contexts[snapshot.RunID] {
			if existing.SnapshotID != snapshot.SnapshotID {
				continue
			}
			if existing.ContentHash == snapshot.ContentHash && existing.Revision == snapshot.Revision {
				return nil
			}
			return agentruntime.ErrDuplicate
		}
		st.Contexts[snapshot.RunID] = append(st.Contexts[snapshot.RunID], clone(*snapshot))
		for _, artifact := range artifacts {
			if err := putContextArtifact(st, artifact); err != nil {
				return err
			}
		}
		if st.Checkpoints[snapshot.RunID] == nil {
			st.Checkpoints[snapshot.RunID] = make(map[string]domain.Checkpoint)
		}
		st.Checkpoints[snapshot.RunID][checkpoint.CheckpointID] = clone(*checkpoint)
		run.CurrentStepID, run.UpdatedAt = checkpoint.StepID, time.Now()
		st.Runs[snapshot.RunID] = run
		var err error
		saved, err = appendEvents(st, events)
		return err
	})
	return clone(saved), err
}

func (s *Store) GetRunContextSnapshot(_ context.Context, actor domain.ActorRef, runID string) (*domain.ContextSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[runID]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	items := s.state.Contexts[runID]
	if len(items) == 0 {
		return nil, agentruntime.ErrNotFound
	}
	item := items[0]
	for _, candidate := range items[1:] {
		if candidate.Revision > item.Revision || candidate.Revision == item.Revision && candidate.CreatedAt.After(item.CreatedAt) {
			item = candidate
		}
	}
	result := clone(item)
	return &result, nil
}

func (s *Store) CreateContextArtifacts(_ context.Context, artifacts []domain.ContextArtifact) error {
	return s.write(func(st *state) error {
		for _, item := range artifacts {
			if item.ArtifactID == "" {
				return agentruntime.ErrInvalidInput
			}
			if err := putContextArtifact(st, item); err != nil {
				return err
			}
		}
		return nil
	})
}

func putContextArtifact(st *state, item domain.ContextArtifact) error {
	if item.ArtifactID == "" {
		return agentruntime.ErrInvalidInput
	}
	if existing, ok := st.Artifacts[item.ArtifactID]; ok {
		if existing.ContentHash == item.ContentHash {
			return nil
		}
		return agentruntime.ErrDuplicate
	}
	st.Artifacts[item.ArtifactID] = clone(item)
	return nil
}

func (s *Store) ListRecentContextArtifacts(_ context.Context, actor domain.ActorRef, thread domain.ThreadRef, limit int) ([]domain.ContextArtifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.ContextArtifact, 0)
	for _, item := range s.state.Artifacts {
		run, ok := s.state.Runs[item.RunID]
		if ok && owned(run, actor) && run.Thread == thread {
			items = append(items, clone(item))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Store) ListRecentContextArtifactsByKind(_ context.Context, actor domain.ActorRef, thread domain.ThreadRef, kind domain.ContextArtifactKind, limit int) ([]domain.ContextArtifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.ContextArtifact, 0)
	for _, item := range s.state.Artifacts {
		run, ok := s.state.Runs[item.RunID]
		if ok && owned(run, actor) && run.Thread == thread && item.Kind == kind {
			items = append(items, clone(item))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Store) GetContextArtifact(_ context.Context, actor domain.ActorRef, artifactID string) (*domain.ContextArtifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.state.Artifacts[artifactID]
	if !ok {
		return nil, agentruntime.ErrNotFound
	}
	run, ok := s.state.Runs[item.RunID]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}

func (s *Store) DeleteExpiredContextArtifacts(_ context.Context, now time.Time, limit int) (int64, error) {
	var deleted int64
	err := s.write(func(st *state) error {
		for id, item := range st.Artifacts {
			if limit > 0 && deleted >= int64(limit) {
				break
			}
			if item.ExpiresAt != nil && !item.ExpiresAt.After(now) {
				delete(st.Artifacts, id)
				deleted++
			}
		}
		return nil
	})
	return deleted, err
}

func putOutput(st *state, output domain.OutputRef) (domain.OutputRef, bool, error) {
	if output.OutputID == "" || output.RunID == "" {
		return domain.OutputRef{}, false, agentruntime.ErrInvalidInput
	}
	versions := st.Outputs[output.OutputID]
	for _, existing := range versions {
		if existing.SourceEventID != "" && existing.SourceEventID == output.SourceEventID {
			return clone(existing), false, nil
		}
		if output.Version > 0 && existing.Version == output.Version {
			return domain.OutputRef{}, false, agentruntime.ErrDuplicate
		}
	}
	if output.Version <= 0 {
		output.Version = len(versions) + 1
	}
	if output.CreatedAt.IsZero() {
		output.CreatedAt = time.Now()
	}
	if output.UpdatedAt.IsZero() {
		output.UpdatedAt = output.CreatedAt
	}
	st.Outputs[output.OutputID] = append(versions, clone(output))
	return output, true, nil
}

func (s *Store) FinalizeRun(_ context.Context, input domain.TerminalIntent) (*domain.OutputRef, []domain.Event, bool, error) {
	var output *domain.OutputRef
	var saved []domain.Event
	changed := false
	err := s.write(func(st *state) error {
		run, ok := st.Runs[input.RunID]
		if !ok || !owned(run, input.Actor) {
			return agentruntime.ErrNotFound
		}
		if terminal(run.Status) {
			return nil
		}
		if input.Output != nil {
			item, _, err := putOutput(st, *input.Output)
			if err != nil {
				return err
			}
			output = &item
		}
		if input.Result != nil {
			if input.Outcome != domain.TerminalCompleted || input.Result.RunID != input.RunID {
				return agentruntime.ErrInvalidInput
			}
			if existing, ok := st.Results[input.RunID]; ok && existing.ContentHash != input.Result.ContentHash {
				return agentruntime.ErrWorkflowResultConflict
			}
			result := clone(*input.Result)
			if result.CreatedAt.IsZero() {
				result.CreatedAt = time.Now()
			}
			result.UpdatedAt = result.CreatedAt
			st.Results[input.RunID] = result
		}
		eventType := "run." + input.Outcome
		event := domain.Event{EventID: "terminal:" + input.RunID + ":" + input.Outcome, RunID: input.RunID, EventType: eventType, StepID: input.CurrentStepID, Actor: run.Actor, Thread: run.Thread, Summary: input.Summary, ErrorJSON: input.DiagnosticJSON, CreatedAt: time.Now()}
		var err error
		saved, err = appendEvents(st, []domain.Event{event})
		if err != nil {
			return err
		}
		run = st.Runs[input.RunID]
		run.ErrorCode, run.ErrorMessage = input.ErrorCode, input.ErrorMessage
		st.Runs[input.RunID] = run
		changed = true
		return nil
	})
	return output, clone(saved), changed, err
}

func (s *Store) AppendRunBilling(_ context.Context, runID, segmentKey, currency string, nanousd int64, snapshotJSON string, event domain.Event) (*domain.Event, bool, error) {
	var saved domain.Event
	changed := false
	err := s.write(func(st *state) error {
		run, ok := st.Runs[runID]
		if !ok {
			return agentruntime.ErrNotFound
		}
		for _, existing := range st.Events[runID] {
			if existing.EventID == event.EventID {
				saved = existing
				return nil
			}
		}
		run.BilledCurrency, run.BilledNanousd, run.LastBillingSnapshotJSON = currency, run.BilledNanousd+nanousd, snapshotJSON
		_ = segmentKey
		st.Runs[runID] = run
		item, created, err := appendEvent(st, event)
		saved, changed = item, created
		return err
	})
	return &saved, changed, err
}

func (s *Store) CommitRunToolResultBundle(_ context.Context, checkpoint *domain.Checkpoint, output *domain.OutputRef, events []domain.Event) (*domain.OutputRef, []domain.Event, bool, error) {
	if checkpoint == nil {
		return nil, nil, false, agentruntime.ErrInvalidInput
	}
	var result *domain.OutputRef
	var saved []domain.Event
	changed := false
	err := s.write(func(st *state) error {
		if _, ok := st.Runs[checkpoint.RunID]; !ok {
			return agentruntime.ErrNotFound
		}
		if _, ok := st.Checkpoints[checkpoint.RunID][checkpoint.CheckpointID]; ok {
			return nil
		}
		st.Checkpoints[checkpoint.RunID][checkpoint.CheckpointID] = clone(*checkpoint)
		if output != nil {
			item, _, err := putOutput(st, *output)
			if err != nil {
				return err
			}
			result = &item
		}
		var err error
		saved, err = appendEvents(st, events)
		changed = err == nil
		return err
	})
	return result, clone(saved), changed, err
}

func outputOwned(st state, actor domain.ActorRef, item domain.OutputRef) bool {
	run, ok := st.Runs[item.RunID]
	return ok && owned(run, actor)
}

func (s *Store) ListOutputs(_ context.Context, actor domain.ActorRef, runID string) ([]domain.OutputRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[runID]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	items := make([]domain.OutputRef, 0)
	for _, versions := range s.state.Outputs {
		for _, item := range versions {
			if item.RunID == runID {
				items = append(items, clone(item))
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items, nil
}

func (s *Store) GetOutputsByIDs(_ context.Context, actor domain.ActorRef, ids []string) ([]domain.OutputRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.OutputRef, 0, len(ids))
	for _, id := range ids {
		versions := s.state.Outputs[id]
		if len(versions) > 0 {
			item := versions[len(versions)-1]
			if outputOwned(s.state, actor, item) {
				items = append(items, clone(item))
			}
		}
	}
	return items, nil
}

func outputItem(st state, item domain.OutputRef) domain.OutputListItem {
	run := st.Runs[item.RunID]
	return domain.OutputListItem{OutputRef: clone(item), SourceRunGoal: run.Goal, SourceRunStatus: run.Status, SourceRunModel: run.PlatformModelName, Thread: run.Thread}
}

func (s *Store) ListUserOutputs(_ context.Context, actor domain.ActorRef, kind, cursor string, limit int) ([]domain.OutputListItem, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.OutputListItem, 0)
	for _, versions := range s.state.Outputs {
		if len(versions) == 0 {
			continue
		}
		item := versions[len(versions)-1]
		if outputOwned(s.state, actor, item) && (kind == "" || item.Kind == kind) {
			items = append(items, outputItem(s.state, item))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
	offset, _ := strconv.Atoi(cursor)
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if offset >= len(items) {
		return []domain.OutputListItem{}, "", nil
	}
	end := min(offset+limit, len(items))
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	return items[offset:end], next, nil
}

func (s *Store) GetOutputVersion(_ context.Context, actor domain.ActorRef, outputID string, version int) (*domain.OutputListItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.state.Outputs[outputID] {
		if item.Version == version && outputOwned(s.state, actor, item) {
			result := outputItem(s.state, item)
			return &result, nil
		}
	}
	return nil, agentruntime.ErrNotFound
}

func (s *Store) ListOutputVersions(_ context.Context, actor domain.ActorRef, outputID string, offset, limit int) ([]domain.OutputListItem, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions := s.state.Outputs[outputID]
	items := make([]domain.OutputListItem, 0, len(versions))
	for _, item := range versions {
		if outputOwned(s.state, actor, item) {
			items = append(items, outputItem(s.state, item))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Version > items[j].Version })
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if offset >= len(items) {
		return []domain.OutputListItem{}, false, nil
	}
	end := min(offset+limit, len(items))
	return items[offset:end], end < len(items), nil
}

func (s *Store) CreateEvidence(_ context.Context, item *domain.Evidence) error {
	if item == nil || item.EvidenceID == "" {
		return agentruntime.ErrInvalidInput
	}
	return s.write(func(st *state) error {
		if existing, ok := st.Evidence[item.EvidenceID]; ok {
			if existing.ContentHash == item.ContentHash {
				return nil
			}
			return agentruntime.ErrDuplicate
		}
		st.Evidence[item.EvidenceID] = clone(*item)
		return nil
	})
}

func (s *Store) GetEvidenceByIDs(_ context.Context, actor domain.ActorRef, ids []string) ([]domain.Evidence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Evidence, 0, len(ids))
	for _, id := range ids {
		if item, ok := s.state.Evidence[id]; ok && item.Actor == actor {
			items = append(items, clone(item))
		}
	}
	return items, nil
}

func (s *Store) CreateRunQueueItem(_ context.Context, item *domain.QueueItem) (*domain.QueueItem, bool, error) {
	if item == nil || item.QueueID == "" {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var result domain.QueueItem
	reused := false
	err := s.write(func(st *state) error {
		for _, existing := range st.Queue {
			if existing.Actor == item.Actor && existing.Thread == item.Thread && existing.ClientQueueID == item.ClientQueueID {
				if existing.RequestFingerprint != item.RequestFingerprint {
					return agentruntime.ErrRunQueueConflict
				}
				result = existing
				reused = true
				return nil
			}
		}
		now := time.Now()
		item.Revision = 1
		if item.Status == "" {
			item.Status = domain.QueueQueued
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		item.UpdatedAt = now
		st.Queue[item.QueueID] = clone(*item)
		result = *item
		reposition(st, item.Actor, item.Thread)
		return nil
	})
	return &result, reused, err
}

func reposition(st *state, actor domain.ActorRef, thread domain.ThreadRef) {
	items := make([]domain.QueueItem, 0)
	for _, item := range st.Queue {
		if item.Actor == actor && item.Thread == thread && item.Status == domain.QueueQueued {
			items = append(items, item)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Position != items[j].Position {
			return items[i].Position < items[j].Position
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	for i, item := range items {
		item.Position = i + 1
		st.Queue[item.QueueID] = item
	}
}

func (s *Store) GetRunQueueItem(_ context.Context, actor domain.ActorRef, thread domain.ThreadRef, id string) (*domain.QueueItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.state.Queue[id]
	if !ok || item.Actor != actor || item.Thread != thread {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}
func (s *Store) ListRunQueueItems(_ context.Context, actor domain.ActorRef, thread domain.ThreadRef) ([]domain.QueueItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.QueueItem, 0)
	for _, item := range s.state.Queue {
		if item.Actor == actor && item.Thread == thread {
			items = append(items, clone(item))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Position < items[j].Position })
	return items, nil
}
func (s *Store) UpdateRunQueueItem(_ context.Context, item *domain.QueueItem, expected int) error {
	if item == nil {
		return agentruntime.ErrInvalidInput
	}
	return s.write(func(st *state) error {
		current, ok := st.Queue[item.QueueID]
		if !ok {
			return agentruntime.ErrNotFound
		}
		if current.Revision != expected {
			return agentruntime.ErrRunQueueConflict
		}
		item.Revision = expected + 1
		item.UpdatedAt = time.Now()
		st.Queue[item.QueueID] = clone(*item)
		reposition(st, item.Actor, item.Thread)
		return nil
	})
}
func (s *Store) CancelRunQueueItem(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef, id string) (*domain.QueueItem, error) {
	item, err := s.GetRunQueueItem(ctx, actor, thread, id)
	if err != nil {
		return nil, err
	}
	item.Status = domain.QueueCancelled
	if err = s.UpdateRunQueueItem(ctx, item, item.Revision); err != nil {
		return nil, err
	}
	return item, nil
}
func (s *Store) PrioritizeRunQueueItem(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef, id string) (*domain.QueueItem, error) {
	item, err := s.GetRunQueueItem(ctx, actor, thread, id)
	if err != nil {
		return nil, err
	}
	item.Position = 0
	if err = s.UpdateRunQueueItem(ctx, item, item.Revision); err != nil {
		return nil, err
	}
	return item, nil
}
func (s *Store) ClaimNextRunQueueItem(_ context.Context, now time.Time) (*domain.QueueItem, error) {
	var result domain.QueueItem
	err := s.write(func(st *state) error {
		var candidate *domain.QueueItem
		for _, item := range st.Queue {
			if item.Status != domain.QueueQueued || (item.NextAttemptAt != nil && item.NextAttemptAt.After(now)) {
				continue
			}
			copy := item
			if candidate == nil || copy.CreatedAt.Before(candidate.CreatedAt) {
				candidate = &copy
			}
		}
		if candidate == nil {
			return agentruntime.ErrNotFound
		}
		candidate.Status = domain.QueueDispatching
		candidate.Revision++
		candidate.AttemptCount++
		candidate.UpdatedAt = now
		st.Queue[candidate.QueueID] = *candidate
		result = *candidate
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
func (s *Store) MarkRunQueueStarted(_ context.Context, id, runID string) error {
	return s.write(func(st *state) error {
		item, ok := st.Queue[id]
		if !ok {
			return agentruntime.ErrNotFound
		}
		if item.Status != domain.QueueDispatching {
			return agentruntime.ErrRunQueueConflict
		}
		item.Status, item.StartedRunID = domain.QueueStarted, runID
		item.Revision++
		item.UpdatedAt = time.Now()
		st.Queue[id] = item
		return nil
	})
}
func (s *Store) RequeueRunQueueItem(_ context.Context, id, code, message string, next *time.Time) error {
	return s.write(func(st *state) error {
		item, ok := st.Queue[id]
		if !ok {
			return agentruntime.ErrNotFound
		}
		item.Status, item.ErrorCode, item.ErrorMessage, item.NextAttemptAt = domain.QueueQueued, code, message, next
		item.Revision++
		item.UpdatedAt = time.Now()
		st.Queue[id] = item
		reposition(st, item.Actor, item.Thread)
		return nil
	})
}

func (s *Store) LoadWorkbenchSnapshot(_ context.Context, actor domain.ActorRef, runID string) (*domain.WorkbenchSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[runID]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	result := domain.WorkbenchSnapshot{Run: clone(run), Steps: clone(s.state.Steps[runID]), Plans: clone(s.state.Plans[runID]), Outputs: []domain.OutputRef{}, Events: clone(s.state.Events[runID]), Phases: clone(s.state.Phases[runID])}
	if contextItems := s.state.Contexts[runID]; len(contextItems) > 0 {
		contextItem := contextItems[0]
		for _, candidate := range contextItems[1:] {
			if candidate.Revision > contextItem.Revision || candidate.Revision == contextItem.Revision && candidate.CreatedAt.After(contextItem.CreatedAt) {
				contextItem = candidate
			}
		}
		item := clone(contextItem)
		result.Context = &item
	}
	for _, item := range s.state.Interactions[runID] {
		result.Interactions = append(result.Interactions, clone(item))
	}
	for _, item := range s.state.Checkpoints[runID] {
		result.Checkpoints = append(result.Checkpoints, clone(item))
	}
	for _, versions := range s.state.Outputs {
		for _, item := range versions {
			if item.RunID == runID {
				result.Outputs = append(result.Outputs, clone(item))
			}
		}
	}
	if item, ok := s.state.Workbench[runID]; ok {
		copied := clone(item)
		result.Projection = &copied
	}
	if item, ok := s.state.Executions[runID]; ok {
		copied := clone(item)
		result.Workflow = &copied
	}
	if item, ok := s.state.Results[runID]; ok {
		copied := clone(item)
		result.Result = &copied
	}
	return &result, nil
}
func (s *Store) ReplaceWorkbenchProjection(_ context.Context, actor domain.ActorRef, projection *domain.WorkbenchProjection, phases []domain.PhaseProjection) error {
	if projection == nil {
		return agentruntime.ErrInvalidInput
	}
	return s.write(func(st *state) error {
		run, ok := st.Runs[projection.RunID]
		if !ok || !owned(run, actor) {
			return agentruntime.ErrNotFound
		}
		if current, ok := st.Workbench[projection.RunID]; ok && projection.SourcePresentationEventSeq < current.SourcePresentationEventSeq {
			return agentruntime.ErrDuplicate
		}
		st.Workbench[projection.RunID] = clone(*projection)
		st.Phases[projection.RunID] = clone(phases)
		return nil
	})
}
func (s *Store) ListPresentationEvents(_ context.Context, actor domain.ActorRef, runID string, after int64) ([]domain.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[runID]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	items := make([]domain.Event, 0)
	for _, item := range s.state.Events[runID] {
		if item.Seq > after && item.EventType != "message.delta" {
			items = append(items, clone(item))
		}
	}
	return items, nil
}

var _ = strings.TrimSpace
