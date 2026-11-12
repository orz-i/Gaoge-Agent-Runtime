package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestOwnedPhysicalBindingMigrationPreservesLegacyGrant(t *testing.T) {
	db, e := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy-grant.db")), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	sqlDB, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if e = db.Exec(`CREATE TABLE agent_kernel_child_effect_grants (grant_id text NOT NULL, parent_run_id text NOT NULL, child_run_id text NOT NULL, start_revision integer NOT NULL DEFAULT 0, settled_revision integer NOT NULL DEFAULT 0, PRIMARY KEY(grant_id,parent_run_id))`).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Exec(`INSERT INTO agent_kernel_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,settled_revision) VALUES('old-grant','old-parent','old-child',7,0)`).Error; e != nil {
		t.Fatal(e)
	}
	if e = Migrate(db); e != nil {
		t.Fatal(e)
	}
	var rows []models.KernelChildEffectGrantRecord
	if e = db.Find(&rows).Error; e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].GrantID != "old-grant" || rows[0].ParentRunID != "old-parent" || rows[0].ChildRunID != "old-child" || rows[0].StartRevision != 7 || rows[0].SettledRevision != 0 || pgPhysicalGrant(rows[0]).IsOwned() {
		t.Fatalf("legacy unknown grant was removed/backfilled: %+v", rows)
	}
}

func TestOwnedPhysicalBindingGuardsGORM(t *testing.T) {
	runOwnedPhysicalBindingGuards(t, openKernelStoreTestDB(t))
}

// This low-level test seeds a persisted barrier only to attack Store guards.
// It never claims that a seeded row proves valid first-party admission/settlement.
func runOwnedPhysicalBindingGuards(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := t.Context()
	store := NewKernelStore(db)
	rt, e := kernel.New(kernel.Dependencies{Store: store})
	if e != nil {
		t.Fatal(e)
	}
	original, e := rt.Create(ctx, kernel.CreateRequest{ID: "owned-root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "original", State: json.RawMessage(`{"original":true}`), ExecutionRequired: true})
	if e != nil {
		t.Fatal(e)
	}
	lease, e := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: original.Run.ID, OwnerID: "original-owner", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	grant := kernel.ChildExecutionGrant{ID: "owned-original-grant", ChildRunID: original.Run.ID, ExpectedRevision: original.Run.Revision, ExecutionOwnerID: lease.OwnerID, ExecutionLeaseID: lease.LeaseID, ExecutionEpoch: lease.Epoch, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start"}
	if e = store.BeginOwnedPhysicalExecution(ctx, grant, lease, kernel.OwnedPhysicalAdmission{}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("forged admission: %v", e)
	}
	if e = store.BeginChildExecution(ctx, grant); !errors.Is(e, kernel.ErrInvalidInput) {
		t.Fatalf("legacy Begin accepted owned binding: %v", e)
	}
	row := pgPhysicalRecord(grant, grant.ChildRunID)
	if e = db.Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	got, e := store.LookupChildExecution(ctx, original.Run.ID)
	if e != nil || got != grant {
		t.Fatalf("full binding not persisted: %+v %v", got, e)
	}
	if e = store.CheckExecution(ctx, original.Run.ID, &lease); e != nil {
		t.Fatalf("live original owner rejected: %v", e)
	}
	before := ownedKernelRecord(t, db, original.Run.ID)
	_, e = rt.Cancel(ctx, original.Run.ID, original.Run.Revision, "competing control")
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("Cancel passed owned fence: %v", e)
	}
	_, e = rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: original.Run.ID, OwnerID: "competitor", LeaseDuration: time.Minute})
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("Acquire passed owned fence: %v", e)
	}
	if e = store.ReleaseExecution(ctx, lease); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("Release passed owned fence: %v", e)
	}
	if e = store.QuarantineExecution(ctx, kernel.ExecutionQuarantine{RunID: original.Run.ID, Revision: original.Run.Revision, Generation: 1, Reason: "test"}); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("quarantine revoked owner: %v", e)
	}
	unboundMutation := kernel.Mutation{Status: kernel.RunStatusRunning, State: original.State}
	_, e = rt.Apply(ctx, original.Run.ID, original.Run.Revision, unboundMutation)
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("plain Apply passed scope: %v", e)
	}
	bound, e := rt.WithExecution(lease)
	if e != nil {
		t.Fatal(e)
	}
	_, e = bound.Apply(ctx, original.Run.ID, original.Run.Revision, unboundMutation)
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("lease alone passed scope: %v", e)
	}
	if e = store.MarkChildExecutionSettled(ctx, grant, kernel.ChildExecutionSettlement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("forged completion: %v", e)
	}
	if e = store.FinishChildExecution(ctx, grant); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("premature retirement: %v", e)
	}
	assertOwnedKernelRecord(t, db, before)
	// In this isolated database only, expire the original lease. No wall-clock
	// timeout can retire unknown work or permit a takeover.
	past := time.Now().UTC().Add(-time.Hour)
	if e = db.Model(&models.KernelRunRecord{}).Where("run_id = ?", original.Run.ID).Update("execution_expires_at", past).Error; e != nil {
		t.Fatal(e)
	}
	expired := ownedKernelRecord(t, db, original.Run.ID)
	_, e = rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: original.Run.ID, OwnerID: "after-ttl", LeaseDuration: time.Minute})
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("TTL bypassed owned fence: %v", e)
	}
	_, e = rt.RenewExecution(ctx, kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: time.Minute})
	if !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("expired owner resurrected: %v", e)
	}
	if e = store.CheckExecution(ctx, original.Run.ID, &lease); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("expired owner authorized: %v", e)
	}
	assertOwnedKernelRecord(t, db, expired)
	// A marker fixture tests only exact-identity retirement, including binding
	// fields. First-party marker creation is exercised by Kernel/Agent suites.
	if e = db.Model(&models.KernelChildEffectGrantRecord{}).Where("grant_id = ?", grant.ID).Update("settled_revision", original.Run.Revision).Error; e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*kernel.ChildExecutionGrant){func(g *kernel.ChildExecutionGrant) { g.ExecutionOwnerID = "foreign" }, func(g *kernel.ChildExecutionGrant) { g.ExecutionLeaseID = "foreign" }, func(g *kernel.ChildExecutionGrant) { g.ExecutionEpoch++ }, func(g *kernel.ChildExecutionGrant) { g.AdmissionOperation = "resume" }, func(g *kernel.ChildExecutionGrant) { g.AdmissionSourceHash = strings.Repeat("b", 64) }} {
		forged := grant
		mutate(&forged)
		if e = store.FinishChildExecution(ctx, forged); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("foreign binding retired original marker: %v", e)
		}
		got, e = store.LookupChildExecution(ctx, original.Run.ID)
		if e != nil || got != grant {
			t.Fatal("foreign retirement changed original binding")
		}
	}
	if e = store.FinishChildExecution(ctx, grant); e != nil {
		t.Fatalf("exact marked retirement failed: %v", e)
	}
	if _, e = store.LookupChildExecution(ctx, original.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("original marker not retired: %v", e)
	}
	assertOwnedKernelRecord(t, db, expired)
}
func ownedKernelRecord(t *testing.T, db *gorm.DB, id string) models.KernelRunRecord {
	t.Helper()
	var record models.KernelRunRecord
	if e := db.WithContext(context.Background()).Where("run_id = ?", id).First(&record).Error; e != nil {
		t.Fatal(e)
	}
	return record
}
func assertOwnedKernelRecord(t *testing.T, db *gorm.DB, want models.KernelRunRecord) {
	t.Helper()
	if got := ownedKernelRecord(t, db, want.RunID); !reflect.DeepEqual(got, want) {
		t.Fatal("rejected guard mutated Run or native execution authority")
	}
}
