package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Store) CreatePlanningBundle(_ context.Context, runID, expectedStatus string, plan *domain.Plan, steps []domain.Step, interaction *domain.Interaction, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	if plan == nil || interaction == nil || checkpoint == nil || len(events) == 0 {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := s.write(func(st *state) error {
		run, ok := st.Runs[runID]
		if !ok || run.Status != expectedStatus {
			return agentruntime.ErrDuplicate
		}
		for _, existing := range st.Plans[runID] {
			if existing.PlanID == plan.PlanID {
				return agentruntime.ErrDuplicate
			}
		}
		st.Plans[runID] = append(st.Plans[runID], clone(*plan))
		st.Steps[runID] = append(st.Steps[runID], clone(steps)...)
		if st.Interactions[runID] == nil {
			st.Interactions[runID] = make(map[string]domain.Interaction)
		}
		st.Interactions[runID][interaction.InteractionID] = clone(*interaction)
		if st.Checkpoints[runID] == nil {
			st.Checkpoints[runID] = make(map[string]domain.Checkpoint)
		}
		st.Checkpoints[runID][checkpoint.CheckpointID] = clone(*checkpoint)
		run.CurrentPlanID, run.PendingInteractionID, run.CurrentStepID = plan.PlanID, interaction.InteractionID, interaction.StepID
		run.UpdatedAt = time.Now()
		st.Runs[runID] = run
		var err error
		saved, err = appendEvents(st, events)
		return err
	})
	return clone(saved), err
}

func (s *Store) GetRunCheckpoint(_ context.Context, actor domain.ActorRef, runID, checkpointID string) (*domain.Checkpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[strings.TrimSpace(runID)]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	item, ok := s.state.Checkpoints[run.RunID][strings.TrimSpace(checkpointID)]
	if !ok {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}

func (s *Store) GetCurrentPlan(_ context.Context, actor domain.ActorRef, runID string) (*domain.Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[runID]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	for _, plan := range s.state.Plans[runID] {
		if plan.PlanID == run.CurrentPlanID {
			result := clone(plan)
			return &result, nil
		}
	}
	return nil, agentruntime.ErrNotFound
}

func (s *Store) ListPlans(_ context.Context, actor domain.ActorRef, runID string) ([]domain.Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[runID]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	items := clone(s.state.Plans[runID])
	sort.Slice(items, func(i, j int) bool { return items[i].Revision < items[j].Revision })
	return items, nil
}

func (s *Store) CreateRunInteractionBundle(_ context.Context, runID, expectedStatus string, interaction *domain.Interaction, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	if interaction == nil || checkpoint == nil || len(events) == 0 {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := s.write(func(st *state) error {
		run, ok := st.Runs[runID]
		if !ok || run.Status != expectedStatus {
			return agentruntime.ErrDuplicate
		}
		if st.Interactions[runID] == nil {
			st.Interactions[runID] = make(map[string]domain.Interaction)
		}
		if _, ok = st.Interactions[runID][interaction.InteractionID]; ok {
			return agentruntime.ErrDuplicate
		}
		st.Interactions[runID][interaction.InteractionID] = clone(*interaction)
		if st.Checkpoints[runID] == nil {
			st.Checkpoints[runID] = make(map[string]domain.Checkpoint)
		}
		st.Checkpoints[runID][checkpoint.CheckpointID] = clone(*checkpoint)
		run.PendingInteractionID, run.CurrentStepID, run.UpdatedAt = interaction.InteractionID, interaction.StepID, time.Now()
		st.Runs[runID] = run
		var err error
		saved, err = appendEvents(st, events)
		return err
	})
	return clone(saved), err
}

func (s *Store) GetRunInteraction(_ context.Context, actor domain.ActorRef, runID, interactionID string) (*domain.Interaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[runID]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	item, ok := s.state.Interactions[runID][interactionID]
	if !ok {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}

func (s *Store) ListRunInteractions(_ context.Context, actor domain.ActorRef, runID string) ([]domain.Interaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[runID]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	items := make([]domain.Interaction, 0, len(s.state.Interactions[runID]))
	for _, item := range s.state.Interactions[runID] {
		items = append(items, clone(item))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].RequestedAt.Before(items[j].RequestedAt) })
	return items, nil
}

func (s *Store) ListExpiredRunInteractions(_ context.Context, now time.Time, limit int) ([]domain.ExpiredInteraction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.ExpiredInteraction, 0)
	for runID, interactions := range s.state.Interactions {
		run := s.state.Runs[runID]
		for _, item := range interactions {
			if item.Status == domain.InteractionPending && item.ExpiresAt != nil && !item.ExpiresAt.After(now) {
				items = append(items, domain.ExpiredInteraction{InteractionID: item.InteractionID, RunID: runID, Actor: run.Actor, Thread: run.Thread})
			}
		}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Store) ExpireRunInteraction(_ context.Context, interactionID string) ([]domain.Event, bool, error) {
	var saved []domain.Event
	changed := false
	err := s.write(func(st *state) error {
		for runID, interactions := range st.Interactions {
			item, ok := interactions[interactionID]
			if !ok {
				continue
			}
			if item.Status != domain.InteractionPending {
				return nil
			}
			item.Status, item.UpdatedAt = domain.InteractionExpired, time.Now()
			interactions[interactionID] = item
			run := st.Runs[runID]
			run.PendingInteractionID = ""
			st.Runs[runID] = run
			changed = true
			event := domain.Event{EventID: "expire:" + interactionID, RunID: runID, EventType: "interaction.expired", Actor: run.Actor, Thread: run.Thread, CreatedAt: time.Now()}
			var err error
			saved, err = appendEvents(st, []domain.Event{event})
			return err
		}
		return agentruntime.ErrNotFound
	})
	return clone(saved), changed, err
}

func (s *Store) ResolveRunInteractionWithCheckpoint(_ context.Context, actor domain.ActorRef, runID, interactionID, requestID, responseJSON, fingerprint, nextStatus string, checkpoint *domain.Checkpoint, events []domain.Event) (*domain.Interaction, *domain.Checkpoint, []domain.Event, bool, error) {
	var resolved domain.Interaction
	var savedCheckpoint domain.Checkpoint
	var saved []domain.Event
	changed := false
	err := s.write(func(st *state) error {
		run, ok := st.Runs[runID]
		if !ok || !owned(run, actor) {
			return agentruntime.ErrNotFound
		}
		item, ok := st.Interactions[runID][interactionID]
		if !ok {
			return agentruntime.ErrNotFound
		}
		if item.Status == domain.InteractionResolved {
			if item.ResolveRequestID != requestID || item.ResumeFingerprint != fingerprint {
				return agentruntime.ErrDuplicate
			}
			resolved = item
			return nil
		}
		if item.Status != domain.InteractionPending || checkpoint == nil {
			return agentruntime.ErrDuplicate
		}
		now := time.Now()
		item.Status, item.ResponseJSON, item.ResolveRequestID, item.ResumeFingerprint = domain.InteractionResolved, responseJSON, requestID, fingerprint
		item.ResolvedBy, item.ResolvedAt, item.UpdatedAt = actor, &now, now
		st.Interactions[runID][interactionID] = item
		st.Checkpoints[runID][checkpoint.CheckpointID] = clone(*checkpoint)
		run.PendingInteractionID, run.Status, run.UpdatedAt = "", nextStatus, now
		st.Runs[runID] = run
		var err error
		saved, err = appendEvents(st, events)
		if err != nil {
			return err
		}
		resolved, savedCheckpoint, changed = item, clone(*checkpoint), true
		return nil
	})
	return &resolved, &savedCheckpoint, clone(saved), changed, err
}

func (s *Store) CreateRunCheckpointBundle(_ context.Context, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	if checkpoint == nil {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := s.write(func(st *state) error {
		if _, ok := st.Runs[checkpoint.RunID]; !ok {
			return agentruntime.ErrNotFound
		}
		if st.Checkpoints[checkpoint.RunID] == nil {
			st.Checkpoints[checkpoint.RunID] = make(map[string]domain.Checkpoint)
		}
		if _, ok := st.Checkpoints[checkpoint.RunID][checkpoint.CheckpointID]; ok {
			return agentruntime.ErrDuplicate
		}
		st.Checkpoints[checkpoint.RunID][checkpoint.CheckpointID] = clone(*checkpoint)
		var err error
		saved, err = appendEvents(st, events)
		return err
	})
	return clone(saved), err
}

func (s *Store) ListRunCheckpoints(_ context.Context, actor domain.ActorRef, runID string) ([]domain.Checkpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.state.Runs[runID]
	if !ok || !owned(run, actor) {
		return nil, agentruntime.ErrNotFound
	}
	items := make([]domain.Checkpoint, 0, len(s.state.Checkpoints[runID]))
	for _, item := range s.state.Checkpoints[runID] {
		items = append(items, clone(item))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items, nil
}

func (s *Store) ResumeRun(_ context.Context, actor domain.ActorRef, runID, checkpointID, requestID, fingerprint, nextStatus string, successor *domain.Checkpoint, events []domain.Event) (*domain.Checkpoint, *domain.Checkpoint, []domain.Event, bool, error) {
	var consumed, next domain.Checkpoint
	var saved []domain.Event
	changed := false
	err := s.write(func(st *state) error {
		run, ok := st.Runs[runID]
		if !ok || !owned(run, actor) {
			return agentruntime.ErrNotFound
		}
		current, ok := st.Checkpoints[runID][checkpointID]
		if !ok {
			return agentruntime.ErrNotFound
		}
		if current.Status == domain.CheckpointConsumed {
			if current.ResumeRequestID != requestID || current.ResumeFingerprint != fingerprint {
				return agentruntime.ErrDuplicate
			}
			consumed = current
			return nil
		}
		if current.Status != domain.CheckpointReady || successor == nil {
			return agentruntime.ErrDuplicate
		}
		current.Status, current.ResumeRequestID, current.ResumeFingerprint, current.UpdatedAt = domain.CheckpointConsumed, requestID, fingerprint, time.Now()
		st.Checkpoints[runID][checkpointID] = current
		st.Checkpoints[runID][successor.CheckpointID] = clone(*successor)
		run.Status, run.UpdatedAt = nextStatus, time.Now()
		st.Runs[runID] = run
		var err error
		saved, err = appendEvents(st, events)
		if err != nil {
			return err
		}
		consumed, next, changed = current, clone(*successor), true
		return nil
	})
	return &consumed, &next, clone(saved), changed, err
}

func (s *Store) RenewExpiredRunInteraction(ctx context.Context, actor domain.ActorRef, runID, expiredInteractionID, checkpointID, requestID, fingerprint string, renewed *domain.Interaction, successor *domain.Checkpoint, events []domain.Event) (*domain.Checkpoint, *domain.Checkpoint, *domain.Interaction, []domain.Event, bool, error) {
	consumed, next, saved, changed, err := s.ResumeRun(ctx, actor, runID, checkpointID, requestID, fingerprint, domain.RunStatusWaitingInput, successor, events)
	if err != nil || !changed {
		return consumed, next, renewed, saved, changed, err
	}
	err = s.write(func(st *state) error {
		item, ok := st.Interactions[runID][expiredInteractionID]
		if !ok || item.Status != domain.InteractionExpired {
			return agentruntime.ErrDuplicate
		}
		if renewed == nil {
			return agentruntime.ErrInvalidInput
		}
		st.Interactions[runID][renewed.InteractionID] = clone(*renewed)
		run := st.Runs[runID]
		run.PendingInteractionID = renewed.InteractionID
		st.Runs[runID] = run
		return nil
	})
	return consumed, next, renewed, saved, changed, err
}
