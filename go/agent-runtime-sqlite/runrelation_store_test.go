package sqlite_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func sqliteRelation(parent, child, owner string, at time.Time) runrelation.Relation {
	return runrelation.Relation{
		ParentRunID: parent, ChildRunID: child, Kind: runrelation.KindCapability,
		OwnerNodeID: owner, CreatedAt: at,
	}
}

func TestSQLiteRunRelationStoreConformance(t *testing.T) {
	conformance.RunRunRelationStoreSuite(t, func(tb testing.TB) runrelation.Store {
		tb.Helper()
		child, ok := tb.(*testing.T)
		if !ok {
			tb.Fatal("expected *testing.T")
		}
		return sqlite.NewRunRelationStore(openStore(child))
	})
}

func TestSQLiteRunRelationReopenAndDeterministicOrdering(t *testing.T) {
	path := filepath.Join(privateDir(t), "relations.sqlite")
	first, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	relationStore := sqlite.NewRunRelationStore(first)
	at := time.Date(2026, 10, 17, 12, 0, 0, 123, time.FixedZone("offset", 3600))
	items := []runrelation.Relation{
		sqliteRelation("parent", "child-b", "owner-b", at),
		sqliteRelation("parent", "child-a", "owner-a", at),
		sqliteRelation("other", "child-c", "owner-c", at.Add(-time.Second)),
	}
	for _, item := range items {
		if _, reused, err := relationStore.Put(t.Context(), item); err != nil || reused {
			t.Fatalf("Put(%+v) reused=%v err=%v", item, reused, err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	relationStore = sqlite.NewRunRelationStore(reopened)
	children, err := relationStore.ListChildren(t.Context(), " parent ")
	if err != nil || len(children) != 2 || children[0].ChildRunID != "child-a" || children[1].ChildRunID != "child-b" {
		t.Fatalf("reopened children=%+v err=%v", children, err)
	}
	all, err := relationStore.ListAll(t.Context())
	if err != nil || len(all) != 3 || all[0].ChildRunID != "child-c" || all[1].ChildRunID != "child-a" || all[2].ChildRunID != "child-b" {
		t.Fatalf("reopened order=%+v err=%v", all, err)
	}
	original, err := relationStore.GetByChild(t.Context(), " child-a ")
	if err != nil || !original.CreatedAt.Equal(at.UTC()) || original.CreatedAt.Location() != time.UTC {
		t.Fatalf("reopened timestamp=%+v err=%v", original, err)
	}
	replay := items[1]
	replay.CreatedAt = replay.CreatedAt.Add(time.Hour)
	persisted, reused, err := relationStore.Put(t.Context(), replay)
	if err != nil || !reused || !persisted.CreatedAt.Equal(at.UTC()) {
		t.Fatalf("reopen idempotence=%+v reused=%v err=%v", persisted, reused, err)
	}
	if _, err := relationStore.GetByChild(t.Context(), "absent"); !errors.Is(err, runrelation.ErrNotFound) {
		t.Fatalf("unknown child: %v", err)
	}
	if _, err := relationStore.ListChildren(t.Context(), "  "); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("blank parent: %v", err)
	}
}

func TestSQLiteRunRelationAtomicFailureAndRetry(t *testing.T) {
	path := filepath.Join(privateDir(t), "relations.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	_, err = raw.Exec(`CREATE TRIGGER abort_relation AFTER INSERT ON runtime_run_relations
		BEGIN SELECT RAISE(ABORT, 'injected relation write failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	relation := sqliteRelation("parent", "child", "owner", time.Now().UTC())
	relationStore := sqlite.NewRunRelationStore(store)
	if _, _, err = relationStore.Put(t.Context(), relation); err == nil {
		t.Fatal("injected after-insert failure incorrectly committed")
	}
	var count int
	if err = raw.QueryRow("SELECT count(*) FROM runtime_run_relations").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial insert count=%d err=%v", count, err)
	}
	if _, err = relationStore.GetByChild(t.Context(), "child"); !errors.Is(err, runrelation.ErrNotFound) {
		t.Fatalf("failed insert visible: %v", err)
	}
	if _, err = raw.Exec("DROP TRIGGER abort_relation"); err != nil {
		t.Fatal(err)
	}
	if _, reused, err := relationStore.Put(t.Context(), relation); err != nil || reused {
		t.Fatalf("retry after rollback reused=%v err=%v", reused, err)
	}
}

// SQLite lock contention is retryable and never masquerades as an immutable
// relation identity conflict.
func TestSQLiteRunRelationBusyNotIdentityConflict(t *testing.T) {
	path := filepath.Join(privateDir(t), "relations.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	tx, err := raw.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(t.Context(), "INSERT INTO runtime_run_relations(child_run_id,parent_run_id,kind,owner_node_id,created_ns) VALUES(?,?,?,?,?)",
		"occupied", "held-parent", string(runrelation.KindCapability), "held-owner", time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	item := sqliteRelation("parent", "child", "owner", time.Now().UTC())
	if _, _, err = sqlite.NewRunRelationStore(store).Put(t.Context(), item); !errors.Is(err, sqlite.ErrBusy) ||
		errors.Is(err, runrelation.ErrConflict) {
		t.Fatalf("concurrent uncommitted writer must be retryable busy, got %v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, reused, err := sqlite.NewRunRelationStore(store).Put(t.Context(), item); err != nil || reused {
		t.Fatalf("released writer lock could not proceed: reused=%v err=%v", reused, err)
	}
}

func TestSQLiteRunRelationInvalidTimeAndCancelledContext(t *testing.T) {
	store := openStore(t)
	relations := sqlite.NewRunRelationStore(store)
	item := sqliteRelation("parent", "child", "owner", time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, _, err := relations.Put(t.Context(), item); !errors.Is(err, sqlite.ErrInvalidTime) {
		t.Fatalf("out of range timestamp: %v", err)
	}
	item.CreatedAt = time.Now().UTC()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := relations.Put(ctx, item); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Put: %v", err)
	}
	items, err := relations.ListAll(t.Context())
	if err != nil || len(items) != 0 {
		t.Fatalf("invalid/cancelled Put persisted entries: %+v err=%v", items, err)
	}
}

func TestSQLiteRunRelationOldUnreleasedV1FailsClosed(t *testing.T) {
	path := filepath.Join(privateDir(t), "older-development.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("DROP TABLE runtime_run_relations"); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sqlite.OpenExisting(t.Context(), path); !errors.Is(err, sqlite.ErrUnsupportedSchema) {
		t.Fatalf("older development v1 accepted: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected database modified: %v", err)
	}
}
