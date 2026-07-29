package agentruntime

import (
	"encoding/json"
	"fmt"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func validateWorkflowNodeShape(node model.WorkflowNode) error {
	raw, err := json.Marshal(node)
	if err != nil {
		return ErrWorkflowDefinitionInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrWorkflowDefinitionInvalid
	}
	// encoding/json does not apply omitempty to zero-valued struct fields.
	// Remove the two union references only when they are genuinely unset so
	// valid nodes do not appear to contain fields from another node variant.
	if node.ManifestRef == (model.ResourceRef{}) {
		delete(fields, "manifestRef")
	}
	if node.DefinitionRef == (model.ResourceRef{}) {
		delete(fields, "definitionRef")
	}
	allowed := map[string]map[string]bool{
		model.WorkflowNodeSequence:    workflowAllowedFields("children"),
		model.WorkflowNodeAgent:       workflowAllowedFields("manifestRef", "goal", "toolKeys", "outputSchema", "resultAttempts", "perNodeLimits", "cache"),
		model.WorkflowNodeParallel:    workflowAllowedFields("branches", "failurePolicy"),
		model.WorkflowNodeForEach:     workflowAllowedFields("items", "body", "maxConcurrency", "failurePolicy"),
		model.WorkflowNodePipeline:    workflowAllowedFields("items", "stages", "maxConcurrency", "failurePolicy"),
		model.WorkflowNodeIf:          workflowAllowedFields("condition", "then", "else"),
		model.WorkflowNodeLoop:        workflowAllowedFields("condition", "body", "maxIterations"),
		model.WorkflowNodeSet:         workflowAllowedFields("assignments"),
		model.WorkflowNodeLog:         workflowAllowedFields("level", "message", "data"),
		model.WorkflowNodeTool:        workflowAllowedFields("toolKey", "arguments", "cache"),
		model.WorkflowNodeWorkflow:    workflowAllowedFields("definitionRef", "input", "cache"),
		model.WorkflowNodeInteraction: workflowAllowedFields("title", "prompt", "schema", "expiresAfterSeconds"),
		model.WorkflowNodeTimer:       workflowAllowedFields("delaySeconds", "wakeAt"),
		model.WorkflowNodeCompensate:  workflowAllowedFields("do", "undo"),
		model.WorkflowNodeReturn:      workflowAllowedFields("value", "presentation"),
	}
	selected, ok := allowed[node.Type]
	if !ok {
		return fmt.Errorf("%w: unknown node type %s", ErrWorkflowDefinitionInvalid, node.Type)
	}
	for field := range fields {
		if !selected[field] {
			return fmt.Errorf("%w: field %s is not legal for %s", ErrWorkflowDefinitionInvalid, field, node.Type)
		}
	}
	return validateWorkflowNodeRequired(node)
}

func workflowAllowedFields(values ...string) map[string]bool {
	result := map[string]bool{"id": true, workflowPayloadType: true}
	for _, value := range values {
		result[value] = true
	}
	return result
}

func validateWorkflowNodeRequired(node model.WorkflowNode) error {
	validators := map[string]func(model.WorkflowNode) error{
		model.WorkflowNodeSequence:    validateWorkflowSequenceNode,
		model.WorkflowNodeAgent:       validateWorkflowAgentNode,
		model.WorkflowNodeParallel:    validateWorkflowParallelNode,
		model.WorkflowNodeForEach:     validateWorkflowForEachNode,
		model.WorkflowNodePipeline:    validateWorkflowPipelineNode,
		model.WorkflowNodeIf:          validateWorkflowIfNode,
		model.WorkflowNodeLoop:        validateWorkflowLoopNode,
		model.WorkflowNodeSet:         validateWorkflowSetNode,
		model.WorkflowNodeLog:         validateWorkflowLogNode,
		model.WorkflowNodeTool:        validateWorkflowToolNode,
		model.WorkflowNodeWorkflow:    validateWorkflowNestedNode,
		model.WorkflowNodeInteraction: validateWorkflowInteractionNode,
		model.WorkflowNodeTimer:       validateWorkflowTimerNode,
		model.WorkflowNodeCompensate:  validateWorkflowCompensateNode,
		model.WorkflowNodeReturn:      validateWorkflowReturnNode,
	}
	validator, ok := validators[node.Type]
	if !ok {
		return ErrWorkflowDefinitionInvalid
	}
	return validator(node)
}

func invalidWorkflowNodeIf(invalid bool) error {
	if invalid {
		return ErrWorkflowDefinitionInvalid
	}
	return nil
}

func validWorkflowFailurePolicy(value string) bool {
	return value == model.WorkflowFailureCollect || value == model.WorkflowFailureFailFast
}

func validateWorkflowSequenceNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(len(node.Children) == 0)
}

func validateWorkflowAgentNode(node model.WorkflowNode) error {
	if err := invalidWorkflowNodeIf(node.ManifestRef.Kind != model.AgentManifestKind || node.ManifestRef.ID == "" ||
		node.Goal == nil || len(node.OutputSchema) == 0 || node.ResultAttempts < 0 || node.ResultAttempts > 2); err != nil {
		return err
	}
	_, err := validateWorkflowSchema(node.OutputSchema)
	return err
}

func validateWorkflowParallelNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(len(node.Branches) == 0 || !validWorkflowFailurePolicy(node.FailurePolicy))
}

func validateWorkflowForEachNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.ItemsExpr == nil || node.Body == nil || node.MaxConcurrency <= 0 || !validWorkflowFailurePolicy(node.FailurePolicy))
}

func validateWorkflowPipelineNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.ItemsExpr == nil || len(node.Stages) == 0 || node.MaxConcurrency <= 0 || !validWorkflowFailurePolicy(node.FailurePolicy))
}

func validateWorkflowIfNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.Condition == nil || node.Then == nil)
}

func validateWorkflowLoopNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.Condition == nil || node.Body == nil || node.MaxIterations <= 0)
}

func validateWorkflowSetNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(len(node.Assignments) == 0)
}

func validateWorkflowLogNode(node model.WorkflowNode) error {
	levels := map[string]struct{}{
		model.WorkflowLogLevelDebug: {}, model.WorkflowLogLevelInfo: {},
		model.WorkflowLogLevelWarn: {}, model.WorkflowLogLevelError: {},
	}
	_, validLevel := levels[node.Level]
	return invalidWorkflowNodeIf(node.Message == nil || !validLevel)
}

func validateWorkflowToolNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(strings.TrimSpace(node.ToolKey) == "" || node.Arguments == nil)
}

func validateWorkflowNestedNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.DefinitionRef.Kind != model.WorkflowDefinitionKind || node.DefinitionRef.ID == "" || node.Input == nil)
}

func validateWorkflowInteractionNode(node model.WorkflowNode) error {
	if err := invalidWorkflowNodeIf(strings.TrimSpace(node.Title) == "" || strings.TrimSpace(node.Prompt) == "" ||
		len(node.Schema) == 0 || node.ExpiresAfterSeconds <= 0); err != nil {
		return err
	}
	_, err := validateWorkflowSchema(node.Schema)
	return err
}

func validateWorkflowTimerNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf((node.DelaySeconds == nil) == (node.WakeAt == nil))
}

func validateWorkflowCompensateNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.Do == nil || node.Undo == nil)
}

func validateWorkflowReturnNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.Value == nil)
}

func (c *workflowDefinitionCompiler) compileNodeExpressions(node model.WorkflowNode, scope workflowCompileScope) error {
	expressions := []*model.WorkflowExpr{node.Goal, node.ItemsExpr, node.Condition, node.Arguments, node.Input, node.Message, node.Data, node.DelaySeconds, node.WakeAt, node.Value, node.Presentation}
	for _, expression := range expressions {
		if expression != nil {
			if err := c.validateExpression(*expression, scope, 1, new(int)); err != nil {
				return err
			}
		}
	}
	for _, expression := range node.Assignments {
		if err := c.validateExpression(expression, scope, 1, new(int)); err != nil {
			return err
		}
	}
	return nil
}

func (c *workflowDefinitionCompiler) validateExpression(expr model.WorkflowExpr, scope workflowCompileScope, depth int, operations *int) error {
	ceiling := c.service.workflowCeilings()
	if depth > ceiling.MaxExpressionDepth {
		return ErrWorkflowExpressionLimit
	}
	*operations++
	if *operations > ceiling.MaxExpressionOps {
		return ErrWorkflowExpressionLimit
	}
	if err := validateWorkflowExprShape(expr); err != nil {
		return err
	}
	if expr.Op == model.WorkflowExprOpRef {
		return validateWorkflowReference(expr.Ref, scope)
	}
	for _, child := range workflowExpressionChildren(expr) {
		if err := c.validateExpression(child, scope, depth+1, operations); err != nil {
			return err
		}
	}
	return nil
}

func workflowExpressionChildren(expr model.WorkflowExpr) []model.WorkflowExpr {
	children := make([]model.WorkflowExpr, 0, len(expr.Fields)+len(expr.Items)+len(expr.Args))
	for _, child := range expr.Fields {
		children = append(children, child)
	}
	children = append(children, expr.Items...)
	children = append(children, expr.Args...)
	return children
}

func validateWorkflowExprShape(expr model.WorkflowExpr) error {
	if err := validateWorkflowExpressionFields(expr); err != nil {
		return err
	}
	validators := map[string]workflowExpressionShapeValidator{
		model.WorkflowExprOpLiteral:  validateWorkflowLiteralExpression,
		model.WorkflowExprOpRef:      validateWorkflowReferenceExpression,
		model.WorkflowExprOpObject:   validateWorkflowObjectExpression,
		model.WorkflowExprOpArray:    validateWorkflowArrayExpression,
		model.WorkflowExprOpNot:      validateWorkflowUnaryExpression,
		model.WorkflowExprOpLength:   validateWorkflowUnaryExpression,
		model.WorkflowExprOpEq:       validateWorkflowBinaryExpression,
		model.WorkflowExprOpNe:       validateWorkflowBinaryExpression,
		model.WorkflowExprOpLt:       validateWorkflowBinaryExpression,
		model.WorkflowExprOpLte:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpGt:       validateWorkflowBinaryExpression,
		model.WorkflowExprOpGte:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpAppend:   validateWorkflowBinaryExpression,
		model.WorkflowExprOpContains: validateWorkflowBinaryExpression,
		model.WorkflowExprOpAdd:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpSub:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpMul:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpDiv:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpMod:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpAnd:      validateWorkflowNonEmptyArgsExpression,
		model.WorkflowExprOpOr:       validateWorkflowNonEmptyArgsExpression,
		model.WorkflowExprOpCoalesce: validateWorkflowNonEmptyArgsExpression,
		model.WorkflowExprOpConcat:   validateWorkflowNonEmptyArgsExpression,
		model.WorkflowExprOpMerge:    validateWorkflowMergeExpression,
	}
	validator, ok := validators[expr.Op]
	if !ok {
		return fmt.Errorf("%w: unknown operator %s", ErrWorkflowExpressionInvalid, expr.Op)
	}
	return validator(expr)
}

func validateWorkflowExpressionFields(expr model.WorkflowExpr) error {
	raw, err := json.Marshal(expr)
	if err != nil {
		return ErrWorkflowExpressionInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrWorkflowExpressionInvalid
	}
	allowed := map[string]bool{"op": true, workflowExpressionPayloadField(expr.Op): true}
	for field := range fields {
		if !allowed[field] {
			return fmt.Errorf("%w: field %s is not legal for %s", ErrWorkflowExpressionInvalid, field, expr.Op)
		}
	}
	return nil
}

func workflowExpressionPayloadField(operation string) string {
	switch operation {
	case model.WorkflowExprOpLiteral:
		return workflowPayloadValue
	case model.WorkflowExprOpRef:
		return "ref"
	case model.WorkflowExprOpObject:
		return "fields"
	case model.WorkflowExprOpArray:
		return workflowPayloadItems
	default:
		return "args"
	}
}

func validateWorkflowLiteralExpression(expr model.WorkflowExpr) error {
	if len(expr.Value) == 0 {
		return ErrWorkflowExpressionInvalid
	}
	_, err := decodeWorkflowJSON(expr.Value)
	return err
}

func validateWorkflowReferenceExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(strings.TrimSpace(expr.Ref) == "")
}

func validateWorkflowObjectExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(expr.Fields == nil)
}

func validateWorkflowArrayExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(expr.Items == nil)
}

func validateWorkflowUnaryExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(len(expr.Args) != 1)
}

func validateWorkflowBinaryExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(len(expr.Args) != 2)
}

func validateWorkflowNonEmptyArgsExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(len(expr.Args) == 0)
}

func validateWorkflowMergeExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(len(expr.Args) < 2)
}

func invalidWorkflowExpressionIf(invalid bool) error {
	if invalid {
		return ErrWorkflowExpressionInvalid
	}
	return nil
}

func validateWorkflowReference(value string, scope workflowCompileScope) error {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) == 0 {
		return ErrWorkflowExpressionInvalid
	}
	switch parts[0] {
	case model.WorkflowExprRefInput, model.WorkflowExprRefVars:
		return nil
	case model.WorkflowExprRefSteps:
		return validateWorkflowStepReference(parts, scope)
	case model.WorkflowExprRefItem, model.WorkflowExprRefIndex:
		return requireWorkflowReferenceScope(scope.item)
	case model.WorkflowExprRefError:
		return requireWorkflowReferenceScope(scope.errorContext)
	case model.WorkflowExprRefCompensation:
		return requireWorkflowReferenceScope(scope.compensation)
	default:
		return ErrWorkflowExpressionInvalid
	}
}

func validateWorkflowStepReference(parts []string, scope workflowCompileScope) error {
	if len(parts) < 2 {
		return ErrWorkflowExpressionInvalid
	}
	if _, ok := scope.availableNodes[parts[1]]; !ok {
		return fmt.Errorf("%w: node %s is not upstream", ErrWorkflowExpressionInvalid, parts[1])
	}
	return nil
}

func requireWorkflowReferenceScope(available bool) error {
	if !available {
		return ErrWorkflowExpressionInvalid
	}
	return nil
}
