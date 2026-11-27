package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestPhysicalEpochMigrationPreservesOriginalCustodyAndLeaseHistory(t *testing.T) {
	path := ownedPrivatePath(t, "physical-v7.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range schemaV7DDL() {
		if _, err = db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("PRAGMA user_version=7"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO runtime_managed_authority VALUES('managed-recovery-authority/v1',?,1,1)", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: legacyRunFixtureStore{&Store{db: db}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"acknowledged", "active", "lease-only"} {
		if _, err = rt.Create(t.Context(), kernel.CreateRequest{ID: id, Kind: "agent", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "retain", State: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := rt.Load(t.Context(), "acknowledged")
	if err != nil {
		t.Fatal(err)
	}
	g := kernel.ChildExecutionGrant{ID: "retired-original", ChildRunID: snapshot.Run.ID, ExpectedRevision: 1,
		ExecutionOwnerID: "old", ExecutionLeaseID: "old-lease", ExecutionEpoch: 3, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "prepare"}
	c := kernel.OwnedPhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: 1, ReturnSourceHash: strings.Repeat("b", 64), HostCompleted: true, LeaseOwned: true, ControlCustody: kernel.OwnedCustodyPrepared}
	r := kernel.OwnedRetirementReceipt{Version: 1, Evidence: kernel.OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: snapshot, Completion: c}, Closures: []kernel.ManagedTransfer{}}
	r.Digest = r.Identity()
	raw, err := json.Marshal(r)
	if err != nil || !r.Valid() {
		t.Fatalf("migration fixture: %v", err)
	}
	if _, err = db.Exec(`INSERT INTO runtime_owned_retirements VALUES(?,?,?,?,?,?)`, g.ID, g.ChildRunID, snapshot.Run.Revision, r.Version, "ACKNOWLEDGED", raw); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,execution_epoch) VALUES('pending','active','active',1,5)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE runtime_runs SET execution_epoch=9`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(t.Context(), path); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("implicit physical epoch migration: %v", err)
	}
	for range 2 {
		if err = MigrateV7ToV8(t.Context(), path); err != nil {
			t.Fatal(err)
		}
	}
	if err = MigrateV8ToV9(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err = MigrateV9ToV10(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err = MigrateV10ToV11(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for id, wanted := range map[string]uint64{"acknowledged": 3, "active": 5, "lease-only": 0} {
		var physical, epoch uint64
		if err = s.db.QueryRow(`SELECT execution_physical_epoch,execution_epoch FROM runtime_runs WHERE run_id=?`, id).Scan(&physical, &epoch); err != nil || physical != wanted || epoch != 9 {
			t.Fatalf("migration guessed from lease instead of original dispatch: %s physical=%d epoch=%d %v", id, physical, epoch, err)
		}
	}
	for id, owner := range map[string]string{"retired-original": "acknowledged", "pending": "active"} {
		var got string
		if err = s.db.QueryRow("SELECT run_id FROM runtime_grant_identities WHERE grant_id=?", id).Scan(&got); err != nil || got != owner {
			t.Fatalf("migration lost known identity: %s %s %v", id, got, err)
		}
	}
	var stored []byte
	if err = s.db.QueryRow(`SELECT payload_json FROM runtime_owned_retirements WHERE grant_id=?`, g.ID).Scan(&stored); err != nil || string(stored) != string(raw) {
		t.Fatalf("migration rewrote original custody bytes: %v", err)
	}
	if err = s.VerifyPreparedRootSettlement(t.Context(), snapshot); err != nil {
		t.Fatalf("migrated safe prepared history lost control: %v", err)
	}
}
