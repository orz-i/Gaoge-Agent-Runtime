package continuation_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	continuationadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type durableWaitClock struct{ now time.Time }

func (clock *durableWaitClock) Now() time.Time { return clock.now }

type durableWaitModel struct{ calls int }

func (client *durableWaitModel) Generate(context.Context, model.Request) (model.Response, error) {
	client.calls++
	return model.Response{ToolCalls: []tools.Call{{ID: "wait-call", ToolKey: "test.durable_wait", Arguments: json.RawMessage(`{}`)}}}, nil
}

type durableWaitFixture struct {
	store      *memory.Store
	runtime    *kernel.Runtime
	clock      *durableWaitClock
	agent      *agent.Runner
	teams      *team.Runner
	queue      *queuecore.Memory
	scheduler  *continuation.Scheduler
	relations  *runrelation.Registry
	dispatcher *continuation.Dispatcher
	model      *durableWaitModel
	executions int
	ready      bool
}

func newDurableWaitFixture(t *testing.T) *durableWaitFixture {
	t.Helper()
	fixture := &durableWaitFixture{store: memory.NewStore(), clock: &durableWaitClock{now: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}, model: &durableWaitModel{}}
	var err error
	fixture.runtime, err = kernel.New(kernel.Dependencies{Store: fixture.store, Clock: fixture.clock})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "test.durable_wait", Name: "durable_wait", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			fixture.executions++
			if fixture.ready {
				return tools.ExecutionResult{Content: json.RawMessage(`42`), Receipt: tools.Receipt{ExecutionID: "wait-operation", Disposition: "committed"}}, nil
			}
			deadline := fixture.clock.Now().Add(2 * time.Minute)
			return tools.ExecutionResult{Content: json.RawMessage(`null`), Receipt: tools.Receipt{ExecutionID: "wait-operation", Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "wait-operation", InputDigest: strings.Repeat("c", 64), DeadlineAt: &deadline}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.agent, err = agent.NewRunner(agent.Dependencies{Runtime: fixture.runtime, Model: fixture.model, Catalog: registry, Executor: registry, Clock: fixture.clock})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), fixture.clock)
	if err != nil {
		t.Fatal(err)
	}
	fixture.relations = relations
	coordinator, err := handoff.New(fixture.agent)
	if err != nil {
		t.Fatal(err)
	}
	fixture.teams, err = team.NewRunner(team.Dependencies{Runtime: fixture.runtime, Handoffs: coordinator, Relations: relations})
	if err != nil {
		t.Fatal(err)
	}
	fixture.queue = queuecore.NewMemory(queuecore.Dependencies{Clock: fixture.clock})
	fixture.scheduler, err = continuation.NewScheduler(continuation.SchedulerDependencies{Outbox: fixture.store, Queue: fixture.queue, Relations: relations, Runs: fixture.runtime, Clock: fixture.clock}, continuationadapter.Triggers()...)
	if err != nil {
		t.Fatal(err)
	}
	fixture.dispatcher, err = continuation.NewDispatcher(fixture.runtime, continuation.RegisterResumer(agent.RunKind, fixture.agent), continuation.RegisterResumer(team.RunKind, fixture.teams))
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *durableWaitFixture) pump(t *testing.T) {
	t.Helper()
	if err := fixture.scheduler.Project(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	deliveries, err := fixture.queue.Claim(t.Context(), queuecore.ClaimRequest{Queue: continuation.QueueName, WorkerID: "wait-test-worker", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range deliveries {
		var payload continuation.Payload
		if err = json.Unmarshal(delivery.Job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if err = fixture.dispatcher.Dispatch(t.Context(), payload); err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.queue.Ack(t.Context(), queuecore.LeaseRequest{JobID: delivery.Job.ID, LeaseID: delivery.Lease.ID, WorkerID: "wait-test-worker"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExternalWaitDeadlineSurvivesRevisionAndRejectsObsoleteGeneration(t *testing.T) {
	fixture := newDurableWaitFixture(t)
	waiting, err := fixture.agent.StartRun(t.Context(), agent.StartRequest{ID: "deadline-wait", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "wait", ToolKeys: []string{"test.durable_wait"}})
	if err != nil {
		t.Fatal(err)
	}
	query, err := fixture.agent.QueryPendingTool(t.Context(), waiting.Run.ID, "wait-call")
	if err != nil {
		t.Fatal(err)
	}
	unchanged, staleErr := fixture.agent.ResumeToolWait(t.Context(), waiting.Run.ID, waiting.Run.Revision, query.Generation+1)
	if staleErr != nil || unchanged.Run.Revision != waiting.Run.Revision || fixture.executions != 1 {
		t.Fatalf("obsolete generation dispatched work: %#v, %v", unchanged.Run, staleErr)
	}
	newer, err := fixture.runtime.Apply(t.Context(), waiting.Run.ID, waiting.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: waiting.State, Events: []kernel.EventDraft{{Type: "test.unrelated_fact"}}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.now = fixture.clock.now.Add(2 * time.Minute)
	fixture.pump(t)
	expired, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || expired.Run.Status != kernel.RunStatusFailed || expired.Run.ErrorCode != "agent.tool_deadline_exceeded" {
		t.Fatalf("lost deadline after revision %d: %#v, %v", newer.Run.Revision, expired.Run, err)
	}
	// A consumed or stale wait generation must not run any executor.
	if _, err = fixture.agent.ResumeToolWait(t.Context(), expired.Run.ID, expired.Run.Revision, query.Generation+1); err != nil {
		t.Fatal(err)
	}
	if fixture.executions != 1 || fixture.model.calls != 1 {
		t.Fatalf("deadline re-executed: %d/%d", fixture.executions, fixture.model.calls)
	}
}

func TestChildTerminalReconcileRequeuesFactAfterParentWaitCommit(t *testing.T) {
	fixture := newDurableWaitFixture(t)
	create := func(id string) kernel.Snapshot {
		t.Helper()
		snapshot, err := fixture.runtime.Create(t.Context(), kernel.CreateRequest{ID: id, Kind: "test.reconcile", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "test", State: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	parent, child := create("parent-before-wait"), create("child-before-wait")
	if _, err := fixture.relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID, Kind: runrelation.KindTeamMember, OwnerNodeID: "member"}); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.runtime.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: child.State, Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`42`)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.scheduler.Project(t.Context()); err != nil {
		t.Fatal(err)
	}
	fixture.clock.now = fixture.clock.now.Add(time.Second)
	delivered, err := fixture.queue.Claim(t.Context(), queuecore.ClaimRequest{Queue: continuation.QueueName, WorkerID: "early-child-worker", Limit: 1})
	if err != nil || len(delivered) != 1 {
		t.Fatalf("early terminal=%#v, %v", delivered, err)
	}
	// The child delivery was consumed before its parent committed the wait.
	if _, err = fixture.queue.Ack(t.Context(), queuecore.LeaseRequest{JobID: delivered[0].Job.ID, LeaseID: delivered[0].Lease.ID, WorkerID: "early-child-worker"}); err != nil {
		t.Fatal(err)
	}
	committed, err := fixture.runtime.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"waiting":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	fixture.clock.now = fixture.clock.now.Add(time.Second)
	recovered, err := fixture.queue.Claim(t.Context(), queuecore.ClaimRequest{Queue: continuation.QueueName, WorkerID: "recovered-child-worker", Limit: 1})
	if err != nil || len(recovered) != 1 {
		t.Fatalf("child fact lost after parent wait commit: %#v, %v", recovered, err)
	}
	var payload continuation.Payload
	if err = json.Unmarshal(recovered[0].Job.Payload, &payload); err != nil || payload.ExpectedRevision != committed.Run.Revision {
		t.Fatalf("recovered parent generation=%#v, %v", payload, err)
	}
}

func TestTeamExternalWaitSixtySecondsNoPollingThenDurableChildWakeup(t *testing.T) {
	fixture := newDurableWaitFixture(t)
	parent, err := fixture.teams.StartRun(t.Context(), team.StartRequest{
		ID: "team-external-wait", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "wait", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "member", Goal: "compute", ToolKeys: []string{"test.durable_wait"}}},
		Join:    handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureCollect},
	})
	if !errors.Is(err, team.ErrMemberPending) {
		t.Fatalf("team start = %v", err)
	}
	view, err := team.ViewState(parent)
	if err != nil {
		t.Fatal(err)
	}
	childID := view.Members[0].Delegation.ChildRunID
	child, err := fixture.runtime.Load(t.Context(), childID)
	if err != nil {
		t.Fatal(err)
	}
	for second := 0; second < 60; second++ {
		fixture.clock.now = fixture.clock.now.Add(time.Second)
		fixture.pump(t)
	}
	after, err := fixture.runtime.Load(t.Context(), parent.Run.ID)
	if err != nil || after.Run.Revision != parent.Run.Revision {
		t.Fatalf("team polled: %#v, %v", after.Run, err)
	}
	childAfter, err := fixture.runtime.Load(t.Context(), childID)
	if err != nil || childAfter.Run.Revision != child.Run.Revision || fixture.executions != 1 || fixture.model.calls != 1 {
		t.Fatalf("child polled: %#v calls=%d/%d %v", childAfter.Run, fixture.executions, fixture.model.calls, err)
	}
	// Public refresh/replay is also a no-op while every child is unchanged.
	after, err = fixture.teams.Resume(t.Context(), after.Run.ID, after.Run.Revision)
	if !errors.Is(err, team.ErrMemberPending) || after.Run.Revision != parent.Run.Revision {
		t.Fatalf("team refresh wrote state: %#v %v", after.Run, err)
	}
	fixture.ready = true
	_, err = fixture.agent.NotifyExternalToolResultReady(t.Context(), agent.ExternalToolResultSignal{RunID: childID, CallID: "wait-call", OperationID: "wait-operation", InputDigest: strings.Repeat("c", 64), SignalID: "team-result"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate restart: recovery uses stored facts rather than an in-memory hook.
	fixture.pump(t)
	fixture.pump(t)
	fixture.clock.now = fixture.clock.now.Add(time.Second)
	fixture.pump(t)
	completed, err := fixture.runtime.Load(t.Context(), parent.Run.ID)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("parent lost child wakeup: %#v, %v", completed.Run, err)
	}
	if fixture.executions != 2 || fixture.model.calls != 1 {
		t.Fatalf("duplicate work: %d/%d", fixture.executions, fixture.model.calls)
	}
}
