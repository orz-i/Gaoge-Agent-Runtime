package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

func exercisePoisonRunIsolation(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	poisoned := models.KernelTransitionOutboxRecord{
		ID: "poisoned:1", RunID: "poisoned", Kind: "agent", Status: "running", Revision: 1,
		EventsJSON: "{invalid-json", CommittedAt: now.Add(-3 * time.Minute),
		AvailableAt: now.Add(-3 * time.Minute),
	}
	invalidProjection := models.KernelTransitionOutboxRecord{
		ID: "invalid_projection:1", RunID: "invalid_projection", Kind: "agent", Status: "running", Revision: 1,
		EventsJSON:  `[{"type":"non.wakeup","wakeup":false}]`,
		CommittedAt: now.Add(-150 * time.Second), AvailableAt: now.Add(-150 * time.Second),
	}
	healthy := models.KernelTransitionOutboxRecord{
		ID: "healthy:1", RunID: "healthy", Kind: "agent", Status: "running", Revision: 1,
		EventsJSON:  `[{"type":"run.ready","wakeup":true}]`,
		CommittedAt: now.Add(-2 * time.Minute), AvailableAt: now.Add(-2 * time.Minute),
	}
	if err := db.Create(&poisoned).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&invalidProjection).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&healthy).Error; err != nil {
		t.Fatal(err)
	}
	store := NewKernelStore(db)
	claims, err := store.ClaimTransitions(t.Context(), kernel.TransitionClaimRequest{
		WorkerID: "projector", Limit: 1, LeaseDuration: time.Second, Now: now,
	})
	if err != nil || len(claims) != 1 || claims[0].Transition.RunID != "healthy" {
		t.Fatalf("poisoned head starved healthy different Run: claims=%+v err=%v", claims, err)
	}
	quarantined, err := store.ListQuarantinedTransitions(t.Context(), 10)
	if err != nil || len(quarantined) != 2 {
		t.Fatalf("expected two isolated corrupt rows: %+v %v", quarantined, err)
	}
	byID := make(map[string]QuarantinedTransition, len(quarantined))
	for _, entry := range quarantined {
		byID[entry.WorkID] = entry
	}
	if byID["poisoned:1"].FailureCode != "invalid_events" ||
		byID["poisoned:1"].RunID != "poisoned" ||
		byID["invalid_projection:1"].FailureCode != "invalid_projection" {
		t.Fatalf("corrupt rows not quarantined independently: %+v", quarantined)
	}
	var raw models.KernelTransitionOutboxRecord
	if err = db.First(&raw, "id = ?", "poisoned:1").Error; err != nil || raw.EventsJSON != "{invalid-json" {
		t.Fatalf("quarantine modified original diagnostic event: %#v %v", raw, err)
	}
	if err = store.AckTransition(t.Context(), kernel.TransitionLeaseRequest{
		TransitionID: "poisoned:1", LeaseID: "projector:poisoned:1:1", WorkerID: "projector",
	}); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("malformed/quarantined Outbox was ACKed: %v", err)
	}
	claim := claims[0]
	if err = store.AckTransition(t.Context(), kernel.TransitionLeaseRequest{
		TransitionID: claim.Transition.ID, LeaseID: claim.LeaseID, WorkerID: claim.WorkerID,
	}); err != nil {
		t.Fatalf("healthy Run could not ACK independent projection: %v", err)
	}
	next, err := NewKernelStore(db).ClaimTransitions(context.Background(), kernel.TransitionClaimRequest{
		WorkerID: "after-restart", Limit: 1, LeaseDuration: time.Second, Now: now.Add(2 * time.Second),
	})
	if err != nil || len(next) != 0 {
		t.Fatalf("quarantined work reappeared after restart: %+v %v", next, err)
	}
}

func TestPoisonedOutboxDoesNotStarveOtherRun(t *testing.T) {
	exercisePoisonRunIsolation(t, openKernelStoreTestDB(t))
}
func TestRealPostgresPoisonedOutboxDoesNotStarveOtherRun(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	exercisePoisonRunIsolation(t, db)
}
