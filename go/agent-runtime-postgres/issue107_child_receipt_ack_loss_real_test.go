package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type issue107PostMarkAckLossPG struct{ *KernelStore }

func (s issue107PostMarkAckLossPG) MarkChildExecutionSettled(ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
	if err := s.KernelStore.MarkChildExecutionSettled(ctx, grant, permit); err != nil {
		return err
	}
	return kernel.ErrExecutionBusy // the original ACK was lost AFTER durable Mark
}

type issue107SimpleTextModelPG struct{}

func (issue107SimpleTextModelPG) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "completed", Usage: &model.Usage{InputTokens: 3, OutputTokens: 2}}, nil
}

func TestIssue107RealPostgresPhysicalSettlementAckLostAcrossStoreInstances(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	wrapped := issue107PostMarkAckLossPG{KernelStore: NewKernelStore(db)}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	root, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "workflow",
		Actor: actor, Thread: thread, Goal: "parent", State: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: issue107SimpleTextModelPG{}})
	if err != nil {
		t.Fatal(err)
	}
	child, callErr := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "child", RequestID: "frozen-effect", ParentRunID: root.Run.ID,
		OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"isolatedContext":true}`)),
		Actor:             actor, Thread: thread, Goal: "complete one Model call", Model: "test-model",
	})
	if !errors.Is(callErr, kernel.ErrChildExecutionBusy) {
		t.Fatalf("post-commit settlement ACK loss incorrectly reported success/no effect: %+v err=%v", child.Run, callErr)
	}
	if _, err = rt.Cancel(t.Context(), root.Run.ID, root.Run.Revision, "before original recovery"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("pending original Grant released on ACK loss: %v", err)
	}
	secondDB, err := gorm.Open(pgdriver.Open(isolatedDSN), realPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	secondSQL, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secondSQL.Close() }()
	resumedRuntime, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(secondDB)})
	if err != nil {
		t.Fatal(err)
	}
	resumedAgent, err := agent.NewRunner(agent.Dependencies{Runtime: resumedRuntime, Model: issue107SimpleTextModelPG{}})
	if err != nil {
		t.Fatal(err)
	}
	original, err := resumedRuntime.LookupChildExecution(t.Context(), "child")
	if err != nil || original.ChildRunID != "child" {
		t.Fatalf("original committed Grant missing: %+v err=%v", original, err)
	}
	if err = resumedAgent.ReconcileChildPhysicalGrant(t.Context(), "child"); err != nil {
		t.Fatalf("durably proven callback completion cannot recover: %v", err)
	}
	if _, err = resumedRuntime.Cancel(t.Context(), root.Run.ID, root.Run.Revision, "after original settled"); err != nil {
		t.Fatal(err)
	}
	if _, err = resumedRuntime.LookupChildExecution(t.Context(), "child"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("reconciled Grant still live/replayable: %v", err)
	}
}
