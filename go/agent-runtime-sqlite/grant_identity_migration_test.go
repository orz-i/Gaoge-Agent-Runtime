package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestGrantIdentityV9MigrationRejectsAmbiguousOwnersAtomically(t *testing.T) {
	ctx := t.Context()
	path := ownedPrivatePath(t, "identity-v9.sqlite")
	db, e := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	for _, ddl := range schemaV9DDL() {
		if _, e = db.Exec(ddl); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = db.Exec("PRAGMA user_version=9"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO runtime_managed_authority VALUES('managed-recovery-authority/v1',?,1,1)", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); e != nil {
		t.Fatal(e)
	}
	rt, e := kernel.New(kernel.Dependencies{Store: &Store{db: db}})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"parent", "child", "foreign"} {
		if _, e = rt.Create(ctx, kernel.CreateRequest{ID: id, Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "identity migration", State: []byte("{}")}); e != nil {
			t.Fatal(e)
		}
	}
	for _, fence := range [][2]string{{"parent", "child"}, {"child", "child"}, {"foreign", "foreign"}} {
		if _, e = db.Exec("INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision) VALUES('same',?,?,1)", fence[0], fence[1]); e != nil {
			t.Fatal(e)
		}
	}
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	if e = MigrateV9ToV10(ctx, path); e == nil {
		t.Fatal("ambiguous grant ID migrated")
	}
	var version, exists, count int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 9 {
		t.Fatalf("failed migration changed version: %d %v", version, e)
	}
	if e = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='runtime_grant_identities'").Scan(&exists); e != nil || exists != 0 {
		t.Fatalf("failed migration committed table: %d %v", exists, e)
	}
	if e = db.QueryRow("SELECT count(*) FROM runtime_child_effect_grants").Scan(&count); e != nil || count != 3 {
		t.Fatalf("failed migration changed fences: %d %v", count, e)
	}
	if _, e = db.Exec("DELETE FROM runtime_child_effect_grants WHERE parent_run_id='foreign'"); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = MigrateV9ToV10(ctx, path); e != nil {
			t.Fatal(e)
		}
	}
	s, e := OpenExisting(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	var owner string
	if e = s.db.QueryRow("SELECT run_id FROM runtime_grant_identities WHERE grant_id='same'").Scan(&owner); e != nil || owner != "child" {
		t.Fatalf("lost legitimate multi-fence identity: %s %v", owner, e)
	}
	if e = s.db.QueryRow("SELECT count(*) FROM runtime_child_effect_grants").Scan(&count); e != nil || count != 2 {
		t.Fatalf("migration changed valid ancestor fences: %d %v", count, e)
	}
}
