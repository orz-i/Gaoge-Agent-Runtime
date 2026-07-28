package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	workflowTestCountKey = "count"
	workflowTestOther    = "other"
	workflowTestSame     = "same"
	workflowTestBody     = "body"
	workflowTestDone     = "done"
	workflowTestNested   = "nested"
	workflowTestReturnID = "return"
)

func TestWorkflowRunnerExecutesDeterministicControlFlow(t *testing.T) {
	root := model.WorkflowNode{
		ID: workflowRootScope, Type: model.WorkflowNodeSequence,
		Children: []model.WorkflowNode{
			{
				ID: "initialize", Type: model.WorkflowNodeSet,
				Assignments: map[string]model.WorkflowExpr{
					"x":                  workflowTestOp(model.WorkflowExprOpAdd, workflowTestRef(model.WorkflowExprRefInput+".base"), workflowTestLiteral(2)),
					workflowTestCountKey: workflowTestLiteral(0),
				},
			},
			{
				ID: "choice", Type: model.WorkflowNodeIf,
				Condition: workflowExprPointer(workflowTestOp(model.WorkflowExprOpEq, workflowTestRef(model.WorkflowExprRefVars+".x"), workflowTestLiteral(3))),
				Then: &model.WorkflowNode{
					ID: "chosen", Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo,
					Message: workflowExprPointer(workflowTestLiteral("then")), Data: workflowExprPointer(workflowTestLiteral("selected")),
				},
				Else: &model.WorkflowNode{
					ID: workflowTestOther, Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo,
					Message: workflowExprPointer(workflowTestLiteral("else")), Data: workflowExprPointer(workflowTestLiteral(workflowTestOther)),
				},
			},
			{
				ID: "counter", Type: model.WorkflowNodeLoop, MaxIterations: 3,
				Condition: workflowExprPointer(workflowTestOp(model.WorkflowExprOpLt, workflowTestRef(model.WorkflowExprRefVars+"."+workflowTestCountKey), workflowTestLiteral(2))),
				Body: &model.WorkflowNode{
					ID: "increment", Type: model.WorkflowNodeSet,
					Assignments: map[string]model.WorkflowExpr{
						workflowTestCountKey: workflowTestOp(model.WorkflowExprOpAdd, workflowTestRef(model.WorkflowExprRefVars+"."+workflowTestCountKey), workflowTestLiteral(1)),
					},
				},
			},
			{
				ID: "parallel", Type: model.WorkflowNodeParallel, FailurePolicy: model.WorkflowFailureFailFast,
				Branches: []model.WorkflowNode{
					{ID: "lane_a", Type: model.WorkflowNodeSet, Assignments: map[string]model.WorkflowExpr{workflowPayloadLane: workflowTestLiteral("a")}},
					{ID: "lane_b", Type: model.WorkflowNodeSet, Assignments: map[string]model.WorkflowExpr{workflowPayloadLane: workflowTestLiteral("b")}},
				},
			},
			{
				ID: "each", Type: model.WorkflowNodeForEach, MaxConcurrency: 2, FailurePolicy: model.WorkflowFailureFailFast,
				ItemsExpr: workflowExprPointer(workflowTestRef(model.WorkflowExprRefInput + "." + workflowPayloadItems)),
				Body: &model.WorkflowNode{
					ID: "copy_item", Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo,
					Message: workflowExprPointer(workflowTestLiteral(model.WorkflowExprRefItem)), Data: workflowExprPointer(workflowTestRef(model.WorkflowExprRefItem)),
				},
			},
			{
				ID: "pipe", Type: model.WorkflowNodePipeline, MaxConcurrency: 2, FailurePolicy: model.WorkflowFailureFailFast,
				ItemsExpr: workflowExprPointer(workflowTestRef(model.WorkflowExprRefInput + "." + workflowPayloadItems)),
				Stages: []model.WorkflowNode{
					{
						ID: "plus_one", Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo,
						Message: workflowExprPointer(workflowTestLiteral("plus")), Data: workflowExprPointer(workflowTestOp(model.WorkflowExprOpAdd, workflowTestRef(model.WorkflowExprRefItem), workflowTestLiteral(1))),
					},
					{
						ID: "times_two", Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo,
						Message: workflowExprPointer(workflowTestLiteral("times")), Data: workflowExprPointer(workflowTestOp(model.WorkflowExprOpMul, workflowTestRef(model.WorkflowExprRefItem), workflowTestLiteral(2))),
					},
				},
			},
			{
				ID: "result", Type: model.WorkflowNodeReturn,
				Value: workflowExprPointer(model.WorkflowExpr{Op: model.WorkflowExprOpObject, Fields: map[string]model.WorkflowExpr{
					"choice":             workflowTestRef(model.WorkflowExprRefSteps + ".choice"),
					workflowTestCountKey: workflowTestRef(model.WorkflowExprRefVars + "." + workflowTestCountKey),
					"parallel":           workflowTestRef(model.WorkflowExprRefSteps + ".parallel"),
					"each":               workflowTestRef(model.WorkflowExprRefSteps + ".each"),
					"pipeline":           workflowTestRef(model.WorkflowExprRefSteps + ".pipe"),
				}}),
				Presentation: workflowExprPointer(workflowTestLiteral(workflowTestDone)),
			},
		},
	}
	now := time.Now()
	limits := model.WorkflowLimits{
		MaxNodeActivations: 100, MaxChildRuns: 4, MaxConcurrentRuns: 2,
		MaxTotalLLMCalls: 10, MaxTotalToolCalls: 10, MaxDurationSeconds: 60,
		MaxLoopIterations: 10, MaxNestedDepth: 2, MaxStateBytes: 1 << 20,
	}
	runner := workflowRunner{
		service: &Engine{cfg: StaticConfigProvider(Config{})},
		run: model.Run{
			RunID: "run_workflow_control", RuntimeKind: model.RuntimeKindWorkflow,
			Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, StartedAt: now,
		},
		definition: model.WorkflowDefinition{
			WorkflowID: "workflow_control", Revision: 1, Root: root,
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"required":["choice","count","parallel","each","pipeline"],
				"additionalProperties":false,
				"properties":{
					"choice":{"const":"selected"},
					"count":{"const":2},
					"parallel":{"type":"array"},
					"each":{"type":"array"},
					"pipeline":{"type":"array"}
				}
			}`),
		},
		execution: model.WorkflowExecution{RunID: "run_workflow_control", Version: 1},
		state: workflowRuntimeState{
			SemanticVersion: RuntimeSnapshotVersion,
			Input:           map[string]interface{}{"base": json.Number("1"), workflowPayloadItems: []interface{}{json.Number("1"), json.Number("2"), json.Number("3")}},
			Scopes:          map[string]workflowScopeState{workflowRootScope: {Vars: map[string]interface{}{}, Outputs: map[string]interface{}{}}},
			Activations:     map[string]workflowActivationState{},
			Waits:           map[string]model.WorkflowWait{},
		},
		budget:       model.WorkflowBudget{Limits: limits},
		now:          now,
		steps:        map[string]model.Step{},
		interactions: map[string]model.Interaction{},
		changedSteps: map[string]struct{}{},
	}
	executeWorkflowTestRunner(t, &runner)
	assertWorkflowControlFlowResult(t, runner)
}

func executeWorkflowTestRunner(t *testing.T, runner *workflowRunner) {
	t.Helper()
	complete := false
	for attempt := 0; attempt < 10 && !complete; attempt++ {
		_, complete, _ = runner.advanceNode(&runner.definition.Root, runner.definition.Root.ID, workflowRootScope, "")
	}
	if !complete || !runner.state.Returned {
		t.Fatalf("workflow did not finish: complete=%t state=%#v", complete, runner.state)
	}
	if err := runner.completeSuccessfulRun(); err != nil {
		t.Fatalf("completeSuccessfulRun() error = %v", err)
	}
}

func assertWorkflowControlFlowResult(t *testing.T, runner workflowRunner) {
	t.Helper()
	if runner.terminalOutcome != model.TerminalCompleted || runner.result == nil || runner.result.Presentation != workflowTestDone {
		t.Fatalf("terminal result = %#v, outcome=%s", runner.result, runner.terminalOutcome)
	}
	rootScope := runner.state.Scopes[workflowRootScope]
	if _, leaked := rootScope.Vars[workflowPayloadLane]; leaked {
		t.Fatal("parallel lane variable leaked into the parent scope")
	}
	result, ok := runner.state.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("workflow result type = %T", runner.state.Result)
	}
	assertWorkflowCanonicalEqual(t, result["pipeline"], []interface{}{json.Number("4"), json.Number("6"), json.Number("8")})
	assertWorkflowCanonicalEqual(t, result["each"], []interface{}{json.Number("1"), json.Number("2"), json.Number("3")})
	assertWorkflowCanonicalEqual(t, result["parallel"], []interface{}{
		map[string]interface{}{workflowPayloadLane: "a"},
		map[string]interface{}{workflowPayloadLane: "b"},
	})
}

func workflowExprPointer(value model.WorkflowExpr) *model.WorkflowExpr {
	return &value
}

func assertWorkflowCanonicalEqual(t *testing.T, got, want interface{}) {
	t.Helper()
	gotJSON, _ := canonicalWorkflowJSON(got)
	wantJSON, _ := canonicalWorkflowJSON(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("value = %s, want %s", gotJSON, wantJSON)
	}
}

func TestWorkflowRunnerCompensatesInReverseOrderAndResumesOnlyFailedUndo(t *testing.T) {
	root := model.WorkflowNode{
		ID: workflowRootScope, Type: model.WorkflowNodeSequence,
		Children: []model.WorkflowNode{
			workflowTestCompensation("first", workflowTestLiteral(1)),
			workflowTestCompensation("second", workflowTestLiteral("undo-second")),
			{
				ID: "fail", Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo,
				Message: workflowExprPointer(workflowTestLiteral(1)),
			},
			{ID: workflowTestReturnID, Type: model.WorkflowNodeReturn, Value: workflowExprPointer(workflowTestLiteral("unreachable"))},
		},
	}
	runner := workflowCompensationTestRunner(root)
	if err := runner.advanceRoot(); err != nil {
		t.Fatalf("advanceRoot() error = %v", err)
	}
	assertWorkflowCompensationSuspended(t, runner)
	assertWorkflowLogSummaries(t, runner.events, []string{"undo-second"})

	if _, err := resetFailedWorkflowCompensation(&runner.state, runner.run.RunID, runner.now); err != nil {
		t.Fatalf("resetFailedWorkflowCompensation() error = %v", err)
	}
	runner.state.Compensations[0].Undo.Message = workflowExprPointer(workflowTestLiteral("undo-first"))
	runner.terminalOutcome, runner.terminalCode, runner.terminalMessage = "", "", ""
	if err := runner.advanceCompensations(false); err != nil {
		t.Fatalf("advanceCompensations() error = %v", err)
	}
	assertWorkflowCompensationCompleted(t, runner)
	assertWorkflowLogSummaries(t, runner.events, []string{"undo-second", "undo-first"})
}

func assertWorkflowCompensationSuspended(t *testing.T, runner workflowRunner) {
	t.Helper()
	if runner.terminalOutcome != model.RunStatusSuspended {
		t.Fatalf("terminal outcome = %s, want %s", runner.terminalOutcome, model.RunStatusSuspended)
	}
	if len(runner.state.Compensations) != 2 ||
		runner.state.Compensations[0].Status != model.WorkflowCompensationFailed ||
		runner.state.Compensations[1].Status != model.WorkflowCompensationCompleted {
		t.Fatalf("compensations after failure = %#v", runner.state.Compensations)
	}
}

func assertWorkflowCompensationCompleted(t *testing.T, runner workflowRunner) {
	t.Helper()
	if runner.terminalOutcome != model.TerminalFailed {
		t.Fatalf("terminal outcome after resume = %s, want %s", runner.terminalOutcome, model.TerminalFailed)
	}
	for _, compensation := range runner.state.Compensations {
		if compensation.Status != model.WorkflowCompensationCompleted {
			t.Fatalf("compensation after resume = %#v", compensation)
		}
	}
}

func workflowTestCompensation(id string, undoMessage model.WorkflowExpr) model.WorkflowNode {
	return model.WorkflowNode{
		ID: id, Type: model.WorkflowNodeCompensate,
		Do: &model.WorkflowNode{
			ID: id + "_do", Type: model.WorkflowNodeSet,
			Assignments: map[string]model.WorkflowExpr{id: workflowTestLiteral(true)},
		},
		Undo: &model.WorkflowNode{
			ID: id + "_undo", Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo,
			Message: workflowExprPointer(undoMessage),
		},
	}
}

func workflowCompensationTestRunner(root model.WorkflowNode) workflowRunner {
	now := time.Now()
	limits := model.WorkflowLimits{
		MaxNodeActivations: 100, MaxChildRuns: 4, MaxConcurrentRuns: 2,
		MaxTotalLLMCalls: 10, MaxTotalToolCalls: 10, MaxDurationSeconds: 60,
		MaxLoopIterations: 10, MaxNestedDepth: 2, MaxStateBytes: 1 << 20,
	}
	return workflowRunner{
		service: &Engine{cfg: StaticConfigProvider(Config{})},
		run: model.Run{
			RunID: "run_workflow_compensation", RuntimeKind: model.RuntimeKindWorkflow,
			Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, StartedAt: now,
		},
		definition: model.WorkflowDefinition{
			WorkflowID: "workflow_compensation", Revision: 1, Root: root,
			OutputSchema: json.RawMessage(`{"type":"string"}`),
		},
		execution: model.WorkflowExecution{RunID: "run_workflow_compensation", Version: 1},
		state: workflowRuntimeState{
			SemanticVersion: RuntimeSnapshotVersion,
			Input:           map[string]interface{}{},
			Scopes:          map[string]workflowScopeState{workflowRootScope: {Vars: map[string]interface{}{}, Outputs: map[string]interface{}{}}},
			Activations:     map[string]workflowActivationState{},
			Waits:           map[string]model.WorkflowWait{},
		},
		budget:       model.WorkflowBudget{Limits: limits},
		now:          now,
		steps:        map[string]model.Step{},
		interactions: map[string]model.Interaction{},
		changedSteps: map[string]struct{}{},
	}
}

func assertWorkflowLogSummaries(t *testing.T, events []model.Event, want []string) {
	t.Helper()
	got := make([]string, 0, len(want))
	for _, event := range events {
		if event.EventType == "workflow.log" {
			got = append(got, event.Summary)
		}
	}
	assertWorkflowCanonicalEqual(t, got, want)
}

func TestWorkflowCompilerRequiresCompensationSafeNestedWorkflow(t *testing.T) {
	safe := workflowNestedCompensationDefinition("safe", model.WorkflowNode{
		ID: "safe_log", Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo,
		Message: workflowExprPointer(workflowTestLiteral("safe")),
	})
	unsafe := workflowNestedCompensationDefinition("unsafe", model.WorkflowNode{
		ID: "unsafe_agent", Type: model.WorkflowNodeAgent,
	})
	store := &workflowCompilerTestStore{definitions: map[string]model.WorkflowDefinition{
		safe.WorkflowID: safe, unsafe.WorkflowID: unsafe,
	}}
	for _, test := range []struct {
		name       string
		definition model.WorkflowDefinition
		wantErr    bool
	}{
		{name: "safe nested workflow", definition: safe},
		{name: "nested workflow containing agent", definition: unsafe, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := workflowNestedCompensationRoot(test.definition.Ref())
			compiler := workflowDefinitionCompiler{
				service: &Engine{cfg: StaticConfigProvider(Config{}), repo: store},
				ctx:     context.Background(), actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey},
				workflowID: "parent", nodeIDs: make(map[string]struct{}),
				dependencies: make(map[string]model.WorkflowDependency), maxNodes: 100,
			}
			err := compiler.compileRoot(&root)
			if test.wantErr && !errors.Is(err, ErrWorkflowDefinitionInvalid) {
				t.Fatalf("compileRoot() error = %v", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("compileRoot() error = %v", err)
			}
		})
	}
}

type workflowCompilerTestStore struct {
	Store
	definitions map[string]model.WorkflowDefinition
}

func (s *workflowCompilerTestStore) GetWorkflowDefinition(
	_ context.Context,
	_ model.ActorRef,
	ref model.ResourceRef,
) (*model.WorkflowDefinition, error) {
	item, ok := s.definitions[ref.ID]
	if !ok {
		return nil, ErrNotFound
	}
	return &item, nil
}

func workflowNestedCompensationDefinition(id string, body model.WorkflowNode) model.WorkflowDefinition {
	return model.WorkflowDefinition{
		WorkflowID: id, Revision: 1, Status: model.WorkflowDefinitionStatusActive,
		DefinitionHash: id + "_hash",
		Root: model.WorkflowNode{
			ID: id + "_root", Type: model.WorkflowNodeSequence,
			Children: []model.WorkflowNode{
				body,
				{ID: id + "_return", Type: model.WorkflowNodeReturn, Value: workflowExprPointer(workflowTestLiteral("ok"))},
			},
		},
	}
}

func workflowNestedCompensationRoot(ref model.ResourceRef) model.WorkflowNode {
	return model.WorkflowNode{
		ID: workflowRootScope, Type: model.WorkflowNodeSequence,
		Children: []model.WorkflowNode{
			{
				ID: "guarded", Type: model.WorkflowNodeCompensate,
				Do: &model.WorkflowNode{
					ID: "do", Type: model.WorkflowNodeSet,
					Assignments: map[string]model.WorkflowExpr{workflowTestDone: workflowTestLiteral(true)},
				},
				Undo: &model.WorkflowNode{
					ID: "undo", Type: model.WorkflowNodeWorkflow, DefinitionRef: ref,
					Input: workflowExprPointer(workflowTestLiteral(map[string]interface{}{})),
				},
			},
			{ID: workflowTestReturnID, Type: model.WorkflowNodeReturn, Value: workflowExprPointer(workflowTestLiteral("ok"))},
		},
	}
}

func TestWorkflowNodeUnionAcceptsEveryValidVariant(t *testing.T) {
	leaf := model.WorkflowNode{
		ID: "leaf", Type: model.WorkflowNodeSet,
		Assignments: map[string]model.WorkflowExpr{workflowPayloadValue: workflowTestLiteral(true)},
	}
	nodes := []model.WorkflowNode{
		{ID: model.WorkflowNodeSequence, Type: model.WorkflowNodeSequence, Children: []model.WorkflowNode{leaf}},
		{
			ID: model.WorkflowNodeAgent, Type: model.WorkflowNodeAgent,
			ManifestRef:  model.ResourceRef{Kind: model.AgentManifestKind, ID: "manifest"},
			Goal:         workflowExprPointer(workflowTestLiteral("goal")),
			OutputSchema: json.RawMessage(`{"type":"string"}`),
		},
		{
			ID: model.WorkflowNodeParallel, Type: model.WorkflowNodeParallel,
			Branches: []model.WorkflowNode{leaf}, FailurePolicy: model.WorkflowFailureFailFast,
		},
		{
			ID: model.WorkflowNodeForEach, Type: model.WorkflowNodeForEach,
			ItemsExpr: workflowExprPointer(workflowTestLiteral([]interface{}{})), Body: &leaf,
			MaxConcurrency: 1, FailurePolicy: model.WorkflowFailureFailFast,
		},
		{
			ID: model.WorkflowNodePipeline, Type: model.WorkflowNodePipeline,
			ItemsExpr: workflowExprPointer(workflowTestLiteral([]interface{}{})), Stages: []model.WorkflowNode{leaf},
			MaxConcurrency: 1, FailurePolicy: model.WorkflowFailureFailFast,
		},
		{
			ID: model.WorkflowNodeIf, Type: model.WorkflowNodeIf,
			Condition: workflowExprPointer(workflowTestLiteral(true)), Then: &leaf,
		},
		{
			ID: model.WorkflowNodeLoop, Type: model.WorkflowNodeLoop,
			Condition: workflowExprPointer(workflowTestLiteral(false)), Body: &leaf, MaxIterations: 1,
		},
		leaf,
		{
			ID: model.WorkflowNodeLog, Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo,
			Message: workflowExprPointer(workflowTestLiteral("log")),
		},
		{
			ID: model.WorkflowNodeTool, Type: model.WorkflowNodeTool, ToolKey: valueRead3A612695,
			Arguments: workflowExprPointer(workflowTestLiteral(map[string]interface{}{})),
		},
		{
			ID: model.WorkflowNodeWorkflow, Type: model.WorkflowNodeWorkflow,
			DefinitionRef: model.ResourceRef{Kind: model.WorkflowDefinitionKind, ID: workflowTestNested},
			Input:         workflowExprPointer(workflowTestLiteral(map[string]interface{}{})),
		},
		{
			ID: model.WorkflowNodeInteraction, Type: model.WorkflowNodeInteraction,
			Title: "input", Prompt: "provide input", Schema: json.RawMessage(`{"type":"string"}`), ExpiresAfterSeconds: 60,
		},
		{
			ID: model.WorkflowNodeTimer, Type: model.WorkflowNodeTimer,
			DelaySeconds: workflowExprPointer(workflowTestLiteral(1)),
		},
		{
			ID: model.WorkflowNodeCompensate, Type: model.WorkflowNodeCompensate,
			Do: &leaf, Undo: &model.WorkflowNode{
				ID: "undo", Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo,
				Message: workflowExprPointer(workflowTestLiteral("undo")),
			},
		},
		{
			ID: model.WorkflowNodeReturn, Type: model.WorkflowNodeReturn,
			Value: workflowExprPointer(workflowTestLiteral("result")),
		},
	}
	for index := range nodes {
		if err := validateWorkflowNodeShape(nodes[index]); err != nil {
			t.Fatalf("validateWorkflowNodeShape(%s) error = %v", nodes[index].Type, err)
		}
	}

	invalid := nodes[0]
	invalid.ManifestRef = model.ResourceRef{Kind: model.AgentManifestKind, ID: "illegal"}
	if err := validateWorkflowNodeShape(invalid); !errors.Is(err, ErrWorkflowDefinitionInvalid) {
		t.Fatalf("cross-variant field error = %v", err)
	}
}

func TestWorkflowCompilerRejectsInvalidControlFlow(t *testing.T) {
	validReturn := model.WorkflowNode{ID: workflowTestReturnID, Type: model.WorkflowNodeReturn, Value: workflowExprPointer(workflowTestLiteral("ok"))}
	tests := []struct {
		name    string
		root    model.WorkflowNode
		wantErr error
	}{
		{name: "root is not sequence", root: validReturn, wantErr: ErrWorkflowDefinitionInvalid},
		{name: "duplicate node id", root: model.WorkflowNode{ID: workflowRootScope, Type: model.WorkflowNodeSequence, Children: []model.WorkflowNode{
			{ID: workflowTestSame, Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo, Message: workflowExprPointer(workflowTestLiteral("one"))},
			{ID: workflowTestSame, Type: model.WorkflowNodeReturn, Value: workflowExprPointer(workflowTestLiteral("ok"))},
		}}, wantErr: ErrWorkflowDefinitionInvalid},
		{name: "forward reference", root: model.WorkflowNode{ID: workflowRootScope, Type: model.WorkflowNodeSequence, Children: []model.WorkflowNode{
			{ID: "before", Type: model.WorkflowNodeSet, Assignments: map[string]model.WorkflowExpr{workflowPayloadValue: workflowTestRef(model.WorkflowExprRefSteps + ".after")}},
			{ID: "after", Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo, Message: workflowExprPointer(workflowTestLiteral("after"))},
			validReturn,
		}}, wantErr: ErrWorkflowExpressionInvalid},
		{name: "unbounded loop", root: model.WorkflowNode{ID: workflowRootScope, Type: model.WorkflowNodeSequence, Children: []model.WorkflowNode{
			{ID: "loop", Type: model.WorkflowNodeLoop, Condition: workflowExprPointer(workflowTestLiteral(true)), Body: &model.WorkflowNode{ID: workflowTestBody, Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo, Message: workflowExprPointer(workflowTestLiteral(workflowTestBody))}},
			validReturn,
		}}, wantErr: ErrWorkflowDefinitionInvalid},
		{name: "illegal compensation undo", root: model.WorkflowNode{ID: workflowRootScope, Type: model.WorkflowNodeSequence, Children: []model.WorkflowNode{
			{
				ID: "guarded", Type: model.WorkflowNodeCompensate,
				Do: &model.WorkflowNode{ID: "do", Type: model.WorkflowNodeSet, Assignments: map[string]model.WorkflowExpr{workflowTestDone: workflowTestLiteral(true)}},
				Undo: &model.WorkflowNode{
					ID: "undo_loop", Type: model.WorkflowNodeLoop, MaxIterations: 1, Condition: workflowExprPointer(workflowTestLiteral(false)),
					Body: &model.WorkflowNode{ID: "undo_body", Type: model.WorkflowNodeLog, Level: model.WorkflowLogLevelInfo, Message: workflowExprPointer(workflowTestLiteral("undo"))},
				},
			},
			validReturn,
		}}, wantErr: ErrWorkflowDefinitionInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiler := workflowDefinitionCompiler{
				service: &Engine{cfg: StaticConfigProvider(Config{})},
				nodeIDs: make(map[string]struct{}), dependencies: make(map[string]model.WorkflowDependency), maxNodes: 100,
			}
			if err := compiler.compileRoot(&test.root); !errors.Is(err, test.wantErr) {
				t.Fatalf("compileRoot() error = %v", err)
			}
		})
	}
}
