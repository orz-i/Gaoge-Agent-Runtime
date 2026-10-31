package harnesspostgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// Keep the Feature-owned physical Start open AFTER the parent Team Run has
// been created but BEFORE the first Member's relation / ChildRunner dispatch.
// This stage is not protected by Kernel CAS: the external ChildRunner can
// still be invoked after the parent has been cancelled.
type pr82MemberStartGate struct {
	runrelation.Recorder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *pr82MemberStartGate) Ensure(ctx context.Context, draft runrelation.Draft) (runrelation.Relation, error) {
	g.once.Do(func() {
		close(g.entered)
		<-g.release
	})
	return g.Recorder.Ensure(ctx, draft)
}

type pr82PhysicalMemberRunner struct{ starts atomic.Int32 }

func (*pr82PhysicalMemberRunner) LoadRun(context.Context, string) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrNotFound
}

func (r *pr82PhysicalMemberRunner) StartRun(_ context.Context, request agent.StartRequest) (kernel.Snapshot, error) {
	r.starts.Add(1)
	return kernel.Snapshot{Run: kernel.Run{
		ID: request.ID, Kind: agent.RunKind, Actor: request.Actor,
		Thread: request.Thread, RequestID: request.RequestID, Goal: request.Goal,
		Status: kernel.RunStatusRunning, Revision: 1,
	}}, nil
}

func TestPR82CompositeStartGrantFencesCancelAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			assertPR82CompositeStartCancelFenced(t, create(t), name)
		})
	}
}

func TestRealPostgresPR82CompositeStartGrantFencesCancel(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, _ := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	assertPR82CompositeStartCancelFenced(t, store, "real-postgres")
}

func assertPR82CompositeStartCancelFenced(t *testing.T, store harness.Store, prefix string) {
	t.Helper()
	ctx := t.Context()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	nativeAgent, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: pr72RealContextModel{}})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := &pr82MemberStartGate{
		Recorder: relations, entered: make(chan struct{}), release: make(chan struct{}),
	}
	closeGate := sync.OnceFunc(func() { close(gate.release) })
	defer closeGate()
	members := &pr82PhysicalMemberRunner{}
	coordinator, err := handoff.New(members)
	if err != nil {
		t.Fatal(err)
	}
	feature, err := team.NewRunner(team.Dependencies{
		Runtime: rt, Handoffs: coordinator, Relations: gate, MaxMembers: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: nativeAgent, Teams: feature,
		Store: store, Clock: pr72RealContextClock{}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}

	thread := harness.HostRef{Kind: "conversation", ID: "pr82-start-cancel-thread-" + prefix}
	turn := harness.HostRef{Kind: "conversation_turn", ID: "pr82-start-cancel-turn-" + prefix}
	sessionID, err := harness.SessionID(thread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, turn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	startDone := make(chan error, 1)
	go func() {
		_, startErr := host.StartTeamTurn(context.Background(), harness.TeamTurnRequest{
			StartRequest: harness.StartRequest{
				HostThread: thread, HostTurn: turn, Actor: actor,
				Thread:    kernel.ThreadRef{Kind: thread.Kind, ID: thread.ID},
				RequestID: "pr82-running-team-start", Goal: "dispatch a remote member",
				Config: harness.ConfigSnapshot{Model: "fixture-model"},
			},
			Mode:    team.ExecutionSequential,
			Members: []team.Member{{ID: "member", Goal: "remote Member effect"}},
			Join:    handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureCollect},
		})
		startDone <- startErr
	}()
	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("Team did not reach physical Member pre-dispatch")
	}
	persisted, err := store.GetTurn(ctx, turnID)
	if err != nil || persisted.Status != harness.TurnAccepted {
		t.Fatalf("Team Start did not leave an open Harness admission: %+v %v", persisted, err)
	}
	root, ok := func() (harness.Invocation, bool) {
		invs, err := store.ListInvocations(ctx, turnID)
		if err != nil || len(invs) != 1 {
			return harness.Invocation{}, false
		}
		return invs[0], true
	}()
	if !ok || root.ExecutionClass != harness.ExecutionTeam {
		t.Fatalf("expected a claimed Team Invocation: %+v", root)
	}
	run, err := rt.Load(ctx, root.ExecutionRefID)
	if err != nil || run.Run.Kind != team.RunKind || run.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("Team Kernel parent was not created: %+v %v", run.Run, err)
	}
	cancelled, cancelErr := host.Cancel(ctx, turnID, "cancel before physical Member Start")
	if !errors.Is(cancelErr, harness.ErrConflict) {
		t.Fatalf("cancel declared success with in-flight Team Start: status=%s err=%v", cancelled.Turn.Status, cancelErr)
	}
	if members.starts.Load() != 0 {
		t.Fatalf("Member physically started before release: %d", members.starts.Load())
	}
	after, err := store.GetTurn(ctx, turnID)
	if err != nil || after.Status == harness.TurnCancelled {
		t.Fatalf("rejected cancel marked root terminal: %+v %v", after, err)
	}
	closeGate()
	select {
	case startErr := <-startDone:
		if startErr != nil {
			t.Fatalf("physical Team Start could not finish after rejected Cancel: %v", startErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Team did not finish after blocked cancellation returned")
	}
	if members.starts.Load() != 1 {
		t.Fatalf("expected exactly one Member dispatch under an un-cancelled root, got %d", members.starts.Load())
	}
}
