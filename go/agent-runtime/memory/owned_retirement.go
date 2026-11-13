package memory

import (
	"context"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.OwnedRetirementStore = (*Store)(nil)

func cloneOwnedRetirement(r kernel.OwnedRetirementReceipt) kernel.OwnedRetirementReceipt {
	b, _ := json.Marshal(r)
	var out kernel.OwnedRetirementReceipt
	_ = json.Unmarshal(b, &out)
	return out
}

// retireOwnedLocked is validation-only for managed T4; it never closes it.
func (s *Store) retireOwnedLocked(previous memoryChildGrant) error {
	g := previous.grant
	current := s.records[g.ChildRunID]
	if _, exists := s.ownedRetirements[g.ID]; exists {
		return kernel.ErrExecutionConflict
	}
	if !g.MatchesExecution(s.execution[g.ChildRunID].lease) {
		return kernel.ErrExecutionConflict
	}
	histories := make([]kernel.ManagedTransfer, 0, len(s.managedTransfers))
	for _, t := range s.managedTransfers {
		histories = append(histories, t)
	}
	closures, err := kernel.ValidateOwnedRetirementClosures(g, current, s.managedAuthority, histories)
	if err != nil {
		return err
	}
	var evidence kernel.OwnedSettlementEvidence
	if previous.settlement != nil {
		evidence = *previous.settlement
	} else {
		origin := "LEGACY_UNKNOWN"
		evidence = kernel.OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: cloneSnapshot(current), Completion: previous.completion, MarkOrigin: origin}
	}
	if !evidence.Valid() || evidence.Grant != g || kernel.ManagedDigest(evidence.Snapshot) != kernel.ManagedDigest(current) || evidence.Completion != previous.completion {
		return kernel.ErrExecutionConflict
	}
	r := kernel.OwnedRetirementReceipt{Version: 1, Evidence: evidence, Closures: closures, Phase: "RETIRED"}
	if !r.Valid() {
		return kernel.ErrExecutionConflict
	}
	if s.ownedRetirements == nil {
		s.ownedRetirements = make(map[string]kernel.OwnedRetirementReceipt)
	}
	s.ownedRetirements[g.ID] = cloneOwnedRetirement(r)
	return nil
}
func (s *Store) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s == nil || ctx == nil || id == "" {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.ownedRetirements[id]
	if !ok {
		return r, kernel.ErrNotFound
	}
	if !r.Valid() || r.Evidence.Grant.ID != id {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrExecutionConflict
	}
	return cloneOwnedRetirement(r), nil
}
func (s *Store) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	if s == nil || ctx == nil || g.ID == "" {
		return kernel.OwnedRetirementAckResult{}, kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.ownedRetirements[g.ID]
	if !ok {
		return kernel.OwnedRetirementAckResult{}, kernel.ErrExecutionConflict
	}
	native := s.execution[g.ChildRunID]
	hasGrants := len(s.childGrantOwners[g.ChildRunID]) != 0
	for _, active := range s.childGrants {
		if active.grant.ChildRunID == g.ChildRunID {
			hasGrants = true
		}
	}
	next, result, release, e := kernel.ReduceOwnedRetirementACK(r, g, p, s.records[g.ChildRunID], native.epoch, native.lease, hasGrants)
	if e != nil {
		return result, e
	}
	if result.PreviouslyAcknowledged {
		return result, nil
	}
	if e = s.managedGateLocked(g.ChildRunID); e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	if native.quarantineRev != 0 {
		return kernel.OwnedRetirementAckResult{}, kernel.ErrExecutionConflict
	}
	if release {
		native.lease = kernel.ExecutionLease{}
		s.execution[g.ChildRunID] = native
	}
	s.ownedRetirements[g.ID] = cloneOwnedRetirement(next)
	return result, nil
}

func (s *Store) LookupOwnedRetirementForRun(ctx context.Context, runID string) (kernel.OwnedRetirementReceipt, error) {
	if s == nil || ctx == nil || runID == "" {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrInvalidInput
	}
	if e := ctx.Err(); e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]kernel.OwnedRetirementReceipt, 0)
	for id, r := range s.ownedRetirements {
		if r.Evidence.Grant.ChildRunID != runID {
			continue
		}
		if r.Evidence.Grant.ID != id {
			return kernel.OwnedRetirementReceipt{}, kernel.ErrExecutionConflict
		}
		rows = append(rows, r)
	}
	native := s.execution[runID]
	hasGrants := len(s.childGrantOwners[runID]) != 0
	for _, g := range s.childGrants {
		if g.grant.ChildRunID == runID {
			hasGrants = true
		}
	}
	if e := s.managedGateLocked(runID); e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	if native.quarantineRev != 0 {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrExecutionConflict
	}
	r, e := kernel.SelectOwnedRetirementForRun(runID, rows, s.records[runID], native.epoch, native.lease, hasGrants)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return cloneOwnedRetirement(r), nil
}
