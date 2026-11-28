package a2a

import "testing"

// Existing current-state lifecycle tests explicitly select a revision. Stale
// caller revision tests use their original snapshot instead (never this helper).
func shadowRevision(t *testing.T, runner *ShadowRunner, id string) uint64 {
	t.Helper()
	snapshot, err := runner.runtime.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Run.Revision
}
