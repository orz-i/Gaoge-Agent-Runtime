package groupchat

import (
	"context"
	"strings"
)

// VisibleHandoffSignal is one durable child-owned request to continue the
// visible conversation with another frozen participant.
type VisibleHandoffSignal struct {
	TargetParticipantID string `json:"targetParticipantID"`
}

// VisibleHandoffResolver loads the durable signal produced by one completed
// visible speaker child. It must not mutate the Group Chat parent Run.
type VisibleHandoffResolver interface {
	ResolveVisibleHandoff(context.Context, string) (VisibleHandoffSignal, bool, error)
}

// VisibleHandoffAuthority validates one requested target against the active
// handoff-policy Group Chat that owns the child Run.
type VisibleHandoffAuthority interface {
	ValidateVisibleHandoff(context.Context, string, string) error
}

func normalizedVisibleHandoffSignal(value VisibleHandoffSignal) VisibleHandoffSignal {
	value.TargetParticipantID = strings.TrimSpace(value.TargetParticipantID)
	return value
}
