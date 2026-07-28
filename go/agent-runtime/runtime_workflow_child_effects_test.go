package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	testWorkflowChildAgentNodeID  = "agent-node"
	testWorkflowChildNestedNodeID = "nested-node"
)

type workflowChildEffectTestStore struct {
	Store
	events     map[string]model.Event
	manifest   *model.AgentManifest
	definition *model.WorkflowDefinition
}

func (s *workflowChildEffectTestStore) GetRunEvent(_ context.Context, _ model.ActorRef, _, eventID string) (*model.Event, error) {
	event, ok := s.events[eventID]
	if !ok {
		return nil, ErrNotFound
	}
	eventCopy := event
	return &eventCopy, nil
}

func (s *workflowChildEffectTestStore) GetAgentManifest(_ context.Context, _ model.ActorRef, _ model.ResourceRef) (*model.AgentManifest, error) {
	if s.manifest == nil {
		return nil, ErrNotFound
	}
	manifestCopy := *s.manifest
	return &manifestCopy, nil
}

func (s *workflowChildEffectTestStore) GetWorkflowDefinition(_ context.Context, _ model.ActorRef, _ model.ResourceRef) (*model.WorkflowDefinition, error) {
	if s.definition == nil {
		return nil, ErrNotFound
	}
	definitionCopy := *s.definition
	return &definitionCopy, nil
}

func newWorkflowChildEffectTestRunner(store Store) *workflowRunner {
	runner := newWorkflowEffectTestRunner(store)
	runner.service.cfg = StaticConfigProvider(Config{})
	runner.run.Actor = model.ActorRef{TenantID: "tenant-child-effect", ActorID: "actor-child-effect"}
	runner.run.RequestID = "request-child-effect"
	runner.run.Thread = model.ThreadRef{Kind: threadKindConversation, ID: "thread-child-effect"}
	runner.budget.Limits = model.WorkflowLimits{
		MaxNodeActivations: 100,
		MaxChildRuns:       10,
		MaxConcurrentRuns:  4,
		MaxTotalLLMCalls:   20,
		MaxTotalToolCalls:  20,
		MaxNestedDepth:     4,
	}
	return runner
}

func addWorkflowChildActivation(runner *workflowRunner, node model.WorkflowNode, path string) workflowActivationState {
	stepID := deterministicWorkflowID("step", runner.run.RunID, path)
	activation := workflowActivationState{
		NodeID:   node.ID,
		Path:     path,
		ScopeKey: workflowRootScope,
		StepID:   stepID,
		Status:   model.WorkflowStepStatusRunning,
		Attempt:  1,
	}
	runner.state.Activations[path] = activation
	runner.steps[stepID] = model.Step{
		StepID: stepID,
		RunID:  runner.run.RunID,
		NodeID: node.ID,
		Kind:   node.Type,
		Status: model.WorkflowStepStatusRunning,
	}
	return activation
}

func TestWorkflowAgentRegistersIntentBeforeChildStart(t *testing.T) {
	manifest := model.AgentManifest{
		ManifestID:   "agent-child-effect",
		Revision:     1,
		MaxLLMCalls:  3,
		MaxToolCalls: 2,
	}
	store := &workflowChildEffectTestStore{events: map[string]model.Event{}, manifest: &manifest}
	runner := newWorkflowChildEffectTestRunner(store)
	node := model.WorkflowNode{
		ID:          testWorkflowChildAgentNodeID,
		Type:        model.WorkflowNodeAgent,
		ManifestRef: manifest.Ref(),
		Goal:        workflowExprPointer(workflowTestLiteral("Perform bounded child work")),
	}
	activation := addWorkflowChildActivation(runner, node, node.ID)

	_, complete, err := runner.advanceAgent(&node, activation)
	if err != nil || complete {
		t.Fatalf("advanceAgent() complete=%v err=%v", complete, err)
	}
	activation = runner.state.Activations[activation.Path]
	effect := runner.state.Effects[activation.EffectID]
	if effect.Kind != workflowEffectKindAgent || effect.Status != workflowEffectStatusIntent {
		t.Fatalf("unexpected agent effect: %#v", effect)
	}
	if activation.WaitID != "" || activation.ChildRunID != "" || runner.dispatchEffectID != "" {
		t.Fatalf("child became visible before parent CAS: activation=%#v dispatch=%q", activation, runner.dispatchEffectID)
	}
	var payload workflowAgentEffectPayload
	if err = json.Unmarshal([]byte(effect.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	_, expectedChildRunID := delegatedPublicIDs(runner.run.Actor, payload.ClientHandoffID)
	if effect.ChildRunID != expectedChildRunID {
		t.Fatalf("agent child run ID = %q, want %q", effect.ChildRunID, expectedChildRunID)
	}
}

func TestNestedWorkflowRegistersIntentBeforeChildStart(t *testing.T) {
	definition := model.WorkflowDefinition{
		WorkflowID:  "nested-child-effect",
		Revision:    2,
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Limits: model.WorkflowLimits{
			MaxChildRuns:      2,
			MaxConcurrentRuns: 1,
			MaxTotalLLMCalls:  4,
			MaxTotalToolCalls: 3,
		},
	}
	store := &workflowChildEffectTestStore{events: map[string]model.Event{}, definition: &definition}
	runner := newWorkflowChildEffectTestRunner(store)
	node := model.WorkflowNode{
		ID:            testWorkflowChildNestedNodeID,
		Type:          model.WorkflowNodeWorkflow,
		DefinitionRef: definition.Ref(),
		Input:         workflowExprPointer(workflowTestLiteral(map[string]interface{}{"value": "frozen"})),
	}
	activation := addWorkflowChildActivation(runner, node, node.ID)

	_, complete, err := runner.advanceNestedWorkflow(&node, activation)
	if err != nil || complete {
		t.Fatalf("advanceNestedWorkflow() complete=%v err=%v", complete, err)
	}
	assertNestedWorkflowIntentBeforeCAS(t, runner, activation.Path, definition.Ref())
}

func assertNestedWorkflowIntentBeforeCAS(t *testing.T, runner *workflowRunner, activationPath string, definitionRef model.ResourceRef) {
	t.Helper()
	activation := runner.state.Activations[activationPath]
	effect := runner.state.Effects[activation.EffectID]
	if effect.Kind != workflowEffectKindWorkflow || effect.Status != workflowEffectStatusIntent {
		t.Fatalf("unexpected nested workflow effect: %#v", effect)
	}
	if activation.WaitID != "" || activation.ChildRunID != "" || runner.dispatchEffectID != "" {
		t.Fatalf("nested child became visible before parent CAS: activation=%#v dispatch=%q", activation, runner.dispatchEffectID)
	}
	var payload workflowNestedEffectPayload
	if err := json.Unmarshal([]byte(effect.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ClientRunID != effect.ChildRunID || payload.Definition != definitionRef {
		t.Fatalf("unexpected frozen nested payload: %#v", payload)
	}
}

func TestWorkflowChildEffectClaimPersistsWaitBeforeDispatch(t *testing.T) {
	store := &workflowChildEffectTestStore{events: map[string]model.Event{}}
	runner := newWorkflowChildEffectTestRunner(store)
	node := model.WorkflowNode{ID: testWorkflowChildAgentNodeID, Type: model.WorkflowNodeAgent}
	activation := addWorkflowChildActivation(runner, node, node.ID)
	activation.ReservedLLM, activation.ReservedTools, activation.ReservedChildren = 2, 1, 1
	runner.state.Activations[activation.Path] = activation
	runner.budget.ReservedLLMCalls, runner.budget.ReservedToolCalls, runner.budget.ChildRuns, runner.budget.ConcurrentRuns = 2, 1, 1, 1
	clientID := "client-agent-effect"
	_, childRunID := delegatedPublicIDs(runner.run.Actor, clientID)
	if err := runner.registerWorkflowChildEffect(node, activation, workflowEffectKindAgent, childRunID, workflowAgentEffectPayload{ClientHandoffID: clientID}); err != nil {
		t.Fatal(err)
	}
	activation = runner.state.Activations[activation.Path]

	_, complete, err := runner.advanceWorkflowChildEffect(&node, activation)
	if err != nil || complete {
		t.Fatalf("advanceWorkflowChildEffect() complete=%v err=%v", complete, err)
	}
	assertWorkflowChildClaim(t, runner, activation.Path, childRunID)
}

func assertWorkflowChildClaim(t *testing.T, runner *workflowRunner, activationPath, childRunID string) {
	t.Helper()
	activation := runner.state.Activations[activationPath]
	effect := runner.state.Effects[activation.EffectID]
	if activation.Status != model.WorkflowStepStatusWaiting || activation.ChildRunID != childRunID || activation.WaitID == "" {
		t.Fatalf("parent wait was not persisted before dispatch: %#v", activation)
	}
	if effect.Status != workflowEffectStatusDispatching || runner.dispatchEffectID != effect.EffectID {
		t.Fatalf("effect was not claimed after wait persistence: effect=%#v dispatch=%q", effect, runner.dispatchEffectID)
	}
	wait := runner.state.Waits[activation.WaitID]
	if wait.ChildRunID != childRunID || wait.Kind != model.WorkflowWaitAgent {
		t.Fatalf("unexpected durable wait: %#v", wait)
	}
}

func TestRecoveredWorkflowChildEffectReusesChildRunID(t *testing.T) {
	store := &workflowChildEffectTestStore{events: map[string]model.Event{}}
	runner := newWorkflowChildEffectTestRunner(store)
	node := model.WorkflowNode{ID: testWorkflowChildNestedNodeID, Type: model.WorkflowNodeWorkflow}
	activation := addWorkflowChildActivation(runner, node, node.ID)
	childRunID := "run-nested-replay"
	if err := runner.registerWorkflowChildEffect(node, activation, workflowEffectKindWorkflow, childRunID, workflowNestedEffectPayload{ClientRunID: childRunID}); err != nil {
		t.Fatal(err)
	}
	activation = runner.state.Activations[activation.Path]
	if _, _, err := runner.advanceWorkflowChildEffect(&node, activation); err != nil {
		t.Fatal(err)
	}
	activation = runner.state.Activations[activation.Path]
	effect := runner.state.Effects[activation.EffectID]
	firstAttempt := effect.DispatchAttempt
	runner.dispatchEffectID = ""
	runner.segment = workflowSegmentState{}

	if _, _, err := runner.advanceWorkflowChildEffect(&node, activation); err != nil {
		t.Fatal(err)
	}
	replayed := runner.state.Effects[effect.EffectID]
	if replayed.ChildRunID != childRunID || runner.dispatchEffectID != effect.EffectID {
		t.Fatalf("recovery changed child identity: effect=%#v dispatch=%q", replayed, runner.dispatchEffectID)
	}
	if replayed.DispatchAttempt != firstAttempt+1 {
		t.Fatalf("dispatch attempt = %d, want %d", replayed.DispatchAttempt, firstAttempt+1)
	}
}

func TestWorkflowChildStartSuccessKeepsParentWait(t *testing.T) {
	runner, node, activation, effect := claimedWorkflowChildEffectFixture(t)
	terminal := workflowChildTerminalEvent(runner.run, effect, effect.ChildRunID, nil, "")

	_, complete, err := runner.consumeWorkflowChildEffect(&node, activation, effect, terminal)
	if err != nil || complete {
		t.Fatalf("consumeWorkflowChildEffect() complete=%v err=%v", complete, err)
	}
	activation = runner.state.Activations[activation.Path]
	if activation.EffectID != "" || activation.WaitID == "" || activation.ChildRunID != effect.ChildRunID {
		t.Fatalf("successful child start lost parent wait ownership: %#v", activation)
	}
	if _, exists := runner.state.Effects[effect.EffectID]; exists {
		t.Fatal("successful child start must consume the start effect")
	}
	if runner.budget.ConcurrentRuns != 1 || runner.budget.ChildRuns != 1 {
		t.Fatalf("successful child start released live child budget early: %#v", runner.budget)
	}
}

func TestWorkflowChildTerminalRejectsWrongChildRunID(t *testing.T) {
	runner, node, activation, effect := claimedWorkflowChildEffectFixture(t)
	terminal := workflowChildTerminalEvent(runner.run, effect, "different-child", nil, "")
	terminal.PayloadJSON = mustRunJSON(map[string]interface{}{
		workflowPayloadEffectID:   effect.EffectID,
		workflowPayloadChildRunID: "different-child",
		workflowPayloadWaitKind:   model.WorkflowWaitAgent,
	})

	_, _, err := runner.consumeWorkflowChildEffect(&node, activation, effect, terminal)
	var failure workflowNodeFailure
	if !errors.As(err, &failure) || failure.Code != "workflow_effect_invalid" {
		t.Fatalf("wrong child terminal must fail binding validation, got %#v", err)
	}
	if _, exists := runner.state.Effects[effect.EffectID]; !exists {
		t.Fatal("invalid child terminal must not consume the durable effect")
	}
}

func claimedWorkflowChildEffectFixture(t *testing.T) (*workflowRunner, model.WorkflowNode, workflowActivationState, workflowEffectState) {
	t.Helper()
	store := &workflowChildEffectTestStore{events: map[string]model.Event{}}
	runner := newWorkflowChildEffectTestRunner(store)
	node := model.WorkflowNode{ID: testWorkflowChildAgentNodeID, Type: model.WorkflowNodeAgent}
	activation := addWorkflowChildActivation(runner, node, node.ID)
	activation.ReservedLLM, activation.ReservedTools, activation.ReservedChildren = 2, 1, 1
	runner.state.Activations[activation.Path] = activation
	runner.budget.ReservedLLMCalls, runner.budget.ReservedToolCalls = 2, 1
	runner.budget.ChildRuns, runner.budget.ConcurrentRuns = 1, 1
	childRunID := "run-agent-claimed"
	if err := runner.registerWorkflowChildEffect(node, activation, workflowEffectKindAgent, childRunID, workflowAgentEffectPayload{ClientHandoffID: "client-claimed"}); err != nil {
		t.Fatal(err)
	}
	activation = runner.state.Activations[activation.Path]
	if _, _, err := runner.advanceWorkflowChildEffect(&node, activation); err != nil {
		t.Fatal(err)
	}
	activation = runner.state.Activations[activation.Path]
	effect := runner.state.Effects[activation.EffectID]
	return runner, node, activation, effect
}

func TestWorkflowChildStartFailureReleasesReservation(t *testing.T) {
	store := &workflowChildEffectTestStore{events: map[string]model.Event{}}
	runner := newWorkflowChildEffectTestRunner(store)
	node := model.WorkflowNode{ID: testWorkflowChildAgentNodeID, Type: model.WorkflowNodeAgent}
	activation := addWorkflowChildActivation(runner, node, node.ID)
	activation.ReservedLLM, activation.ReservedTools, activation.ReservedChildren = 2, 1, 1
	runner.state.Activations[activation.Path] = activation
	runner.budget.ReservedLLMCalls, runner.budget.ReservedToolCalls = 2, 1
	runner.budget.ChildRuns, runner.budget.ConcurrentRuns = 1, 1
	childRunID := "run-agent-start-failure"
	if err := runner.registerWorkflowChildEffect(node, activation, workflowEffectKindAgent, childRunID, workflowAgentEffectPayload{ClientHandoffID: "client-failure"}); err != nil {
		t.Fatal(err)
	}
	activation = runner.state.Activations[activation.Path]
	if _, _, err := runner.advanceWorkflowChildEffect(&node, activation); err != nil {
		t.Fatal(err)
	}
	activation = runner.state.Activations[activation.Path]
	effect := runner.state.Effects[activation.EffectID]
	terminal := workflowChildTerminalEvent(runner.run, effect, childRunID, workflowNodeFailure{Code: workflowAgentStartFailureCode, Message: "child start rejected"}, workflowAgentStartFailureCode)

	_, _, err := runner.consumeWorkflowChildEffect(&node, activation, effect, terminal)
	assertWorkflowChildStartFailure(t, runner, activation.Path, err)
}

func assertWorkflowChildStartFailure(t *testing.T, runner *workflowRunner, activationPath string, err error) {
	t.Helper()
	var failure workflowNodeFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected workflowNodeFailure, got %#v", err)
	}
	if failure.Code != workflowAgentStartFailureCode {
		t.Fatalf("expected %s, got %#v", workflowAgentStartFailureCode, err)
	}
	activation := runner.state.Activations[activationPath]
	if activation.WaitID != "" {
		t.Fatalf("failed child start left wait: %#v", activation)
	}
	if activation.ChildRunID != "" {
		t.Fatalf("failed child start left child ID: %#v", activation)
	}
	if activation.EffectID != "" {
		t.Fatalf("failed child start left durable ownership: %#v", activation)
	}
	if runner.budget.ReservedLLMCalls != 0 {
		t.Fatalf("failed child start leaked LLM budget: %#v", runner.budget)
	}
	if runner.budget.ReservedToolCalls != 0 {
		t.Fatalf("failed child start leaked tool budget: %#v", runner.budget)
	}
	if runner.budget.ChildRuns != 0 {
		t.Fatalf("failed child start leaked child budget: %#v", runner.budget)
	}
	if runner.budget.ConcurrentRuns != 0 {
		t.Fatalf("failed child start leaked concurrency budget: %#v", runner.budget)
	}
}

func TestDrainingWorkflowChildIntentAbortsWithoutDispatch(t *testing.T) {
	store := &workflowChildEffectTestStore{events: map[string]model.Event{}}
	runner := newWorkflowChildEffectTestRunner(store)
	runner.drainingEffects = true
	node := model.WorkflowNode{ID: testWorkflowChildNestedNodeID, Type: model.WorkflowNodeWorkflow}
	activation := addWorkflowChildActivation(runner, node, node.ID)
	activation.ReservedLLM, activation.ReservedTools, activation.ReservedChildren = 1, 1, 1
	runner.state.Activations[activation.Path] = activation
	runner.budget.ReservedLLMCalls, runner.budget.ReservedToolCalls = 1, 1
	runner.budget.ChildRuns, runner.budget.ConcurrentRuns = 1, 1
	if err := runner.registerWorkflowChildEffect(node, activation, workflowEffectKindWorkflow, "run-aborted-child", workflowNestedEffectPayload{ClientRunID: "run-aborted-child"}); err != nil {
		t.Fatal(err)
	}
	activation = runner.state.Activations[activation.Path]

	_, _, err := runner.advanceWorkflowChildEffect(&node, activation)
	var failure workflowNodeFailure
	if !errors.As(err, &failure) || failure.Code != workflowChildFailureAborted {
		t.Fatalf("expected %q, got %#v", workflowChildFailureAborted, err)
	}
	if runner.dispatchEffectID != "" {
		t.Fatalf("aborted child intent was dispatched: %q", runner.dispatchEffectID)
	}
	if len(runner.state.Effects) != 0 || runner.budget.ConcurrentRuns != 0 || runner.budget.ChildRuns != 0 {
		t.Fatalf("aborted child intent leaked state: effects=%#v budget=%#v", runner.state.Effects, runner.budget)
	}
}
