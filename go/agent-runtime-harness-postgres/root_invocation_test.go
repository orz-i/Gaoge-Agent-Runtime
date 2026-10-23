package harnesspostgres_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
)

func TestStoresEnforceOneRootInvocationPerTurn(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			store := create(t)
			root := rootInvocationFixture("root", "turn", " \t\n")
			created, fresh, err := store.CreateInvocation(t.Context(), root)
			if err != nil || !fresh || created.ParentItemID != "" {
				t.Fatalf("create root: invocation=%#v fresh=%v err=%v", created, fresh, err)
			}
			replayed, fresh, err := store.CreateInvocation(t.Context(), root)
			if err != nil || fresh || replayed.ID != created.ID {
				t.Fatalf("replay root: invocation=%#v fresh=%v err=%v", replayed, fresh, err)
			}
			if _, fresh, err = store.CreateInvocation(t.Context(), rootInvocationFixture("another-root", "turn", "")); !errors.Is(err, harness.ErrConflict) || fresh {
				t.Fatalf("second root: fresh=%v err=%v", fresh, err)
			}
			for _, value := range []harness.Invocation{
				rootInvocationFixture("child-a", "turn", "parent-item"),
				rootInvocationFixture("child-b", "turn", "parent-item"),
				rootInvocationFixture("other-root", "another-turn", ""),
			} {
				if _, fresh, err = store.CreateInvocation(t.Context(), value); err != nil || !fresh {
					t.Fatalf("create allowed invocation %s: fresh=%v err=%v", value.ID, fresh, err)
				}
			}
			values, err := store.ListInvocations(t.Context(), "turn")
			if err != nil || len(values) != 3 {
				t.Fatalf("rejected root left records: invocations=%#v err=%v", values, err)
			}
			if _, err = store.GetInvocationByExecutionRefID(t.Context(), "run-another-root"); !errors.Is(err, harness.ErrNotFound) {
				t.Fatalf("rejected root retained execution mapping: %v", err)
			}
		})
	}
}

func TestStoresConcurrentRootInvocationCreation(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			store := create(t)
			assertConcurrentRootInvocationCreation(t, []harness.Store{store}, "competing-turn")
		})
	}
}

func rootInvocationStores() map[string]func(*testing.T) harness.Store {
	return map[string]func(*testing.T) harness.Store{
		"memory": func(*testing.T) harness.Store { return harness.NewMemoryStore() },
		"sql":    func(t *testing.T) harness.Store { return newStore(t) },
	}
}

func rootInvocationFixture(id, turnID, parentItemID string) harness.Invocation {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	return harness.Invocation{
		ID: id, TurnID: turnID, ParentItemID: parentItemID, CapabilityKey: harness.CapabilityAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionAgent,
		ExecutionRefID: "run-" + id, Status: harness.InvocationAccepted, Attempt: 1, Revision: 1,
		OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
}

func assertConcurrentRootInvocationCreation(t *testing.T, stores []harness.Store, turnID string) {
	t.Helper()
	const writers = 8
	start := make(chan struct{})
	results := make(chan error, writers)
	for index := range writers {
		go func() {
			<-start
			_, _, err := stores[index%len(stores)].CreateInvocation(t.Context(),
				rootInvocationFixture(fmt.Sprintf("root-%d", index), turnID, ""))
			results <- err
		}()
	}
	close(start)
	winners := 0
	for range writers {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("concurrent root create: %v", err)
		}
	}
	values, err := stores[0].ListInvocations(t.Context(), turnID)
	if err != nil || winners != 1 || len(values) != 1 || values[0].ParentItemID != "" {
		t.Fatalf("concurrent roots: winners=%d invocations=%#v err=%v", winners, values, err)
	}
}

func TestStoreMigratesExistingInvocationsToSingleRootConstraint(t *testing.T) {
	store, db := newStoreWithDB(t)
	if err := db.Migrator().DropIndex("agent_harness_invocations", "uk_harness_invocation_root"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateInvocation(t.Context(), rootInvocationFixture("legacy-root", "turn", "")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := harnesspostgres.Migrate(db); err != nil {
			t.Fatalf("migrate existing root: %v", err)
		}
	}
	if _, _, err := store.CreateInvocation(t.Context(), rootInvocationFixture("second-root", "turn", "")); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("upgraded schema allowed another root: %v", err)
	}
}

func TestStoreMigrationPreservesHistoricalDuplicateRoots(t *testing.T) {
	store, db := newStoreWithDB(t)
	if err := db.Migrator().DropIndex("agent_harness_invocations", "uk_harness_invocation_root"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"legacy-root-a", "legacy-root-b"} {
		if _, _, err := store.CreateInvocation(t.Context(), rootInvocationFixture(id, "turn", "")); err != nil {
			t.Fatal(err)
		}
	}
	if err := harnesspostgres.Migrate(db); err == nil {
		t.Fatal("migration accepted historical duplicate roots")
	}
	values, err := store.ListInvocations(t.Context(), "turn")
	if err != nil || len(values) != 2 {
		t.Fatalf("migration rewrote historical roots: invocations=%#v err=%v", values, err)
	}
}

func TestStoreMigrationRejectsHistoricalWhitespaceRoots(t *testing.T) {
	for name, parentItemID := range map[string]string{
		"space": " ", "tab": "\t", "mixed": " \t", "nonbreaking-space": "\u00a0",
	} {
		t.Run(name, func(t *testing.T) {
			store, db := newStoreWithDB(t)
			if err := db.Migrator().DropIndex("agent_harness_invocations", "uk_harness_invocation_root"); err != nil {
				t.Fatal(err)
			}
			value := rootInvocationFixture("legacy-root", "turn", "")
			if _, _, err := store.CreateInvocation(t.Context(), value); err != nil {
				t.Fatal(err)
			}
			// Simulate a value accepted by the old Store, bypassing today's
			// ParentItemID normalization at the public creation boundary.
			if err := db.Exec("UPDATE agent_harness_invocations SET parent_item_id = ? WHERE id = ?", parentItemID, value.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err == nil || !strings.Contains(err.Error(), "whitespace-only") {
				t.Fatalf("migration accepted historical whitespace root: %v", err)
			}
			values, err := store.ListInvocations(t.Context(), value.TurnID)
			if err != nil || len(values) != 1 || values[0].ParentItemID != parentItemID {
				t.Fatalf("migration changed historical root: invocations=%#v err=%v", values, err)
			}
			if db.Migrator().HasIndex("agent_harness_invocations", "uk_harness_invocation_root") {
				t.Fatal("migration installed a root constraint that omits the historical root")
			}
		})
	}
}

func TestStoreRootMigrationHandlesMissingTableOrParentColumn(t *testing.T) {
	for _, missing := range []string{"table", "column"} {
		t.Run(missing, func(t *testing.T) {
			store, db := newStoreWithDB(t)
			if missing == "table" {
				if err := db.Migrator().DropTable("agent_harness_invocations"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.Migrator().DropIndex("agent_harness_invocations", "uk_harness_invocation_root"); err != nil {
					t.Fatal(err)
				}
				if err := db.Exec("ALTER TABLE agent_harness_invocations DROP COLUMN parent_item_id").Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatalf("migrate schema missing %s: %v", missing, err)
			}
			if _, fresh, err := store.CreateInvocation(t.Context(), rootInvocationFixture("root", "turn", "")); err != nil || !fresh {
				t.Fatalf("create root after schema migration: fresh=%v err=%v", fresh, err)
			}
		})
	}
}
