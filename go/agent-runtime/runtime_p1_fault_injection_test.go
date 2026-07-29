package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

var errInjectedWorkflowTerminalPersist = errors.New("injected workflow terminal persist failure")

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

type workflowCASConflictStore struct {
	Store
	calls int
}

func (s *workflowCASConflictStore) ApplyWorkflowTransition(
	_ context.Context,
	_ model.ActorRef,
	_ string,
	transition model.WorkflowTransition,
) (*model.WorkflowExecution, []model.Event, bool, error) {
	s.calls++
	current := transition.Execution
	current.Version = transition.ExpectedVersion
	return &current, nil, false, nil
}

func TestWorkflowCASConflictStopsBeforeEffectDispatch(t *testing.T) {
	store := &workflowCASConflictStore{}
	runner := newWorkflowEffectTestRunner(store)
	runner.execution = model.WorkflowExecution{RunID: runner.run.RunID, Version: 4}
	runner.budget.Limits.MaxStateBytes = 1 << 20
	runner.dispatchEffectID = "effect-cas-conflict"

	applied, err := runner.commit()
	if err != nil || applied || store.calls != 1 {
		t.Fatalf("applied=%v calls=%d err=%v", applied, store.calls, err)
	}
	if runner.dispatchEffectID != "effect-cas-conflict" {
		t.Fatalf("CAS conflict mutated pending dispatch: %q", runner.dispatchEffectID)
	}
}

type workflowTerminalPersistFaultStore struct {
	Store
	events      map[string]model.Event
	appendCalls int
}

func (s *workflowTerminalPersistFaultStore) AppendRunEvent(_ context.Context, event *model.Event) (*model.Event, bool, error) {
	s.appendCalls++
	if s.appendCalls == 1 {
		return nil, false, errInjectedWorkflowTerminalPersist
	}
	eventCopy := *event
	s.events[event.EventID] = eventCopy
	return &eventCopy, true, nil
}

type workflowReplayRecordingExecutor struct {
	requestIDs []string
}

func (e *workflowReplayRecordingExecutor) Execute(_ context.Context, input ToolExecutionInput) (string, error) {
	e.requestIDs = append(e.requestIDs, input.RequestID)
	return `{"ok":true}`, nil
}

func TestWorkflowTerminalPersistFailureReplaysWithStableRequestKey(t *testing.T) {
	store := &workflowTerminalPersistFaultStore{events: map[string]model.Event{}}
	runner := newWorkflowEffectTestRunner(store)
	runner.service.cfg = StaticConfigProvider(Config{Tools: ToolConfig{MaxConcurrentCalls: 1}})
	runner.service.generationStreams = newGenerationStreamRegistry(nil, generationStreamOptions{})
	tool, _ := frozenFirewallTestTool(t, json.RawMessage(`{"type":"object"}`), nil, valueNever4C6E2E88)
	tool.IdempotencyMode = ToolIdempotencyRequestKey
	fingerprint, err := hashWorkflowValue(tool)
	if err != nil {
		t.Fatal(err)
	}
	runner.definition.Dependencies = []model.WorkflowDependency{{
		Kind: model.WorkflowDependencyTool, ToolKey: tool.ToolKey,
		DefinitionVersion: tool.DefinitionVersion, Fingerprint: fingerprint,
	}}
	runner.service.toolCatalog = workflowDeadlineToolCatalog{tool: tool}
	executor := &workflowReplayRecordingExecutor{}
	runner.service.toolExecutor = executor
	effect := workflowEffectState{
		EffectID: "effect-terminal-replay", Kind: workflowEffectKindTool, Status: workflowEffectStatusDispatching,
		ActivationPath: "tool-terminal-replay", StepID: "step-terminal-replay", ToolCallID: "call-terminal-replay",
		ToolKey: tool.ToolKey, ToolName: tool.ModelName, ArgumentsJSON: `{}`, ReservedAttempts: 1,
	}

	if err = runner.dispatchWorkflowToolEffect(t.Context(), effect); !errors.Is(err, errInjectedWorkflowTerminalPersist) {
		t.Fatalf("first dispatch error = %v", err)
	}
	if err = runner.dispatchWorkflowToolEffect(t.Context(), effect); err != nil {
		t.Fatalf("replayed dispatch error = %v", err)
	}
	if len(executor.requestIDs) != 2 || executor.requestIDs[0] != executor.requestIDs[1] {
		t.Fatalf("request IDs are not stable: %#v", executor.requestIDs)
	}
	wantRequestID := runner.run.RunID + ":tool:" + effect.ToolCallID
	if executor.requestIDs[0] != wantRequestID {
		t.Fatalf("request ID = %q, want %q", executor.requestIDs[0], wantRequestID)
	}
	if _, ok := store.events[workflowEffectTerminalEventID(effect.EffectID)]; !ok {
		t.Fatal("replayed terminal event was not persisted")
	}
}
