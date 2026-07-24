package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Store) CreateContinuationJob(_ context.Context, item *domain.ContinuationJob) (*domain.ContinuationJob, bool, error) {
	if !validMemoryContinuationJob(item) {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var saved domain.ContinuationJob
	reused := false
	err := s.write(func(st *state) error {
		existing, found, err := continuationBySegment(st, item)
		if err != nil {
			return err
		}
		if found {
			saved, reused = clone(existing), true
			return nil
		}
		if _, exists := st.Continuations[item.JobID]; exists {
			return agentruntime.ErrDuplicate
		}
		saved = normalizedContinuationJob(*item, time.Now())
		st.Continuations[saved.JobID] = clone(saved)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &saved, reused, nil
}

func continuationRunTerminal(run domain.Run) bool {
	if run.EndedAt != nil {
		return true
	}
	switch run.Status {
	case domain.RunStatusWaitingInput, domain.RunStatusWaitingHandoff, domain.RunStatusSuspended, domain.RunStatusCompleted, domain.RunStatusFailed, domain.RunStatusCancelled:
		return true
	default:
		return false
	}
}

func (s *Store) DeadLetterExpiredContinuationJob(_ context.Context, now time.Time) (*domain.ContinuationJob, error) {
	var deadLettered *domain.ContinuationJob
	err := s.write(func(st *state) error {
		deadLettered = expiredContinuationCandidate(st, now)
		if deadLettered == nil {
			return nil
		}
		deadLettered.Status = domain.ContinuationJobDeadLetter
		deadLettered.LeaseOwner, deadLettered.LeaseExpiresAt = "", nil
		deadLettered.LastError = "continuation attempts exhausted"
		deadLettered.UpdatedAt = now
		st.Continuations[deadLettered.JobID] = clone(*deadLettered)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if deadLettered == nil {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(*deadLettered)
	return &result, nil
}

func (s *Store) GetContinuationJob(_ context.Context, jobID string) (*domain.ContinuationJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.state.Continuations[strings.TrimSpace(jobID)]
	if !ok {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(item)
	return &result, nil
}

func (s *Store) ListContinuationJobs(_ context.Context, filter domain.ContinuationJobFilter) (domain.ContinuationJobPage, error) {
	filter = normalizedMemoryContinuationFilter(filter)
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.ContinuationJob, 0, len(s.state.Continuations))
	for _, item := range s.state.Continuations {
		if memoryContinuationMatches(item, filter) {
			items = append(items, clone(item))
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].JobID > items[j].JobID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	total := int64(len(items))
	start := min(filter.Offset, len(items))
	end := min(start+filter.Limit, len(items))
	return domain.ContinuationJobPage{Items: items[start:end], Total: total}, nil
}

func normalizedMemoryContinuationFilter(filter domain.ContinuationJobFilter) domain.ContinuationJobFilter {
	filter.TenantID = strings.TrimSpace(filter.TenantID)
	filter.ActorID = strings.TrimSpace(filter.ActorID)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.RunID = strings.TrimSpace(filter.RunID)
	filter.JobID = strings.TrimSpace(filter.JobID)
	filter.Source = strings.TrimSpace(filter.Source)
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return filter
}

func memoryContinuationMatches(item domain.ContinuationJob, filter domain.ContinuationJobFilter) bool {
	return matchesContinuationValue(filter.TenantID, item.Actor.TenantID) &&
		matchesContinuationValue(filter.ActorID, item.Actor.ActorID) &&
		matchesContinuationValue(filter.Status, item.Status) &&
		matchesContinuationValue(filter.RunID, item.RunID) &&
		matchesContinuationValue(filter.JobID, item.JobID) &&
		matchesContinuationValue(filter.Source, item.Source)
}

func matchesContinuationValue(filter, value string) bool {
	return filter == "" || filter == value
}

func (s *Store) RequeueDeadLetterContinuationJob(_ context.Context, jobID string, availableAt time.Time) (*domain.ContinuationJob, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || availableAt.IsZero() {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved domain.ContinuationJob
	err := s.write(func(st *state) error {
		item, ok := st.Continuations[jobID]
		if !ok || item.Status != domain.ContinuationJobDeadLetter {
			return agentruntime.ErrContinuationJobConflict
		}
		run, ok := st.Runs[item.RunID]
		if !ok || run.Actor != item.Actor {
			return agentruntime.ErrNotFound
		}
		if continuationRunTerminal(run) {
			return agentruntime.ErrContinuationRunTerminal
		}
		item.Status = domain.ContinuationJobQueued
		item.AttemptCount = 0
		item.AvailableAt = availableAt
		item.LeaseOwner, item.LeaseExpiresAt, item.HeartbeatAt = "", nil, nil
		item.ReservationAmountNanousd, item.ReservationRefNo = 0, ""
		item.LastError = ""
		item.UpdatedAt = availableAt
		st.Continuations[jobID] = clone(item)
		saved = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := clone(saved)
	return &result, nil
}

func (s *Store) ClaimNextContinuationJob(_ context.Context, owner string, now, leaseUntil time.Time) (*domain.ContinuationJob, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || !leaseUntil.After(now) {
		return nil, agentruntime.ErrInvalidInput
	}
	var claimed *domain.ContinuationJob
	err := s.write(func(st *state) error {
		claimed = claimableContinuationCandidate(st, now)
		if claimed == nil {
			return nil
		}
		claimContinuation(claimed, owner, now, leaseUntil)
		st.Continuations[claimed.JobID] = clone(*claimed)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if claimed == nil {
		return nil, agentruntime.ErrNotFound
	}
	result := clone(*claimed)
	return &result, nil
}

func (s *Store) SetContinuationJobReservation(_ context.Context, jobID, owner string, amountNanousd int64, refNo string, updatedAt time.Time) (*domain.ContinuationJob, bool, error) {
	jobID, owner, refNo = strings.TrimSpace(jobID), strings.TrimSpace(owner), strings.TrimSpace(refNo)
	if !validContinuationReservationUpdate(jobID, owner, amountNanousd, refNo, updatedAt) {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var saved domain.ContinuationJob
	var reused bool
	err := s.write(func(st *state) error {
		updated, wasReused, updateErr := setMemoryContinuationReservation(st, jobID, owner, amountNanousd, refNo, updatedAt)
		saved, reused = updated, wasReused
		return updateErr
	})
	if err != nil {
		return nil, false, err
	}
	result := clone(saved)
	return &result, reused, nil
}

func validContinuationReservationUpdate(jobID, owner string, amountNanousd int64, refNo string, updatedAt time.Time) bool {
	return jobID != "" && owner != "" && amountNanousd >= 0 && refNo != "" && !updatedAt.IsZero()
}

func setMemoryContinuationReservation(st *state, jobID, owner string, amountNanousd int64, refNo string, updatedAt time.Time) (domain.ContinuationJob, bool, error) {
	item, ok := st.Continuations[jobID]
	if !ok || item.Status != domain.ContinuationJobRunning || item.LeaseOwner != owner {
		return domain.ContinuationJob{}, false, agentruntime.ErrContinuationJobConflict
	}
	if continuationHasReservation(item) {
		if item.ReservationRefNo != refNo || item.ReservationAmountNanousd != amountNanousd {
			return domain.ContinuationJob{}, false, agentruntime.ErrContinuationJobConflict
		}
		return clone(item), true, nil
	}
	item.ReservationAmountNanousd, item.ReservationRefNo, item.UpdatedAt = amountNanousd, refNo, updatedAt
	st.Continuations[jobID] = clone(item)
	return item, false, nil
}

func continuationHasReservation(item domain.ContinuationJob) bool {
	return item.ReservationRefNo != "" || item.ReservationAmountNanousd != 0
}

func (s *Store) HeartbeatContinuationJob(_ context.Context, jobID, owner string, heartbeatAt, leaseUntil time.Time) error {
	return s.updateContinuationJob(jobID, owner, func(item *domain.ContinuationJob) {
		item.HeartbeatAt = timePointer(heartbeatAt)
		item.LeaseExpiresAt = timePointer(leaseUntil)
		item.UpdatedAt = heartbeatAt
	})
}

func (s *Store) CompleteContinuationJob(_ context.Context, jobID, owner string, completedAt time.Time) error {
	return s.updateContinuationJob(jobID, owner, func(item *domain.ContinuationJob) {
		item.Status = domain.ContinuationJobCompleted
		item.LeaseOwner, item.LeaseExpiresAt = "", nil
		item.HeartbeatAt = timePointer(completedAt)
		item.LastError = ""
		item.UpdatedAt = completedAt
	})
}

func (s *Store) RetryContinuationJob(_ context.Context, jobID, owner, message string, availableAt time.Time, deadLetter bool) error {
	return s.updateContinuationJob(jobID, owner, func(item *domain.ContinuationJob) {
		item.Status = domain.ContinuationJobRetryWait
		if deadLetter {
			item.Status = domain.ContinuationJobDeadLetter
		}
		item.AvailableAt = availableAt
		item.LeaseOwner, item.LeaseExpiresAt = "", nil
		item.ReservationAmountNanousd, item.ReservationRefNo = 0, ""
		item.LastError = strings.TrimSpace(message)
		item.UpdatedAt = time.Now()
	})
}

func (s *Store) updateContinuationJob(jobID, owner string, update func(*domain.ContinuationJob)) error {
	return s.write(func(st *state) error {
		item, ok := st.Continuations[strings.TrimSpace(jobID)]
		if !ok || item.Status != domain.ContinuationJobRunning || item.LeaseOwner != strings.TrimSpace(owner) {
			return agentruntime.ErrContinuationJobConflict
		}
		update(&item)
		st.Continuations[item.JobID] = clone(item)
		return nil
	})
}

func timePointer(value time.Time) *time.Time { return &value }

func validMemoryContinuationJob(item *domain.ContinuationJob) bool {
	return item != nil && strings.TrimSpace(item.JobID) != "" && strings.TrimSpace(item.SegmentKey) != "" && strings.TrimSpace(item.RunID) != "" && strings.TrimSpace(item.CheckpointID) != "" && strings.TrimSpace(item.Actor.ActorID) != ""
}

func continuationBySegment(st *state, item *domain.ContinuationJob) (domain.ContinuationJob, bool, error) {
	for _, existing := range st.Continuations {
		if existing.SegmentKey != item.SegmentKey {
			continue
		}
		if !sameContinuationIdentity(existing, *item) {
			return domain.ContinuationJob{}, false, agentruntime.ErrDuplicate
		}
		return existing, true, nil
	}
	return domain.ContinuationJob{}, false, nil
}

func sameContinuationIdentity(left, right domain.ContinuationJob) bool {
	return left.RunID == right.RunID && left.CheckpointID == right.CheckpointID && left.Actor == right.Actor && left.ReservationAmountNanousd == right.ReservationAmountNanousd && left.ReservationRefNo == right.ReservationRefNo
}

func normalizedContinuationJob(item domain.ContinuationJob, now time.Time) domain.ContinuationJob {
	result := clone(item)
	if result.Status == "" {
		result.Status = domain.ContinuationJobQueued
	}
	if result.MaxAttempts <= 0 {
		result.MaxAttempts = 5
	}
	if result.AvailableAt.IsZero() {
		result.AvailableAt = now
	}
	if result.CreatedAt.IsZero() {
		result.CreatedAt = now
	}
	result.UpdatedAt = now
	return result
}

func expiredContinuationCandidate(st *state, now time.Time) *domain.ContinuationJob {
	var selected *domain.ContinuationJob
	for jobID, item := range st.Continuations {
		if !continuationAvailable(item, now) || item.AttemptCount < item.MaxAttempts || continuationAfter(item, selected) {
			continue
		}
		candidate := clone(item)
		candidate.JobID = jobID
		selected = &candidate
	}
	return selected
}

func claimableContinuationCandidate(st *state, now time.Time) *domain.ContinuationJob {
	var selected *domain.ContinuationJob
	for jobID, item := range st.Continuations {
		if !continuationAvailable(item, now) || item.AttemptCount >= item.MaxAttempts || continuationAfter(item, selected) {
			continue
		}
		candidate := clone(item)
		candidate.JobID = jobID
		selected = &candidate
	}
	return selected
}

func continuationAvailable(item domain.ContinuationJob, now time.Time) bool {
	queued := item.Status == domain.ContinuationJobQueued || item.Status == domain.ContinuationJobRetryWait
	if queued {
		return !item.AvailableAt.After(now)
	}
	return item.Status == domain.ContinuationJobRunning && item.LeaseExpiresAt != nil && !item.LeaseExpiresAt.After(now)
}

func continuationAfter(item domain.ContinuationJob, selected *domain.ContinuationJob) bool {
	if selected == nil {
		return false
	}
	return item.AvailableAt.After(selected.AvailableAt) || item.AvailableAt.Equal(selected.AvailableAt) && item.CreatedAt.After(selected.CreatedAt)
}

func claimContinuation(item *domain.ContinuationJob, owner string, now, leaseUntil time.Time) {
	item.Status = domain.ContinuationJobRunning
	item.LeaseOwner = owner
	item.LeaseExpiresAt = timePointer(leaseUntil)
	item.HeartbeatAt = timePointer(now)
	item.AttemptCount++
	item.LastError = ""
	item.UpdatedAt = now
}
