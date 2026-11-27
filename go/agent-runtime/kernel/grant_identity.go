package kernel

import (
	"encoding/json"
	"sort"
	"strings"
)

// GrantIdentity is the permanently occupied identity of one physical grant,
// not one of its potentially many ancestor fence rows. Consuming a grant
// must never release its identity for a new admission.
type GrantIdentity struct {
	GrantID string
	RunID   string
}

func (g ChildExecutionGrant) Identity() GrantIdentity {
	return GrantIdentity{GrantID: g.ID, RunID: g.ChildRunID}
}

func (i GrantIdentity) Valid() bool {
	return i.GrantID != "" && strings.TrimSpace(i.GrantID) == i.GrantID &&
		i.RunID != "" && strings.TrimSpace(i.RunID) == i.RunID
}

// ManagedGrantIdentities enumerates the original and selected successor
// retained in a managed history. Used only to reserve retained identities
// during the stopped-writer schema migration, never to recreate authority.
func ManagedGrantIdentities(raw []byte, runID string) ([]GrantIdentity, error) {
	var t ManagedTransfer
	if err := json.Unmarshal(raw, &t); err != nil || !t.Valid() || t.SafePoint.Host.RunID != runID {
		return nil, ErrExecutionConflict
	}
	out := []GrantIdentity{t.SafePoint.OriginalGrant.Identity()}
	if t.SuccessorGrant.ID != "" {
		out = append(out, t.SuccessorGrant.Identity())
	}
	for _, i := range out {
		if !i.Valid() || i.RunID != runID {
			return nil, ErrExecutionConflict
		}
	}
	return out, nil
}

// MergeGrantIdentities validates and merges active, retired, and managed
// identities into deterministic GrantID-sorted reservations. Active ancestor
// replicas may share an identity, while active and retired rows must be
// disjoint and cross-Run ownership or invalid identifiers are rejected.
func MergeGrantIdentities(active, retired, managed []GrantIdentity) ([]GrantIdentity, error) {
	activeOwners := make(map[string]string, len(active))
	merged := make(map[string]string, len(active)+len(retired)+len(managed))
	for _, i := range active {
		if !i.Valid() {
			return nil, ErrExecutionConflict
		}
		if prev, exists := activeOwners[i.GrantID]; exists && prev != i.RunID {
			return nil, ErrExecutionConflict
		}
		activeOwners[i.GrantID] = i.RunID
		merged[i.GrantID] = i.RunID
	}
	for _, i := range retired {
		if !i.Valid() {
			return nil, ErrExecutionConflict
		}
		if _, isActive := activeOwners[i.GrantID]; isActive {
			return nil, ErrExecutionConflict
		}
		if prev, exists := merged[i.GrantID]; exists && prev != i.RunID {
			return nil, ErrExecutionConflict
		}
		merged[i.GrantID] = i.RunID
	}
	for _, i := range managed {
		if !i.Valid() {
			return nil, ErrExecutionConflict
		}
		if prev, exists := merged[i.GrantID]; exists && prev != i.RunID {
			return nil, ErrExecutionConflict
		}
		merged[i.GrantID] = i.RunID
	}
	out := make([]GrantIdentity, 0, len(merged))
	for grantID, runID := range merged {
		out = append(out, GrantIdentity{GrantID: grantID, RunID: runID})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GrantID < out[j].GrantID
	})
	return out, nil
}
