package sqlite

import (
	"bytes"
	"database/sql"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOwnedRetirementV5MigrationPreservesOriginalNativeMark(t *testing.T) {
	path := ownedPrivatePath(t, "native-v5.sqlite")
	raw, e := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if e != nil {
		t.Fatal(e)
	}
	for _, ddl := range schemaV5DDL() {
		if _, e = raw.Exec(ddl); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = raw.Exec("PRAGMA user_version=5"); e != nil {
		t.Fatal(e)
	}
	// Original v5 row fixture, not a newly asserted authorization proof.
	old := &Store{db: raw}
	rt, e := kernel.New(kernel.Dependencies{Store: old})
	if e != nil {
		t.Fatal(e)
	}
	snap, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "th"}, Goal: "preserve", State: []byte("{}")})
	if e != nil {
		t.Fatal(e)
	}
	_, e = raw.Exec("INSERT INTO runtime_managed_authority VALUES('managed-recovery-authority/v1',?,1,1)", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if e != nil {
		t.Fatal(e)
	}
	_, e = raw.Exec("INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,settled_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation,callback_return_revision,callback_return_source_hash,callback_host_completed,callback_lease_owned) VALUES('g','root','root',1,1,'owner','lease',7,'source','start',1,'returned',1,1)")
	if e != nil {
		t.Fatal(e)
	}
	read := func(db *sql.DB) []any {
		t.Helper()
		rows, e := db.Query("SELECT grant_id,parent_run_id,child_run_id,start_revision,settled_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation,callback_return_revision,callback_return_source_hash,callback_host_completed,callback_lease_owned FROM runtime_child_effect_grants")
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("missing original grant")
		}
		v := make([]any, 14)
		p := make([]any, 14)
		for i := range v {
			p[i] = &v[i]
		}
		if e = rows.Scan(p...); e != nil {
			t.Fatal(e)
		}
		return v
	}
	before := read(raw)
	if e = raw.Close(); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	if e = MigrateV5ToV6(t.Context(), path); e != nil {
		t.Fatal(e)
	}
	current, e := OpenExisting(t.Context(), path)
	if e != nil {
		t.Fatal(e)
	}
	defer current.Close()
	if !reflect.DeepEqual(read(current.db), before) {
		t.Fatal("migration changed original native mark/callback")
	}
	got, e := current.Load(t.Context(), "root")
	if e != nil || !reflect.DeepEqual(got, snap) {
		t.Fatalf("changed original aggregate: %v", e)
	}
	var count int
	if e = current.db.QueryRow("SELECT count(*) FROM runtime_owned_retirements").Scan(&count); e != nil || count != 0 {
		t.Fatalf("fabricated receipt: %v", e)
	}
	if e = current.db.QueryRow("SELECT count(*) FROM runtime_child_effect_grants WHERE settlement_evidence_json IS NOT NULL").Scan(&count); e != nil || count != 0 {
		t.Fatalf("fabricated mark origin: %v", e)
	}
}

func TestOwnedRetirementMigrationRejectsUnknownAndMalformedWithoutWrites(t *testing.T) {
	for _, kind := range []string{"unknown", "malformed-v5"} {
		t.Run(kind, func(t *testing.T) {
			path := ownedPrivatePath(t, "invalid.sqlite")
			db, e := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
			if e != nil {
				t.Fatal(e)
			}
			for _, ddl := range schemaV5DDL() {
				if _, e = db.Exec(ddl); e != nil {
					t.Fatal(e)
				}
			}
			version := 999
			if kind == "malformed-v5" {
				version = 5
				if _, e = db.Exec("DROP TABLE runtime_managed_history"); e != nil {
					t.Fatal(e)
				}
			}
			if version == 999 {
				_, e = db.Exec("PRAGMA user_version=999")
			} else {
				_, e = db.Exec("PRAGMA user_version=5")
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = db.Close(); e != nil {
				t.Fatal(e)
			}
			if e = os.Chmod(path, 0600); e != nil {
				t.Fatal(e)
			}
			before, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if e = MigrateV5ToV6(t.Context(), path); e == nil {
				t.Fatal("invalid schema accepted")
			}
			after, e := os.ReadFile(path)
			if e != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected migration modified database: %v", e)
			}
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				if _, e = os.Stat(path + suffix); !os.IsNotExist(e) {
					t.Fatalf("unexpected sidecar %s: %v", suffix, e)
				}
			}
		})
	}
}
