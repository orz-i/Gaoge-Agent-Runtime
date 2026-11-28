package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
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

// Direct physical fixtures deliberately bypass admission to probe Store
// guards, but seed the actual canonical effect and ancestor index.
func seedPhysicalFixture(t *testing.T, s *Store, g kernel.ChildExecutionGrant) {
	t.Helper()
	tx, e := s.db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = sqliteReserveGrantIdentity(t.Context(), tx, g); e != nil {
		t.Fatal(e)
	}
	if e = sqliteActivateEffect(t.Context(), tx, g, []string{g.ChildRunID}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
}

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
	if _, err = s.BeginPhysicalExecution(t.Context(), kernel.PhysicalAdmission{}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("forged admission accepted: %v", err)
	}
	seedPhysicalFixture(t, s, g)
	if _, err = s.db.Exec("UPDATE runtime_runs SET execution_physical_epoch=? WHERE run_id=?", g.ExecutionEpoch, g.ChildRunID); err != nil {
		t.Fatal(err)
	}
	record := kernel.Record{Run: before.Run, State: before.State, Checkpoint: before.Checkpoint, Result: before.Result}
	record.Run.Revision++
	mutation := kernel.StoreMutation{Record: record}
	if _, _, err = s.Commit(t.Context(), "root", before.Run.Revision, mutation, kernel.CommitAuthority{}); !errors.Is(err, kernel.ErrExecutionRequired) {
		t.Fatalf("direct Store.Apply authority precedence: %v", err)
	}
	if _, _, err = s.Commit(t.Context(), (lease).RunID, before.Run.Revision, mutation, kernel.LeaseCommit(lease)); !errors.Is(err, kernel.ErrChildExecutionBusy) {
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
		if _, _, err = s.Commit(t.Context(), (candidate).RunID, snap.Run.Revision, kernel.StoreMutation{Record: next}, kernel.LeaseCommit(candidate)); !errors.Is(err, expected) {
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
	seedPhysicalFixture(t, s, g)
	if _, err = s.db.Exec("UPDATE runtime_runs SET execution_physical_epoch=? WHERE run_id=?", g.ExecutionEpoch, g.ChildRunID); err != nil {
		t.Fatal(err)
	}
	record := kernel.Record{Run: before.Run, State: before.State, Checkpoint: before.Checkpoint, Result: before.Result}
	record.Run.Revision++
	mutation := kernel.StoreMutation{Record: record}

	// Missing callback return (callback_return_revision = 0) yields ErrExecutionConflict.
	if _, _, err = s.Commit(ctx, (g).ChildRunID, before.Run.Revision, mutation, kernel.ReceiptCommit(g, kernel.OwnedPhysicalRecovery{})); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("missing callback return got %v, want ErrExecutionConflict", err)
	}

	// Corrupt callback_return_revision triggers a scan failure that must not be
	// masked as ErrExecutionConflict.
	if _, err = s.db.Exec(`UPDATE runtime_effects SET callback_return_revision='corrupt' WHERE grant_id=?`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Commit(ctx, (g).ChildRunID, before.Run.Revision, mutation, kernel.ReceiptCommit(g, kernel.OwnedPhysicalRecovery{})); err == nil || errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("corrupt callback return scan error was masked: %v", err)
	}

	// Corrupt owned grant whose parent_run_id != child_run_id must fail with
	// ErrExecutionConflict rather than leaking sql.ErrNoRows.
	if _, err = s.db.Exec(`UPDATE runtime_effects SET callback_return_revision=0 WHERE grant_id=?; UPDATE runtime_effect_owners SET parent_run_id=? WHERE grant_id=?`, g.ID, other.Run.ID, g.ID); err != nil {
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
	if _, err = sqliteBuildOwnedRetirement(ctx, tx, g, before, lease, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("sqliteBuildOwnedRetirement on non-self-scoped grant got %v, want ErrExecutionConflict", err)
	}
}
