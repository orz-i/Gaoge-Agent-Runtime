package harness

import (
	"context"
	"strings"
	"sync/atomic"
)

// subtaskApprovalDispatchScope is minted only after the original Harness
// Store admitted an exact physical Dispatch Grant. Its Context key remains
// private: a Host cannot manufacture approval-continuation authority from a
// matching Run ID, checkpoint value or a stored projection alone.
type subtaskApprovalDispatchScope struct {
	GrantID      string
	ChildRunID   string
	CheckpointID string
	Revision     uint64
	active       *atomic.Bool
}

type subtaskApprovalDispatchContextKey struct{}

// VerifiedSubtaskApprovalDispatch proves that this call is executing *inside*
// the original Harness physical grant for the same child and exact frozen
// checkpoint revision. The proof is process-local and expires with the call;
// the Kernel still controls the actual approval CAS. A direct Agent caller
// without a Harness grant has no proof, even if it knows all public IDs.
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
