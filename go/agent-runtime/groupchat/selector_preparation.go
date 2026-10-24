package groupchat

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func (runner *Runner) prepareSelectorInput(ctx context.Context, invocation SelectorInvocation) (SelectorInvocation, error) {
	preparer, ok := runner.selector.(SelectorRequestPreparer)
	if !ok {
		return invocation, nil
	}
	prepared, err := runner.prepareSelectorWithPolicy(ctx, preparer, invocation.Request)
	if err != nil {
		return invocation, err
	}
	if !sameSelectorLogicalRequest(invocation.Request, prepared) {
		return invocation, ErrInvalidRequest
	}
	// Normalize once before both the first dispatch and persistence. RawMessage
	// marshaling compacts and escapes JSON; doing it here preserves identical
	// payload bytes when a new process decodes the pending invocation.
	if len(prepared.PreparedInput) != 0 {
		prepared.PreparedInput, err = json.Marshal(prepared.PreparedInput)
		if err != nil {
			return invocation, errors.Join(ErrInvalidRequest, err)
		}
	}
	invocation.Request = cloneSelectorRequest(prepared)
	invocation.RequestHash, err = selectorRequestHash(invocation.Request)
	return invocation, err
}

func sameSelectorLogicalRequest(original, prepared SelectorRequest) bool {
	original.PreparedInput, prepared.PreparedInput = nil, nil
	return reflect.DeepEqual(original, prepared)
}

func (runner *Runner) prepareSelectorWithPolicy(ctx context.Context, preparer SelectorRequestPreparer, request SelectorRequest) (SelectorRequest, error) {
	callCtx, cancel := context.WithTimeout(ctx, runner.selectorExecution.Timeout)
	defer cancel()
	release, err := runner.selectorLimiter.acquire(callCtx)
	if err != nil {
		return SelectorRequest{}, selectorPreparationError(ctx, callCtx, err)
	}
	defer release()
	if err = callCtx.Err(); err != nil {
		return SelectorRequest{}, selectorPreparationError(ctx, callCtx, err)
	}
	prepared, err := safePrepareSelectorRequest(callCtx, preparer, cloneSelectorRequest(request))
	if err == nil {
		err = callCtx.Err()
	}
	if err != nil {
		return SelectorRequest{}, selectorPreparationError(ctx, callCtx, err)
	}
	return prepared, nil
}

func safePrepareSelectorRequest(ctx context.Context, preparer SelectorRequestPreparer, request SelectorRequest) (prepared SelectorRequest, err error) {
	defer func() {
		if recover() != nil {
			prepared, err = SelectorRequest{}, ErrSelectorPanic
		}
	}()
	return preparer.PrepareSelectorRequest(ctx, request)
}

func selectorPreparationError(ctx, callCtx context.Context, err error) error {
	if parentErr := ctx.Err(); parentErr != nil {
		return parentErr
	}
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return retryableSelectorFailure{cause: errors.Join(ErrSelectorTimeout, context.DeadlineExceeded)}
	}
	return err
}

func (runner *Runner) handleSelectorPreparationFailure(ctx context.Context, snapshot kernel.Snapshot, state executionState, cause error) (kernel.Snapshot, error) {
	if parentErr := ctx.Err(); parentErr != nil {
		return snapshot, parentErr
	}
	var retryable interface{ Retryable() bool }
	if errors.As(cause, &retryable) && retryable.Retryable() {
		// Like Agent's pre-intent preparation, this has not spent a physical
		// model attempt. Keep the committed aggregate resumable without an
		// unprepared pending invocation or an uncertain budget reservation.
		return snapshot, errors.Join(ErrSelectorPending, cause)
	}
	return runner.fail(ctx, snapshot, state, "groupchat.selector_failed", errors.Join(ErrSelectorFailure, cause))
}
