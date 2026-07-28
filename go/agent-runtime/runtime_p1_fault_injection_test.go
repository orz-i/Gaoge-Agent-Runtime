package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type continuationHeartbeatFaultStore struct {
	Store
	err   error
	calls int
}

func (s *continuationHeartbeatFaultStore) HeartbeatContinuationJob(_ context.Context, _, _ string, _, _ time.Time) error {
	s.calls++
	return s.err
}

func TestContinuationHeartbeatLeaseLossCancelsExecution(t *testing.T) {
	store := &continuationHeartbeatFaultStore{err: ErrContinuationJobConflict}
	engine := &Engine{repo: store}
	jobCtx, cancel := context.WithCancel(t.Context())
	errOut := make(chan error, 1)
	done := make(chan struct{})
	ticks := make(chan time.Time, 1)
	go engine.heartbeatContinuationJobTicks(jobCtx, cancel, "worker-a", "job-a", errOut, done, ticks)
	ticks <- time.Date(2026, time.July, 28, 13, 0, 0, 0, time.UTC)
	<-done

	if !errors.Is(jobCtx.Err(), context.Canceled) || store.calls != 1 {
		t.Fatalf("ctx=%v calls=%d", jobCtx.Err(), store.calls)
	}
	select {
	case err := <-errOut:
		if !errors.Is(err, ErrContinuationJobConflict) {
			t.Fatalf("heartbeat error = %v", err)
		}
	default:
		t.Fatal("lease loss was not surfaced")
	}
}

func TestContinuationWorkerPanicBecomesRetryableError(t *testing.T) {
	err := continuationPanicError()
	if !errors.Is(err, ErrContinuationWorkerPanic) {
		t.Fatalf("panic error = %v", err)
	}
}

func continuationPanicError() (resultErr error) {
	defer recoverContinuationWorkerPanic(&resultErr)
	panic("fault injection")
}

type workflowDeadlineToolCatalog struct {
	tool ResolvedTool
}

func (c workflowDeadlineToolCatalog) DefaultToolKeys(context.Context, string, string, string) ([]string, error) {
	return []string{c.tool.ToolKey}, nil
}

func (c workflowDeadlineToolCatalog) ResolveAvailable(context.Context, model.ActorRef, []string, string, string, string) ([]ResolvedTool, []string, error) {
	return []ResolvedTool{c.tool}, nil, nil
}

type workflowDeadlineToolExecutor struct {
	clock *mutableRuntimeClock
	after time.Time
}

func (e workflowDeadlineToolExecutor) Execute(context.Context, ToolExecutionInput) (string, error) {
	e.clock.now = e.after
	return `{"ok":true}`, nil
}

func TestWorkflowToolCrossingDeadlinePersistsFailureTerminal(t *testing.T) {
	startedAt := time.Date(2026, time.July, 28, 14, 0, 0, 0, time.UTC)
	clock := &mutableRuntimeClock{now: startedAt.Add(500 * time.Millisecond)}
	store := &workflowEffectTestStore{events: map[string]model.Event{}}
	runner := newWorkflowEffectTestRunner(store)
	runner.service.cfg = StaticConfigProvider(Config{Tools: ToolConfig{MaxConcurrentCalls: 1}})
	runner.service.clock = clock
	runner.service.generationStreams = newGenerationStreamRegistry(nil, generationStreamOptions{})
	runner.run.StartedAt = startedAt
	runner.budget.Limits.MaxDurationSeconds = 1
	tool, _ := frozenFirewallTestTool(t, json.RawMessage(`{"type":"object"}`), nil, valueNever4C6E2E88)
	fingerprint, err := hashWorkflowValue(tool)
	if err != nil {
		t.Fatal(err)
	}
	runner.definition.Dependencies = []model.WorkflowDependency{{
		Kind: model.WorkflowDependencyTool, ToolKey: tool.ToolKey,
		DefinitionVersion: tool.DefinitionVersion, Fingerprint: fingerprint,
	}}
	runner.service.toolCatalog = workflowDeadlineToolCatalog{tool: tool}
	runner.service.toolExecutor = workflowDeadlineToolExecutor{clock: clock, after: startedAt.Add(2 * time.Second)}
	effect := workflowEffectState{
		EffectID: "effect-deadline", Kind: workflowEffectKindTool, Status: workflowEffectStatusDispatching,
		ActivationPath: "tool-deadline", StepID: "step-deadline", ToolCallID: "call-deadline",
		ToolKey: tool.ToolKey, ToolName: tool.ModelName, ArgumentsJSON: `{}`, ReservedAttempts: 1,
	}

	if err = runner.dispatchWorkflowToolEffect(t.Context(), effect); err != nil {
		t.Fatal(err)
	}
	terminal := store.events[workflowEffectTerminalEventID(effect.EffectID)]
	if terminal.EventType != valueToolFailedFB145984 {
		t.Fatalf("terminal event = %#v", terminal)
	}
	var failure map[string]interface{}
	if json.Unmarshal([]byte(terminal.ErrorJSON), &failure) != nil || failure[workflowPayloadCode] != workflowFailureDurationExceeded {
		t.Fatalf("terminal failure = %#v", failure)
	}
}
