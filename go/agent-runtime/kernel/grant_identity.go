package kernel

import (
	"encoding/json"
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

// ManagedGrantIdentities enumerates the original and every activation
// retained in a managed history. Used only to reserve known old identities
// during the stopped-writer schema migration, never to recreate authority.
func ManagedGrantIdentities(raw []byte, runID string) ([]GrantIdentity, error) {
	var t ManagedTransfer
	if err := json.Unmarshal(raw, &t); err != nil || !t.SafePoint.Valid() || t.SafePoint.Host.RunID != runID {
		return nil, ErrExecutionConflict
	}
	out := []GrantIdentity{t.SafePoint.OriginalGrant.Identity()}
	if t.SuccessorGrant.ID != "" {
		out = append(out, t.SuccessorGrant.Identity())
	}
	for _, a := range t.Activations {
		out = append(out, a.Grant.Identity())
	}
	for _, i := range out {
		if !i.Valid() || i.RunID != runID {
			return nil, ErrExecutionConflict
		}
	}
	return out, nil
}
