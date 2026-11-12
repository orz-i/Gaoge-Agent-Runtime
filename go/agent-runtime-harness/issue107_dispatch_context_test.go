package harness

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestIssue107SubtaskApprovalDispatchProofIsExactAndUnforgeableByPublicIDs(t *testing.T) {
	ctx := context.Background()
	if VerifiedSubtaskApprovalDispatch(ctx, "child", 7, "checkpoint") {
		t.Fatal("plain context manufactured approval grant")
	}
	active := new(atomic.Bool)
	active.Store(true)
	bound := context.WithValue(ctx, subtaskApprovalDispatchContextKey{}, subtaskApprovalDispatchScope{
		GrantID: "committed-dispatch", ChildRunID: "child", Revision: 7, CheckpointID: "checkpoint", active: active,
	})
	if !VerifiedSubtaskApprovalDispatch(bound, "child", 7, "checkpoint") {
		t.Fatal("original Harness-scoped proof missing")
	}
	for _, item := range []struct {
		run        string
		revision   uint64
		checkpoint string
	}{
		{"foreign", 7, "checkpoint"},
		{"child", 8, "checkpoint"},
		{"child", 7, "later-checkpoint"},
		{"", 7, "checkpoint"},
		{"child", 0, "checkpoint"},
		{"child", 7, ""},
	} {
		if VerifiedSubtaskApprovalDispatch(bound, item.run, item.revision, item.checkpoint) {
			t.Fatalf("proof crossed bound physical identity: %+v", item)
		}
	}
	missing := context.WithValue(ctx, subtaskApprovalDispatchContextKey{}, subtaskApprovalDispatchScope{
		ChildRunID: "child", Revision: 7, CheckpointID: "checkpoint", active: active,
	})
	if VerifiedSubtaskApprovalDispatch(missing, "child", 7, "checkpoint") {
		t.Fatal("uncommitted physical grant accepted")
	}
	// A valid middleware may have retained this Context. The exact
	// checkpoint and Run ID must not re-authorize another call after its
	// originating callback returned and the transient capability expired.
	active.Store(false)
	if VerifiedSubtaskApprovalDispatch(bound, "child", 7, "checkpoint") {
		t.Fatal("retained context silently replayed a completed physical grant")
	}
}
