package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/encrypted"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestEncryptedV8RelationMigrationPreservesOriginalAuthority(t *testing.T) {
	ctx := t.Context()
	path := ownedPrivatePath(t, "encrypted-v8.sqlite")
	var key [32]byte
	key[0] = 27
	defer clear(key[:])
	open := encrypted.WithKey(&key)
	initialize := func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		for _, ddl := range schemaV8DDL() {
			if _, err = tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=8"); err != nil {
			return err
		}
		if err = initializeManagedAuthority(ctx, tx); err != nil {
			return err
		}
		return tx.Commit()
	}
	validate := func(ctx context.Context, db *sql.DB) error { return validateSchemaDDL(ctx, db, schemaV8DDL()) }
	db, err := open.CreateNew(ctx, path, initialize, validate)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db}
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	before, err := rt.Create(ctx, kernel.CreateRequest{ID: "parent", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "original", State: []byte(`{"frozen":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := runrelation.New(NewRunRelationStore(s), nil)
	if err != nil {
		t.Fatal(err)
	}
	relation, err := reg.Ensure(ctx, runrelation.Draft{ParentRunID: "parent", ChildRunID: "original-child", OwnerNodeID: "original", Kind: runrelation.KindDelegation})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := sqliteManagedAuthority(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExistingWith(ctx, path, open); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("implicit migration", err)
	}
	var wrong [32]byte
	if err = MigrateV8ToV9With(ctx, path, encrypted.WithKey(&wrong)); err == nil {
		t.Fatal("wrong key migrated original")
	}
	for range 2 {
		if err = MigrateV8ToV9With(ctx, path, open); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = OpenExistingWith(ctx, path, open); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("implicit v10 migration", err)
	}
	if err = MigrateV9ToV10With(ctx, path, encrypted.WithKey(&wrong)); err == nil {
		t.Fatal("wrong key migrated original v9")
	}
	for range 2 {
		if err = MigrateV9ToV10With(ctx, path, open); err != nil {
			t.Fatal(err)
		}
	}
	s, err = OpenExistingWith(ctx, path, open)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	actual, err := s.Load(ctx, before.Run.ID)
	if err != nil || !reflect.DeepEqual(actual, before) {
		t.Fatal("rewritten original run", err)
	}
	got, err := NewRunRelationStore(s).GetByChild(ctx, relation.ChildRunID)
	if err != nil || got != relation {
		t.Fatal("rewritten original relation", err)
	}
	afterAuthority, err := sqliteManagedAuthority(ctx, s.db)
	if err != nil || afterAuthority != authority {
		t.Fatal("replaced native authority", err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM runtime_run_relation_progress").Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented progress", err)
	}
}

// Confirm the actual SQLite planner selects an indexed bounded key range
// before joining Runs and progress. Never scan all historical relations
// merely to discover one page of eligible terminal children.
func TestSQLiteRelationPagerUsesIndexedRange(t *testing.T) {
	s, err := CreateNew(t.Context(), ownedPrivatePath(t, "indexed-page.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+relationPageSQL, "child-100", 128)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var indexed bool
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
		if strings.Contains(detail, "SEARCH runtime_run_relations USING INDEX") && strings.Contains(detail, "child_run_id>?") {
			indexed = true
		}
		if strings.HasPrefix(detail, "SCAN runtime_run_relations") {
			t.Fatal("unbounded relation scan", detail)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("missing indexed key range", details)
	}
}
