package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestExecutionBudgetAtomic(t *testing.T) {
	conformance.RunExecutionBudgetSuite(t, func(t *testing.T) conformance.ExecutionBudgetStore {
		ctx := t.Context()
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "authority.db")
		s, err := CreateNew(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		reopen := func(t *testing.T) (kernel.Store, budget.LedgerStore) {
			next, err := OpenExisting(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = next.Close() })
			return next, next
		}
		return conformance.ExecutionBudgetStore{Store: s, Ledger: s, Reopen: reopen}
	})
}

func TestNativeBudgetCutoverAndCreateRollback(t *testing.T) {
	ctx := t.Context()
	path := ownedPrivatePath(t, "budget-binding.sqlite")
	s, e := CreateNew(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	rt, e := kernel.New(kernel.Dependencies{Store: s})
	if e != nil {
		t.Fatal(e)
	}
	request := kernel.CreateRequest{ID: "root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "test", State: []byte("{}"), ExecutionRequired: true, Budget: &budget.RunEnrollment{ScopeID: "scope", Limits: budget.Limits{MaxToolCalls: 1}}}
	// Force failure after the native Run insert AND ledger write.
	if _, e = s.db.ExecContext(ctx, `CREATE TRIGGER reject_test_event BEFORE INSERT ON runtime_events BEGIN SELECT RAISE(ABORT,'injected event failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e = rt.Create(ctx, request); e == nil || !strings.Contains(e.Error(), "injected event failure") {
		t.Fatalf("create fault not reached: %v", e)
	}
	if _, e = rt.Load(ctx, "root"); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("partial Run: %v", e)
	}
	if _, e = s.LoadBudget(ctx, "scope"); !errors.Is(e, budget.ErrLedgerNotFound) {
		t.Fatalf("partial ledger: %v", e)
	}
	if _, e = s.db.ExecContext(ctx, "DROP TRIGGER reject_test_event"); e != nil {
		t.Fatal(e)
	}
	if _, e = rt.Create(ctx, request); e != nil {
		t.Fatal(e)
	}
	// Simulate an older writer which recorded a ledger but no Run binding.
	request.ID = "old"
	request.Budget = nil
	old, e := rt.Create(ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	c := budget.Coordinator{Store: s}
	legacy, e := c.RegisterRun(ctx, "scope", "old", budget.RunBudget{})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	opened, e := OpenExisting(ctx, path)
	if opened != nil {
		_ = opened.Close()
	}
	if !errors.Is(e, ErrUnsupportedSchema) || !strings.Contains(e.Error(), "inconsistent native budget bindings") {
		t.Fatalf("old budget membership admitted: %v", e)
	}
	raw, e := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = raw.Close() }()
	var got string
	if e = raw.QueryRow("SELECT payload_json FROM agent_harness_budgets WHERE id='scope'").Scan(&got); e != nil {
		t.Fatal(e)
	}
	var actual budget.Ledger
	if e = json.Unmarshal([]byte(got), &actual); e != nil || !reflect.DeepEqual(legacy, actual) {
		t.Fatalf("legacy ledger mutated: %v", e)
	}
	if e = raw.QueryRow("SELECT run_json FROM runtime_runs WHERE run_id='old'").Scan(&got); e != nil {
		t.Fatal(e)
	}
	var run kernel.Run
	if e = json.Unmarshal([]byte(got), &run); e != nil || !reflect.DeepEqual(run, old.Run) {
		t.Fatalf("legacy Run mutated: %v", e)
	}
}
