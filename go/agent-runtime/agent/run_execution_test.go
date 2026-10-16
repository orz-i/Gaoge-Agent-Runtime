package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type nativeLeaseFixtureModel struct{ calls int }

func (m *nativeLeaseFixtureModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	m.calls++
	return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

type stealerOnCreateStore struct {
	kernel.Store
	kernel.ExecutionStore
	stolen kernel.ExecutionLease
}

func (s *stealerOnCreateStore) Create(ctx context.Context, record kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	snapshot, err := s.Store.Create(ctx, record, events)
	if err != nil {
		return snapshot, err
	}
	stolen, err := s.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: record.Run.ID, OwnerID: "competing", LeaseDuration: 3 * time.Minute,
	})
	if err != nil {
		return snapshot, err
	}
	s.stolen = stolen
	return snapshot, nil
}

type legacyStoreView struct{ kernel.Store }

func TestRunExecutionRejectsUnsupportedOrUnsafeConfig(t *testing.T) {
	m := &nativeLeaseFixtureModel{}
	legacy, err := kernel.New(kernel.Dependencies{Store: &legacyStoreView{Store: memory.NewStore()}})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.SupportsExecution() {
		t.Fatal("opaque legacy Store advertised execution safety")
	}
	if _, err = agent.NewRunner(agent.Dependencies{Runtime: legacy, Model: m,
		RunExecution: &agent.RunExecution{OwnerID: "owner", LeaseDuration: 3 * time.Minute}}); !errors.Is(err, agent.ErrInvalidRequest) {
		t.Fatalf("opt-in native leasing accepted a Store without ExecutionStore: %v", err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []agent.RunExecution{
		{OwnerID: "", LeaseDuration: 2 * time.Minute},
		{OwnerID: "a ", LeaseDuration: 2 * time.Minute},
		{OwnerID: strings.Repeat("x", 129), LeaseDuration: 2 * time.Minute},
		{OwnerID: "owner", LeaseDuration: time.Second},
		{OwnerID: "owner", LeaseDuration: 25 * time.Hour},
	} {
		_, err = agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, RunExecution: &tc})
		if !errors.Is(err, agent.ErrInvalidRequest) {
			t.Fatalf("unsafe native lease accepted %+v err=%v", tc, err)
		}
	}
}

func TestNativeStartRunClaimConflictLeavesRecoverableRun(t *testing.T) {
	base := memory.NewStore()
	wrapper := &stealerOnCreateStore{Store: base, ExecutionStore: base}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapper})
	if err != nil {
		t.Fatal(err)
	}
	m := &nativeLeaseFixtureModel{}
	config := &agent.RunExecution{OwnerID: "worker", LeaseDuration: 3 * time.Minute}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, RunExecution: config})
	if err != nil {
		t.Fatal(err)
	}
	created, err := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "native-start-conflict", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "resume safely after competing claim",
	})
	if !errors.Is(err, kernel.ErrExecutionConflict) || created.Run.ID != "native-start-conflict" {
		t.Fatalf("conflicting claim hid created Run: %+v %v", created.Run, err)
	}
	if m.calls != 0 {
		t.Fatalf("start dispatched model without ownership: %d", m.calls)
	}
	stored, err := rt.Load(t.Context(), created.Run.ID)
	if err != nil || stored.Run.Revision != created.Run.Revision {
		t.Fatalf("unrecovered orphan after claim conflict: %+v %v", stored.Run, err)
	}
	if err = rt.ReleaseExecution(t.Context(), wrapper.stolen); err != nil {
		t.Fatal(err)
	}
	// RunID from the returned Snapshot is sufficient to retry without
	// creating another Run or replaying any already completed Model/Tool.
	completed, err := runner.Resume(t.Context(), created.Run.ID, created.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || m.calls != 1 {
		t.Fatalf("native retry not fenced/executed correctly %+v calls=%d err=%v", completed.Run, m.calls, err)
	}
	if err = rt.RequireExecution(t.Context(), created.Run.ID); !errors.Is(err, kernel.ErrTerminal) {
		t.Fatalf("native completion not durable terminal: %v", err)
	}
}
