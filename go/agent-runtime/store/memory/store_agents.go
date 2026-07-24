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
	return validStatus && validMode && item.MaxChildRuns > 0 && item.MaxDepth > 0
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

func (s *Store) CompleteRunHandoff(_ context.Context, actor domain.ActorRef, childRunID string, input domain.RunHandoffCompletion) (*domain.RunHandoff, bool, error) {
	if !validHandoffCompletion(input) || strings.TrimSpace(childRunID) == "" {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var result domain.RunHandoff
	var reused bool
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
		result, reused = clone(updated), wasReused
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &result, reused, nil
}
