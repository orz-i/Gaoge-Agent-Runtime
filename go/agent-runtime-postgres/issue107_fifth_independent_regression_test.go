package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type auditBeginChildResult struct {
	grant kernel.ChildExecutionGrant
	err   error
}

// Force BeginChildExecution's first (unlocked) child.Run.Status observation,
// then block it on the parent row lock. While it waits, the other connection
// commits child Cancel, without modifying the parent. The Begin contract must
// recheck child liveness after acquiring the authoritative lock.
func TestAuditIssue107RealPostgresBeginOnAlreadyCancelledChild(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	otherDB, err := gorm.Open(pgdriver.Open(isolatedDSN), realPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	otherSQL, err := otherDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = otherSQL.Close() }()
	original, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	parent, err := original.Create(t.Context(), kernel.CreateRequest{
		ID: "parent", Kind: "workflow", Actor: actor, Thread: thread, Goal: "live parent", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := original.Create(t.Context(), kernel.CreateRequest{
		ID: "child", Kind: "agent", ParentRunID: parent.Run.ID, Actor: actor, Thread: thread,
		Goal: "child already cancelling", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	lockTx := db.Begin()
	if lockTx.Error != nil {
		t.Fatal(lockTx.Error)
	}
	defer func() { _ = lockTx.Rollback() }()
	var locked models.KernelRunRecord
	if err = lockTx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("run_id = ?", parent.Run.ID).First(&locked).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	done := make(chan auditBeginChildResult, 1)
	go func() {
		beginErr := NewKernelStore(otherDB).BeginChildExecution(ctx, kernel.ChildExecutionGrant{
			ID: "known-original-grant", ChildRunID: child.Run.ID, ExpectedRevision: child.Run.Revision,
		})
		done <- auditBeginChildResult{grant: kernel.ChildExecutionGrant{ID: "known-original-grant", ChildRunID: child.Run.ID, ExpectedRevision: child.Run.Revision}, err: beginErr}
	}()
	// Confirm the child Begin has already read child state and is blocked
	// waiting on the locked parent row, rather than relying on a timed sleep.
	waiting := false
	for i := 0; i < 90; i++ {
		var count int64
		queryErr := db.Raw(`SELECT count(*) FROM pg_stat_activity
    WHERE datname=current_database() AND wait_event_type='Lock'
    AND query LIKE '%agent_kernel_runs%'`).Scan(&count).Error
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		if count > 0 {
			waiting = true
			break
		}
		select {
		case early := <-done:
			t.Fatalf("Begin unexpectedly finished before parent row release: %v", early.err)
		default:
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("could not confirm physical Begin blocked behind parent row")
	}
	cancelled, cancelErr := original.Cancel(t.Context(), child.Run.ID, child.Run.Revision, "child cancelled before original Grant commit")
	if cancelErr != nil || cancelled.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("independent child terminal failed: %+v %v", cancelled.Run, cancelErr)
	}
	if err = lockTx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	var result auditBeginChildResult
	select {
	case result = <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("original Begin did not return")
	}
	after, readErr := original.Load(t.Context(), child.Run.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("child=%s grantID=%s BeginErr=%v parent still running=%v",
		after.Run.Status, result.grant.ID, result.err, true)
	if result.err == nil {
		t.Fatal("PostgreSQL admitted a physical child Grant using its stale pre-lock Running snapshot after child Cancel committed")
	}
	if !errors.Is(result.err, kernel.ErrExecutionConflict) {
		t.Logf("candidate denial error is not the expected ownership error: %v", result.err)
	}
}
