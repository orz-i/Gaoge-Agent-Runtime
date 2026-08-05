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

func workflowKey(workflowID string) string { return strings.TrimSpace(workflowID) }

func (s *Store) CreateWorkflowDefinitionRevision(_ context.Context, input *domain.WorkflowDefinition, expectedRevision int) (*domain.WorkflowDefinition, bool, error) {
	if !validMemoryWorkflowDefinition(input) || expectedRevision < 0 {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var result domain.WorkflowDefinition
	var reused bool
	err := s.write(func(st *state) error {
		var err error
		result, reused, err = createMemoryWorkflowDefinitionRevision(st, input, expectedRevision)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return &result, reused, nil
}

func createMemoryWorkflowDefinitionRevision(
	st *state,
	input *domain.WorkflowDefinition,
	expectedRevision int,
) (domain.WorkflowDefinition, bool, error) {
	key := workflowKey(input.WorkflowID)
	revisions := st.Workflows[key]
	if replay, found, err := memoryWorkflowDefinitionReplay(revisions, input); found || err != nil {
		return replay, found, err
	}
	latestRevision, err := validateMemoryWorkflowDefinitionRevision(revisions, input, expectedRevision)
	if err != nil {
		return domain.WorkflowDefinition{}, false, err
	}
	item := newMemoryWorkflowDefinitionRevision(input, key, latestRevision+1)
	st.Workflows[key] = append(revisions, clone(item))
	return item, false, nil
}

func memoryWorkflowDefinitionReplay(
	revisions []domain.WorkflowDefinition,
	input *domain.WorkflowDefinition,
) (domain.WorkflowDefinition, bool, error) {
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return domain.WorkflowDefinition{}, false, nil
	}
	for _, existing := range revisions {
		if existing.RequestID != requestID {
			continue
		}
		if existing.RequestFingerprint != strings.TrimSpace(input.RequestFingerprint) {
			return domain.WorkflowDefinition{}, false, agentruntime.ErrWorkflowDefinitionConflict
		}
		return clone(existing), true, nil
	}
	return domain.WorkflowDefinition{}, false, nil
}

func validateMemoryWorkflowDefinitionRevision(
	revisions []domain.WorkflowDefinition,
	input *domain.WorkflowDefinition,
	expectedRevision int,
) (int, error) {
	if len(revisions) == 0 {
		if expectedRevision != 0 {
			return 0, agentruntime.ErrWorkflowDefinitionConflict
		}
		return 0, nil
	}
	latest := revisions[len(revisions)-1]
	if latest.Scope != input.Scope || latest.TenantID != input.TenantID || latest.OwnerActorID != input.OwnerActorID {
		return 0, agentruntime.ErrWorkflowDefinitionConflict
	}
	if latest.Revision != expectedRevision {
		return 0, agentruntime.ErrWorkflowDefinitionConflict
	}
	return latest.Revision, nil
}

func newMemoryWorkflowDefinitionRevision(
	input *domain.WorkflowDefinition,
	workflowID string,
	revision int,
) domain.WorkflowDefinition {
	item := clone(*input)
	item.WorkflowID = workflowID
	item.Revision = revision
	item.RequestID = strings.TrimSpace(item.RequestID)
	item.RequestFingerprint = strings.TrimSpace(item.RequestFingerprint)
	now := time.Now()
	item.CreatedAt, item.UpdatedAt = now, now
	return item
}

func validMemoryWorkflowDefinition(item *domain.WorkflowDefinition) bool {
	return item != nil &&
		validMemoryWorkflowDefinitionIdentity(*item) &&
		validMemoryWorkflowDefinitionStatus(item.Status) &&
		validMemoryWorkflowDefinitionScope(*item)
}

func validMemoryWorkflowDefinitionIdentity(item domain.WorkflowDefinition) bool {
	return strings.TrimSpace(item.WorkflowID) != "" &&
		item.SchemaVersion == 1 &&
		strings.TrimSpace(item.Name) != "" &&
		strings.TrimSpace(item.DefinitionHash) != "" &&
		strings.TrimSpace(item.DependencyHash) != "" &&
		item.Root.Type == domain.WorkflowNodeSequence
}

func validMemoryWorkflowDefinitionStatus(status string) bool {
	return status == domain.WorkflowDefinitionStatusActive || status == domain.WorkflowDefinitionStatusDisabled
}

func validMemoryWorkflowDefinitionScope(item domain.WorkflowDefinition) bool {
	switch item.Scope {
	case domain.WorkflowDefinitionScopeActor:
		return item.TenantID != "" && item.OwnerActorID != ""
	case domain.WorkflowDefinitionScopeTenant:
		return item.TenantID != "" && item.OwnerActorID == ""
	case domain.WorkflowDefinitionScopeSystem:
		return item.TenantID == "" && item.OwnerActorID == ""
	default:
		return false
	}
}

func (s *Store) GetWorkflowDefinition(_ context.Context, actor domain.ActorRef, ref domain.ResourceRef) (*domain.WorkflowDefinition, error) {
	if strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.ActorID) == "" ||
		ref.Kind != domain.WorkflowDefinitionKind || strings.TrimSpace(ref.ID) == "" {
		return nil, agentruntime.ErrInvalidInput
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	revisions := s.state.Workflows[workflowKey(ref.ID)]
	if len(revisions) == 0 {
		return nil, agentruntime.ErrNotFound
	}
	item, err := selectMemoryWorkflowRevision(revisions, ref.Revision)
	if err != nil {
		return nil, err
	}
	if !domain.WorkflowDefinitionVisibleTo(item, actor) {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}

func selectMemoryWorkflowRevision(revisions []domain.WorkflowDefinition, value string) (domain.WorkflowDefinition, error) {
	if strings.TrimSpace(value) == "" {
		return revisions[len(revisions)-1], nil
	}
	revision, err := strconv.Atoi(value)
	if err != nil || revision <= 0 {
		return domain.WorkflowDefinition{}, agentruntime.ErrInvalidInput
	}
	for _, item := range revisions {
		if item.Revision == revision {
			return item, nil
		}
	}
	return domain.WorkflowDefinition{}, agentruntime.ErrNotFound
}

func (s *Store) ListWorkflowDefinitions(_ context.Context, actor domain.ActorRef, filter domain.WorkflowDefinitionFilter) (domain.WorkflowDefinitionPage, error) {
	if strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.ActorID) == "" {
		return domain.WorkflowDefinitionPage{}, agentruntime.ErrInvalidInput
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.WorkflowDefinition, 0, len(s.state.Workflows))
	for _, revisions := range s.state.Workflows {
		if len(revisions) == 0 {
			continue
		}
		item := revisions[len(revisions)-1]
		if workflowDefinitionMatches(item, actor, filter) {
			items = append(items, clone(item))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].WorkflowID < items[j].WorkflowID
		}
		return items[i].Name < items[j].Name
	})
	total := int64(len(items))
	offset, limit := boundedPage(filter.Offset, filter.Limit, 50, 200)
	if offset >= len(items) {
		return domain.WorkflowDefinitionPage{Total: total, Results: []domain.WorkflowDefinition{}}, nil
	}
	return domain.WorkflowDefinitionPage{Total: total, Results: items[offset:min(offset+limit, len(items))]}, nil
}

func workflowDefinitionMatches(item domain.WorkflowDefinition, actor domain.ActorRef, filter domain.WorkflowDefinitionFilter) bool {
	if !filter.Admin && !domain.WorkflowDefinitionVisibleTo(item, actor) {
		return false
	}
	return (filter.Status == "" || item.Status == filter.Status) &&
		(filter.Scope == "" || item.Scope == filter.Scope) &&
		(filter.TenantID == "" || item.TenantID == filter.TenantID) &&
		(filter.OwnerActorID == "" || item.OwnerActorID == filter.OwnerActorID)
}

func (s *Store) CreateWorkflowRunStartBundle(_ context.Context, run *domain.Run, step *domain.Step, snapshot *domain.ContextSnapshot, artifacts []domain.ContextArtifact, execution *domain.WorkflowExecution, checkpoint *domain.Checkpoint, job *domain.ContinuationJob, events []domain.Event) ([]domain.Event, error) {
	if !validMemoryWorkflowStartBundle(run, step, snapshot, execution, checkpoint, job, events) {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := s.write(func(st *state) error {
		var err error
		saved, err = createMemoryWorkflowRunStartBundle(
			st,
			run,
			step,
			snapshot,
			artifacts,
			execution,
			checkpoint,
			job,
			events,
		)
		return err
	})
	return clone(saved), err
}

func validMemoryWorkflowStartBundle(
	run *domain.Run,
	step *domain.Step,
	snapshot *domain.ContextSnapshot,
	execution *domain.WorkflowExecution,
	checkpoint *domain.Checkpoint,
	job *domain.ContinuationJob,
	events []domain.Event,
) bool {
	if memoryWorkflowStartBundleMissing(run, step, snapshot, execution, checkpoint, job) {
		return false
	}
	return validMemoryWorkflowStartBundleIDs(run, execution, checkpoint, job, events)
}

func memoryWorkflowStartBundleMissing(
	run *domain.Run,
	step *domain.Step,
	snapshot *domain.ContextSnapshot,
	execution *domain.WorkflowExecution,
	checkpoint *domain.Checkpoint,
	job *domain.ContinuationJob,
) bool {
	return run == nil || step == nil || snapshot == nil || execution == nil || checkpoint == nil || job == nil
}

func validMemoryWorkflowStartBundleIDs(
	run *domain.Run,
	execution *domain.WorkflowExecution,
	checkpoint *domain.Checkpoint,
	job *domain.ContinuationJob,
	events []domain.Event,
) bool {
	return run.RunID != "" &&
		execution.RunID == run.RunID &&
		checkpoint.RunID == run.RunID &&
		job.RunID == run.RunID &&
		len(events) != 0
}

func createMemoryWorkflowRunStartBundle(
	st *state,
	run *domain.Run,
	step *domain.Step,
	snapshot *domain.ContextSnapshot,
	artifacts []domain.ContextArtifact,
	execution *domain.WorkflowExecution,
	checkpoint *domain.Checkpoint,
	job *domain.ContinuationJob,
	events []domain.Event,
) ([]domain.Event, error) {
	if _, exists := st.Runs[run.RunID]; exists {
		return nil, agentruntime.ErrDuplicate
	}
	now := time.Now()
	normalizeMemoryWorkflowStart(run, execution, now)
	st.Runs[run.RunID] = clone(*run)
	st.Steps[run.RunID] = []domain.Step{clone(*step)}
	if snapshot.Revision <= 0 {
		snapshot.Revision = 1
	}
	if snapshot.ManagementStatus == "" {
		snapshot.ManagementStatus = domain.ContextManagementStatusBaseline
	}
	st.Contexts[run.RunID] = []domain.ContextSnapshot{clone(*snapshot)}
	if err := storeMemoryWorkflowArtifacts(st, artifacts); err != nil {
		return nil, err
	}
	checkpoint.ContextSnapshotID = snapshot.SnapshotID
	st.Checkpoints[run.RunID] = map[string]domain.Checkpoint{checkpoint.CheckpointID: clone(*checkpoint)}
	st.Executions[run.RunID] = clone(*execution)
	st.Continuations[job.JobID] = normalizedContinuationJob(*job, now)
	return appendEvents(st, events)
}

func normalizeMemoryWorkflowStart(run *domain.Run, execution *domain.WorkflowExecution, now time.Time) {
	run.RuntimeKind = domain.RuntimeKindWorkflow
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = now
	}
	if execution.CreatedAt.IsZero() {
		execution.CreatedAt = now
	}
	execution.UpdatedAt = now
	if execution.Version <= 0 {
		execution.Version = 1
	}
}

func storeMemoryWorkflowArtifacts(st *state, artifacts []domain.ContextArtifact) error {
	for _, artifact := range artifacts {
		if err := putContextArtifact(st, artifact); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetWorkflowExecution(_ context.Context, actor domain.ActorRef, runID string) (*domain.WorkflowExecution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[strings.TrimSpace(runID)]
	if !ok || !owned(run, actor) || run.RuntimeKind != domain.RuntimeKindWorkflow {
		return nil, agentruntime.ErrNotFound
	}
	item, ok := s.state.Executions[run.RunID]
	if !ok {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}

func (s *Store) ApplyWorkflowTransition(_ context.Context, actor domain.ActorRef, runID string, transition domain.WorkflowTransition) (*domain.WorkflowExecution, []domain.Event, bool, error) {
	var result domain.WorkflowExecution
	var saved []domain.Event
	applied := false
	err := s.write(func(st *state) error {
		run, current, currentVersion, currentErr := memoryWorkflowTransitionCurrent(st, actor, runID, transition)
		result = clone(current)
		if currentErr != nil || !currentVersion {
			return currentErr
		}
		var applyErr error
		result, saved, applyErr = applyMemoryWorkflowTransition(st, run, transition)
		applied = applyErr == nil
		return applyErr
	})
	return &result, clone(saved), applied, err
}

func memoryWorkflowTransitionCurrent(
	st *state,
	actor domain.ActorRef,
	runID string,
	transition domain.WorkflowTransition,
) (domain.Run, domain.WorkflowExecution, bool, error) {
	run, ok := st.Runs[strings.TrimSpace(runID)]
	if !ok || !owned(run, actor) || run.RuntimeKind != domain.RuntimeKindWorkflow {
		return domain.Run{}, domain.WorkflowExecution{}, false, agentruntime.ErrNotFound
	}
	current, ok := st.Executions[run.RunID]
	if !ok {
		return domain.Run{}, domain.WorkflowExecution{}, false, agentruntime.ErrNotFound
	}
	if current.Version != transition.ExpectedVersion {
		return run, current, false, nil
	}
	if transition.Execution.RunID != run.RunID ||
		transition.Run.RunID != run.RunID ||
		transition.Execution.Version != current.Version+1 {
		return domain.Run{}, domain.WorkflowExecution{}, false, agentruntime.ErrInvalidInput
	}
	return run, current, true, nil
}

func applyMemoryWorkflowTransition(
	st *state,
	run domain.Run,
	transition domain.WorkflowTransition,
) (domain.WorkflowExecution, []domain.Event, error) {
	upsertMemoryWorkflowSteps(st, run.RunID, transition.Steps)
	upsertMemoryWorkflowInteractions(st, run.RunID, transition.Interactions)
	upsertMemoryWorkflowCheckpoints(st, run.RunID, transition.Checkpoints)
	if err := storeMemoryWorkflowContinuationJobs(st, run.RunID, transition.ContinuationJobs); err != nil {
		return domain.WorkflowExecution{}, nil, err
	}
	storeMemoryWorkflowCacheEntries(st, transition.CacheEntries)
	saved, err := appendEvents(st, transition.Events)
	if err != nil {
		return domain.WorkflowExecution{}, nil, err
	}
	nextRun := memoryWorkflowTransitionRun(st.Runs[run.RunID], transition.Run)
	if err = storeMemoryWorkflowResult(st, run.RunID, nextRun.Status, transition.Result); err != nil {
		return domain.WorkflowExecution{}, nil, err
	}
	st.Runs[run.RunID] = nextRun
	st.Executions[run.RunID] = clone(transition.Execution)
	return clone(transition.Execution), saved, nil
}

func storeMemoryWorkflowContinuationJobs(st *state, runID string, items []domain.ContinuationJob) error {
	for _, item := range items {
		if item.RunID != runID {
			return agentruntime.ErrInvalidInput
		}
		if existing, exists := st.Continuations[item.JobID]; exists && existing.SegmentKey != item.SegmentKey {
			return agentruntime.ErrDuplicate
		}
		st.Continuations[item.JobID] = normalizedContinuationJob(item, time.Now())
	}
	return nil
}

func storeMemoryWorkflowCacheEntries(st *state, entries []domain.WorkflowCacheEntry) {
	for _, entry := range entries {
		st.WorkflowCache[entry.CacheKey] = clone(entry)
	}
}

func memoryWorkflowTransitionRun(projected domain.Run, input domain.Run) domain.Run {
	nextRun := clone(input)
	nextRun.RuntimeKind = domain.RuntimeKindWorkflow
	nextRun.LastEventSeq = projected.LastEventSeq
	nextRun.LastPresentationEventSeq = projected.LastPresentationEventSeq
	if nextRun.UpdatedAt.IsZero() {
		nextRun.UpdatedAt = time.Now()
	}
	return nextRun
}

func storeMemoryWorkflowResult(
	st *state,
	runID string,
	nextRunStatus string,
	result *domain.RunResult,
) error {
	if result == nil {
		return nil
	}
	if result.RunID != runID || nextRunStatus != domain.RunStatusCompleted {
		return agentruntime.ErrInvalidInput
	}
	if existing, exists := st.Results[runID]; exists && existing.ContentHash != result.ContentHash {
		return agentruntime.ErrWorkflowResultConflict
	}
	st.Results[runID] = clone(*result)
	return nil
}

func upsertMemoryWorkflowSteps(st *state, runID string, items []domain.Step) {
	byID := make(map[string]int, len(st.Steps[runID]))
	for index, item := range st.Steps[runID] {
		byID[item.StepID] = index
	}
	for _, item := range items {
		if index, ok := byID[item.StepID]; ok {
			st.Steps[runID][index] = clone(item)
			continue
		}
		byID[item.StepID] = len(st.Steps[runID])
		st.Steps[runID] = append(st.Steps[runID], clone(item))
	}
}

func upsertMemoryWorkflowInteractions(st *state, runID string, items []domain.Interaction) {
	if st.Interactions[runID] == nil {
		st.Interactions[runID] = make(map[string]domain.Interaction)
	}
	for _, item := range items {
		st.Interactions[runID][item.InteractionID] = clone(item)
	}
}

func upsertMemoryWorkflowCheckpoints(st *state, runID string, items []domain.Checkpoint) {
	if st.Checkpoints[runID] == nil {
		st.Checkpoints[runID] = make(map[string]domain.Checkpoint)
	}
	for _, item := range items {
		st.Checkpoints[runID][item.CheckpointID] = clone(item)
	}
}

func (s *Store) GetRunResult(_ context.Context, actor domain.ActorRef, runID string) (*domain.RunResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[strings.TrimSpace(runID)]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	item, ok := s.state.Results[run.RunID]
	if !ok {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}

func (s *Store) GetWorkflowCacheEntry(_ context.Context, actor domain.ActorRef, cacheKey string, now time.Time) (*domain.WorkflowCacheEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.state.WorkflowCache[strings.TrimSpace(cacheKey)]
	if !ok || item.Actor != actor || !item.ExpiresAt.After(now) {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}

func (s *Store) PutWorkflowCacheEntry(_ context.Context, input *domain.WorkflowCacheEntry) error {
	if input == nil || strings.TrimSpace(input.CacheKey) == "" || strings.TrimSpace(input.Actor.ActorID) == "" ||
		strings.TrimSpace(input.ValueJSON) == "" || input.ExpiresAt.IsZero() {
		return agentruntime.ErrInvalidInput
	}
	return s.write(func(st *state) error {
		item := clone(*input)
		now := time.Now()
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		item.UpdatedAt = now
		st.WorkflowCache[item.CacheKey] = item
		return nil
	})
}

func (s *Store) DeleteExpiredWorkflowCacheEntries(_ context.Context, before time.Time, limit int) (int64, error) {
	var deleted int64
	err := s.write(func(st *state) error {
		keys := make([]string, 0)
		for key, item := range st.WorkflowCache {
			if !item.ExpiresAt.After(before) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		if limit > 0 && len(keys) > limit {
			keys = keys[:limit]
		}
		for _, key := range keys {
			delete(st.WorkflowCache, key)
			deleted++
		}
		return nil
	})
	return deleted, err
}
