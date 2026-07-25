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

func manifestKey(tenantID, manifestID string) string {
	return strings.TrimSpace(tenantID) + ":" + strings.TrimSpace(manifestID)
}

func (s *Store) ExpireNextRunHandoffJoin(_ context.Context, now time.Time) (*domain.RunHandoffJoin, error) {
	var result domain.RunHandoffJoin
	err := s.write(func(st *state) error {
		selectedID, selected, found := earliestExpiredMemoryRunHandoffJoin(st.HandoffJoins, now)
		if !found {
			return agentruntime.ErrNotFound
		}
		updated, applied := domain.ExpireRunHandoffJoin(selected, now)
		if !applied {
			return agentruntime.ErrNotFound
		}
		st.HandoffJoins[selectedID] = clone(updated)
		result = clone(updated)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func earliestExpiredMemoryRunHandoffJoin(items map[string]domain.RunHandoffJoin, now time.Time) (string, domain.RunHandoffJoin, bool) {
	var selectedID string
	var selected domain.RunHandoffJoin
	for joinID, join := range items {
		if !memoryRunHandoffJoinDue(join, now) {
			continue
		}
		if selectedID == "" || join.DeadlineAt.Before(*selected.DeadlineAt) {
			selectedID, selected = joinID, join
		}
	}
	return selectedID, selected, selectedID != ""
}

func memoryRunHandoffJoinDue(join domain.RunHandoffJoin, now time.Time) bool {
	return join.Status == domain.RunHandoffJoinStatusPending && join.DeadlineAt != nil && !join.DeadlineAt.After(now)
}

func validManifest(item *domain.AgentManifest) bool {
	return item != nil && validManifestIdentity(*item) && validManifestPolicy(*item)
}

func validManifestIdentity(item domain.AgentManifest) bool {
	return strings.TrimSpace(item.ManifestID) != "" && strings.TrimSpace(item.TenantID) != "" && strings.TrimSpace(item.Name) != "" &&
		item.CreatedBy.TenantID == item.TenantID && strings.TrimSpace(item.CreatedBy.ActorID) != ""
}

func validManifestPolicy(item domain.AgentManifest) bool {
	validStatus := item.Status == domain.AgentManifestStatusActive || item.Status == domain.AgentManifestStatusDisabled
	validMode := item.ExecutionMode == "" || item.ExecutionMode == "auto" || item.ExecutionMode == "direct" || item.ExecutionMode == "plan"
	validLLMBudget := item.MaxLLMCalls == 0 || item.MaxLLMCalls >= 2 && item.MaxLLMCalls <= 32
	validToolBudget := item.MaxToolCalls == 0 || item.MaxToolCalls >= 1 && item.MaxToolCalls <= 64
	return validStatus && validMode && validLLMBudget && validToolBudget && item.MaxChildRuns > 0 && item.MaxDepth > 0
}

func findManifestRequest(revisions []domain.AgentManifest, requestID, fingerprint string) (domain.AgentManifest, bool, error) {
	if requestID == "" {
		return domain.AgentManifest{}, false, nil
	}
	for _, existing := range revisions {
		if existing.RequestID != requestID {
			continue
		}
		if existing.RequestFingerprint != fingerprint {
			return domain.AgentManifest{}, false, agentruntime.ErrAgentManifestConflict
		}
		return existing, true, nil
	}
	return domain.AgentManifest{}, false, nil
}

func (s *Store) CreateAgentManifestRevision(_ context.Context, input *domain.AgentManifest, expectedRevision int) (*domain.AgentManifest, bool, error) {
	if !validManifest(input) || expectedRevision < 0 {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var result domain.AgentManifest
	var reused bool
	err := s.write(func(st *state) error {
		key := manifestKey(input.TenantID, input.ManifestID)
		revisions := st.Manifests[key]
		requestID := strings.TrimSpace(input.RequestID)
		existing, found, findErr := findManifestRequest(revisions, requestID, strings.TrimSpace(input.RequestFingerprint))
		if findErr != nil {
			return findErr
		}
		if found {
			result, reused = clone(existing), true
			return nil
		}
		latestRevision := 0
		if len(revisions) > 0 {
			latestRevision = revisions[len(revisions)-1].Revision
		}
		if expectedRevision != latestRevision {
			return agentruntime.ErrAgentManifestConflict
		}
		item := clone(*input)
		item.ManifestID = strings.TrimSpace(item.ManifestID)
		item.TenantID = strings.TrimSpace(item.TenantID)
		item.Name = strings.TrimSpace(item.Name)
		item.Description = strings.TrimSpace(item.Description)
		item.Instructions = strings.TrimSpace(item.Instructions)
		item.ModelName = strings.TrimSpace(item.ModelName)
		item.RequestID = requestID
		item.RequestFingerprint = strings.TrimSpace(item.RequestFingerprint)
		item.Revision = latestRevision + 1
		now := time.Now()
		item.CreatedAt, item.UpdatedAt = now, now
		st.Manifests[key] = append(revisions, clone(item))
		result = item
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &result, reused, nil
}

func (s *Store) CreateRunHandoffJoinWaitBundle(_ context.Context, input *domain.RunHandoffJoin, expectedStatus string, expectedLastEventSeq int64, checkpoint *domain.Checkpoint, events []domain.Event) (*domain.RunHandoffJoin, []domain.Event, bool, error) {
	if !validMemoryRunHandoffJoinWaitBundle(input, checkpoint, events) {
		return nil, nil, false, agentruntime.ErrInvalidInput
	}
	var result memoryRunHandoffJoinWaitBundle
	err := s.write(func(st *state) error {
		applied, applyErr := applyMemoryRunHandoffJoinWaitBundle(st, *input, expectedStatus, expectedLastEventSeq, *checkpoint, events)
		result = applied
		return applyErr
	})
	if err != nil {
		return nil, nil, false, err
	}
	join := result.join
	return &join, clone(result.events), result.reused, nil
}

type memoryRunHandoffJoinWaitBundle struct {
	join   domain.RunHandoffJoin
	events []domain.Event
	reused bool
}

func validMemoryRunHandoffJoinWaitBundle(input *domain.RunHandoffJoin, checkpoint *domain.Checkpoint, events []domain.Event) bool {
	return domain.ValidRunHandoffJoin(input) && checkpoint != nil && checkpoint.RunID == input.ParentRunID &&
		checkpoint.CheckpointID == input.ResumeCheckpointID && checkpoint.Status == domain.CheckpointReady && len(events) > 0
}

func applyMemoryRunHandoffJoinWaitBundle(st *state, input domain.RunHandoffJoin, expectedStatus string, expectedLastEventSeq int64, checkpoint domain.Checkpoint, events []domain.Event) (memoryRunHandoffJoinWaitBundle, error) {
	existing, found, err := findMemoryRunHandoffJoin(st.HandoffJoins, input)
	if err != nil || found {
		return memoryRunHandoffJoinWaitBundle{join: existing, reused: found}, err
	}
	run, err := memoryRunHandoffJoinWaitParent(st, input, expectedStatus, expectedLastEventSeq, checkpoint.CheckpointID)
	if err != nil {
		return memoryRunHandoffJoinWaitBundle{}, err
	}
	handoffs, err := joinHandoffsFromMemory(st.Handoffs, input)
	if err != nil {
		return memoryRunHandoffJoinWaitBundle{}, err
	}
	join := resolveMemoryRunHandoffJoin(input, handoffs)
	st.HandoffJoins[join.JoinID] = clone(join)
	st.Checkpoints[run.RunID][checkpoint.CheckpointID] = clone(checkpoint)
	saved, err := appendEvents(st, events)
	return memoryRunHandoffJoinWaitBundle{join: join, events: saved}, err
}

func memoryRunHandoffJoinWaitParent(st *state, input domain.RunHandoffJoin, expectedStatus string, expectedLastEventSeq int64, checkpointID string) (domain.Run, error) {
	run, ok := st.Runs[input.ParentRunID]
	if !ok || !owned(run, input.Actor) {
		return domain.Run{}, agentruntime.ErrNotFound
	}
	if run.Status != expectedStatus || run.LastEventSeq != expectedLastEventSeq {
		return domain.Run{}, agentruntime.ErrDuplicate
	}
	if st.Checkpoints[run.RunID] == nil {
		st.Checkpoints[run.RunID] = make(map[string]domain.Checkpoint)
	}
	if _, exists := st.Checkpoints[run.RunID][checkpointID]; exists {
		return domain.Run{}, agentruntime.ErrDuplicate
	}
	return run, nil
}

func resolveMemoryRunHandoffJoin(input domain.RunHandoffJoin, handoffs []domain.RunHandoff) domain.RunHandoffJoin {
	join := clone(input)
	join.HandoffIDs = normalizeMemoryJoinIDs(join.HandoffIDs)
	if join.CreatedAt.IsZero() {
		join.CreatedAt = time.Now()
	}
	join.UpdatedAt = join.CreatedAt
	return domain.ResolveRunHandoffJoin(join, handoffs, join.CreatedAt)
}

func findMemoryRunHandoffJoin(items map[string]domain.RunHandoffJoin, input domain.RunHandoffJoin) (domain.RunHandoffJoin, bool, error) {
	for _, existing := range items {
		if existing.JoinID != input.JoinID && (existing.Actor != input.Actor || existing.ClientJoinID != input.ClientJoinID) {
			continue
		}
		if existing.RequestFingerprint != input.RequestFingerprint {
			return domain.RunHandoffJoin{}, false, agentruntime.ErrRunHandoffJoinConflict
		}
		return clone(existing), true, nil
	}
	return domain.RunHandoffJoin{}, false, nil
}

func joinHandoffsFromMemory(items map[string]domain.RunHandoff, input domain.RunHandoffJoin) ([]domain.RunHandoff, error) {
	seen := make(map[string]struct{}, len(input.HandoffIDs))
	handoffs := make([]domain.RunHandoff, 0, len(input.HandoffIDs))
	for _, rawID := range input.HandoffIDs {
		handoffID := strings.TrimSpace(rawID)
		if handoffID == "" {
			return nil, agentruntime.ErrInvalidInput
		}
		if _, duplicate := seen[handoffID]; duplicate {
			return nil, agentruntime.ErrInvalidInput
		}
		seen[handoffID] = struct{}{}
		handoff, ok := items[handoffID]
		if !ok || handoff.Actor != input.Actor || handoff.RootRunID != input.RootRunID || handoff.ParentRunID != input.ParentRunID {
			return nil, agentruntime.ErrRunHandoffJoinMember
		}
		handoffs = append(handoffs, handoff)
	}
	return handoffs, nil
}

func (s *Store) CreateRunHandoffJoin(_ context.Context, input *domain.RunHandoffJoin) (*domain.RunHandoffJoin, bool, error) {
	if !domain.ValidRunHandoffJoin(input) {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var result domain.RunHandoffJoin
	var reused bool
	err := s.write(func(st *state) error {
		existing, found, err := findMemoryRunHandoffJoin(st.HandoffJoins, *input)
		if err != nil {
			return err
		}
		if found {
			result, reused = existing, true
			return nil
		}
		handoffs, err := joinHandoffsFromMemory(st.Handoffs, *input)
		if err != nil {
			return err
		}
		item := clone(*input)
		item.HandoffIDs = normalizeMemoryJoinIDs(item.HandoffIDs)
		now := time.Now()
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		item.UpdatedAt = item.CreatedAt
		item = domain.ResolveRunHandoffJoin(item, handoffs, item.CreatedAt)
		st.HandoffJoins[item.JoinID] = clone(item)
		result = item
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &result, reused, nil
}

func normalizeMemoryJoinIDs(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, strings.TrimSpace(value))
	}
	return result
}

func reconcileMemoryRunHandoffJoins(st *state, handoff domain.RunHandoff) []domain.RunHandoffJoin {
	resolved := make([]domain.RunHandoffJoin, 0)
	for joinID, join := range st.HandoffJoins {
		if domain.RunHandoffJoinTerminal(join.Status) || join.Actor != handoff.Actor || join.ParentRunID != handoff.ParentRunID || !memoryJoinContains(join, handoff.HandoffID) {
			continue
		}
		handoffs, err := joinHandoffsFromMemory(st.Handoffs, join)
		if err != nil {
			continue
		}
		updated := domain.ResolveRunHandoffJoin(join, handoffs, time.Now())
		st.HandoffJoins[joinID] = updated
		if !domain.RunHandoffJoinTerminal(join.Status) && domain.RunHandoffJoinTerminal(updated.Status) {
			resolved = append(resolved, clone(updated))
		}
	}
	return resolved
}

func memoryJoinContains(join domain.RunHandoffJoin, handoffID string) bool {
	for _, value := range join.HandoffIDs {
		if value == handoffID {
			return true
		}
	}
	return false
}

func (s *Store) GetRunHandoffJoin(_ context.Context, actor domain.ActorRef, joinID string) (*domain.RunHandoffJoin, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.state.HandoffJoins[strings.TrimSpace(joinID)]
	if !ok || item.Actor != actor {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}

func joinMatches(item domain.RunHandoffJoin, actor domain.ActorRef, filter domain.RunHandoffJoinFilter) bool {
	return item.Actor == actor && (filter.RootRunID == "" || item.RootRunID == filter.RootRunID) &&
		(filter.ParentRunID == "" || item.ParentRunID == filter.ParentRunID) && (filter.Status == "" || item.Status == filter.Status)
}

func (s *Store) ListRunHandoffJoins(_ context.Context, actor domain.ActorRef, filter domain.RunHandoffJoinFilter) (domain.RunHandoffJoinPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.RunHandoffJoin, 0)
	for _, item := range s.state.HandoffJoins {
		if joinMatches(item, actor, filter) {
			items = append(items, clone(item))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	total := int64(len(items))
	offset, limit := boundedPage(filter.Offset, filter.Limit, 100, 500)
	if offset >= len(items) {
		return domain.RunHandoffJoinPage{Total: total, Results: []domain.RunHandoffJoin{}}, nil
	}
	return domain.RunHandoffJoinPage{Total: total, Results: items[offset:min(offset+limit, len(items))]}, nil
}

func (s *Store) CancelPendingRunHandoffJoins(_ context.Context, actor domain.ActorRef, parentRunID string, now time.Time, code, message string) ([]domain.RunHandoffJoin, error) {
	parentRunID = strings.TrimSpace(parentRunID)
	if strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.ActorID) == "" || parentRunID == "" {
		return nil, agentruntime.ErrInvalidInput
	}
	result := make([]domain.RunHandoffJoin, 0)
	err := s.write(func(st *state) error {
		for joinID, join := range st.HandoffJoins {
			if join.Actor != actor || join.ParentRunID != parentRunID || domain.RunHandoffJoinTerminal(join.Status) {
				continue
			}
			updated := domain.CancelRunHandoffJoin(join, now, code, message)
			st.HandoffJoins[joinID] = clone(updated)
			result = append(result, clone(updated))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func findMemoryRunHandoff(items map[string]domain.RunHandoff, input domain.RunHandoff) (domain.RunHandoff, bool, error) {
	for _, existing := range items {
		if !sameHandoffClient(existing, input.Actor, input.ClientHandoffID) && existing.HandoffID != input.HandoffID {
			continue
		}
		if existing.RequestFingerprint != input.RequestFingerprint {
			return domain.RunHandoff{}, false, agentruntime.ErrRunHandoffConflict
		}
		return clone(existing), true, nil
	}
	return domain.RunHandoff{}, false, nil
}

func countMemoryRunHandoffChildren(items map[string]domain.RunHandoff, actor domain.ActorRef, parentRunID string) int {
	children := 0
	for _, existing := range items {
		if existing.Actor == actor && existing.ParentRunID == parentRunID {
			children++
		}
	}
	return children
}

func findChildHandoff(items map[string]domain.RunHandoff, actor domain.ActorRef, childRunID string) (domain.RunHandoff, string, bool) {
	for id, item := range items {
		if item.Actor == actor && item.ChildRunID == childRunID {
			return item, id, true
		}
	}
	return domain.RunHandoff{}, "", false
}

func applyHandoffCompletion(item domain.RunHandoff, input domain.RunHandoffCompletion) (domain.RunHandoff, bool, error) {
	if item.Status != domain.RunHandoffStatusQueued {
		if item.Status == input.Status {
			return item, true, nil
		}
		return domain.RunHandoff{}, false, agentruntime.ErrRunHandoffConflict
	}
	completedAt := input.CompletedAt
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	item.Status = input.Status
	item.ResultSummary = strings.TrimSpace(input.ResultSummary)
	item.ResultOutputIDs = clone(input.ResultOutputIDs)
	item.ErrorCode = strings.TrimSpace(input.ErrorCode)
	item.ErrorMessage = strings.TrimSpace(input.ErrorMessage)
	item.CompletedAt = &completedAt
	item.UpdatedAt = completedAt
	return item, false, nil
}

func boundedPage(offset, limit, defaultLimit, maxLimit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > maxLimit {
		limit = defaultLimit
	}
	return offset, limit
}

func (s *Store) GetAgentManifest(_ context.Context, actor domain.ActorRef, ref domain.ResourceRef) (*domain.AgentManifest, error) {
	if !validManifestLookup(actor, ref) {
		return nil, agentruntime.ErrInvalidInput
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	revisions := s.state.Manifests[manifestKey(actor.TenantID, ref.ID)]
	if len(revisions) == 0 {
		return nil, agentruntime.ErrNotFound
	}
	item, err := selectManifestRevision(revisions, ref.Revision)
	if err != nil {
		return nil, err
	}
	result := clone(item)
	return &result, nil
}

func validManifestLookup(actor domain.ActorRef, ref domain.ResourceRef) bool {
	return strings.TrimSpace(actor.TenantID) != "" && strings.TrimSpace(actor.ActorID) != "" && ref.Kind == domain.AgentManifestKind && strings.TrimSpace(ref.ID) != ""
}

func selectManifestRevision(revisions []domain.AgentManifest, rawRevision string) (domain.AgentManifest, error) {
	if strings.TrimSpace(rawRevision) == "" {
		return revisions[len(revisions)-1], nil
	}
	revision, err := strconv.Atoi(rawRevision)
	if err != nil || revision <= 0 {
		return domain.AgentManifest{}, agentruntime.ErrInvalidInput
	}
	for _, item := range revisions {
		if item.Revision == revision {
			return item, nil
		}
	}
	return domain.AgentManifest{}, agentruntime.ErrNotFound
}

func (s *Store) ListAgentManifests(_ context.Context, actor domain.ActorRef, filter domain.AgentManifestFilter) (domain.AgentManifestPage, error) {
	if strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.ActorID) == "" {
		return domain.AgentManifestPage{}, agentruntime.ErrInvalidInput
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := collectLatestManifests(s.state.Manifests, actor.TenantID, filter.Status)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].ManifestID < items[j].ManifestID
		}
		return items[i].Name < items[j].Name
	})
	return paginateManifests(items, filter), nil
}

func collectLatestManifests(all map[string][]domain.AgentManifest, tenantID, status string) []domain.AgentManifest {
	items := make([]domain.AgentManifest, 0)
	for _, revisions := range all {
		if len(revisions) == 0 {
			continue
		}
		item := revisions[len(revisions)-1]
		if item.TenantID == tenantID && (status == "" || item.Status == status) {
			items = append(items, clone(item))
		}
	}
	return items
}

func paginateManifests(items []domain.AgentManifest, filter domain.AgentManifestFilter) domain.AgentManifestPage {
	total := int64(len(items))
	offset, limit := boundedPage(filter.Offset, filter.Limit, 50, 200)
	if offset >= len(items) {
		return domain.AgentManifestPage{Total: total, Results: []domain.AgentManifest{}}
	}
	return domain.AgentManifestPage{Total: total, Results: items[offset:min(offset+limit, len(items))]}
}

func validHandoff(item *domain.RunHandoff) bool {
	return item != nil && validHandoffIdentity(*item) && validHandoffContract(*item)
}

func validHandoffIdentity(item domain.RunHandoff) bool {
	return strings.TrimSpace(item.HandoffID) != "" && strings.TrimSpace(item.ClientHandoffID) != "" && strings.TrimSpace(item.RequestFingerprint) != "" &&
		strings.TrimSpace(item.Actor.TenantID) != "" && strings.TrimSpace(item.Actor.ActorID) != "" && strings.TrimSpace(item.RootRunID) != "" &&
		strings.TrimSpace(item.ParentRunID) != "" && strings.TrimSpace(item.ChildRunID) != ""
}

func validHandoffContract(item domain.RunHandoff) bool {
	return item.AgentManifest.Kind == domain.AgentManifestKind && strings.TrimSpace(item.AgentManifest.ID) != "" && strings.TrimSpace(item.AgentManifest.Revision) != "" &&
		strings.TrimSpace(item.Goal) != "" && item.Status == domain.RunHandoffStatusQueued && item.Depth > 0
}

func sameHandoffClient(left domain.RunHandoff, actor domain.ActorRef, clientID string) bool {
	return left.Actor == actor && left.ClientHandoffID == clientID
}

func (s *Store) CreateRunHandoff(_ context.Context, input *domain.RunHandoff) (*domain.RunHandoff, bool, error) {
	return s.createRunHandoff(input, 0)
}

func (s *Store) CreateRunHandoffWithinLimit(_ context.Context, input *domain.RunHandoff, maxChildren int) (*domain.RunHandoff, bool, error) {
	if maxChildren <= 0 {
		return nil, false, agentruntime.ErrInvalidInput
	}
	return s.createRunHandoff(input, maxChildren)
}

func (s *Store) createRunHandoff(input *domain.RunHandoff, maxChildren int) (*domain.RunHandoff, bool, error) {
	if !validHandoff(input) {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var result domain.RunHandoff
	var reused bool
	err := s.write(func(st *state) error {
		existing, found, findErr := findMemoryRunHandoff(st.Handoffs, *input)
		if findErr != nil {
			return findErr
		}
		if found {
			result, reused = existing, true
			return nil
		}
		if maxChildren > 0 && countMemoryRunHandoffChildren(st.Handoffs, input.Actor, input.ParentRunID) >= maxChildren {
			return agentruntime.ErrRunHandoffLimit
		}
		item := clone(*input)
		now := time.Now()
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		item.UpdatedAt = item.CreatedAt
		st.Handoffs[item.HandoffID] = clone(item)
		result = item
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &result, reused, nil
}

func (s *Store) GetRunHandoff(_ context.Context, actor domain.ActorRef, handoffID string) (*domain.RunHandoff, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.state.Handoffs[strings.TrimSpace(handoffID)]
	if !ok || item.Actor != actor {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}

func (s *Store) GetRunHandoffByChildRun(_ context.Context, actor domain.ActorRef, childRunID string) (*domain.RunHandoff, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.state.Handoffs {
		if item.Actor == actor && item.ChildRunID == strings.TrimSpace(childRunID) {
			result := clone(item)
			return &result, nil
		}
	}
	return nil, agentruntime.ErrNotFound
}

func handoffMatches(item domain.RunHandoff, actor domain.ActorRef, filter domain.RunHandoffFilter) bool {
	return item.Actor == actor && (filter.RootRunID == "" || item.RootRunID == filter.RootRunID) &&
		(filter.ParentRunID == "" || item.ParentRunID == filter.ParentRunID) && (filter.ChildRunID == "" || item.ChildRunID == filter.ChildRunID) &&
		(filter.Status == "" || item.Status == filter.Status)
}

func (s *Store) ListRunHandoffs(_ context.Context, actor domain.ActorRef, filter domain.RunHandoffFilter) (domain.RunHandoffPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.RunHandoff, 0)
	for _, item := range s.state.Handoffs {
		if handoffMatches(item, actor, filter) {
			items = append(items, clone(item))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	total := int64(len(items))
	offset, limit := boundedPage(filter.Offset, filter.Limit, 100, 500)
	if offset >= len(items) {
		return domain.RunHandoffPage{Total: total, Results: []domain.RunHandoff{}}, nil
	}
	end := min(offset+limit, len(items))
	return domain.RunHandoffPage{Total: total, Results: items[offset:end]}, nil
}

func validHandoffCompletion(input domain.RunHandoffCompletion) bool {
	return input.Status == domain.RunHandoffStatusCompleted || input.Status == domain.RunHandoffStatusFailed || input.Status == domain.RunHandoffStatusCancelled
}

func (s *Store) CompleteRunHandoff(ctx context.Context, actor domain.ActorRef, childRunID string, input domain.RunHandoffCompletion) (*domain.RunHandoff, bool, error) {
	result, err := s.CompleteRunHandoffWithJoins(ctx, actor, childRunID, input)
	if err != nil {
		return nil, false, err
	}
	handoff := result.Handoff
	return &handoff, result.Reused, nil
}

func (s *Store) CompleteRunHandoffWithJoins(_ context.Context, actor domain.ActorRef, childRunID string, input domain.RunHandoffCompletion) (domain.RunHandoffCompletionResult, error) {
	if !validHandoffCompletion(input) || strings.TrimSpace(childRunID) == "" {
		return domain.RunHandoffCompletionResult{}, agentruntime.ErrInvalidInput
	}
	var result domain.RunHandoffCompletionResult
	err := s.write(func(st *state) error {
		item, id, found := findChildHandoff(st.Handoffs, actor, childRunID)
		if !found {
			return agentruntime.ErrNotFound
		}
		updated, wasReused, err := applyHandoffCompletion(item, input)
		if err != nil {
			return err
		}
		if !wasReused {
			st.Handoffs[id] = updated
		}
		result.Handoff = clone(updated)
		result.Reused = wasReused
		result.ResolvedJoins = reconcileMemoryRunHandoffJoins(st, updated)
		return nil
	})
	if err != nil {
		return domain.RunHandoffCompletionResult{}, err
	}
	return result, nil
}
