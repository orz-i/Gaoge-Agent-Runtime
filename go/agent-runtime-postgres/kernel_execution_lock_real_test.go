package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// An UPDATE can evaluate its lease-expiry predicate before waiting for an
// unchanged row's FOR UPDATE lock. Check authority only after acquiring that
// lock, even when no replacement owner has advanced the epoch or revision.
func TestRealPostgresExpiredExecutionCannotCommitAfterRowLockWait(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	rt, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.Create(ctx, kernel.CreateRequest{
		ID: "expiry-lock-wait", Kind: "agent", ExecutionRequired: true,
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "reject an expired owner after a row-lock wait", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "old-owner", LeaseDuration: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := rt.WithExecution(lease)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	var blockerPID int
	err = blocker.QueryRowContext(ctx,
		"SELECT pg_backend_pid() FROM agent_kernel_runs WHERE run_id=$1 FOR UPDATE",
		created.Run.ID).Scan(&blockerPID)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, writeErr := bound.Apply(ctx, created.Run.ID, created.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{"staleOwnerCommitted":true}`),
			Events: []kernel.EventDraft{{Type: "test.expired-owner", Wakeup: true}},
		})
		done <- writeErr
	}()
	// Match this exact lock holder rather than an unrelated blocked query
	// elsewhere on the database server.
	for {
		var waiting int64
		err = db.WithContext(ctx).Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE ? = ANY(pg_blocking_pids(pid))`, blockerPID).Scan(&waiting).Error
		if err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	var storeNow time.Time
	if err = db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&storeNow).Error; err != nil {
		t.Fatal(err)
	}
	if !storeNow.Before(lease.ExpiresAt) {
		t.Fatal("lease expired before the writer reached the row-lock wait")
	}
	for !storeNow.After(lease.ExpiresAt.Add(50 * time.Millisecond)) {
		time.Sleep(5 * time.Millisecond)
		if err = db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&storeNow).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	var applyErr error
	select {
	case applyErr = <-done:
	case <-ctx.Done():
		t.Fatal("fenced Apply did not return after unlocking:", ctx.Err())
	}

	stored, loadErr := rt.Load(ctx, created.Run.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	var journalCount, outboxCount int64
	err = db.WithContext(ctx).Table("agent_kernel_events").
		Where("run_id = ? AND seq > ?", created.Run.ID, created.EventHead).Count(&journalCount).Error
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithContext(ctx).Table("agent_kernel_transition_outbox").
		Where("run_id = ? AND revision > ?", created.Run.ID, created.Run.Revision).Count(&outboxCount).Error
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(applyErr, kernel.ErrExecutionConflict) {
		t.Errorf("expired owner Apply error = %v, want ErrExecutionConflict", applyErr)
	}
	if stored.Run.Revision != created.Run.Revision || !bytes.Equal(stored.State, created.State) ||
		stored.EventHead != created.EventHead || journalCount != 0 || outboxCount != 0 {
		t.Fatalf("expired owner changed durable state: revision=%d state=%s head=%d new journal=%d outbox=%d",
			stored.Run.Revision, stored.State, stored.EventHead, journalCount, outboxCount)
	}
}
