package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// This test uses two genuinely independent PostgreSQL connections. It proves
// that the parent row lock serializes cancellation against the child physical
// Grant, not merely that one Go mutex serializes the SDK's calls.
func TestIssue107RealPostgresChildGrantVsParentTermination(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	oneDB, isolatedDSN := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(oneDB); err != nil {
		t.Fatal(err)
	}
	twoDB, err := gorm.Open(pgdriver.Open(isolatedDSN), realPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	twoSQL, err := twoDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = twoSQL.Close() }()
	one, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(oneDB)})
	if err != nil {
		t.Fatal(err)
	}
	two, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(twoDB)})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("i-%d", i)
		t.Run(name, func(t *testing.T) {
			root, err := one.Create(t.Context(), kernel.CreateRequest{ID: "root-" + name, Kind: "workflow",
				Actor: actor, Thread: thread, Goal: "competing physical authority", State: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			child, err := one.Create(t.Context(), kernel.CreateRequest{ID: "child-" + name, ParentRunID: root.Run.ID, Kind: "agent",
				Actor: actor, Thread: thread, Goal: "effect", State: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			var grant kernel.ChildExecutionGrant
			var beginErr, cancelErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				grant, beginErr = one.BeginChildExecution(context.Background(), child.Run.ID, child.Run.Revision)
			}()
			go func() {
				defer wg.Done()
				<-start
				_, cancelErr = two.Cancel(context.Background(), root.Run.ID, root.Run.Revision, "competing terminal CAS")
			}()
			close(start)
			wg.Wait()
			if beginErr == nil && cancelErr == nil {
				t.Fatal("parent termination and child admission both succeeded")
			}
			if beginErr == nil {
				if !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) {
					t.Fatalf("child grant won but root Cancel=%v", cancelErr)
				}
				if err = NewKernelStore(twoDB).MarkChildExecutionSettled(t.Context(), grant, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) {
					t.Fatalf("untrusted direct Store Mark authorized cleanup: %v", err)
				}
				if err = NewKernelStore(twoDB).FinishChildExecution(t.Context(), grant); !errors.Is(err, kernel.ErrExecutionConflict) {
					t.Fatalf("pending physical Grant was released without receipt: %v", err)
				}
			} else if cancelErr == nil {
				if !errors.Is(beginErr, kernel.ErrExecutionConflict) {
					t.Fatalf("parent cancellation won but child Begin=%v", beginErr)
				}
			} else {
				t.Fatalf("two unsuccessful competing writes: begin=%v cancel=%v", beginErr, cancelErr)
			}
		})
	}
}
