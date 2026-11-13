package postgres

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

func TestOwnedRetirementFinishNativeIdentityGORM(t *testing.T) {
	runOwnedRetirementFinishNativeIdentity(t, func(t *testing.T) *gorm.DB { return openKernelStoreTestDB(t) })
}
func TestRealPostgresOwnedRetirementFinishNativeIdentity(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	runOwnedRetirementFinishNativeIdentity(t, func(t *testing.T) *gorm.DB {
		db, _ := openIsolatedRealPostgres(t, dsn)
		if e := Migrate(db); e != nil {
			t.Fatal(e)
		}
		return db
	})
}
func runOwnedRetirementFinishNativeIdentity(t *testing.T, factory func(*testing.T) *gorm.DB) {
	for _, mode := range []string{"owner", "lease", "epoch", "cleared", "expired"} {
		t.Run(mode, func(t *testing.T) {
			db := factory(t)
			store := NewKernelStore(db)
			rt, e := kernel.New(kernel.Dependencies{Store: store})
			if e != nil {
				t.Fatal(e)
			}
			snap, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "retire-root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "th"}, Goal: "original", State: []byte("{}"), ExecutionRequired: true})
			if e != nil {
				t.Fatal(e)
			}
			lease, e := store.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: snap.Run.ID, OwnerID: "original", LeaseDuration: time.Minute})
			if e != nil {
				t.Fatal(e)
			}
			g := kernel.ChildExecutionGrant{ID: "retire-grant", ChildRunID: snap.Run.ID, ExpectedRevision: snap.Run.Revision, ExecutionOwnerID: lease.OwnerID, ExecutionLeaseID: lease.LeaseID, ExecutionEpoch: lease.Epoch, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start"}
			// Original legacy marker row: tests Store integrity, not first-party completion issuance.
			row := pgPhysicalRecord(g, g.ChildRunID)
			row.SettledRevision = snap.Run.Revision
			if e = db.Create(&row).Error; e != nil {
				t.Fatal(e)
			}
			delta := map[string]any{}
			switch mode {
			case "owner":
				delta["execution_owner"] = "other"
			case "lease":
				delta["execution_lease_id"] = "other"
			case "epoch":
				delta["execution_epoch"] = lease.Epoch + 1
			case "cleared":
				delta["execution_owner"] = ""
				delta["execution_lease_id"] = ""
				delta["execution_expires_at"] = nil
			case "expired":
				delta["execution_expires_at"] = time.Unix(1, 0).UTC()
			}
			if e = db.Model(&models.KernelRunRecord{}).Where("run_id = ?", g.ChildRunID).UpdateColumns(delta).Error; e != nil {
				t.Fatal(e)
			}
			before := ownedKernelRecord(t, db, g.ChildRunID)
			var grantBefore []models.KernelChildEffectGrantRecord
			if e = db.Find(&grantBefore).Error; e != nil {
				t.Fatal(e)
			}
			e = store.FinishChildExecution(t.Context(), g)
			if mode == "expired" {
				if e != nil {
					t.Fatalf("expiry changed original identity: %v", e)
				}
				r, e := store.LoadOwnedRetirement(t.Context(), g.ID)
				if e != nil || r.Evidence.MarkOrigin != "LEGACY_UNKNOWN" || r.Evidence.Completion.GrantID != "" {
					t.Fatalf("legacy retirement fabricated proof: %+v %v", r, e)
				}
				if _, e = store.LookupChildExecution(t.Context(), g.ChildRunID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("grant not retired: %v", e)
				}
			} else {
				if !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("identity %s accepted: %v", mode, e)
				}
				var after []models.KernelChildEffectGrantRecord
				if e = db.Find(&after).Error; e != nil || !reflect.DeepEqual(after, grantBefore) {
					t.Fatalf("rejected finish changed grants: %v", e)
				}
				var n int64
				if e = db.Model(&models.KernelOwnedRetirementRecord{}).Count(&n).Error; e != nil || n != 0 {
					t.Fatalf("rejected finish inserted receipt: %d %v", n, e)
				}
			}
			assertOwnedKernelRecord(t, db, before)
		})
	}
}
