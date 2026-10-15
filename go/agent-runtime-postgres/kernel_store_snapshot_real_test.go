package postgres

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Create must expose the database's actual timestamp precision. A value
// built from the pre-insert Go record can differ from PostgreSQL's microseconds,
// even though both are revision 1.
func TestRealPostgresKernelStoreCreateReturnsPersistedTimestamp(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	store := NewKernelStore(db)
	record := snapshotTestRecord("create-timestamp")
	record.Run.CreatedAt = time.Date(2026, 10, 15, 8, 7, 36, 430715135, time.UTC)
	record.Run.UpdatedAt = record.Run.CreatedAt
	created, err := store.Create(t.Context(), record, []kernel.EventDraft{{Type: "run.created"}})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load(t.Context(), record.Run.ID)
	if err != nil || !reflect.DeepEqual(created, persisted) {
		t.Fatalf("Create returned non-persisted snapshot: created=%#v persisted=%#v err=%v", created, persisted, err)
	}
}

func TestRealPostgresKernelStoreApplyReturnsOwnCommittedSnapshot(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	testKernelStoreApplyOwnSnapshot(t, db)
}

func TestRealPostgresKernelStoreCreateReturnsOwnCommittedSnapshot(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	testKernelStoreCreateOwnSnapshot(t, db)
}
