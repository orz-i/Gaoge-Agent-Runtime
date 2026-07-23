package postgres

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"gorm.io/gorm"
)

const (
	expiryTestThreadConversation = "conversation"
	expiryTestDueEarlier         = "due-earlier"
	expiryTestDueTiedFirst       = "due-tied-first"
)

func TestListExpiredRunInteractions(t *testing.T) {
	testListExpiredRunInteractions(t, openConversationRepositoryTestDB(t))
}

func TestListExpiredRunInteractionsPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	db := openConversationPostgresContractDB(t, dsn)
	if err := migrateAgentRuntimeTest(db); err != nil {
		t.Fatal(err)
	}
	testListExpiredRunInteractions(t, db)
}

func testListExpiredRunInteractions(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	runs := []models.RunRecord{
		{RunID: "run-expiry-1", TenantID: "tenant-1", ActorID: "actor-1", ThreadKind: expiryTestThreadConversation, ThreadID: "thread-1"},
		{RunID: "run-expiry-2", TenantID: "tenant-2", ActorID: "actor-2", ThreadKind: "document", ThreadID: "thread-2"},
	}
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
	earlier := now.Add(-2 * time.Minute)
	tied := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	interactions := []models.RunInteraction{
		{InteractionID: expiryTestDueEarlier, RunID: runs[0].RunID, Status: domain.InteractionPending, ExpiresAt: &earlier},
		{InteractionID: expiryTestDueTiedFirst, RunID: runs[1].RunID, Status: domain.InteractionPending, ExpiresAt: &tied},
		{InteractionID: "due-tied-second", RunID: runs[0].RunID, Status: domain.InteractionPending, ExpiresAt: &tied},
		{InteractionID: "future", RunID: runs[0].RunID, Status: domain.InteractionPending, ExpiresAt: &future},
		{InteractionID: "resolved", RunID: runs[0].RunID, Status: domain.InteractionResolved, ExpiresAt: &earlier},
		{InteractionID: "without-expiry", RunID: runs[0].RunID, Status: domain.InteractionPending},
	}
	if err := db.Create(&interactions).Error; err != nil {
		t.Fatal(err)
	}
	repository := New(db, StaticSessions(db))
	limited, err := repository.ListExpiredRunInteractions(t.Context(), now, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertExpiredInteractionIDs(t, limited, []string{expiryTestDueEarlier, expiryTestDueTiedFirst})
	if limited[0].Actor.TenantID != "tenant-1" || limited[0].Actor.ActorID != "actor-1" ||
		limited[0].Thread.Kind != expiryTestThreadConversation || limited[0].Thread.ID != "thread-1" {
		t.Fatalf("unexpected ownership projection: %+v", limited[0])
	}
	all, err := repository.ListExpiredRunInteractions(t.Context(), now, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertExpiredInteractionIDs(t, all, []string{expiryTestDueEarlier, expiryTestDueTiedFirst, "due-tied-second"})
}

func assertExpiredInteractionIDs(t *testing.T, items []domain.ExpiredInteraction, want []string) {
	t.Helper()
	if len(items) != len(want) {
		t.Fatalf("interaction count = %d, want %d: %+v", len(items), len(want), items)
	}
	for index := range want {
		if items[index].InteractionID != want[index] {
			t.Fatalf("interaction[%d] = %q, want %q", index, items[index].InteractionID, want[index])
		}
	}
}
