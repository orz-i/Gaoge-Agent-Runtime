package continuation_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Exhausting Queue delivery attempts must not erase the Native Run's physical
// Tool intent. Even an operator's later explicit DeadLetter requeue cannot
// replay an unverified Tool. Only a provider-owned confirmed receipt closes it.
func TestNativeUnknownToolDeadLetterRequeueKeepsCommitProof(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &durableWaitModel{}
	toolEffects, verifierQueries := 0, 0
	providerCommitted := false
	catalog, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: "test.durable_wait", Name: "External", Terminal: true,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			toolEffects++
			deadline := time.Now().UTC().Add(2 * time.Minute)
			return tools.ExecutionResult{
				Content: json.RawMessage(`null`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{
					Kind: tools.PendingExternalResult, OperationID: "provider-dlq",
					InputDigest: strings.Repeat("a", 64), DeadlineAt: &deadline,
				},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: modelClient, Catalog: catalog, Executor: catalog,
		RunExecution: &agent.RunExecution{OwnerID: "dlq-owner", LeaseDuration: 2 * time.Minute},
		ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
			verifierQueries++
			if req.RunID != "native-dlq-proof" || req.Call.ID != "wait-call" || !providerCommitted {
				return tools.ExecutionResult{}, errors.New("provider has no committed result")
			}
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"committed":true}`),
				Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"},
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "native-dlq-proof", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "durable provider recovery", ToolKeys: []string{"test.durable_wait"},
	})
	if err != nil || pending.Run.Status != kernel.RunStatusRunning || toolEffects != 1 {
		t.Fatalf("native pending result: run=%+v tool=%d err=%v", pending.Run, toolEffects, err)
	}
	wait, err := runner.QueryPendingTool(t.Context(), pending.Run.ID, "wait-call")
	if err != nil || wait.Generation == 0 {
		t.Fatalf("native wait not persisted: %+v err=%v", wait, err)
	}
	signal, err := runner.NotifyExternalToolResultReady(t.Context(), agent.ExternalToolResultSignal{
		RunID: pending.Run.ID, CallID: "wait-call", OperationID: "provider-dlq",
		InputDigest: strings.Repeat("a", 64), SignalID: "ready-once",
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(agent.RunKind, runner))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: signal.Run.ID,
		ExpectedRevision: signal.Run.Revision, SourceRunID: signal.Run.ID,
		SourceRevision: signal.Run.Revision, Trigger: continuation.TriggerToolResultReady,
		WaitGeneration: wait.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	queue := queuecore.NewMemory(queuecore.Dependencies{})
	enqueued, err := queue.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "native-dlq-wakeup",
		Kind: continuation.JobKind, Payload: payload,
		Policy: queuecore.Policy{MaxAttempts: 1, VisibilityTimeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatchOne := func(owner string, status queuecore.Status) queuecore.Job {
		t.Helper()
		worker, createErr := continuation.NewWorker(queue, dispatcher, continuation.WorkerOptions{
			WorkerID: owner, PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		ctx, cancel := context.WithCancel(t.Context())
		if createErr = worker.Start(ctx); createErr != nil {
			t.Fatal(createErr)
		}
		defer func() {
			cancel()
			_ = worker.Close(context.Background())
		}()
		until := time.Now().Add(3 * time.Second)
		for time.Now().Before(until) {
			job, getErr := queue.Get(t.Context(), enqueued.Job.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if job.Status == status {
				return job
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("worker %s did not settle to %s", owner, status)
		return queuecore.Job{}
	}
	for cycle := 0; cycle < 2; cycle++ {
		job := dispatchOne("unknown-provider-"+string(rune('a'+cycle)), queuecore.StatusDeadLetter)
		if job.Attempt != 1 || job.LastErrorCode != "continuation.dispatch_failed" || toolEffects != 1 {
			t.Fatalf("unverified DLQ lost intent: job=%+v effects=%d", job, toolEffects)
		}
		current, loadErr := runtime.Load(t.Context(), pending.Run.ID)
		if loadErr != nil || current.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("DLQ changed unsettled Run: %+v err=%v", current.Run, loadErr)
		}
		currentWait, waitErr := runner.QueryPendingTool(t.Context(), pending.Run.ID, "wait-call")
		if waitErr != nil || currentWait.SignalID != "ready-once" {
			t.Fatalf("DLQ erased durable wait/signal: wait=%+v err=%v", currentWait, waitErr)
		}
		if _, err = queue.RequeueDeadLetter(t.Context(), enqueued.Job.ID); err != nil {
			t.Fatalf("operator could not requeue unresolved work: %v", err)
		}
	}
	providerCommitted = true
	job := dispatchOne("provider-confirmed", queuecore.StatusCompleted)
	completed, err := runtime.Load(t.Context(), pending.Run.ID)
	if err != nil || job.Attempt != 1 || completed.Run.Status != kernel.RunStatusCompleted ||
		toolEffects != 1 || verifierQueries != 3 {
		t.Fatalf("verified receipt was not safely consumed: job=%+v run=%+v tool=%d verify=%d err=%v",
			job, completed.Run, toolEffects, verifierQueries, err)
	}
}
