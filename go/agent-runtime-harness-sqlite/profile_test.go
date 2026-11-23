package harnesssqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesssqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-sqlite"
	kernelsqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	"modernc.org/sqlite"
)

func newHost(t *testing.T) (string, *harnesssqlite.Store) {
	t.Helper()
	path := filepath.Join(testprofile.Dir(t), "Harness #100%.sqlite")
	host, err := harnesssqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return path, host
}

func reopenHost(t *testing.T, path string) *harnesssqlite.Store {
	t.Helper()
	host, err := harnesssqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host
}

func rawPool(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := profile.OpenExisting(t.Context(), path, func(context.Context, *sql.DB) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestProfileRequiresItsOriginalSchemaAndAuthority(t *testing.T) {
	path := filepath.Join(testprofile.Dir(t), "missing.sqlite")
	if _, err := harnesssqlite.OpenExisting(t.Context(), path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open missing file: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reopen created a file: %v", err)
	}
	k, err := kernelsqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = k.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := harnesssqlite.OpenExisting(t.Context(), path); !errors.Is(err, profile.ErrUnsupportedSchema) {
		t.Fatalf("opened a Kernel database as Harness: %v", err)
	}
	k, err = kernelsqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = k.Close(); err != nil {
		t.Fatal(err)
	}
	for name, damage := range map[string]string{
		"schema-version":     "PRAGMA user_version=3",
		"old-schema-version": "PRAGMA user_version=1",
		"profile-kind":       "PRAGMA application_id=123",
		"missing-root-index": "DROP INDEX uk_harness_invocation_root",
		"extra-trigger":      "CREATE TRIGGER extra AFTER INSERT ON agent_harness_turns BEGIN SELECT 1; END",
		"missing-authority":  "DELETE FROM agent_harness_managed_authority",
		"invalid-authority":  "UPDATE agent_harness_managed_authority SET generation=0",
	} {
		t.Run(name, func(t *testing.T) {
			path, host := newHost(t)
			if _, err := harnesssqlite.CreateNew(t.Context(), path); !errors.Is(err, os.ErrExist) {
				t.Fatalf("replaced existing profile: %v", err)
			}
			if err := host.Close(); err != nil {
				t.Fatal(err)
			}
			db := rawPool(t, path)
			if _, err := db.ExecContext(t.Context(), damage); err != nil {
				t.Fatal(err)
			}
			if _, err := harnesssqlite.OpenExisting(t.Context(), path); !errors.Is(err, profile.ErrUnsupportedSchema) {
				t.Fatalf("accepted damaged profile: %v", err)
			}
			if name == "missing-authority" {
				var count int
				if err := db.QueryRow("SELECT COUNT(*) FROM agent_harness_managed_authority").Scan(&count); err != nil || count != 0 {
					t.Fatalf("reopen recreated authority: %d, %v", count, err)
				}
			}
		})
	}
}

func TestWriterContentionPreservesRetryableDatabaseError(t *testing.T) {
	path, host := newHost(t)
	db := rawPool(t, path)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	req := request()
	now := time.Now().UTC()
	session := harness.Session{ID: "concurrent-session", HostThread: req.HostThread, Actor: req.Actor, Revision: 1, CreatedAt: now, UpdatedAt: now}
	_, fresh, err := host.CreateSession(t.Context(), session)
	var busy *sqlite.Error
	if fresh || errors.Is(err, harness.ErrConflict) || !errors.As(err, &busy) || busy.Code()&0xff != 5 {
		t.Fatalf("writer lock incorrectly classified: %t %v", fresh, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err = host.CreateSession(t.Context(), session); err != nil || !fresh {
		t.Fatalf("retry after lock release: %t %v", fresh, err)
	}
	session.Actor.ActorID = "another-actor"
	if _, fresh, err = host.CreateSession(t.Context(), session); fresh || !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("changed original actor: %t %v", fresh, err)
	}
}
