package harnesspostgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealPostgresAppendItemConcurrentIdentity(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	second, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	turnIDs := []string{"item-turn-a-" + uuid.NewString(), "item-turn-b-" + uuid.NewString()}
	for _, id := range turnIDs {
		if _, fresh, createErr := first.CreateTurn(t.Context(), harness.Turn{
			ID: id, SessionID: "items-session", HostTurn: harness.HostRef{Kind: "conversation_turn", ID: id},
			ConfigSnapshotID: "config", Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}); createErr != nil || !fresh {
			t.Fatalf("create turn %s: fresh=%v err=%v", id, fresh, createErr)
		}
	}

	for _, testCase := range []struct {
		name         string
		crossTurn    bool
		emptyPayload bool
	}{
		{name: "same turn retries are idempotent"},
		{name: "same turn empty payload is idempotent", emptyPayload: true},
		{name: "cross turn global ID collision is a conflict", crossTurn: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			id := "item-" + uuid.NewString()
			left := harness.Item{
				ID: id, TurnID: turnIDs[0], Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
				Payload: json.RawMessage(`{"ok":true}`), CreatedAt: now, UpdatedAt: now,
			}
			if testCase.emptyPayload {
				left.Payload = nil
			}
			right := left
			lockedIDs := []string{turnIDs[0]}
			if testCase.crossTurn {
				right.TurnID = turnIDs[1]
				lockedIDs = turnIDs
			}

			// Hold the parent Turn row locks so both AppendItem calls must
			// contend before either can insert. The old code read the ID before
			// locking, then surfaced SQLSTATE 23505 on the second insert.
			lock := db.Begin()
			if lock.Error != nil {
				t.Fatal(lock.Error)
			}
			locked := true
			defer func() {
				if locked {
					_ = lock.Rollback().Error
				}
			}()
			if err := lock.Exec("UPDATE agent_harness_turns SET revision = revision WHERE id IN ?", lockedIDs).Error; err != nil {
				t.Fatal(err)
			}

			type result struct {
				item    harness.Item
				created bool
				err     error
			}
			outcomes := make(chan result, 2)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			start := make(chan struct{})
			for index, value := range []harness.Item{left, right} {
				store := first
				if index == 1 {
					store = second
				}
				go func(store *harnesspostgres.Store, value harness.Item) {
					<-start
					item, created, err := store.AppendItem(ctx, value)
					outcomes <- result{item: item, created: created, err: err}
				}(store, value)
			}
			close(start)

			// Both must be waiting on the locked Turn(s) before release; an
			// ordinary simultaneous start does not reliably exercise the race.
			waitForBlockedHarnessItemAppends(t, db, 2)
			if err := lock.Commit().Error; err != nil {
				t.Fatal(err)
			}
			locked = false

			firstResult, secondResult := <-outcomes, <-outcomes
			if !testCase.crossTurn {
				if firstResult.err != nil || secondResult.err != nil {
					t.Fatalf("concurrent same-ID replay returned errors: %v, %v", firstResult.err, secondResult.err)
				}
				freshCount := 0
				for _, outcome := range []result{firstResult, secondResult} {
					if outcome.created {
						freshCount++
					}
					if outcome.item.ID != id || outcome.item.Seq == 0 {
						t.Errorf("invalid replay response: %#v", outcome)
					}
				}
				if freshCount != 1 || firstResult.item.Seq != secondResult.item.Seq ||
					string(firstResult.item.Payload) != string(secondResult.item.Payload) {
					t.Fatalf("replay identity changed: fresh=%d left=%#v right=%#v", freshCount, firstResult, secondResult)
				}
			} else {
				freshCount, conflicts := 0, 0
				for _, outcome := range []result{firstResult, secondResult} {
					switch {
					case outcome.err == nil && outcome.created:
						freshCount++
					case errors.Is(outcome.err, harness.ErrConflict):
						conflicts++
					default:
						t.Errorf("cross-turn collision returned an unexpected result: %#v", outcome)
					}
				}
				if freshCount != 1 || conflicts != 1 {
					t.Fatalf("cross-turn identity: fresh=%d conflicts=%d", freshCount, conflicts)
				}
			}
			// Count by global ID across both Turn streams; replay must not
			// produce two persisted Items with distinct sequence numbers.
			count := int64(0)
			if err := db.Table("agent_harness_items").Where("id = ?", id).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("persisted ID count=%d err=%v", count, err)
			}
		})
	}
}

func waitForBlockedHarnessItemAppends(t *testing.T, db *gorm.DB, count int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int64
		if err := db.Raw(`
			SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%agent_harness_turns%'
			  AND query ILIKE '%FOR UPDATE%'
		`).Scan(&waiting).Error; err != nil {
			t.Fatalf("inspect PostgreSQL lock waiters: %v", err)
		}
		if waiting >= count {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("did not observe %d concurrent PostgreSQL Turn lock waiters", count)
}
