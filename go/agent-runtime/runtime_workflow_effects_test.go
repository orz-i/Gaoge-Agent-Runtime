package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type workflowEffectTestStore struct {
	Store
	events map[string]model.Event
}

func (s *workflowEffectTestStore) AppendRunEvent(_ context.Context, event *model.Event) (*model.Event, bool, error) {
	if s.events == nil {
		s.events = make(map[string]model.Event)
	}
	eventCopy := *event
	s.events[event.EventID] = eventCopy
	return &eventCopy, true, nil
}

func (s *workflowEffectTestStore) GetRunEvent(_ context.Context, _ model.ActorRef, _, eventID string) (*model.Event, error) {
	event, ok := s.events[eventID]
	if !ok {
		return nil, ErrNotFound
	}
	eventCopy := event
	return &eventCopy, nil
}

func newWorkflowEffectTestRunner(store Store) *workflowRunner {
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	return &workflowRunner{
		service: &Engine{repo: store},
		ctx:     context.Background(),
		run:     model.Run{RunID: "run_effect_test", StartedAt: now},
		state: workflowRuntimeState{
			Scopes:      map[string]workflowScopeState{workflowRootScope: {Vars: map[string]interface{}{}, Outputs: map[string]interface{}{}}},
			Activations: map[string]workflowActivationState{},
			Effects:     map[string]workflowEffectState{},
			Waits:       map[string]model.WorkflowWait{},
		},
		budget:       model.WorkflowBudget{Limits: model.WorkflowLimits{MaxNodeActivations: 100, MaxTotalToolCalls: 10}},
		now:          now,
		steps:        map[string]model.Step{},
		interactions: map[string]model.Interaction{},
		changedSteps: map[string]struct{}{},
	}
}

func addWorkflowToolEffect(
	runner *workflowRunner,
	node model.WorkflowNode,
	path string,
	status string,
	idempotencyMode string,
) (workflowActivationState, workflowEffectState) {
	stepID := deterministicWorkflowID("step", runner.run.RunID, path)
	effectID := workflowEffectID(runner.run.RunID, path)
	activation := workflowActivationState{
		NodeID:        node.ID,
		Path:          path,
		ScopeKey:      workflowRootScope,
		StepID:        stepID,
		Status:        model.WorkflowStepStatusRunning,
		EffectID:      effectID,
		ReservedTools: 1,
	}
	effect := workflowEffectState{
		EffectID:         effectID,
		Kind:             workflowEffectKindTool,
		Status:           status,
		ActivationPath:   path,
		NodeID:           node.ID,
		StepID:           stepID,
		ToolCallID:       deterministicWorkflowID("tool_call", runner.run.RunID, path),
		ToolKey:          "tool:test",
		ToolName:         "test_tool",
		ArgumentsJSON:    `{"value":1}`,
		SideEffectLevel:  ToolSideEffectWrite,
		IdempotencyMode:  idempotencyMode,
		ReservedAttempts: 1,
		DispatchAttempt:  1,
	}
	runner.state.Activations[path] = activation
	runner.state.Effects[effectID] = effect
	runner.steps[stepID] = model.Step{
		StepID: stepID,
		RunID:  runner.run.RunID,
		NodeID: node.ID,
		Kind:   node.Type,
		Status: model.WorkflowStepStatusRunning,
	}
	runner.budget.ReservedToolCalls++
	return activation, effect
}

func TestWorkflowEffectIdentifiersAreStable(t *testing.T) {
	effectID := workflowEffectID("run-1", "root/branch:0/tool")
	if effectID != workflowEffectID("run-1", "root/branch:0/tool") {
		t.Fatal("effect ID must be deterministic")
	}
	terminalEventID := workflowEffectTerminalEventID(effectID)
	secondTerminalEventID := workflowEffectTerminalEventID(effectID)
	if terminalEventID != secondTerminalEventID {
		t.Fatal("terminal event ID must be deterministic")
	}
	if effectID == workflowEffectID("run-1", "root/branch:1/tool") {
		t.Fatal("different activation paths must not share an effect ID")
	}
}

func TestWorkflowToolEffectReplaySafetyUsesFrozenSemantics(t *testing.T) {
	tests := []struct {
		name   string
		effect workflowEffectState
		want   bool
	}{
		{name: "request key", effect: workflowEffectState{IdempotencyMode: ToolIdempotencyRequestKey, SideEffectLevel: ToolSideEffectWrite}, want: true},
		{name: "provider receipt", effect: workflowEffectState{IdempotencyMode: ToolIdempotencyProviderReceipt, SideEffectLevel: ToolSideEffectWrite}, want: true},
		{name: "read only", effect: workflowEffectState{IdempotencyMode: ToolIdempotencyNone, SideEffectLevel: ToolSideEffectRead}, want: true},
		{name: "non idempotent write", effect: workflowEffectState{IdempotencyMode: ToolIdempotencyNone, SideEffectLevel: ToolSideEffectWrite}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := workflowToolEffectReplaySafe(test.effect); got != test.want {
				t.Fatalf("workflowToolEffectReplaySafe() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRecoveredNonIdempotentWorkflowToolFailsClosed(t *testing.T) {
	runner := newWorkflowEffectTestRunner(&workflowEffectTestStore{events: map[string]model.Event{}})
	node := model.WorkflowNode{ID: valueToolCCF14517, Type: model.WorkflowNodeTool}
	activation, effect := addWorkflowToolEffect(runner, node, "root/tool", workflowEffectStatusDispatching, ToolIdempotencyNone)

	_, _, err := runner.advanceWorkflowToolEffect(&node, activation)
	var failure workflowNodeFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected workflowNodeFailure, got %v", err)
	}
	if failure.Code != workflowEffectFailureUnknown {
		t.Fatalf("failure code = %q, want %q", failure.Code, workflowEffectFailureUnknown)
	}
	if runner.dispatchEffectID != "" {
		t.Fatalf("unsafe recovered effect must not dispatch again: %q", runner.dispatchEffectID)
	}
	if _, exists := runner.state.Effects[effect.EffectID]; exists {
		t.Fatal("failed-closed effect must be consumed")
	}
	if runner.budget.ReservedToolCalls != 0 || runner.budget.UsedToolCalls != 0 {
		t.Fatalf("unexpected tool budget after fail-closed: reserved=%d used=%d", runner.budget.ReservedToolCalls, runner.budget.UsedToolCalls)
	}
	if len(runner.events) == 0 || runner.events[0].EventID != workflowEffectTerminalEventID(effect.EffectID) {
		t.Fatal("fail-closed recovery must emit the deterministic terminal event")
	}
}

func TestRecoveredReplaySafeWorkflowToolReusesStableCallID(t *testing.T) {
	runner := newWorkflowEffectTestRunner(&workflowEffectTestStore{events: map[string]model.Event{}})
	node := model.WorkflowNode{ID: valueToolCCF14517, Type: model.WorkflowNodeTool}
	activation, effect := addWorkflowToolEffect(runner, node, "root/tool", workflowEffectStatusDispatching, ToolIdempotencyRequestKey)

	_, complete, err := runner.advanceWorkflowToolEffect(&node, activation)
	if err != nil || complete {
		t.Fatalf("advanceWorkflowToolEffect() complete=%v err=%v", complete, err)
	}
	if runner.dispatchEffectID != effect.EffectID {
		t.Fatalf("dispatch effect = %q, want %q", runner.dispatchEffectID, effect.EffectID)
	}
	claimed := runner.state.Effects[effect.EffectID]
	if claimed.ToolCallID != effect.ToolCallID {
		t.Fatalf("replay changed tool call ID: got %q want %q", claimed.ToolCallID, effect.ToolCallID)
	}
	if claimed.DispatchAttempt != effect.DispatchAttempt+1 {
		t.Fatalf("dispatch attempt = %d, want %d", claimed.DispatchAttempt, effect.DispatchAttempt+1)
	}
}

func TestDrainingIntentAbortsBeforeProviderDispatch(t *testing.T) {
	runner := newWorkflowEffectTestRunner(&workflowEffectTestStore{events: map[string]model.Event{}})
	runner.drainingEffects = true
	node := model.WorkflowNode{ID: valueToolCCF14517, Type: model.WorkflowNodeTool}
	activation, effect := addWorkflowToolEffect(runner, node, "root/tool", workflowEffectStatusIntent, ToolIdempotencyRequestKey)

	_, _, err := runner.advanceWorkflowToolEffect(&node, activation)
	var failure workflowNodeFailure
	if !errors.As(err, &failure) || failure.Code != workflowEffectFailureAborted {
		t.Fatalf("expected %q failure, got %#v", workflowEffectFailureAborted, err)
	}
	if runner.dispatchEffectID != "" {
		t.Fatal("an intent drained during cancellation must not reach the provider")
	}
	if _, exists := runner.state.Effects[effect.EffectID]; exists {
		t.Fatal("aborted intent must be consumed")
	}
}

func TestParallelWorkflowClaimsOnlyOneToolEffectPerTransition(t *testing.T) {
	store := &workflowEffectTestStore{events: map[string]model.Event{}}
	runner := newWorkflowEffectTestRunner(store)
	root := model.WorkflowNode{
		ID:   "root",
		Type: model.WorkflowNodeParallel,
		Branches: []model.WorkflowNode{
			{ID: "tool-a", Type: model.WorkflowNodeTool},
			{ID: "tool-b", Type: model.WorkflowNodeTool},
		},
	}
	runner.definition.Root = root
	runner.state.Activations[root.ID] = workflowActivationState{
		NodeID: root.ID, Path: root.ID, ScopeKey: workflowRootScope,
		StepID: deterministicWorkflowID("step", runner.run.RunID, root.ID),
		Status: model.WorkflowStepStatusRunning,
	}
	firstPath := "root/branch:0/tool-a"
	secondPath := "root/branch:1/tool-b"
	_, first := addWorkflowToolEffect(runner, root.Branches[0], firstPath, workflowEffectStatusIntent, ToolIdempotencyRequestKey)
	_, second := addWorkflowToolEffect(runner, root.Branches[1], secondPath, workflowEffectStatusIntent, ToolIdempotencyRequestKey)

	_, complete, err := runner.advanceNode(&runner.definition.Root, root.ID, workflowRootScope, "")
	if err != nil || complete {
		t.Fatalf("advanceNode() complete=%v err=%v", complete, err)
	}
	if runner.dispatchEffectID != first.EffectID {
		t.Fatalf("dispatch effect = %q, want first branch %q", runner.dispatchEffectID, first.EffectID)
	}
	if runner.state.Effects[first.EffectID].Status != workflowEffectStatusDispatching {
		t.Fatal("first effect was not claimed")
	}
	if runner.state.Effects[second.EffectID].Status != workflowEffectStatusIntent {
		t.Fatal("second effect must remain intent until the next CAS transition")
	}
}

func newWorkflowToolTerminalTest() (*workflowRunner, model.WorkflowNode, workflowActivationState, workflowEffectState, model.Event) {
	runner := newWorkflowEffectTestRunner(&workflowEffectTestStore{events: map[string]model.Event{}})
	node := model.WorkflowNode{ID: valueToolCCF14517, Type: model.WorkflowNodeTool}
	activation, effect := addWorkflowToolEffect(runner, node, node.ID, workflowEffectStatusDispatching, ToolIdempotencyRequestKey)
	terminal := workflowToolTerminalEvent(
		runner.run,
		effect,
		`{"ok":true}`,
		ToolExecutionResult{Attempts: 1},
		nil,
		"",
	)
	return runner, node, activation, effect, terminal
}

func TestWorkflowToolTerminalReceiptRejectsForgedBinding(t *testing.T) {
	runner, node, activation, effect, terminal := newWorkflowToolTerminalTest()
	forged := terminal
	forged.ToolCallID = "different-call"
	_, _, err := runner.consumeWorkflowToolEffect(&node, activation, effect, forged)
	var failure workflowNodeFailure
	if !errors.As(err, &failure) || failure.Code != "workflow_effect_invalid" {
		t.Fatalf("forged terminal event must fail validation, got %#v", err)
	}
	if runner.budget.UsedToolCalls != 0 {
		t.Fatal("forged terminal event must not charge the tool budget")
	}
	if _, exists := runner.state.Effects[effect.EffectID]; !exists {
		t.Fatal("forged terminal event must not consume the effect")
	}
}

func TestWorkflowToolTerminalReceiptIsConsumedOnce(t *testing.T) {
	runner, node, activation, effect, terminal := newWorkflowToolTerminalTest()
	consumeValidWorkflowToolTerminal(t, runner, node, activation, effect, terminal)
	if runner.budget.ReservedToolCalls != 0 || runner.budget.UsedToolCalls != 1 || runner.run.ToolCallsCount != 1 {
		t.Fatalf("unexpected charged budget: reserved=%d used=%d run=%d", runner.budget.ReservedToolCalls, runner.budget.UsedToolCalls, runner.run.ToolCallsCount)
	}
	if _, exists := runner.state.Effects[effect.EffectID]; exists {
		t.Fatal("valid terminal event must consume the effect")
	}

	used := runner.budget.UsedToolCalls
	_, complete, err := runner.advanceNode(&node, node.ID, workflowRootScope, "")
	if err != nil || !complete {
		t.Fatalf("completed activation replay complete=%v err=%v", complete, err)
	}
	if runner.budget.UsedToolCalls != used {
		t.Fatalf("completed activation replay charged budget again: got %d want %d", runner.budget.UsedToolCalls, used)
	}
}

func consumeValidWorkflowToolTerminal(
	t *testing.T,
	runner *workflowRunner,
	node model.WorkflowNode,
	activation workflowActivationState,
	effect workflowEffectState,
	terminal model.Event,
) {
	t.Helper()
	value, complete, err := runner.consumeWorkflowToolEffect(&node, activation, effect, terminal)
	if err != nil || !complete {
		t.Fatalf("valid terminal event complete=%v err=%v", complete, err)
	}
	got, ok := value.(map[string]interface{})
	if !ok || got["ok"] != true {
		t.Fatalf("unexpected terminal output: %#v", value)
	}
}
