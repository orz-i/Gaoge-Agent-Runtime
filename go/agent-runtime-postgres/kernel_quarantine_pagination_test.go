package postgres

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

// A head of 33 bad rows exceeded the former fixed 32-row overfetch and
// forced a healthy Run to wait for a second poll. A bounded continuation scan
// should find the healthy row on the first call without unbounded work.
func exercisePoisonPagination(t *testing.T, db *gorm.DB, poisonedRows int) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	rows := make([]models.KernelTransitionOutboxRecord, 0, poisonedRows+1)
	for i := 0; i < poisonedRows; i++ {
		id := fmt.Sprintf("poison-page-%03d", i)
		rows = append(rows, models.KernelTransitionOutboxRecord{
			ID: id + ":1", RunID: id, Kind: "agent", Status: "running", Revision: 1,
			EventsJSON: "{invalid-json", CommittedAt: now.Add(-time.Hour),
			AvailableAt: now.Add(-time.Hour),
		})
	}
	rows = append(rows, models.KernelTransitionOutboxRecord{
		ID: "healthy-after-poison:1", RunID: "healthy-after-poison",
		Kind: "agent", Status: "running", Revision: 1,
		EventsJSON:  `[{"type":"run.ready","wakeup":true}]`,
		CommittedAt: now.Add(-time.Minute), AvailableAt: now.Add(-time.Minute),
	})
	if err := db.CreateInBatches(rows, 32).Error; err != nil {
		t.Fatal(err)
	}
	store := NewKernelStore(db)
	claim := kernel.TransitionClaimRequest{
		WorkerID: "pagination-projector", Limit: 1, LeaseDuration: time.Minute, Now: now,
	}
	first, err := store.ClaimTransitions(t.Context(), claim)
	if err != nil {
		t.Fatal(err)
	}
	quarantine, err := store.ListQuarantinedTransitions(t.Context(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if poisonedRows == 33 {
		if len(first) != 1 || first[0].Transition.RunID != "healthy-after-poison" || len(quarantine) != poisonedRows {
			t.Fatalf("single-call poisoned-head pagination: claims=%+v quarantined=%d", first, len(quarantine))
		}
	} else {
		// Limit=1 scans 4 pages of 32 rows at most. Larger poisoned heads
		// remain partially quarantined and must not cause an endless loop.
		if len(first) != 0 || len(quarantine) != 128 {
			t.Fatalf("scan exceeded its fixed budget: claims=%+v quarantined=%d", first, len(quarantine))
		}
		second, claimErr := store.ClaimTransitions(t.Context(), claim)
		if claimErr != nil || len(second) != 1 || second[0].Transition.RunID != "healthy-after-poison" {
			t.Fatalf("healthy Run not recovered on bounded follow-up: %+v, %v", second, claimErr)
		}
		first = second
		quarantine, err = store.ListQuarantinedTransitions(t.Context(), 200)
		if err != nil || len(quarantine) != poisonedRows {
			t.Fatalf("remaining poison was not isolated: count=%d err=%v", len(quarantine), err)
		}
	}
	if err := store.AckTransition(t.Context(), kernel.TransitionLeaseRequest{
		TransitionID: first[0].Transition.ID, LeaseID: first[0].LeaseID, WorkerID: first[0].WorkerID,
	}); err != nil {
		t.Fatalf("healthy projection cannot ACK: %v", err)
	}
}

func TestClaimTransitionsScansPastThirtyThreePoisonedRows(t *testing.T) {
	exercisePoisonPagination(t, openKernelStoreTestDB(t), 33)
}

func TestClaimTransitionsBoundsPoisonedScanPerCall(t *testing.T) {
	exercisePoisonPagination(t, openKernelStoreTestDB(t), 129)
}

func TestRealPostgresClaimTransitionsScansPastThirtyThreePoisonedRows(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("requires PostgreSQL")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	exercisePoisonPagination(t, db, 33)
}

func TestRealPostgresClaimTransitionsBoundsPoisonedScanPerCall(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("requires PostgreSQL")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	exercisePoisonPagination(t, db, 129)
}
