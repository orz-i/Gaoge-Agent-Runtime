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

// qualifyToolReturn classifies a returned original Call once. An omitted
// descriptor is a scheduled/opaque pending return; a retryable no-effect
// receipt can be observed but is not a consumable terminal business outcome.
func qualifyToolReturn(result tools.ExecutionResult, err error, id string) (terminal bool, pending string) {
	if result.Receipt.ExecutionID != id {
		return false, ""
	}
	if err != nil {
		return tools.ValidRecoverableNoEffectReceipt(result, err, id), ""
	}
	if tools.ValidateExecutionResult(result) != nil {
		return false, ""
	}
	if result.Receipt.Disposition != tools.ReceiptDispositionPending {
		return true, ""
	}
	if result.Pending == nil {
		return false, "pending"
	}
	return false, string(result.Pending.Kind)
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

	terminal, pending := qualifyToolReturn(result, err, a.id)
	if !terminal && pending == "" && !tools.ValidRetryableNoEffectReceipt(result, err, a.id) {
		return result, err
	}
	actual := newToolOutcomeReceipt(result, err)
	if !a.observed || !reflect.DeepEqual(a.original, actual) || !sameOriginalToolError(a.cause, err) {
		a.invalid = true
		return unqualifiedToolOutcome(errors.Join(ErrToolReceiptMismatch, err))
	}
	return result, err
}
