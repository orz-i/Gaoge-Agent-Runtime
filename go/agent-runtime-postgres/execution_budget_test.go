package postgres

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRealPostgresExecutionBudgetAtomic(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	conformance.RunExecutionBudgetSuite(t, func(t *testing.T) conformance.ExecutionBudgetStore {
		db, isolated := openIsolatedRealPostgres(t, dsn)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		s := NewKernelStore(db)
		reopen := func(t *testing.T) (kernel.Store, budget.LedgerStore) {
			next, err := gorm.Open(postgres.Open(isolated), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := next.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			reopened := NewKernelStore(next)
			return reopened, reopened
		}
		return conformance.ExecutionBudgetStore{Store: s, Ledger: s, Reopen: reopen}
	})
}

// The coupled write must use the Store clock after an actual ledger row lock
// wait, not just after the caller's pre-transaction planning.
func TestRealPostgresExecutionBudgetExpiryDuringLedgerLock(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	s := NewKernelStore(db)
	now := time.Now().UTC()
	original, err := s.Create(ctx, kernel.Record{
		ExecutionRequired: true,
		BudgetEnrollment:  &budget.RunEnrollment{ScopeID: "scope", Limits: budget.Limits{MaxToolCalls: 1}},
		Run:               kernel.Run{ID: "run", BudgetScopeID: "scope", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "thread", ID: "thread"}, Goal: "ledger lock", Revision: 1, Status: kernel.RunStatusRunning, CreatedAt: now, UpdatedAt: now},
		State:             json.RawMessage(`{"phase":"initial"}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.LoadBudget(ctx, "scope")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := budget.PlanLedgerMutation(ctx, l, func(c budget.Coordinator) error {
		_, e := c.Reserve(ctx, "scope", budget.Reservation{ID: "tool:run:call", RunID: "run", RequestHash: "original", Requested: budget.Usage{ToolCalls: 1}}, true)
		return e
	})
	if err != nil || plan == nil {
		t.Fatalf("plan: %v", err)
	}
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var pid int
	if err = tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
		t.Fatal(err)
	}
	var id string
	if err = tx.Raw("SELECT id FROM agent_harness_budgets WHERE id = ? FOR UPDATE", "scope").Scan(&id).Error; err != nil || id != "scope" {
		t.Fatalf("ledger lock: %v", err)
	}
	lease, err := s.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: "run", OwnerID: "owner", LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	run := original.Run
	run.Revision++
	run.UpdatedAt = time.Now().UTC()
	done := make(chan error, 1)
	go func() {
		_, _, e := s.Commit(ctx, (lease).RunID, original.Run.Revision, kernel.StoreMutation{Record: kernel.Record{Run: run, State: json.RawMessage(`{"phase":"prepared"}`)}, Ledger: plan}, kernel.LeaseCommit(lease))
		done <- e
	}()
	// Confirm the actual writer is blocked by this transaction before expiry.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err = db.WithContext(ctx).Raw("SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid)))", pid).Scan(&waiting).Error; err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("write did not wait: %v", e)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("write never waited on ledger row")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if wait := time.Until(lease.ExpiresAt) + 50*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	if err = tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("expired writer: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not return")
	}
	after, err := s.Load(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := s.LoadBudget(ctx, "scope")
	if err != nil {
		t.Fatal(err)
	}
	if after.Run.Revision != original.Run.Revision || after.EventHead != original.EventHead || ledger.Revision != l.Revision || len(ledger.Reservations) != 0 {
		t.Fatal("expired writer changed Run or ledger")
	}
}

func TestRealPostgresNativeBudgetCutoverAndCreateRollback(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	ctx := t.Context()
	s := NewKernelStore(db)
	rt, e := kernel.New(kernel.Dependencies{Store: s})
	if e != nil {
		t.Fatal(e)
	}
	request := kernel.CreateRequest{ID: "root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "test", State: []byte("{}"), ExecutionRequired: true, Budget: &budget.RunEnrollment{ScopeID: "scope", Limits: budget.Limits{MaxToolCalls: 1}}}
	// PostgreSQL rejects the event length after both native writes.
	request.Events = []kernel.EventDraft{{Type: strings.Repeat("x", 129)}}
	if _, e = rt.Create(ctx, request); e == nil || !strings.Contains(e.Error(), "22001") {
		t.Fatalf("create event fault not reached: %v", e)
	}
	if _, e = rt.Load(ctx, "root"); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("partial Run: %v", e)
	}
	if _, e = s.LoadBudget(ctx, "scope"); !errors.Is(e, budget.ErrLedgerNotFound) {
		t.Fatalf("partial ledger: %v", e)
	}
	request.Events = nil
	if _, e = rt.Create(ctx, request); e != nil {
		t.Fatal(e)
	}
	request.ID = "old"
	request.Budget = nil
	old, e := rt.Create(ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	legacy, e := (budget.Coordinator{Store: s}).RegisterRun(ctx, "scope", "old", budget.RunBudget{})
	if e != nil {
		t.Fatal(e)
	}
	if e = Migrate(db); e == nil || !strings.Contains(e.Error(), "inconsistent native budget bindings") {
		t.Fatalf("old membership admitted: %v", e)
	}
	actual, e := s.LoadBudget(ctx, "scope")
	if e != nil || !reflect.DeepEqual(legacy, actual) {
		t.Fatalf("legacy ledger mutated: %v", e)
	}
	run, e := rt.Load(ctx, "old")
	if e != nil || !reflect.DeepEqual(run, old) {
		t.Fatalf("legacy Run mutated: %v", e)
	}
}
