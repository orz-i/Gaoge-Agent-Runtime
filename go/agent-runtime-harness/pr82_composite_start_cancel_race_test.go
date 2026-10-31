package harness_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type auditWaitBeforeMemberRelation struct {
	runrelation.Recorder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *auditWaitBeforeMemberRelation) Ensure(ctx context.Context, d runrelation.Draft) (runrelation.Relation, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.Recorder.Ensure(ctx, d)
}
func TestPR82CancelCannotPrecedePhysicalTeamMemberStart(t *testing.T) {
	rt := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	barrier := &auditWaitBeforeMemberRelation{Recorder: relations, entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(barrier.release) })
	defer release()
	children := &pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}}
	handoffs, err := handoff.New(children)
	if err != nil {
		t.Fatal(err)
	}
	feature, err := team.NewRunner(team.Dependencies{Runtime: rt, Handoffs: handoffs, Relations: barrier, MaxMembers: 2})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: store,
		Clock: featureInvocationClock{}, Teams: feature, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "audit-cancel-team-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "audit-cancel-team-turn"}
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	startDone := make(chan error, 1)
	go func() {
		_, startErr := runner.StartTeamTurn(context.Background(), harness.TeamTurnRequest{
			StartRequest: harness.StartRequest{HostThread: hostThread, HostTurn: hostTurn,
				Actor:     kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
				Thread:    kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID},
				RequestID: "audit-team-race", Goal: "physical team member",
				Config: harness.ConfigSnapshot{Model: "fixture-model"}},
			Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "remote-effect"}},
			Join: handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureCollect},
		})
		startDone <- startErr
	}()
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("failed to park Team after Kernel Create and before Member Start")
	}
	cancelled, cancelErr := runner.Cancel(t.Context(), turnID, "operator cancelled")
	if !errors.Is(cancelErr, harness.ErrConflict) || cancelled.Turn.Status == harness.TurnCancelled {
		t.Fatalf("unsettled Team Start must fence successful Cancel: status=%s err=%v", cancelled.Turn.Status, cancelErr)
	}
	children.mu.Lock()
	before := len(children.runs)
	children.mu.Unlock()
	if before != 0 {
		t.Fatalf("Team Member dispatched before blocked start was released: %d", before)
	}
	release()
	select {
	case startErr := <-startDone:
		children.mu.Lock()
		starts := len(children.runs)
		children.mu.Unlock()
		if startErr != nil || starts != 1 {
			t.Fatalf("rejected Cancel must leave original Team owner free to start once: starts=%d err=%v", starts, startErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Team Start did not settle after cancellation")
	}
}
