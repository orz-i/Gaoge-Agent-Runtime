package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func ownedPrivatePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(testprofile.Dir(t), name)
}

// legacyRunFixtureStore writes historical root fixtures using only the
// schema shared by v3-v10. Do not run the modern identity/ledger writer
// against an older schema or add new tables before testing migration.
type legacyRunFixtureStore struct{ *Store }

func (s legacyRunFixtureStore) Create(ctx context.Context, record kernel.Record, drafts []kernel.EventDraft) (kernel.Snapshot, error) {
	if record.ExecutionRequired || record.BudgetEnrollment != nil || record.Run.ParentRunID != "" {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	encoded, err := encodeRecord(record)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	events, err := encodeEvents(drafts)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_runs(run_id,scope_id,kind,status,revision,run_json,state_json,checkpoint_json,result_json,event_head)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, record.Run.ID, record.Run.Actor.TenantID, string(record.Run.Kind), string(record.Run.Status), record.Run.Revision,
		encoded.run, encoded.state, encoded.checkpoint, encoded.result, len(drafts))
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if err = writeEvents(ctx, tx, record.Run.ID, 0, record.Run.UpdatedAt, drafts); err != nil {
		return kernel.Snapshot{}, err
	}
	if err = writeReady(ctx, tx, record.Run, drafts, events); err != nil {
		return kernel.Snapshot{}, err
	}
	if err = writeCommitProof(ctx, tx, record.Run, commitFingerprint(encoded, events)); err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := loadTx(ctx, tx, record.Run.ID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return snapshot, tx.Commit()
}

func TestOwnedSchemaMigrationPreservesOriginalGrants(t *testing.T) {
	path := ownedPrivatePath(t, "legacy.sqlite")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range schemaV3DDL() {
		if _, err = raw.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = raw.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	old := &Store{db: raw}
	rt, err := kernel.New(kernel.Dependencies{Store: legacyRunFixtureStore{old}})
	if err != nil {
		t.Fatal(err)
	}
	snapshots := map[string]kernel.Snapshot{}
	for _, id := range []string{"active", "marked"} {
		s, e := rt.Create(t.Context(), kernel.CreateRequest{ID: id, Kind: "agent", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "th"}, Goal: "original", State: json.RawMessage("{}")})
		if e != nil {
			t.Fatal(e)
		}
		snapshots[id] = s
		marked := 0
		if id == "marked" {
			marked = 1
		}
		if _, e = raw.Exec("INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,settled_revision) VALUES(?,?,?,?,?)", id+"-grant", id, id, 1, marked); e != nil {
			t.Fatal(e)
		}
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(t.Context(), path); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("implicit migration: %v", err)
	}
	if err = MigrateV3ToV4(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err = MigrateV3ToV4(t.Context(), path); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	// V3ToV4 retains its exact historical destination. No implicit V5 upgrade.
	raw, err = sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("V3ToV4 destination: %d %v", version, err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(t.Context(), path); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("implicit V4 migration: %v", err)
	}
	if err = MigrateV4ToV5(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err = MigrateV4ToV5(t.Context(), path); err != nil {
		t.Fatalf("repeat V5 migration: %v", err)
	}
	raw, err = sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatalf("V4ToV5 destination: %d %v", version, err)
	}
	var oldNamespace string
	if err = raw.QueryRow("SELECT namespace FROM runtime_managed_authority").Scan(&oldNamespace); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(t.Context(), path); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("implicit V5 migration: %v", err)
	}
	if err = MigrateV5ToV6(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err = MigrateV5ToV6(t.Context(), path); err != nil {
		t.Fatalf("repeat V6 migration: %v", err)
	}
	if err = MigrateV6ToV7(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err = MigrateV6ToV7(t.Context(), path); err != nil {
		t.Fatalf("repeat V7 migration: %v", err)
	}
	if err = MigrateV7ToV8(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err = MigrateV7ToV8(t.Context(), path); err != nil {
		t.Fatalf("repeat V8 migration: %v", err)
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
	current, err := OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = current.Close() }()
	authority, e := current.ManagedRecoveryAuthority(t.Context())
	if e != nil || !authority.Valid() {
		t.Fatalf("V5 authority: %+v %v", authority, e)
	}
	if authority.Namespace != oldNamespace {
		t.Fatal("migration replaced authority")
	}
	if e = current.db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != schemaVersion {
		t.Fatalf("V6ToV7 destination: %d %v", version, e)
	}

	var count int
	if e = current.db.QueryRow("SELECT count(*) FROM runtime_owned_retirements").Scan(&count); e != nil || count != 0 {
		t.Fatalf("fabricated retirement: %d %v", count, e)
	}
	if e = current.db.QueryRow("SELECT count(*) FROM runtime_child_effect_grants WHERE settlement_evidence_json IS NOT NULL").Scan(&count); e != nil || count != 0 {
		t.Fatalf("fabricated mark evidence: %d %v", count, e)
	}
	for id, want := range snapshots {
		if _, e := current.LoadManagedRecovery(t.Context(), id); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("migration fabricated suspension: %v", e)
		}

		got, e := current.Load(t.Context(), id)
		if e != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("migration changed original %s: %v", id, e)
		}
		g, marked, e := loadPhysicalGrant(t.Context(), current.db, id)
		if e != nil || g.IsOwned() || g.ID != id+"-grant" || g.ExpectedRevision != 1 {
			t.Fatalf("migration guessed/dropped grant: %+v %v", g, e)
		}
		expected := uint64(0)
		if id == "marked" {
			expected = 1
		}
		if marked != expected {
			t.Fatal("migration changed completion marker")
		}
		if _, e := loadPhysicalCallbackReturn(t.Context(), current.db, g); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("migration fabricated callback return: %v", e)
		}
	}
}

// Seed only the low-level durable row to exercise Store enforcement. This is
// not evidence for first-party admission; conformance covers the actual entry.
func TestOwnedBarrierRejectsUnboundControlAndExpiredTakeover(t *testing.T) {
	path := ownedPrivatePath(t, "owned.sqlite")
	s, err := CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	before, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "th"}, Goal: "original", State: json.RawMessage("{}"), ExecutionRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: "root", OwnerID: "original", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	g := kernel.ChildExecutionGrant{ID: "owned", ChildRunID: "root", ExpectedRevision: before.Run.Revision, ExecutionOwnerID: lease.OwnerID, ExecutionLeaseID: lease.LeaseID, ExecutionEpoch: lease.Epoch, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start"}
	if err = s.BeginChildExecution(t.Context(), g); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("old Begin accepted owned binding: %v", err)
	}
	if _, err = s.BeginOwnedPhysicalExecution(t.Context(), kernel.OwnedPhysicalAdmission{}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("forged admission accepted: %v", err)
	}
	if _, err = s.db.Exec(`INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation) VALUES(?,?,?,?,?,?,?,?,?)`, g.ID, g.ChildRunID, g.ChildRunID, g.ExpectedRevision, g.ExecutionOwnerID, g.ExecutionLeaseID, g.ExecutionEpoch, g.AdmissionSourceHash, g.AdmissionOperation); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE runtime_runs SET execution_physical_epoch=? WHERE run_id=?", g.ExecutionEpoch, g.ChildRunID); err != nil {
		t.Fatal(err)
	}
	record := kernel.Record{Run: before.Run, State: before.State, Checkpoint: before.Checkpoint, Result: before.Result}
	record.Run.Revision++
	mutation := kernel.StoreMutation{Record: record}
	if _, err = s.Apply(t.Context(), "root", before.Run.Revision, mutation); !errors.Is(err, kernel.ErrExecutionRequired) {
		t.Fatalf("direct Store.Apply authority precedence: %v", err)
	}
	if _, err = s.ApplyExecution(t.Context(), lease, before.Run.Revision, mutation); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("native lease without physical permit: %v", err)
	}
	unchanged, err := s.Load(t.Context(), "root")
	if err != nil || !reflect.DeepEqual(unchanged, before) {
		t.Fatal("denied Apply changed full snapshot")
	}
	unchangedGrant, err := s.LookupChildExecution(t.Context(), "root")
	if err != nil || unchangedGrant != g {
		t.Fatal("denied Apply changed original grant/epoch")
	}
	if err = s.ReleaseExecution(t.Context(), lease); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("owned Release: %v", err)
	}
	if _, err = rt.Cancel(t.Context(), "root", before.Run.Revision, "competing"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("owned Cancel: %v", err)
	}
	if err = s.QuarantineExecution(t.Context(), kernel.ExecutionQuarantine{RunID: "root", Revision: before.Run.Revision, Reason: "test"}); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("quarantine revoked owned epoch: %v", err)
	}
	if err = s.CheckExecution(t.Context(), "root", &lease); err != nil {
		t.Fatalf("rejected controls revoked original owner: %v", err)
	}
	forged := lease
	forged.OwnerID = "foreign"
	if err = s.CheckExecution(t.Context(), "root", &forged); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("foreign identity: %v", err)
	}
	// Isolated low-level state fault, not first-party approval evidence.
	waitingRun := before.Run
	waitingRun.Status = kernel.RunStatusWaitingInput
	waitingJSON, err := json.Marshal(waitingRun)
	if err != nil {
		t.Fatal(err)
	}
	originalJSON, err := json.Marshal(before.Run)
	if err != nil {
		t.Fatal(err)
	}
	var originalExpiry int64
	if err = s.db.QueryRow(`SELECT execution_expiry_ms FROM runtime_runs WHERE run_id='root'`).Scan(&originalExpiry); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"valid", "foreign", "expired", "quarantined", "foreign-quarantined"} {
		expiry, quarantine := originalExpiry, int64(0)
		candidate := lease
		expected := kernel.ErrExecutionRequired
		if scenario == "expired" {
			expiry = 1
			expected = kernel.ErrExecutionConflict
		}
		if scenario == "quarantined" || scenario == "foreign-quarantined" {
			quarantine = int64(before.Run.Revision)
			expected = kernel.ErrExecutionQuarantined
		}
		if scenario == "foreign" || scenario == "foreign-quarantined" {
			candidate.OwnerID += "-foreign"
			expected = kernel.ErrExecutionConflict
		}
		if _, err = s.db.Exec(`UPDATE runtime_runs SET status=?,run_json=?,execution_expiry_ms=?,execution_quarantine_revision=? WHERE run_id='root'`, string(waitingRun.Status), string(waitingJSON), expiry, quarantine); err != nil {
			t.Fatal(err)
		}
		snap, e := s.Load(t.Context(), "root")
		if e != nil {
			t.Fatal(e)
		}
		next := kernel.Record{Run: snap.Run, State: snap.State, Checkpoint: snap.Checkpoint, Result: snap.Result}
		next.Run.Revision++
		if _, err = s.ApplyExecution(t.Context(), candidate, snap.Run.Revision, kernel.StoreMutation{Record: next}); !errors.Is(err, expected) {
			t.Fatalf("waiting %s authority precedence: %v want %v", scenario, err, expected)
		}
		after, e := s.Load(t.Context(), "root")
		if e != nil || !reflect.DeepEqual(after, snap) {
			t.Fatal("waiting rejection changed full snapshot")
		}
		retained, e := s.LookupChildExecution(t.Context(), "root")
		if e != nil || retained != g {
			t.Fatal("waiting rejection changed grant")
		}
		var epoch uint64
		var owner, id string
		var gotExpiry, gotQuarantine int64
		if err = s.db.QueryRow(`SELECT execution_epoch,execution_owner,execution_lease_id,execution_expiry_ms,execution_quarantine_revision FROM runtime_runs WHERE run_id='root'`).Scan(&epoch, &owner, &id, &gotExpiry, &gotQuarantine); err != nil {
			t.Fatal(err)
		}
		if epoch != lease.Epoch || owner != lease.OwnerID || id != lease.LeaseID || gotExpiry != expiry || gotQuarantine != quarantine {
			t.Fatal("waiting rejection changed native authority")
		}
	}
	if _, err = s.db.Exec(`UPDATE runtime_runs SET status=?,run_json=?,execution_expiry_ms=?,execution_quarantine_revision=0 WHERE run_id='root'`, string(before.Run.Status), string(originalJSON), originalExpiry); err != nil {
		t.Fatal(err)
	}
	renewed, err := s.RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic isolated stored-expiry fault, not a caller clock override.
	if _, err = s.db.Exec("UPDATE runtime_runs SET execution_expiry_ms=1 WHERE run_id='root'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: "root", OwnerID: "replacement", LeaseDuration: time.Minute}); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("TTL bypassed barrier: %v", err)
	}
	if _, err = s.RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: renewed, LeaseDuration: time.Minute}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("expired lease revived: %v", err)
	}
	if _, err = s.LookupPhysicalCallbackReturn(t.Context(), g); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("missing completion: %v", err)
	}
	wrongGrant := g
	wrongGrant.ExecutionEpoch++
	if _, err = s.LookupPhysicalCallbackReturn(t.Context(), wrongGrant); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("foreign completion lookup: %v", err)
	}
	if err = s.RecordPhysicalCallbackReturn(t.Context(), g, before.Run.Revision, kernel.PhysicalCallbackOutcome{HostCompleted: true}, kernel.PhysicalCallbackReturn{}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("forged callback return: %v", err)
	}
	if err = s.RecordPhysicalCallbackReturn(t.Context(), g, g.ExpectedRevision, kernel.PhysicalCallbackOutcome{HostCompleted: true}, kernel.PhysicalCallbackReturn{}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("forged Mark: %v", err)
	}
	if err = s.FinishChildExecution(t.Context(), g, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("unmarked Finish: %v", err)
	}
	after, err := s.Load(t.Context(), "root")
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("rejected operations mutated snapshot")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	loaded, err := reopened.LookupChildExecution(t.Context(), "root")
	if err != nil || loaded != g {
		t.Fatalf("binding did not survive reopen: %+v %v", loaded, err)
	}
}

func TestOwnedRecoveryPropagatesNonNotFoundAndRejectsCorruptSelfScope(t *testing.T) {
	ctx := t.Context()
	s, err := CreateNew(ctx, ownedPrivatePath(t, "owned-errors.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	before, err := rt.Create(ctx, kernel.CreateRequest{
		ID: "root", Kind: "agent",
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "th"},
		Goal:   "errors", State: json.RawMessage("{}"), ExecutionRequired: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := rt.Create(ctx, kernel.CreateRequest{
		ID: "other", Kind: "agent",
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "th2"},
		Goal:   "other", State: json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: "root", OwnerID: "owner", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	g := kernel.ChildExecutionGrant{
		ID: "owned-err", ChildRunID: "root", ExpectedRevision: before.Run.Revision,
		ExecutionOwnerID: lease.OwnerID, ExecutionLeaseID: lease.LeaseID, ExecutionEpoch: lease.Epoch,
		AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start",
	}
	if _, err = s.db.Exec(
		`INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation) VALUES(?,?,?,?,?,?,?,?,?)`,
		g.ID, g.ChildRunID, g.ChildRunID, g.ExpectedRevision, g.ExecutionOwnerID, g.ExecutionLeaseID, g.ExecutionEpoch, g.AdmissionSourceHash, g.AdmissionOperation,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE runtime_runs SET execution_physical_epoch=? WHERE run_id=?", g.ExecutionEpoch, g.ChildRunID); err != nil {
		t.Fatal(err)
	}
	record := kernel.Record{Run: before.Run, State: before.State, Checkpoint: before.Checkpoint, Result: before.Result}
	record.Run.Revision++
	mutation := kernel.StoreMutation{Record: record}

	// Missing callback return (callback_return_revision = 0) yields ErrExecutionConflict.
	if _, err = s.ApplyOwnedPhysicalRecovery(ctx, g, before.Run.Revision, mutation, kernel.OwnedPhysicalRecovery{}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("missing callback return got %v, want ErrExecutionConflict", err)
	}

	// Corrupt callback_return_revision triggers a scan failure that must not be
	// masked as ErrExecutionConflict.
	if _, err = s.db.Exec(`UPDATE runtime_child_effect_grants SET callback_return_revision='corrupt' WHERE grant_id=?`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyOwnedPhysicalRecovery(ctx, g, before.Run.Revision, mutation, kernel.OwnedPhysicalRecovery{}); err == nil || errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("corrupt callback return scan error was masked: %v", err)
	}

	// Corrupt owned grant whose parent_run_id != child_run_id must fail with
	// ErrExecutionConflict rather than leaking sql.ErrNoRows.
	if _, err = s.db.Exec(`UPDATE runtime_child_effect_grants SET parent_run_id=?,callback_return_revision=0,settled_revision=? WHERE grant_id=?`, other.Run.ID, before.Run.Revision, g.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishChildExecution(ctx, g, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("FinishChildExecution on non-self-scoped grant got %v, want ErrExecutionConflict", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = sqlitePersistOwnedRetirement(ctx, tx, g, before, lease, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("sqlitePersistOwnedRetirement on non-self-scoped grant got %v, want ErrExecutionConflict", err)
	}
}
