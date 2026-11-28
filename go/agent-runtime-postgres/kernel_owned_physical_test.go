package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

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
	if _, e = store.BeginPhysicalExecution(ctx, kernel.PhysicalAdmission{}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("forged admission: %v", e)
	}
	row := pgPhysicalRecord(grant)
	if e = db.Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Create(&models.KernelEffectOwnerRecord{GrantID: grant.ID, ParentRunID: grant.ChildRunID}).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Model(&models.KernelRunRecord{}).Where("run_id = ?", grant.ChildRunID).UpdateColumn("execution_physical_epoch", grant.ExecutionEpoch).Error; e != nil {
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
	if !errors.Is(e, kernel.ErrExecutionRequired) {
		t.Fatalf("plain Apply did not require native authority: %v", e)
	}
	record := kernel.Record{Run: original.Run, State: original.State, Checkpoint: original.Checkpoint, Result: original.Result}
	record.Run.Revision++
	_, _, e = store.Commit(ctx, original.Run.ID, original.Run.Revision, kernel.StoreMutation{Record: record}, kernel.CommitAuthority{})
	if !errors.Is(e, kernel.ErrExecutionRequired) {
		t.Fatalf("direct Store.Apply authority precedence: %v", e)
	}
	bound, e := rt.WithExecution(lease)
	if e != nil {
		t.Fatal(e)
	}
	_, e = bound.Apply(ctx, original.Run.ID, original.Run.Revision, unboundMutation)
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("lease alone passed scope: %v", e)
	}
	if e = store.RecordPhysicalCallbackReturn(ctx, grant, grant.ExpectedRevision, kernel.PhysicalCallbackOutcome{HostCompleted: true}, kernel.PhysicalCallbackReturn{}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("forged completion: %v", e)
	}
	if e = store.FinishChildExecution(ctx, grant, kernel.ChildExecutionSettlement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("premature retirement: %v", e)
	}
	assertOwnedKernelRecord(t, db, before)
	// Isolated low-level state fault: only test authority/error precedence,
	// not a claim that this seeded row proves first-party approval admission.
	for _, scenario := range []string{"valid", "foreign", "expired", "quarantined", "foreign-quarantined"} {
		waiting := before
		waiting.Status = string(kernel.RunStatusWaitingInput)
		candidate := lease
		expected := kernel.ErrExecutionRequired
		if scenario == "expired" {
			past := time.Now().UTC().Add(-time.Hour)
			waiting.ExecutionExpiresAt = &past
			expected = kernel.ErrExecutionConflict
		}
		if scenario == "quarantined" || scenario == "foreign-quarantined" {
			waiting.ExecutionQuarantineRevision = waiting.Revision
			expected = kernel.ErrExecutionQuarantined
		}
		if scenario == "foreign" || scenario == "foreign-quarantined" {
			candidate.OwnerID += "-foreign"
			expected = kernel.ErrExecutionConflict
		}
		if e = db.Model(&models.KernelRunRecord{}).Where("run_id = ?", before.RunID).UpdateColumns(map[string]any{"status": waiting.Status, "execution_expires_at": waiting.ExecutionExpiresAt, "execution_quarantine_revision": waiting.ExecutionQuarantineRevision}).Error; e != nil {
			t.Fatal(e)
		}
		authoritative := ownedKernelRecord(t, db, before.RunID)
		snap, loadErr := store.Load(ctx, before.RunID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		next := kernel.Record{Run: snap.Run, State: snap.State, Checkpoint: snap.Checkpoint, Result: snap.Result}
		next.Run.Revision++
		if _, _, e = store.Commit(ctx, (candidate).RunID, snap.Run.Revision, kernel.StoreMutation{Record: next}, kernel.LeaseCommit(candidate)); !errors.Is(e, expected) {
			t.Fatalf("waiting %s authority precedence: %v want %v", scenario, e, expected)
		}
		assertOwnedKernelRecord(t, db, authoritative)
		retained, lookupErr := store.LookupChildExecution(ctx, before.RunID)
		if lookupErr != nil || retained != grant {
			t.Fatal("waiting rejection changed original grant")
		}
	}
	if e = db.Model(&models.KernelRunRecord{}).Where("run_id = ?", before.RunID).UpdateColumns(map[string]any{"status": before.Status, "execution_expires_at": before.ExecutionExpiresAt, "execution_quarantine_revision": before.ExecutionQuarantineRevision}).Error; e != nil {
		t.Fatal(e)
	}
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
	// The active effect still has no callback return. No legacy Mark
	// column exists, and forged proofs cannot consume it.
	for _, mutate := range []func(*kernel.ChildExecutionGrant){func(g *kernel.ChildExecutionGrant) { g.ExecutionOwnerID = "foreign" }, func(g *kernel.ChildExecutionGrant) { g.ExecutionLeaseID = "foreign" }, func(g *kernel.ChildExecutionGrant) { g.ExecutionEpoch++ }, func(g *kernel.ChildExecutionGrant) { g.AdmissionOperation = "resume" }, func(g *kernel.ChildExecutionGrant) { g.AdmissionSourceHash = strings.Repeat("b", 64) }} {
		forged := grant
		mutate(&forged)
		if e = store.FinishChildExecution(ctx, forged, kernel.ChildExecutionSettlement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("foreign binding retired original effect: %v", e)
		}
		got, e = store.LookupChildExecution(ctx, original.Run.ID)
		if e != nil || got != grant {
			t.Fatal("foreign retirement changed original binding")
		}
	}
	if e = store.FinishChildExecution(ctx, grant, kernel.ChildExecutionSettlement{}); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("unknown physical effect authorized retirement: %v", e)
	}
	if retained, e := store.LookupChildExecution(ctx, original.Run.ID); e != nil || retained != grant {
		t.Fatalf("unknown physical effect fence was removed: %v", e)
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

func TestNativeLeaseOnlyPreservesRunProjectionGORM(t *testing.T) {
	runNativeLeaseOnlyPreservesRunProjection(t, openKernelStoreTestDB(t))
}
func runNativeLeaseOnlyPreservesRunProjection(t *testing.T, db *gorm.DB) {
	t.Helper()
	store := NewKernelStore(db)
	rt, e := kernel.New(kernel.Dependencies{Store: store})
	if e != nil {
		t.Fatal(e)
	}
	original, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "lease-projection", Kind: "agent", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "thread", ID: "one"}, Goal: "lease-only", State: json.RawMessage(`{}`), ExecutionRequired: true})
	if e != nil {
		t.Fatal(e)
	}
	same := func() {
		t.Helper()
		current, err := rt.Load(t.Context(), original.Run.ID)
		if err != nil || !reflect.DeepEqual(current, original) {
			t.Fatalf("lease changed Run projection: before=%#v after=%#v err=%v", original, current, err)
		}
	}
	l, e := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: original.Run.ID, OwnerID: "owner", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	same()
	l, e = rt.RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: l, LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	same()
	if e = rt.ReleaseExecution(t.Context(), l); e != nil {
		t.Fatal(e)
	}
	same()
}
