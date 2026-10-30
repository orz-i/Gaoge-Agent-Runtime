package agent

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"golang.org/x/sync/semaphore"
)

// modelPreDispatchDenied distinguishes a failed ownership/authorization
// preflight from an actual Provider error. No physical send occurred, so it
// must not be charged, retried as a Provider failure, or terminalize the Run.
type modelPreDispatchDenied struct{ cause error }

func (denied modelPreDispatchDenied) Error() string { return denied.cause.Error() }
func (denied modelPreDispatchDenied) Unwrap() error { return denied.cause }

// IsModelPreDispatchDenied proves that Agent's final physical Provider
// boundary rejected a call before invoking model.Client. It is intentionally
// distinct from a similarly named error returned by a Provider: only Agent
// constructs this private evidence type. Trusted budget middleware may use it
// to recover a not-sent dispatch marker with a matching ledger CAS revision.
func IsModelPreDispatchDenied(err error) bool {
	var denied modelPreDispatchDenied
	return errors.As(err, &denied)
}

var (
	ErrModelTimeout = errors.New("agent model invocation timeout")
	ErrToolTimeout  = errors.New("agent tool invocation timeout")
	ErrModelPanic   = errors.New("agent model invocation panic")
	ErrToolPanic    = errors.New("agent tool invocation panic")
)

// CallPolicy bounds one class of external calls. MaxAttempts applies only to
// explicitly retryable failures. Tool retries are opt-in because a timeout does
// not prove that an external side effect did not commit.
type CallPolicy struct {
	Timeout        time.Duration
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	MaxConcurrency int64
}

// ExecutionPolicy applies independent protection to model and Tool boundaries.
type ExecutionPolicy struct {
	Model CallPolicy
	Tool  CallPolicy
}

var defaultExecutionPolicy = ExecutionPolicy{
	Model: CallPolicy{
		Timeout:        90 * time.Second,
		MaxAttempts:    3,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     30 * time.Second,
		MaxConcurrency: 8,
	},
	Tool: CallPolicy{
		Timeout:        30 * time.Second,
		MaxAttempts:    2,
		InitialBackoff: 250 * time.Millisecond,
		MaxBackoff:     5 * time.Second,
		MaxConcurrency: 32,
	},
}

// DefaultExecutionPolicy returns the bounded defaults used when a Runner is
// constructed with zero-valued policy fields.
func DefaultExecutionPolicy() ExecutionPolicy {
	return defaultExecutionPolicy
}

// Resolve fills zero-valued fields from defaults and validates all limits.
func (policy ExecutionPolicy) Resolve() (ExecutionPolicy, error) {
	return normalizeExecutionPolicy(policy)
}

func normalizeExecutionPolicy(policy ExecutionPolicy) (ExecutionPolicy, error) {
	model, err := normalizeCallPolicy(policy.Model, defaultExecutionPolicy.Model)
	if err != nil {
		return ExecutionPolicy{}, err
	}
	tool, err := normalizeCallPolicy(policy.Tool, defaultExecutionPolicy.Tool)
	if err != nil {
		return ExecutionPolicy{}, err
	}
	return ExecutionPolicy{Model: model, Tool: tool}, nil
}

func normalizeCallPolicy(policy CallPolicy, defaults CallPolicy) (CallPolicy, error) {
	if policy.Timeout < 0 || policy.MaxAttempts < 0 || policy.InitialBackoff < 0 ||
		policy.MaxBackoff < 0 || policy.MaxConcurrency < 0 {
		return CallPolicy{}, ErrInvalidRequest
	}
	if policy.Timeout == 0 {
		policy.Timeout = defaults.Timeout
	}
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = defaults.MaxAttempts
	}
	if policy.InitialBackoff == 0 {
		policy.InitialBackoff = defaults.InitialBackoff
	}
	if policy.MaxBackoff == 0 {
		policy.MaxBackoff = defaults.MaxBackoff
	}
	if policy.MaxConcurrency == 0 {
		policy.MaxConcurrency = defaults.MaxConcurrency
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > 32 ||
		policy.MaxConcurrency < 1 || policy.MaxConcurrency > math.MaxInt32 ||
		policy.MaxBackoff < policy.InitialBackoff {
		return CallPolicy{}, ErrInvalidRequest
	}
	return policy, nil
}

type callLimiter struct {
	slots *semaphore.Weighted
}

func newCallLimiter(maxConcurrency int64) *callLimiter {
	return &callLimiter{slots: semaphore.NewWeighted(maxConcurrency)}
}

func (limiter *callLimiter) acquire(ctx context.Context) (func(), error) {
	if limiter == nil || limiter.slots == nil {
		return func() {}, nil
	}
	if err := limiter.slots.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	return func() { limiter.slots.Release(1) }, nil
}

func callContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func (runner *Runner) boundedCallTimeout(deadlineAt *time.Time, timeout time.Duration) (time.Duration, bool) {
	if deadlineAt == nil {
		return timeout, false
	}
	remaining := deadlineAt.UTC().Sub(runner.clock.Now().UTC())
	if remaining <= 0 {
		return 0, true
	}
	if timeout <= 0 || remaining < timeout {
		return remaining, true
	}
	return timeout, false
}

// Acquire a shared call slot under the Run's deadline, not only the caller's
// Context. Per-call Timeout begins after admission; it must not be consumed
// while queued for another Run's physical Model or Tool execution.
func (runner *Runner) acquireCallPermit(ctx context.Context, limiter *callLimiter, deadlineAt *time.Time) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	permitCtx := ctx
	if deadlineAt != nil {
		remaining, _ := runner.boundedCallTimeout(deadlineAt, 0)
		if remaining <= 0 {
			return nil, kernel.ErrDeadline
		}
		var cancel context.CancelFunc
		permitCtx, cancel = context.WithTimeout(ctx, remaining)
		defer cancel()
	}
	release, err := limiter.acquire(permitCtx)
	if err != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return nil, parentErr
		}
		if deadlineAt != nil && errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.Join(kernel.ErrDeadline, err)
		}
		return nil, err
	}
	// The semaphore can grant a slot while cancellation is racing. Never
	// dispatch with a cancelled permission context or leak that new permit.
	if parentErr := ctx.Err(); parentErr != nil {
		release()
		return nil, parentErr
	}
	if deadlineAt != nil {
		if permitErr := permitCtx.Err(); permitErr != nil {
			release()
			if parentErr := ctx.Err(); parentErr != nil {
				return nil, parentErr
			}
			return nil, errors.Join(kernel.ErrDeadline, permitErr)
		}
		remaining, _ := runner.boundedCallTimeout(deadlineAt, 0)
		if remaining <= 0 {
			release()
			return nil, kernel.ErrDeadline
		}
	}
	return release, nil
}

func safeModelCall(call func() (model.Response, error)) (response model.Response, err error) {
	defer func() {
		if recover() != nil {
			response = model.Response{}
			err = ErrModelPanic
		}
	}()
	return call()
}

func safeToolCall(call func() (tools.ExecutionResult, error)) (result tools.ExecutionResult, err error) {
	defer func() {
		if recover() != nil {
			result = tools.ExecutionResult{}
			err = ErrToolPanic
		}
	}()
	return call()
}

func safeHostedToolResolve(
	call func() (model.HostedTool, bool, error),
) (resolved model.HostedTool, ok bool, err error) {
	defer func() {
		if recover() != nil {
			resolved = model.HostedTool{}
			ok = false
			err = ErrToolPanic
		}
	}()
	return call()
}

func retryableBoundaryError(err error) bool {
	var retryable interface{ Retryable() bool }
	return errors.As(err, &retryable) && retryable.Retryable()
}

// retryDelay uses exponential backoff with deterministic +/-20% jitter. Stable
// jitter avoids a thundering herd while keeping recovery tests reproducible.
func retryDelay(policy CallPolicy, attempt int, identity string) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := policy.InitialBackoff
	for current := 1; current < attempt && delay < policy.MaxBackoff; current++ {
		if delay > policy.MaxBackoff/2 {
			delay = policy.MaxBackoff
			break
		}
		delay *= 2
	}
	if delay > policy.MaxBackoff {
		delay = policy.MaxBackoff
	}
	if delay <= 0 {
		return 0
	}
	digest := sha256.Sum256([]byte(identity + "\x00" + strconv.Itoa(attempt)))
	fraction := float64(binary.BigEndian.Uint64(digest[:8])) / float64(math.MaxUint64)
	factor := 0.8 + fraction*0.4
	jittered := time.Duration(float64(delay) * factor)
	if jittered < 0 {
		return delay
	}
	return jittered
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (runner *Runner) modelLeaseDuration() time.Duration {
	const minimum = 2 * time.Minute
	duration := runner.execution.Model.Timeout + 30*time.Second
	if duration < minimum {
		return minimum
	}
	return duration
}

func (runner *Runner) generateModelWithPolicy(
	ctx context.Context,
	request model.Request,
	deadlineAt *time.Time,
) (model.Response, error) {
	return runner.generateModelWithPolicyGuarded(ctx, request, deadlineAt, nil)
}

// A durable Agent request may wait for the model limiter after the Run has
// already validated it. Execute the optional permission check again inside
// the acquired, deadline-bounded call immediately before provider dispatch.
func (runner *Runner) generateModelWithPolicyGuarded(
	ctx context.Context,
	request model.Request,
	deadlineAt *time.Time,
	beforeDispatch func(context.Context) error,
) (model.Response, error) {
	release, err := runner.acquireCallPermit(ctx, runner.modelLimiter, deadlineAt)
	if err != nil {
		return model.Response{}, err
	}
	defer release()

	timeout, runBound := runner.boundedCallTimeout(deadlineAt, runner.execution.Model.Timeout)
	if runBound && timeout <= 0 {
		return model.Response{}, kernel.ErrDeadline
	}
	callCtx, cancel := callContext(ctx, timeout)
	response, callErr := safeModelCall(func() (model.Response, error) {
		// Validation must run after every ModelMiddleware and concurrency
		// wait, at the final physical Provider boundary, not here.
		return runner.generateModel(callCtx, request, beforeDispatch)
	})
	callContextErr := callCtx.Err()
	cancel()

	if model.IsResponseRejectedError(callErr) && response.Usage != nil {
		// A final receipt remains authoritative even if cancellation raced with
		// local validation/persistence. Never replace it with a transport retry.
		return response, callErr
	}
	var denied modelPreDispatchDenied
	if errors.As(callErr, &denied) {
		// No physical request was sent. A cancellation/deadline racing with
		// the final guard cannot turn it into a retryable Provider timeout.
		return model.Response{}, callErr
	}
	if callErr == nil {
		if errors.Is(callContextErr, context.DeadlineExceeded) {
			return response, ErrModelLateResult
		}
		return response, nil
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return model.Response{}, parentErr
	}
	if errors.Is(callContextErr, context.DeadlineExceeded) || errors.Is(callErr, context.DeadlineExceeded) {
		if runBound {
			return model.Response{}, errors.Join(kernel.ErrDeadline, context.DeadlineExceeded)
		}
		return model.Response{}, model.NewRetryableError(errors.Join(ErrModelTimeout, context.DeadlineExceeded))
	}
	return model.Response{}, callErr
}

func (runner *Runner) resolveHostedToolWithPolicy(
	ctx context.Context,
	key string,
	modelName string,
	deadlineAt *time.Time,
) (model.HostedTool, bool, error) {
	policy := runner.execution.Tool
	identity := "hosted-tool:" + key + ":" + modelName
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		release, err := runner.acquireCallPermit(ctx, runner.toolLimiter, deadlineAt)
		if err != nil {
			return model.HostedTool{}, false, err
		}
		timeout, runBound := runner.boundedCallTimeout(deadlineAt, policy.Timeout)
		if runBound && timeout <= 0 {
			release()
			return model.HostedTool{}, false, kernel.ErrDeadline
		}
		callCtx, cancel := callContext(ctx, timeout)
		resolved, ok, callErr := safeHostedToolResolve(func() (model.HostedTool, bool, error) {
			return runner.hostedTools.Resolve(callCtx, key, modelName)
		})
		callContextErr := callCtx.Err()
		cancel()
		release()

		if callErr == nil {
			return resolved, ok, nil
		}
		if parentErr := ctx.Err(); parentErr != nil {
			return model.HostedTool{}, false, parentErr
		}
		if errors.Is(callContextErr, context.DeadlineExceeded) || errors.Is(callErr, context.DeadlineExceeded) {
			if runBound {
				return model.HostedTool{}, false, errors.Join(kernel.ErrDeadline, context.DeadlineExceeded)
			}
			callErr = errors.Join(ErrToolTimeout, context.DeadlineExceeded)
		}
		if attempt >= policy.MaxAttempts ||
			(!errors.Is(callErr, ErrToolTimeout) && !retryableBoundaryError(callErr)) {
			return model.HostedTool{}, false, callErr
		}
		if err = waitRetry(ctx, retryDelay(policy, attempt, identity)); err != nil {
			return model.HostedTool{}, false, err
		}
	}
	return model.HostedTool{}, false, ErrToolTimeout
}

func (runner *Runner) executeToolWithPolicy(
	ctx context.Context,
	identity string,
	deadlineAt *time.Time,
	call func(context.Context) (tools.ExecutionResult, error),
) (tools.ExecutionResult, int, error) {
	policy := runner.execution.Tool
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		release, err := runner.acquireCallPermit(ctx, runner.toolLimiter, deadlineAt)
		if err != nil {
			return tools.ExecutionResult{}, attempt, err
		}
		timeout, runBound := runner.boundedCallTimeout(deadlineAt, policy.Timeout)
		if runBound && timeout <= 0 {
			release()
			return tools.ExecutionResult{}, attempt, kernel.ErrDeadline
		}
		callCtx, cancel := callContext(ctx, timeout)
		result, callErr := safeToolCall(func() (tools.ExecutionResult, error) {
			return call(callCtx)
		})
		callContextErr := callCtx.Err()
		cancel()
		release()

		if callErr == nil {
			// Some SDK Tool clients ignore Context. The physical effect
			// may already have happened: return its result AND late warning
			// for durable receipt, never discard it and retry blindly.
			if errors.Is(callContextErr, context.DeadlineExceeded) {
				return result, attempt, ErrToolLateResult
			}
			return result, attempt, nil
		}
		if parentErr := ctx.Err(); parentErr != nil {
			return tools.ExecutionResult{}, attempt, parentErr
		}
		if errors.Is(callContextErr, context.DeadlineExceeded) || errors.Is(callErr, context.DeadlineExceeded) {
			if runBound {
				return tools.ExecutionResult{}, attempt, errors.Join(kernel.ErrDeadline, context.DeadlineExceeded)
			}
			return tools.ExecutionResult{}, attempt, errors.Join(ErrToolTimeout, context.DeadlineExceeded)
		}
		// A trusted Handler's exact original-ID rejection is the one
		// terminal no-effect receipt that can accompany a correctable error.
		// Do not discard it when crossing the timeout/retry policy boundary.
		// An ordinary error (or a mismatched receipt) remains ambiguous.
		if tools.ValidRecoverableNoEffectReceipt(result, callErr, identity) {
			return result, attempt, callErr
		}
		if !tools.IsRetryableExecutionError(callErr) || attempt >= policy.MaxAttempts {
			return tools.ExecutionResult{}, attempt, callErr
		}
		if err := waitRetry(ctx, retryDelay(policy, attempt, identity)); err != nil {
			return tools.ExecutionResult{}, attempt, err
		}
	}
	return tools.ExecutionResult{}, policy.MaxAttempts, ErrToolFailure
}
