package kernel

import (
	"errors"
	"testing"
)

func TestRetirementSuccessorNeverInfersMissingClosure(t *testing.T) {
	r, lease := retirementFixture()
	current := r.Evidence.Snapshot
	for _, tc := range []struct {
		name   string
		mutate func(*OwnedRetirementReceipt, *Snapshot, *ExecutionLease)
		active bool
	}{
		{"unknown-completion", func(r *OwnedRetirementReceipt, _ *Snapshot, _ *ExecutionLease) {
			r.Evidence.Completion = OwnedPhysicalCallbackCompletion{}
			r.Evidence.MarkOrigin = "LEGACY_UNKNOWN"
		}, false},
		{"different-source", func(_ *OwnedRetirementReceipt, s *Snapshot, _ *ExecutionLease) { s.EventHead++ }, false},
		{"different-owner", func(_ *OwnedRetirementReceipt, _ *Snapshot, l *ExecutionLease) { l.OwnerID = "foreign" }, false},
		{"active-grant", func(_ *OwnedRetirementReceipt, _ *Snapshot, _ *ExecutionLease) {}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, source, owner := r, current, lease
			tc.mutate(&candidate, &source, &owner)
			if _, _, err := ReduceOwnedRetirementSuccessor(candidate, source, owner.Epoch, owner, tc.active); !errors.Is(err, ErrOwnedRetirementPending) {
				t.Fatalf("unknown original closure authorized successor: %v", err)
			}
		})
	}
}
