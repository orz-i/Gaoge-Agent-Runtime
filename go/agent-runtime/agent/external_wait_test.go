package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type externalClock struct{ now time.Time }

func (clock *externalClock) Now() time.Time { return clock.now }

type externalModel struct{ calls int }

func (model *externalModel) Generate(context.Context, runtimemodel.Request) (runtimemodel.Response, error) {
	model.calls++
	return runtimemodel.Response{ToolCalls: []tools.Call{{ID: "external-call", ToolKey: "test.external", Arguments: json.RawMessage(`{}`)}}}, nil
}

type externalFixture struct {
	store      *memory.Store
	runtime    *kernel.Runtime
	runner     *agent.Runner
	model      *externalModel
	registry   *tools.Registry
	clock      *externalClock
	ready      bool
	executions int
	lookups    int
}

func newExternalFixture(t *testing.T) *externalFixture {
	t.Helper()
	fixture := &externalFixture{store: memory.NewStore(), model: &externalModel{}, clock: &externalClock{now: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}}
	fixture.registry = mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{Key: "test.external", Name: "external", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			fixture.executions++
			if fixture.executions > 1 {
				return tools.ExecutionResult{}, errors.New("external Tool was incorrectly reissued while waiting for receipt")
			}
			deadline := fixture.clock.Now().Add(2 * time.Minute)
			return tools.ExecutionResult{
				Content: json.RawMessage(`null`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "external-operation", InputDigest: strings.Repeat("a", 64), DeadlineAt: &deadline},
			}, nil
		}),
	}})
	fixture.restart(t, fixture.store)
	return fixture
}

func (fixture *externalFixture) restart(t *testing.T, store kernel.Store) {
	t.Helper()
	var err error
	fixture.runtime, err = kernel.New(kernel.Dependencies{Store: store, Clock: fixture.clock})
	if err != nil {
		t.Fatal(err)
	}
	fixture.runner, err = agent.NewRunner(agent.Dependencies{
		Runtime: fixture.runtime, Model: fixture.model, Catalog: fixture.registry, Executor: fixture.registry, Clock: fixture.clock,
		ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			fixture.lookups++
			if !fixture.ready || request.Call.ID != "external-call" || request.RunID != "external-run" {
				return tools.ExecutionResult{}, errors.New("external provider has no verified COMMIT")
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"answer":42}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (fixture *externalFixture) start(t *testing.T) kernel.Snapshot {
	t.Helper()
	snapshot, err := fixture.runner.StartRun(t.Context(), startRequest("external-run", "external-request", "compute", "test.external"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("status = %s", snapshot.Run.Status)
	}
	return snapshot
}

func externalSignal() agent.ExternalToolResultSignal {
	return agent.ExternalToolResultSignal{RunID: "external-run", CallID: "external-call", OperationID: "external-operation", InputDigest: strings.Repeat("a", 64), SignalID: "receipt-ready"}
}

func TestExternalWaitSixtySecondsDoesNotWriteInvokeOrConsume(t *testing.T) {
	fixture := newExternalFixture(t)
	waiting := fixture.start(t)
	wait, err := fixture.runner.QueryPendingTool(t.Context(), waiting.Run.ID, "external-call")
	if err != nil {
		t.Fatal(err)
	}
	for second := 1; second <= 60; second++ {
		fixture.clock.now = fixture.clock.now.Add(time.Second)
		next, resumeErr := fixture.runner.Resume(t.Context(), waiting.Run.ID, waiting.Run.Revision)
		if !errors.Is(resumeErr, agent.ErrToolOutcomeUnknown) || next.Run.Revision != waiting.Run.Revision || next.EventHead != waiting.EventHead {
			t.Fatalf("second %d wrote state or events: %#v, %v", second, next.Run, resumeErr)
		}
	}
	if fixture.executions != 1 || fixture.model.calls != 1 {
		t.Fatalf("executions=%d model=%d", fixture.executions, fixture.model.calls)
	}
	view, err := agent.ViewState(waiting)
	if err != nil || view.Budget.Usage.ToolCalls != 0 || view.PendingTool == nil || view.PendingTool.Ready {
		t.Fatalf("view=%#v, %v", view, err)
	}
	deadlineCopy := *wait.DeadlineAt
	*wait.DeadlineAt = time.Time{}
	again, err := fixture.runner.QueryPendingTool(t.Context(), waiting.Run.ID, "external-call")
	if err != nil || !again.DeadlineAt.Equal(deadlineCopy) {
		t.Fatalf("query mutated stored deadline: %#v, %v", again, err)
	}
}

func TestExternalSignalConcurrentDuplicateAndRestartConsumesOnce(t *testing.T) {
	fixture := newExternalFixture(t)
	fixture.start(t)
	// Simulates the host's durable receipt/outbox becoming available.
	fixture.ready = true
	var group sync.WaitGroup
	errorsChannel := make(chan error, 12)
	for range 12 {
		group.Go(func() {
			_, err := fixture.runner.NotifyExternalToolResultReady(t.Context(), externalSignal())
			errorsChannel <- err
		})
	}
	group.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture.restart(t, fixture.store)
	current, err := fixture.runtime.Load(t.Context(), "external-run")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := fixture.runner.ReconcileToolOutcome(t.Context(), current.Run.ID, current.Run.Revision, "external-call")
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("completed=%#v, %v", completed.Run, err)
	}
	view, err := agent.ViewState(completed)
	if err != nil || view.Budget.Usage.ToolCalls != 1 || view.PendingTool != nil {
		t.Fatalf("view=%#v, %v", view, err)
	}
	if fixture.executions != 1 || fixture.lookups != 1 || fixture.model.calls != 1 {
		t.Fatalf("executions=%d model=%d", fixture.executions, fixture.model.calls)
	}
	duplicate, err := fixture.runner.NotifyExternalToolResultReady(t.Context(), externalSignal())
	if err != nil || duplicate.Run.Revision != completed.Run.Revision {
		t.Fatalf("duplicate=%#v, %v", duplicate.Run, err)
	}
	conflicting := externalSignal()
	conflicting.SignalID = "different-receipt"
	if _, err = fixture.runner.NotifyExternalToolResultReady(t.Context(), conflicting); !errors.Is(err, agent.ErrToolSignalConflict) {
		t.Fatalf("conflict = %v", err)
	}
}

func TestExternalSignalRejectsWrongIdentityCancelAndDeadline(t *testing.T) {
	for _, field := range []string{"call", "operation", "digest", "cancelled", "deadline"} {
		t.Run(field, func(t *testing.T) {
			fixture := newExternalFixture(t)
			waiting := fixture.start(t)
			signal := externalSignal()
			expected := agent.ErrToolSignalConflict
			switch field {
			case "call":
				signal.CallID = "wrong-call"
			case "operation":
				signal.OperationID = "wrong-operation"
			case "digest":
				signal.InputDigest = strings.Repeat("b", 64)
			case "cancelled":
				var err error
				waiting, err = fixture.runtime.Cancel(t.Context(), waiting.Run.ID, waiting.Run.Revision, "stop")
				if err != nil {
					t.Fatal(err)
				}
				expected = agent.ErrToolNotPending
			case "deadline":
				fixture.clock.now = fixture.clock.now.Add(2 * time.Minute)
				expected = kernel.ErrDeadline
			}
			if _, err := fixture.runner.NotifyExternalToolResultReady(t.Context(), signal); !errors.Is(err, expected) {
				t.Fatalf("error=%v expected=%v", err, expected)
			}
			current, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
			if err != nil || current.Run.Revision != waiting.Run.Revision {
				t.Fatalf("rejected signal mutated state: %#v, %v", current.Run, err)
			}
			if field == "deadline" {
				expired, err := fixture.runner.ResumeToolWait(t.Context(), current.Run.ID, current.Run.Revision, waiting.Run.Revision)
				if expired.Run.Status != kernel.RunStatusRunning ||
					!errors.Is(err, agent.ErrToolOutcomeUnknown) || !errors.Is(err, agent.ErrToolOutcomeOverdue) {
					t.Fatalf("unverified provider expiry must retain original operation identity: expired=%#v err=%v", expired.Run, err)
				}
			}
		})
	}
}

func TestExternalCancellationAndDeadlineCannotBeRevivedBySignals(t *testing.T) {
	for _, outcome := range []string{"cancel", "deadline"} {
		t.Run(outcome, func(t *testing.T) {
			fixture := newExternalFixture(t)
			waiting := fixture.start(t)
			if outcome == "deadline" {
				fixture.clock.now = fixture.clock.now.Add(2 * time.Minute)
			}
			var group sync.WaitGroup
			var notifyErr, terminalErr error
			group.Go(func() { _, notifyErr = fixture.runner.NotifyExternalToolResultReady(t.Context(), externalSignal()) })
			group.Go(func() {
				if outcome == "cancel" {
					_, terminalErr = fixture.runtime.Cancel(t.Context(), waiting.Run.ID, waiting.Run.Revision, "cancel")
				} else {
					_, terminalErr = fixture.runner.ResumeToolWait(t.Context(), waiting.Run.ID, waiting.Run.Revision, waiting.Run.Revision)
				}
			})
			group.Wait()
			if notifyErr != nil && !errors.Is(notifyErr, agent.ErrToolNotPending) && !errors.Is(notifyErr, kernel.ErrDeadline) {
				t.Fatalf("notify race = %v", notifyErr)
			}
			if terminalErr != nil && !errors.Is(terminalErr, kernel.ErrConflict) && !errors.Is(terminalErr, agent.ErrToolOutcomeUnknown) {
				t.Fatalf("terminal race = %v", terminalErr)
			}
			current, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Run.Status == kernel.RunStatusRunning {
				current, err = fixture.runtime.Cancel(t.Context(), current.Run.ID, current.Run.Revision, "cancel")
				if err != nil {
					t.Fatal(err)
				}
			}
			revision := current.Run.Revision
			_, signalErr := fixture.runner.NotifyExternalToolResultReady(t.Context(), externalSignal())
			if signalErr != nil && !errors.Is(signalErr, agent.ErrToolNotPending) {
				t.Fatalf("late signal = %v", signalErr)
			}
			final, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
			if err != nil || final.Run.Revision != revision || final.Run.Status == kernel.RunStatusRunning {
				t.Fatalf("late signal revived run: %#v, %v", final.Run, err)
			}
			view, err := agent.ViewState(final)
			if err != nil || view.Budget.Usage.ToolCalls != 0 || fixture.executions != 1 || fixture.model.calls != 1 {
				t.Fatalf("race consumed result: %#v, %v", view, err)
			}
			// A provider wait expiry without an external COMMIT is not
			// definitive proof that its physical effect was absent. The
			// explicit parent cancellation above may close the Run, but
			// it must not replay or synthesize the external result.
		})
	}
}

func TestExternalWaitCommitAndReadyCommitCrashesRecover(t *testing.T) {
	for _, eventType := range []string{agent.EventToolPending, agent.EventExternalToolResultReady} {
		t.Run(eventType, func(t *testing.T) {
			fixture := newExternalFixture(t)
			fault := &modelInvocationFaultStore{Store: fixture.store, eventType: eventType, afterCommit: true}
			fixture.restart(t, fault)
			waiting, err := fixture.runner.StartRun(t.Context(), startRequest("external-run", "external-request", "compute", "test.external"))
			if eventType == agent.EventToolPending {
				if !errors.Is(err, errInjectedModelInvocationCrash) {
					t.Fatalf("wait commit error = %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				fixture.ready = true
				_, err = fixture.runner.NotifyExternalToolResultReady(t.Context(), externalSignal())
				if !errors.Is(err, errInjectedModelInvocationCrash) {
					t.Fatalf("ready commit error = %v", err)
				}
			}
			fixture.restart(t, fixture.store)
			waiting, err = fixture.runtime.Load(t.Context(), "external-run")
			if err != nil {
				t.Fatal(err)
			}
			if eventType == agent.EventToolPending {
				if _, err = fixture.runner.QueryPendingTool(t.Context(), waiting.Run.ID, "external-call"); err != nil {
					t.Fatal(err)
				}
				fixture.ready = true
			}
			waiting, err = fixture.runner.NotifyExternalToolResultReady(t.Context(), externalSignal())
			if err != nil {
				t.Fatal(err)
			}
			completed, err := fixture.runner.ReconcileToolOutcome(t.Context(), waiting.Run.ID, waiting.Run.Revision, "external-call")
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted || fixture.executions != 1 {
				t.Fatalf("recovery=%#v, executions=%d, %v", completed.Run, fixture.executions, err)
			}
		})
	}
}
