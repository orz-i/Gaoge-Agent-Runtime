package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type issue107PostMarkAckLossSQLite struct {
	*sqlite.Store
}

func (s issue107PostMarkAckLossSQLite) RecordPhysicalCallbackReturn(ctx context.Context, grant kernel.ChildExecutionGrant, rev uint64, outcome kernel.PhysicalCallbackOutcome, permit kernel.PhysicalCallbackReturn) error {
	if err := s.Store.RecordPhysicalCallbackReturn(ctx, grant, rev, outcome, permit); err != nil {
		return err
	}
	return kernel.ErrExecutionBusy // durable Mark committed but the ACK was lost
}

type issue107SimpleTextModelSQLite struct{}

func (issue107SimpleTextModelSQLite) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "completed", Usage: &model.Usage{InputTokens: 3, OutputTokens: 2}}, nil
}

func TestIssue107SQLiteOriginalPhysicalReceiptAckLostAcrossRestart(t *testing.T) {
	path := filepath.Join(privateDir(t), "grant-ack-lost.sqlite")
	first, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	wrapped := issue107PostMarkAckLossSQLite{Store: first}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	root, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "workflow",
		Actor: actor, Thread: thread, Goal: "parent", State: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: issue107SimpleTextModelSQLite{}})
	if err != nil {
		t.Fatal(err)
	}
	child, callErr := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "child", RequestID: "frozen-effect", ParentRunID: root.Run.ID,
		OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"isolatedContext":true}`)),
		Actor:             actor, Thread: thread, Goal: "complete one Model call", Model: "test-model",
	})
	if callErr != nil || child.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("exact committed callback ACK was not recovered: %+v %v", child.Run, callErr)
	}
	if _, err = rt.LookupChildExecution(t.Context(), "child"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("verified original retirement did not commit: %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	resumedRuntime, err := kernel.New(kernel.Dependencies{Store: second})
	if err != nil {
		t.Fatal(err)
	}
	resumedAgent, err := agent.NewRunner(agent.Dependencies{Runtime: resumedRuntime, Model: issue107SimpleTextModelSQLite{}})
	if err != nil {
		t.Fatal(err)
	}
	original, err := resumedRuntime.LookupOwnedRetirementForRun(t.Context(), "child")
	if err != nil || !original.Valid() || original.Evidence.Grant.ChildRunID != "child" || !original.Evidence.Completion.MatchesReturnSnapshot(child) {
		t.Fatalf("original committed retirement missing after restart: %+v %v", original, err)
	}
	if err = resumedAgent.ReconcilePhysicalExecution(t.Context(), "child"); err != nil {
		t.Fatalf("durably proven callback-completed marker did not allow bounded recovery: %v", err)
	}
	if _, err = resumedRuntime.Cancel(t.Context(), root.Run.ID, root.Run.Revision, "now physically settled"); err != nil {
		t.Fatal(err)
	}
	if _, err = resumedRuntime.LookupChildExecution(t.Context(), "child"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("reconciled Grant still live or replayable: %v", err)
	}
}
