package harness

import (
	"context"
	"strings"
	"sync/atomic"
)

// subtaskApprovalDispatchScope carries an exact, immutable Host selection.
// Runtime physical admission and parent fences alone authorize execution.
type subtaskApprovalDispatchScope struct {
	GrantID      string
	ChildRunID   string
	CheckpointID string
	Revision     uint64
	active       *atomic.Bool
}

type subtaskApprovalDispatchContextKey struct{}

// VerifiedSubtaskApprovalDispatch checks the original Host-selected child and
// checkpoint. This read-only preflight cannot grant Runtime execution.
func VerifiedSubtaskApprovalDispatch(ctx context.Context, childRunID string, revision uint64, checkpointID string) bool {
	if ctx == nil || strings.TrimSpace(childRunID) == "" ||
		strings.TrimSpace(checkpointID) == "" || revision == 0 {
		return false
	}
	scope, ok := ctx.Value(subtaskApprovalDispatchContextKey{}).(subtaskApprovalDispatchScope)
	return ok && scope.GrantID != "" && scope.active != nil && scope.active.Load() &&
		scope.ChildRunID == childRunID && scope.CheckpointID == checkpointID &&
		scope.Revision == revision
}
