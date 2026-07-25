package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"gorm.io/gorm"
)

const accountEraseTenant = "default"

func TestEraseAccountDataWithoutRuns(t *testing.T) {
	repo := newTestRepository(t)
	db := repo.db
	ctx := context.Background()
	target := models.AgentManifestRevisionRecord{
		ManifestID: "agent-target", Revision: 1, TenantID: accountEraseTenant, Name: "Target", Status: valueActiveC374515E,
		CreatedByTenantID: accountEraseTenant, CreatedByActorID: "7", RequestID: "request-target", RequestFingerprint: "fingerprint-target",
	}
	other := models.AgentManifestRevisionRecord{
		ManifestID: "agent-other", Revision: 1, TenantID: accountEraseTenant, Name: "Other", Status: valueActiveC374515E,
		CreatedByTenantID: accountEraseTenant, CreatedByActorID: "8", RequestID: "request-other", RequestFingerprint: "fingerprint-other",
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}

	if err := repo.EraseAccountData(ctx, 7); err != nil {
		t.Fatalf("erase account without runs: %v", err)
	}
	assertAccountEraseCount(t, db, &models.AgentManifestRevisionRecord{}, "created_by_actor_id = ?", "7", 0)
	assertAccountEraseCount(t, db, &models.AgentManifestRevisionRecord{}, "created_by_actor_id = ?", "8", 1)
}

func TestEraseAccountDataDeletesOnlyActorRunGraph(t *testing.T) {
	repo := newTestRepository(t)
	db := repo.db
	ctx := context.Background()
	now := time.Now().UTC()
	targetRun := models.RunRecord{
		RunID: "run-target", TenantID: accountEraseTenant, ActorID: "7", ThreadKind: expiryTestThreadConversation, ThreadID: "thread-target",
		Status: "completed", StartedAt: now,
	}
	otherRun := models.RunRecord{
		RunID: "run-other", TenantID: accountEraseTenant, ActorID: "8", ThreadKind: expiryTestThreadConversation, ThreadID: "thread-other",
		Status: "completed", StartedAt: now,
	}
	if err := db.Create(&targetRun).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&otherRun).Error; err != nil {
		t.Fatal(err)
	}
	for _, event := range []models.EventRecord{
		{RunID: targetRun.RunID, TenantID: accountEraseTenant, ActorID: "7", ThreadKind: targetRun.ThreadKind, ThreadID: targetRun.ThreadID, EventScope: "run", EventID: "event-target", EventType: migrationRunCompleted},
		{RunID: otherRun.RunID, TenantID: accountEraseTenant, ActorID: "8", ThreadKind: otherRun.ThreadKind, ThreadID: otherRun.ThreadID, EventScope: "run", EventID: "event-other", EventType: migrationRunCompleted},
	} {
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := repo.EraseAccountData(ctx, 7); err != nil {
		t.Fatalf("erase account run graph: %v", err)
	}
	assertAccountEraseCount(t, db, &models.RunRecord{}, "actor_id = ?", "7", 0)
	assertAccountEraseCount(t, db, &models.EventRecord{}, "actor_id = ?", "7", 0)
	assertAccountEraseCount(t, db, &models.RunRecord{}, "actor_id = ?", "8", 1)
	assertAccountEraseCount(t, db, &models.EventRecord{}, "actor_id = ?", "8", 1)
}

func assertAccountEraseCount(t *testing.T, db *gorm.DB, model interface{}, query string, value string, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(model).Where(query, value).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%T count = %d, want %d", model, count, want)
	}
}
