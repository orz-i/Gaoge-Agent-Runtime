package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type workflowExpressionContext struct {
	Input        interface{}
	Vars         map[string]interface{}
	Steps        map[string]interface{}
	Item         interface{}
	Index        *int
	Error        interface{}
	Compensation interface{}
}

type workflowExpressionEvaluator struct {
	maxDepth int
	maxOps   int
	maxBytes int
	ops      int
}

func (s *Engine) evaluateWorkflowExpression(expression model.WorkflowExpr, ctx workflowExpressionContext) (interface{}, error) {
	limits := s.workflowCeilings()
	evaluator := workflowExpressionEvaluator{maxDepth: limits.MaxExpressionDepth, maxOps: limits.MaxExpressionOps, maxBytes: limits.MaxExpressionBytes}
	value, err := evaluator.evaluate(expression, ctx, 1)
	if err != nil {
		return nil, err
	}
	encoded, err := canonicalWorkflowJSON(value)
	if err != nil {
		return nil, errors.Join(ErrWorkflowExpressionInvalid, err)
	}
	if len(encoded) > evaluator.maxBytes {
		return nil, ErrWorkflowExpressionLimit
	}
	return value, nil
}

func (e *workflowExpressionEvaluator) evaluate(expression model.WorkflowExpr, ctx workflowExpressionContext, depth int) (interface{}, error) {
	if err := e.consume(expression, depth); err != nil {
		return nil, err
	}
	switch expression.Op {
	case model.WorkflowExprOpLiteral:
		return decodeWorkflowJSON(expression.Value)
	case model.WorkflowExprOpRef:
		return evaluateWorkflowReference(expression.Ref, ctx)
	case model.WorkflowExprOpObject:
		return e.evaluateObject(expression.Fields, ctx, depth)
	case model.WorkflowExprOpArray:
		return e.evaluateArray(expression.Items, ctx, depth)
	}
	args, err := e.evaluateArray(expression.Args, ctx, depth)
	if err != nil {
		return nil, err
	}
	return evaluateWorkflowOperation(expression.Op, args)
}

func (e *workflowExpressionEvaluator) consume(expression model.WorkflowExpr, depth int) error {
	if depth > e.maxDepth {
		return ErrWorkflowExpressionLimit
	}
	e.ops++
	if e.ops > e.maxOps {
		return ErrWorkflowExpressionLimit
	}
	return validateWorkflowExprShape(expression)
}

func (e *workflowExpressionEvaluator) evaluateObject(fields map[string]model.WorkflowExpr, ctx workflowExpressionContext, depth int) (map[string]interface{}, error) {
	result := make(map[string]interface{}, len(fields))
	for key, child := range fields {
		value, err := e.evaluate(child, ctx, depth+1)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, nil
}

func (e *workflowExpressionEvaluator) evaluateArray(expressions []model.WorkflowExpr, ctx workflowExpressionContext, depth int) ([]interface{}, error) {
	result := make([]interface{}, 0, len(expressions))
	for _, child := range expressions {
		value, err := e.evaluate(child, ctx, depth+1)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func evaluateWorkflowReference(reference string, ctx workflowExpressionContext) (interface{}, error) {
	parts := strings.Split(strings.TrimSpace(reference), ".")
	if len(parts) == 0 || parts[0] == "" {
		return nil, ErrWorkflowExpressionInvalid
	}
	value, err := workflowReferenceRoot(parts[0], ctx)
	if err != nil {
		return nil, err
	}
	for _, part := range parts[1:] {
		value, err = workflowReferenceChild(value, part)
		if err != nil {
			return nil, err
		}
	}
	return cloneWorkflowValue(value)
}

func workflowReferenceRoot(root string, ctx workflowExpressionContext) (interface{}, error) {
	switch root {
	case model.WorkflowExprRefInput:
		return ctx.Input, nil
	case model.WorkflowExprRefVars:
		return ctx.Vars, nil
	case model.WorkflowExprRefSteps:
		return ctx.Steps, nil
	case model.WorkflowExprRefItem:
		return ctx.Item, nil
	case model.WorkflowExprRefIndex:
		return workflowRequiredReference(ctx.Index != nil, json.Number(strconv.Itoa(workflowIndexValue(ctx.Index))))
	case model.WorkflowExprRefError:
		return workflowRequiredReference(ctx.Error != nil, ctx.Error)
	case model.WorkflowExprRefCompensation:
		return workflowRequiredReference(ctx.Compensation != nil, ctx.Compensation)
	default:
		return nil, ErrWorkflowExpressionInvalid
	}
}

func workflowIndexValue(index *int) int {
	if index == nil {
		return 0
	}
	return *index
}

func workflowRequiredReference(available bool, value interface{}) (interface{}, error) {
	if !available {
		return nil, ErrWorkflowExpressionInvalid
	}
	return value, nil
}

func workflowReferenceChild(value interface{}, part string) (interface{}, error) {
	switch typed := value.(type) {
	case map[string]interface{}:
		child, ok := typed[part]
		if !ok {
			return nil, fmt.Errorf("%w: missing reference path %s", ErrWorkflowExpressionInvalid, part)
		}
		return child, nil
	case []interface{}:
		index, err := strconv.Atoi(part)
		if err != nil || index < 0 || index >= len(typed) {
			return nil, fmt.Errorf("%w: invalid array path %s", ErrWorkflowExpressionInvalid, part)
		}
		return typed[index], nil
	default:
		return nil, fmt.Errorf("%w: cannot traverse %s", ErrWorkflowExpressionInvalid, workflowJSONType(value))
	}
}

type workflowOperationHandler func(string, []interface{}) (interface{}, error)

var workflowOperationHandlers = map[string]workflowOperationHandler{
	model.WorkflowExprOpEq:       workflowEqualityOperation,
	model.WorkflowExprOpNe:       workflowEqualityOperation,
	model.WorkflowExprOpLt:       workflowComparisonOperation,
	model.WorkflowExprOpLte:      workflowComparisonOperation,
	model.WorkflowExprOpGt:       workflowComparisonOperation,
	model.WorkflowExprOpGte:      workflowComparisonOperation,
	model.WorkflowExprOpAnd:      workflowBooleanOperation,
	model.WorkflowExprOpOr:       workflowBooleanOperation,
	model.WorkflowExprOpNot:      workflowNotOperation,
	model.WorkflowExprOpCoalesce: workflowCoalesceOperation,
	model.WorkflowExprOpMerge:    workflowMergeOperation,
	model.WorkflowExprOpAppend:   workflowAppendOperation,
	model.WorkflowExprOpConcat:   workflowConcatOperation,
	model.WorkflowExprOpLength:   workflowLengthOperation,
	model.WorkflowExprOpContains: workflowContainsOperation,
	model.WorkflowExprOpAdd:      workflowArithmeticOperation,
	model.WorkflowExprOpSub:      workflowArithmeticOperation,
	model.WorkflowExprOpMul:      workflowArithmeticOperation,
	model.WorkflowExprOpDiv:      workflowArithmeticOperation,
	model.WorkflowExprOpMod:      workflowArithmeticOperation,
}

func evaluateWorkflowOperation(operation string, args []interface{}) (interface{}, error) {
	handler, ok := workflowOperationHandlers[operation]
	if !ok {
		return nil, ErrWorkflowExpressionInvalid
	}
	return handler(operation, args)
}

func workflowEqualityOperation(operation string, args []interface{}) (interface{}, error) {
	equal, err := workflowStrictEqual(args[0], args[1])
	if operation == model.WorkflowExprOpNe {
		equal = !equal
	}
	return equal, err
}

func workflowComparisonOperation(operation string, args []interface{}) (interface{}, error) {
	return workflowCompare(operation, args[0], args[1])
}

func workflowBooleanOperation(operation string, args []interface{}) (interface{}, error) {
	return workflowBooleanFold(operation, args)
}

func workflowNotOperation(operation string, args []interface{}) (interface{}, error) {
	value, ok := args[0].(bool)
	if !ok {
		return nil, workflowTypeError(operation, "boolean", args[0])
	}
	return !value, nil
}

func workflowCoalesceOperation(_ string, args []interface{}) (interface{}, error) {
	for _, value := range args {
		if value != nil {
			return value, nil
		}
	}
	var result interface{}
	return result, nil
}

func workflowMergeOperation(operation string, args []interface{}) (interface{}, error) {
	result := make(map[string]interface{})
	for _, value := range args {
		object, ok := value.(map[string]interface{})
		if !ok {
			return nil, workflowTypeError(operation, "object", value)
		}
		for key, child := range object {
			result[key] = child
		}
	}
	return result, nil
}

func workflowAppendOperation(operation string, args []interface{}) (interface{}, error) {
	array, ok := args[0].([]interface{})
	if !ok {
		return nil, workflowTypeError(operation, "array", args[0])
	}
	result := append([]interface{}(nil), array...)
	return append(result, args[1]), nil
}

func workflowConcatOperation(operation string, args []interface{}) (interface{}, error) {
	var builder strings.Builder
	for _, value := range args {
		text, ok := value.(string)
		if !ok {
			return nil, workflowTypeError(operation, "string", value)
		}
		builder.WriteString(text)
	}
	return builder.String(), nil
}

func workflowLengthOperation(operation string, args []interface{}) (interface{}, error) {
	switch value := args[0].(type) {
	case string:
		return json.Number(strconv.Itoa(utf8.RuneCountInString(value))), nil
	case []interface{}:
		return json.Number(strconv.Itoa(len(value))), nil
	case map[string]interface{}:
		return json.Number(strconv.Itoa(len(value))), nil
	default:
		return nil, workflowTypeError(operation, "string, array, or object", args[0])
	}
}

func workflowContainsOperation(_ string, args []interface{}) (interface{}, error) {
	return workflowContains(args[0], args[1])
}

func workflowArithmeticOperation(operation string, args []interface{}) (interface{}, error) {
	return workflowArithmetic(operation, args[0], args[1])
}

func workflowStrictEqual(left, right interface{}) (bool, error) {
	if workflowJSONType(left) != workflowJSONType(right) {
		return false, fmt.Errorf("%w: cannot compare %s and %s", ErrWorkflowExpressionInvalid, workflowJSONType(left), workflowJSONType(right))
	}
	if leftNumber, ok := left.(json.Number); ok {
		rightNumber, ok := right.(json.Number)
		if !ok {
			return false, ErrWorkflowExpressionInvalid
		}
		leftValue, leftErr := strconv.ParseFloat(leftNumber.String(), 64)
		rightValue, rightErr := strconv.ParseFloat(rightNumber.String(), 64)
		if leftErr != nil || rightErr != nil {
			return false, ErrWorkflowExpressionInvalid
		}
		return leftValue == rightValue, nil
	}
	return reflect.DeepEqual(left, right), nil
}

func workflowCompare(operation string, left, right interface{}) (bool, error) {
	if workflowJSONType(left) != workflowJSONType(right) {
		return false, fmt.Errorf("%w: comparison types differ", ErrWorkflowExpressionInvalid)
	}
	comparison, err := workflowComparableValues(left, right, operation)
	if err != nil {
		return false, err
	}
	return workflowComparisonResult(operation, comparison)
}

func workflowComparableValues(left, right interface{}, operation string) (int, error) {
	switch leftValue := left.(type) {
	case json.Number:
		return workflowCompareNumbers(leftValue, right)
	case string:
		rightValue, ok := right.(string)
		if !ok {
			return 0, ErrWorkflowExpressionInvalid
		}
		return strings.Compare(leftValue, rightValue), nil
	default:
		return 0, workflowTypeError(operation, "number or string", left)
	}
}

func workflowCompareNumbers(left json.Number, right interface{}) (int, error) {
	rightNumber, ok := right.(json.Number)
	if !ok {
		return 0, ErrWorkflowExpressionInvalid
	}
	leftValue, leftErr := strconv.ParseFloat(left.String(), 64)
	rightValue, rightErr := strconv.ParseFloat(rightNumber.String(), 64)
	if leftErr != nil || rightErr != nil {
		return 0, ErrWorkflowExpressionInvalid
	}
	return workflowNumericComparison(leftValue, rightValue), nil
}

func workflowNumericComparison(left, right float64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func workflowComparisonResult(operation string, comparison int) (bool, error) {
	switch operation {
	case model.WorkflowExprOpLt:
		return comparison < 0, nil
	case model.WorkflowExprOpLte:
		return comparison <= 0, nil
	case model.WorkflowExprOpGt:
		return comparison > 0, nil
	case model.WorkflowExprOpGte:
		return comparison >= 0, nil
	default:
		return false, ErrWorkflowExpressionInvalid
	}
}

func workflowBooleanFold(operation string, args []interface{}) (bool, error) {
	result := operation == model.WorkflowExprOpAnd
	for _, value := range args {
		boolean, ok := value.(bool)
		if !ok {
			return false, workflowTypeError(operation, "boolean", value)
		}
		if operation == model.WorkflowExprOpAnd {
			result = result && boolean
		} else {
			result = result || boolean
		}
	}
	return result, nil
}

func workflowContains(container, value interface{}) (bool, error) {
	switch typed := container.(type) {
	case string:
		needle, ok := value.(string)
		if !ok {
			return false, workflowTypeError(model.WorkflowExprOpContains, "string", value)
		}
		return strings.Contains(typed, needle), nil
	case []interface{}:
		for _, item := range typed {
			equal, err := workflowStrictEqual(item, value)
			if err == nil && equal {
				return true, nil
			}
		}
		return false, nil
	case map[string]interface{}:
		key, ok := value.(string)
		if !ok {
			return false, workflowTypeError(model.WorkflowExprOpContains, "string object key", value)
		}
		_, found := typed[key]
		return found, nil
	default:
		return false, workflowTypeError(model.WorkflowExprOpContains, "string, array, or object", container)
	}
}

func workflowArithmetic(operation string, left, right interface{}) (interface{}, error) {
	leftNumber, rightNumber, err := workflowNumberOperands(operation, left, right)
	if err != nil {
		return nil, err
	}
	if operation == model.WorkflowExprOpMod {
		return workflowModulo(leftNumber, rightNumber)
	}
	leftValue, rightValue, err := workflowFloatOperands(leftNumber, rightNumber)
	if err != nil {
		return nil, err
	}
	if operation == model.WorkflowExprOpDiv && rightValue == 0 {
		return nil, ErrWorkflowExpressionInvalid
	}
	result, err := workflowArithmeticResult(operation, leftValue, rightValue)
	if err != nil {
		return nil, err
	}
	if !workflowFiniteNumber(result) {
		return nil, ErrWorkflowExpressionInvalid
	}
	return json.Number(strconv.FormatFloat(result, 'f', -1, 64)), nil
}

func workflowNumberOperands(operation string, left, right interface{}) (json.Number, json.Number, error) {
	leftNumber, leftOK := left.(json.Number)
	rightNumber, rightOK := right.(json.Number)
	if !leftOK || !rightOK {
		return "", "", workflowTypeError(operation, "number", firstWorkflowTypeMismatch(left, right))
	}
	return leftNumber, rightNumber, nil
}

func workflowFloatOperands(left, right json.Number) (float64, float64, error) {
	leftValue, leftErr := strconv.ParseFloat(left.String(), 64)
	rightValue, rightErr := strconv.ParseFloat(right.String(), 64)
	if leftErr != nil || rightErr != nil {
		return 0, 0, ErrWorkflowExpressionInvalid
	}
	return leftValue, rightValue, nil
}

func workflowFiniteNumber(value float64) bool {
	return !math.IsInf(value, 0) && !math.IsNaN(value)
}

func workflowModulo(left, right json.Number) (interface{}, error) {
	leftInteger, leftErr := strconv.ParseInt(left.String(), 10, 64)
	rightInteger, rightErr := strconv.ParseInt(right.String(), 10, 64)
	if leftErr != nil || rightErr != nil || rightInteger == 0 {
		return nil, ErrWorkflowExpressionInvalid
	}
	return json.Number(strconv.FormatInt(leftInteger%rightInteger, 10)), nil
}

func workflowArithmeticResult(operation string, left, right float64) (float64, error) {
	switch operation {
	case model.WorkflowExprOpAdd:
		return left + right, nil
	case model.WorkflowExprOpSub:
		return left - right, nil
	case model.WorkflowExprOpMul:
		return left * right, nil
	case model.WorkflowExprOpDiv:
		return left / right, nil
	default:
		return 0, ErrWorkflowExpressionInvalid
	}
}

func firstWorkflowTypeMismatch(left, right interface{}) interface{} {
	if _, ok := left.(json.Number); !ok {
		return left
	}
	return right
}

func workflowTypeError(operation, expected string, actual interface{}) error {
	return fmt.Errorf("%w: %s expects %s, got %s", ErrWorkflowExpressionInvalid, operation, expected, workflowJSONType(actual))
}

func workflowJSONType(value interface{}) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []interface{}:
		return model.WorkflowExprOpArray
	case map[string]interface{}:
		return model.WorkflowExprOpObject
	default:
		return fmt.Sprintf("%T", value)
	}
}

func cloneWorkflowValue(value interface{}) (interface{}, error) {
	raw, err := canonicalWorkflowJSON(value)
	if err != nil {
		return nil, err
	}
	return decodeWorkflowJSON(raw)
}
