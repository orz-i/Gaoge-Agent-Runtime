package sqlite

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestCallbackCustodyMigrationPreservesUnknownHistory(t *testing.T) {
	path := ownedPrivatePath(t, "custody-v6.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range schemaV6DDL() {
		if _, err = db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("PRAGMA user_version=6"); err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: &Store{db: db}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.Create(t.Context(), kernel.CreateRequest{ID: "root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "preserve", State: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO runtime_managed_authority VALUES('managed-recovery-authority/v1',?,1,1)", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	// Original opaque mark bytes must survive; migration cannot re-sign them.
	if _, err = db.Exec(`INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,callback_return_revision,callback_return_source_hash,callback_host_completed,callback_lease_owned,settlement_evidence_json) VALUES('old','root','root',1,1,'original-hash',1,1,'original-evidence')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(t.Context(), path); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("implicit custody migration: %v", err)
	}
	for range 2 {
		if err = MigrateV6ToV7(t.Context(), path); err != nil {
			t.Fatal(err)
		}
	}
	if err = MigrateV7ToV8(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err = MigrateV8ToV9(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var custody, hash, evidence, toolReturns string
	if err = s.db.QueryRow(`SELECT callback_control_custody,callback_return_source_hash,settlement_evidence_json,callback_tool_returns FROM runtime_child_effect_grants WHERE grant_id='old'`).Scan(&custody, &hash, &evidence, &toolReturns); err != nil {
		t.Fatal(err)
	}
	if custody != "" || hash != "original-hash" || evidence != "original-evidence" || toolReturns != "" {
		t.Fatalf("migration upgraded or rewrote original custody: %q %q %q %q", custody, hash, evidence, toolReturns)
	}
}
