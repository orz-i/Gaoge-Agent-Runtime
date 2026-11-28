package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// toolCallAuthority freezes the actual returned result for this attempt.
// Terminal Tool success/refusal is never qualified by a middleware error
// classification alone. A first-party accounting failure poisons this
// attempt before the outer layer or the Agent can consume the result.
type toolCallAuthority struct {
	mu                sync.Mutex
	id                string
	observed, invalid bool
	original          toolOutcomeReceipt
	cause             error
}

func toolAuthority(ctx context.Context) *toolCallAuthority {
	a, _ := callbackscope.ToolCall(ctx).(*toolCallAuthority)
	return a
}
func toolReceiptCandidate(result tools.ExecutionResult, err error, id string) bool {
	return result.Receipt.ExecutionID == id &&
		((err == nil && tools.ValidateExecutionResult(result) == nil) ||
			tools.ValidRecoverableNoEffectReceipt(result, err, id) ||
			tools.ValidRetryableNoEffectReceipt(result, err, id))
}

// qualifiedPendingKind is the original Call return classification shared by
// accounting and approval. No descriptor is a valid pending return too.
func qualifiedPendingKind(result tools.ExecutionResult, err error, id string) string {
	if err != nil || result.Receipt.ExecutionID != id || result.Receipt.Disposition != tools.ReceiptDispositionPending ||
		tools.ValidateExecutionResult(result) != nil {
		return ""
	}
	if result.Pending == nil {
		return "pending"
	}
	return string(result.Pending.Kind)
}

// Only a single chain of wrappers may carry the actual original error.
// errors.As alone would also accept an unrelated persistence failure joined
// with the original refusal; a multi-cause wrapper is not the same result.
func sameOriginalToolError(original, returned error) bool {
	if original == nil || returned == nil {
		return original == nil && returned == nil
	}
	for returned != nil {
		if reflect.TypeOf(returned) == reflect.TypeOf(original) && reflect.ValueOf(returned).Comparable() && reflect.ValueOf(original).Comparable() && returned == original { //nolint:errorlint // exact original error identity, not errors.Is equivalence.
			return true
		}
		if joined, ok := returned.(interface{ Unwrap() []error }); ok {
			causes := joined.Unwrap()
			if len(causes) != 1 {
				return false
			}
			returned = causes[0]
		} else {
			returned = errors.Unwrap(returned)
		}
	}
	return false
}

func unqualifiedToolOutcome(err error) (tools.ExecutionResult, error) {
	return tools.ExecutionResult{}, errors.Join(ErrToolOutcomeUnknown, err)
}

func (a *toolCallAuthority) reject(err error) (tools.ExecutionResult, error) {
	if a != nil {
		a.mu.Lock()
		a.invalid = true
		a.mu.Unlock()
	}
	return unqualifiedToolOutcome(err)
}

func (a *toolCallAuthority) record(result tools.ExecutionResult, err error) (tools.ExecutionResult, error) {
	if a == nil {
		return result, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.invalid || a.observed {
		a.invalid = true
		return unqualifiedToolOutcome(errors.Join(ErrToolReceiptMismatch, err))
	}
	a.observed = true
	a.original = newToolOutcomeReceipt(result, err)
	a.cause = err
	return result, err
}

func (a *toolCallAuthority) validate(result tools.ExecutionResult, err error) (tools.ExecutionResult, error) {
	if a == nil {
		return result, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.invalid {
		return unqualifiedToolOutcome(err)
	}

	if !toolReceiptCandidate(result, err, a.id) {
		return result, err
	}
	actual := newToolOutcomeReceipt(result, err)
	if !a.observed || !reflect.DeepEqual(a.original, actual) || !sameOriginalToolError(a.cause, err) {
		a.invalid = true
		return unqualifiedToolOutcome(errors.Join(ErrToolReceiptMismatch, err))
	}
	return result, err
}
