package agentruntime

import (
	"encoding/json"
	"errors"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	workflowTestAda     = "Ada"
	workflowTestEnabled = "enabled"
)

func workflowTestLiteral(value interface{}) model.WorkflowExpr {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return model.WorkflowExpr{Op: model.WorkflowExprOpLiteral, Value: raw}
}

func workflowTestRef(value string) model.WorkflowExpr {
	return model.WorkflowExpr{Op: model.WorkflowExprOpRef, Ref: value}
}

func workflowTestOp(op string, args ...model.WorkflowExpr) model.WorkflowExpr {
	return model.WorkflowExpr{Op: op, Args: args}
}

func TestWorkflowExpressionV1Operators(t *testing.T) {
	index := 1
	ctx := workflowExpressionContext{
		Input: map[string]interface{}{valueName68D33990: workflowTestAda, workflowPayloadItems: []interface{}{json.Number("2"), json.Number("3")}},
		Vars:  map[string]interface{}{workflowTestEnabled: true},
		Steps: map[string]interface{}{
			"prior": map[string]interface{}{workflowPayloadValue: json.Number("5")},
		},
		Item:         map[string]interface{}{"id": "item-1"},
		Index:        &index,
		Error:        map[string]interface{}{workflowPayloadCode: model.WorkflowExecutionFailed},
		Compensation: map[string]interface{}{workflowPayloadCompletionSeq: json.Number("9")},
	}
	engine := &Engine{cfg: StaticConfigProvider(Config{})}
	tests := []struct {
		name string
		expr model.WorkflowExpr
		want interface{}
	}{
		{name: model.WorkflowExprOpLiteral, expr: workflowTestLiteral(workflowPayloadValue), want: workflowPayloadValue},
		{name: "ref input", expr: workflowTestRef(model.WorkflowExprRefInput + "." + valueName68D33990), want: workflowTestAda},
		{name: "ref vars", expr: workflowTestRef(model.WorkflowExprRefVars + "." + workflowTestEnabled), want: true},
		{name: "ref steps", expr: workflowTestRef(model.WorkflowExprRefSteps + ".prior." + workflowPayloadValue), want: json.Number("5")},
		{name: "ref item", expr: workflowTestRef(model.WorkflowExprRefItem + ".id"), want: "item-1"},
		{name: "ref index", expr: workflowTestRef(model.WorkflowExprRefIndex), want: json.Number("1")},
		{name: "ref error", expr: workflowTestRef(model.WorkflowExprRefError + "." + workflowPayloadCode), want: model.WorkflowExecutionFailed},
		{name: "ref compensation", expr: workflowTestRef(model.WorkflowExprRefCompensation + "." + workflowPayloadCompletionSeq), want: json.Number("9")},
		{name: model.WorkflowExprOpObject, expr: model.WorkflowExpr{Op: model.WorkflowExprOpObject, Fields: map[string]model.WorkflowExpr{valueName68D33990: workflowTestRef(model.WorkflowExprRefInput + "." + valueName68D33990)}}, want: map[string]interface{}{valueName68D33990: workflowTestAda}},
		{name: model.WorkflowExprOpArray, expr: model.WorkflowExpr{Op: model.WorkflowExprOpArray, Items: []model.WorkflowExpr{workflowTestLiteral("a"), workflowTestLiteral("b")}}, want: []interface{}{"a", "b"}},
		{name: model.WorkflowExprOpEq, expr: workflowTestOp(model.WorkflowExprOpEq, workflowTestLiteral(2), workflowTestLiteral(2)), want: true},
		{name: model.WorkflowExprOpNe, expr: workflowTestOp(model.WorkflowExprOpNe, workflowTestLiteral(2), workflowTestLiteral(3)), want: true},
		{name: model.WorkflowExprOpLt, expr: workflowTestOp(model.WorkflowExprOpLt, workflowTestLiteral(2), workflowTestLiteral(3)), want: true},
		{name: model.WorkflowExprOpLte, expr: workflowTestOp(model.WorkflowExprOpLte, workflowTestLiteral(3), workflowTestLiteral(3)), want: true},
		{name: model.WorkflowExprOpGt, expr: workflowTestOp(model.WorkflowExprOpGt, workflowTestLiteral(4), workflowTestLiteral(3)), want: true},
		{name: model.WorkflowExprOpGte, expr: workflowTestOp(model.WorkflowExprOpGte, workflowTestLiteral(4), workflowTestLiteral(4)), want: true},
		{name: model.WorkflowExprOpAnd, expr: workflowTestOp(model.WorkflowExprOpAnd, workflowTestLiteral(true), workflowTestLiteral(true)), want: true},
		{name: model.WorkflowExprOpOr, expr: workflowTestOp(model.WorkflowExprOpOr, workflowTestLiteral(false), workflowTestLiteral(true)), want: true},
		{name: model.WorkflowExprOpNot, expr: workflowTestOp(model.WorkflowExprOpNot, workflowTestLiteral(false)), want: true},
		{name: model.WorkflowExprOpCoalesce, expr: workflowTestOp(model.WorkflowExprOpCoalesce, workflowTestLiteral(nil), workflowTestLiteral("fallback")), want: "fallback"},
		{name: model.WorkflowExprOpMerge, expr: workflowTestOp(model.WorkflowExprOpMerge, workflowTestLiteral(map[string]interface{}{"a": 1}), workflowTestLiteral(map[string]interface{}{"b": 2})), want: map[string]interface{}{"a": json.Number("1"), "b": json.Number("2")}},
		{name: model.WorkflowExprOpAppend, expr: workflowTestOp(model.WorkflowExprOpAppend, workflowTestLiteral([]interface{}{"a"}), workflowTestLiteral("b")), want: []interface{}{"a", "b"}},
		{name: model.WorkflowExprOpConcat, expr: workflowTestOp(model.WorkflowExprOpConcat, workflowTestLiteral("a"), workflowTestLiteral("b")), want: "ab"},
		{name: model.WorkflowExprOpLength, expr: workflowTestOp(model.WorkflowExprOpLength, workflowTestLiteral("你好a")), want: json.Number("3")},
		{name: model.WorkflowExprOpContains, expr: workflowTestOp(model.WorkflowExprOpContains, workflowTestLiteral([]interface{}{"a", "b"}), workflowTestLiteral("b")), want: true},
		{name: model.WorkflowExprOpAdd, expr: workflowTestOp(model.WorkflowExprOpAdd, workflowTestLiteral(7), workflowTestLiteral(3)), want: json.Number("10")},
		{name: model.WorkflowExprOpSub, expr: workflowTestOp(model.WorkflowExprOpSub, workflowTestLiteral(7), workflowTestLiteral(3)), want: json.Number("4")},
		{name: model.WorkflowExprOpMul, expr: workflowTestOp(model.WorkflowExprOpMul, workflowTestLiteral(7), workflowTestLiteral(3)), want: json.Number("21")},
		{name: model.WorkflowExprOpDiv, expr: workflowTestOp(model.WorkflowExprOpDiv, workflowTestLiteral(7), workflowTestLiteral(2)), want: json.Number("3.5")},
		{name: model.WorkflowExprOpMod, expr: workflowTestOp(model.WorkflowExprOpMod, workflowTestLiteral(7), workflowTestLiteral(3)), want: json.Number("1")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := engine.evaluateWorkflowExpression(test.expr, ctx)
			if err != nil {
				t.Fatalf("evaluateWorkflowExpression() error = %v", err)
			}
			gotJSON, _ := canonicalWorkflowJSON(got)
			wantJSON, _ := canonicalWorkflowJSON(test.want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("result = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestWorkflowExpressionRejectsImplicitConversionAndLimits(t *testing.T) {
	engine := &Engine{cfg: StaticConfigProvider(Config{
		Workflow: WorkflowConfig{MaxExpressionDepth: 2, MaxExpressionOps: 4, MaxExpressionBytes: 32},
	})}
	if _, err := engine.evaluateWorkflowExpression(
		workflowTestOp(model.WorkflowExprOpAdd, workflowTestLiteral("1"), workflowTestLiteral(1)),
		workflowExpressionContext{},
	); !errors.Is(err, ErrWorkflowExpressionInvalid) {
		t.Fatalf("implicit conversion error = %v", err)
	}
	deep := workflowTestOp(model.WorkflowExprOpNot, workflowTestOp(model.WorkflowExprOpNot, workflowTestLiteral(true)))
	if _, err := engine.evaluateWorkflowExpression(deep, workflowExpressionContext{}); !errors.Is(err, ErrWorkflowExpressionLimit) {
		t.Fatalf("depth limit error = %v", err)
	}
}

func TestWorkflowSchemaRejectsRemoteReferences(t *testing.T) {
	if _, err := validateWorkflowSchema(json.RawMessage(`{"type":"object","properties":{"value":{"$ref":"https://example.com/schema.json"}}}`)); !errors.Is(err, ErrWorkflowSchemaInvalid) {
		t.Fatalf("remote schema ref error = %v", err)
	}
	if _, err := validateWorkflowSchema(json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false}`)); err != nil {
		t.Fatalf("self-contained schema rejected: %v", err)
	}
}
