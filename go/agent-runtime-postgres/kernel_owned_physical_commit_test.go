package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
)

// The statement succeeds but the deferred foreign key makes PostgreSQL
// reject COMMIT. An observation must never adopt a rolled-back Grant.
func TestRealPostgresPhysicalAdmissionCommitRollback(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	for _, ddl := range []string{
		"CREATE TABLE admission_commit_parent(id text PRIMARY KEY)",
		"CREATE TABLE admission_commit_probe(id text REFERENCES admission_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)",
		"CREATE FUNCTION admission_commit_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO admission_commit_probe VALUES(NEW.grant_id); RETURN NEW; END; $$",
		"CREATE TRIGGER admission_commit_fault AFTER INSERT ON agent_kernel_child_effect_grants FOR EACH ROW EXECUTE FUNCTION admission_commit_fault()",
	} {
		if e := db.Exec(ddl).Error; e != nil {
			t.Fatal(e)
		}
	}
	conformance.RunPhysicalAdmissionCommitRollback(t, NewKernelStore(db), func() {
		if e := db.Exec("DROP TRIGGER admission_commit_fault ON agent_kernel_child_effect_grants").Error; e != nil {
			t.Fatal(e)
		}
	})
}
