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
		lease := queuecore.LeaseRequest{JobID: delivery.Job.ID, LeaseID: delivery.Lease.ID, WorkerID: "wait-test-worker"}
		if dispatchErr := fixture.dispatcher.Dispatch(t.Context(), payload); dispatchErr != nil {
			// Mirror the Worker: a changed Run revision without a proven
			// Feature consumption must NACK and preserve the candidate, not
			// make this test fixture silently ACK an unresolved wakeup.
			if !errors.Is(dispatchErr, kernel.ErrConflict) {
				t.Fatalf("unrelated delivery failure: %v", dispatchErr)
			}
			if _, err = fixture.queue.Nack(t.Context(), queuecore.NackRequest{
				LeaseRequest: lease, ErrorCode: "continuation.dispatch_failed", Error: dispatchErr.Error(),
			}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err = fixture.queue.Ack(t.Context(), lease); err != nil {
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

func TestNativeToolWaitDispatchRequiresVerifiedProviderReceipt(t *testing.T) {
	ctx := t.Context()
	store := memory.NewStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	toolExecutions, verifierCalls := 0, 0
	providerCommitted := false
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "test.durable_wait", Name: "Durable", Terminal: true, InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
			toolExecutions++
			expires := time.Now().UTC().Add(time.Minute)
			return tools.ExecutionResult{Content: json.RawMessage(`null`),
				Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult,
					OperationID: "provider-native", InputDigest: strings.Repeat("a", 64), DeadlineAt: &expires}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &durableWaitModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: modelClient, Catalog: registry, Executor: registry,
		RunExecution: &agent.RunExecution{OwnerID: "native-owner", LeaseDuration: 2 * time.Minute},
		ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			verifierCalls++
			if !providerCommitted {
				return tools.ExecutionResult{}, errors.New("provider receipt is not available")
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"committed":true}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := runner.StartRun(ctx, agent.StartRequest{
		ID: "audit-native-wakeup-ack", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "native wait", ToolKeys: []string{"test.durable_wait"},
	})
	if err != nil || pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("pending Run not created: %+v %v", pending.Run, err)
	}
	wait, err := runner.QueryPendingTool(ctx, pending.Run.ID, "wait-call")
	if err != nil {
		t.Fatal(err)
	}
	signaled, err := runner.NotifyExternalToolResultReady(ctx, agent.ExternalToolResultSignal{
		RunID: pending.Run.ID, CallID: "wait-call", OperationID: "provider-native",
		InputDigest: strings.Repeat("a", 64), SignalID: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(agent.RunKind, runner))
	if err != nil {
		t.Fatal(err)
	}
	payload := continuation.Payload{SchemaVersion: continuation.SchemaVersion,
		RunID: signaled.Run.ID, ExpectedRevision: signaled.Run.Revision,
		Trigger:     continuation.TriggerToolResultReady,
		SourceRunID: signaled.Run.ID, SourceRevision: signaled.Run.Revision,
		WaitGeneration: wait.Generation}
	dispatchErr := dispatcher.Dispatch(ctx, payload)
	stored, err := runtime.Load(ctx, signaled.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(stored)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AUDIT Native Tool result ready: Dispatcher.Dispatch error=%v (nil means worker ACKs), persistedStatus=%s pendingTool=%t providerVerifierCalls=%d physicalToolExecs=%d",
		dispatchErr, stored.Run.Status, view.PendingTool != nil, verifierCalls, toolExecutions)
	if !errors.Is(dispatchErr, agent.ErrToolOutcomeUnknown) || view.PendingTool == nil ||
		verifierCalls != 1 || toolExecutions != 1 {
		t.Fatalf("unverified native Tool incorrectly ACKed: error=%v pending=%t verifier=%d Tool=%d",
			dispatchErr, view.PendingTool != nil, verifierCalls, toolExecutions)
	}
	// The host's provider journal now confirms the same immutable Call ID.
	providerCommitted = true
	if err = dispatcher.Dispatch(ctx, payload); err != nil {
		t.Fatalf("verified provider result didn't finish the native Run: %v", err)
	}
	completed, err := runtime.Load(ctx, signaled.Run.ID)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted ||
		verifierCalls != 2 || toolExecutions != 1 {
		t.Fatalf("native Tool resumed without a valid COMMIT: status=%s verifier=%d tool=%d err=%v",
			completed.Run.Status, verifierCalls, toolExecutions, err)
	}

}

func TestNativeUnknownExternalOutcomeAfterDeadlineIsDiagnosable(t *testing.T) {
	ctx := t.Context()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "test.durable_wait", Name: "Durable", Terminal: true, InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
			effects++
			deadline := time.Now().UTC().Add(60 * time.Millisecond)
			return tools.ExecutionResult{Content: json.RawMessage(`null`),
				Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult,
					OperationID: "provider-timeout", InputDigest: strings.Repeat("b", 64), DeadlineAt: &deadline}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: &durableWaitModel{}, Catalog: registry, Executor: registry,
		RunExecution: &agent.RunExecution{OwnerID: "native-owner", LeaseDuration: 2 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := runner.StartRun(ctx, agent.StartRequest{
		ID: "native-wait-timeout", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "timeout native wait",
		ToolKeys: []string{"test.durable_wait"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wait, err := runner.QueryPendingTool(ctx, waiting.Run.ID, "wait-call")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(110 * time.Millisecond)
	resumed, resumeErr := runner.ResumeToolWait(ctx, waiting.Run.ID, waiting.Run.Revision, wait.Generation)
	actual, err := rt.Load(ctx, waiting.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AUDIT Native provider wait deadline already past: ResumeToolWait err=%v returned=%s persisted=%s revision=%d ToolExecCount=%d",
		resumeErr, resumed.Run.Status, actual.Run.Status, actual.Run.Revision, effects)
	if !errors.Is(resumeErr, agent.ErrToolOutcomeUnknown) ||
		!errors.Is(resumeErr, agent.ErrToolOutcomeOverdue) ||
		actual.Run.Status != kernel.RunStatusRunning || effects != 1 {
		t.Fatalf("native overdue unknown outcome wasn't reported: %v status=%s effects=%d",
			resumeErr, actual.Run.Status, effects)
	}
	_, directErr := runner.Resume(ctx, waiting.Run.ID, waiting.Run.Revision)
	if !errors.Is(directErr, agent.ErrToolOutcomeUnknown) ||
		!errors.Is(directErr, agent.ErrToolOutcomeOverdue) || effects != 1 {
		t.Fatalf("direct native Resume lost overdue diagnosis or replayed Tool: %v effects=%d", directErr, effects)
	}
}
