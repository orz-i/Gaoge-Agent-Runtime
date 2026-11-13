package postgres

import (
	"context"
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

// A physically valid CurrentStatus=Running is not enough to establish the
// caller's expected Run revision. The Workflow Resume entrypoint captured
// Revision R before invoking child Begin; while Begin is blocked on a parent
// row lock, a separate valid worker advances the child to R+1. The original
// physical admission still succeeds, because the Grant has no expectedRev.
func TestAuditIssue107PostgresBeginCannotBindCallerExpectedChildRevision(t *testing.T) {
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
	rt, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "parent", Kind: "workflow", Actor: actor, Thread: thread, Goal: "parent", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "child", Kind: "workflow", ParentRunID: parent.Run.ID, Actor: actor, Thread: thread,
		Goal: "child with pending effect at revision R", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	parentLock := db.Begin()
	if parentLock.Error != nil {
		t.Fatal(parentLock.Error)
	}
	defer func() { _ = parentLock.Rollback() }()
	var row models.KernelRunRecord
	if err = parentLock.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("run_id = ?", parent.Run.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	admitted := make(chan error, 1)
	go func() {
		admitted <- NewKernelStore(otherDB).BeginChildExecution(ctx, kernel.ChildExecutionGrant{
			ID: "grant-from-stale-caller-revision", ChildRunID: child.Run.ID, ExpectedRevision: child.Run.Revision,
		})
	}()
	blocked := false
	for i := 0; i < 90; i++ {
		var count int64
		if err = db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()
   AND wait_event_type='Lock' AND query LIKE '%agent_kernel_runs%'`).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			blocked = true
			break
		}
		select {
		case early := <-admitted:
			t.Fatalf("Begin returned before parent lock release: %v", early)
		default:
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("could not confirm child physical admission waiting for parent lock")
	}
	updated, err := rt.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: child.State,
	})
	if err != nil {
		t.Fatalf("concurrent valid child revision increment failed: %v", err)
	}
	if err = parentLock.Commit().Error; err != nil {
		t.Fatal(err)
	}
	var beginErr error
	select {
	case beginErr = <-admitted:
	case <-time.After(4 * time.Second):
		t.Fatal("physical Begin still blocked")
	}
	after, err := rt.Load(t.Context(), child.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("originalCallerRevision=%d currentRevision=%d storeGranted=%v grantError=%v",
		child.Run.Revision, after.Run.Revision, beginErr == nil, beginErr)
	if updated.Run.Revision <= child.Run.Revision {
		t.Fatal("fixture failed to advance original Run")
	}
	if beginErr == nil {
		t.Fatal("PostgreSQL admitted a physical Grant for a different child revision than the invoking Workflow Resume had validated")
	}
}
