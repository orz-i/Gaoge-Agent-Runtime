package sqlite

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/encrypted"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
)

// Both supported SQLite drivers must roll back a failed COMMIT before the
// next read can observe the attempted grant. Test the actual deferred FK.
func TestPhysicalAdmissionCommitRollbackSQLite(t *testing.T) {
	var key [32]byte
	key[0] = 42
	for _, tc := range []struct {
		name string
		open profile.OpenConnection
	}{
		{"plain", nil}, {"encrypted", encrypted.WithKey(&key)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e := CreateNewWith(t.Context(), ownedPrivatePath(t, "admission-commit.sqlite"), tc.open)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = s.Close() })
			for _, ddl := range []string{
				"CREATE TABLE admission_commit_parent(id TEXT PRIMARY KEY)",
				"CREATE TABLE admission_commit_probe(id TEXT REFERENCES admission_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)",
				"CREATE TRIGGER admission_commit_fault AFTER UPDATE OF start_revision ON runtime_effects WHEN NEW.start_revision>0 BEGIN INSERT INTO admission_commit_probe VALUES(NEW.grant_id); END",
			} {
				if _, e = s.db.ExecContext(t.Context(), ddl); e != nil {
					t.Fatal(e)
				}
			}
			conformance.RunPhysicalAdmissionCommitRollback(t, s, func() {
				if _, e = s.db.ExecContext(t.Context(), "DROP TRIGGER admission_commit_fault"); e != nil {
					t.Fatal(e)
				}
			})
		})
	}
}
