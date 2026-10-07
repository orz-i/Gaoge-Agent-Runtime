package groupchat

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"time"

	"golang.org/x/sync/semaphore"
)

// SelectorExecutionPolicy bounds model-like selector calls. Retry attempts are
// durable because the same SelectorInvocation ID is retained across wakeups.
type SelectorExecutionPolicy struct {
	Timeout        time.Duration
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	MaxConcurrency int64
}

var defaultSelectorExecutionPolicy = SelectorExecutionPolicy{
	Timeout:        60 * time.Second,
	MaxAttempts:    3,
	InitialBackoff: 500 * time.Millisecond,
	MaxBackoff:     30 * time.Second,
	MaxConcurrency: 8,
}

func DefaultSelectorExecutionPolicy() SelectorExecutionPolicy {
	return defaultSelectorExecutionPolicy
}

func (policy SelectorExecutionPolicy) Resolve() (SelectorExecutionPolicy, error) {
	if policy.Timeout < 0 || policy.MaxAttempts < 0 || policy.InitialBackoff < 0 ||
		policy.MaxBackoff < 0 || policy.MaxConcurrency < 0 {
		return SelectorExecutionPolicy{}, ErrInvalidRequest
	}
	if policy.Timeout == 0 {
		policy.Timeout = defaultSelectorExecutionPolicy.Timeout
	}
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = defaultSelectorExecutionPolicy.MaxAttempts
	}
	if policy.InitialBackoff == 0 {
		policy.InitialBackoff = defaultSelectorExecutionPolicy.InitialBackoff
	}
	if policy.MaxBackoff == 0 {
		policy.MaxBackoff = defaultSelectorExecutionPolicy.MaxBackoff
	}
	if policy.MaxConcurrency == 0 {
		policy.MaxConcurrency = defaultSelectorExecutionPolicy.MaxConcurrency
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > 32 ||
		policy.MaxConcurrency < 1 || policy.MaxConcurrency > math.MaxInt32 ||
		policy.MaxBackoff < policy.InitialBackoff {
		return SelectorExecutionPolicy{}, ErrInvalidRequest
	}
	return policy, nil
}

type selectorCallLimiter struct {
	slots *semaphore.Weighted
}

func newSelectorCallLimiter(maxConcurrency int64) *selectorCallLimiter {
	return &selectorCallLimiter{slots: semaphore.NewWeighted(maxConcurrency)}
}

func (limiter *selectorCallLimiter) acquire(ctx context.Context) (func(), error) {
	if err := limiter.slots.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	return func() { limiter.slots.Release(1) }, nil
}

type retryableSelectorFailure struct {
	cause error
}

func (failure retryableSelectorFailure) Error() string {
	if failure.cause == nil {
		return "retryable selector failure"
	}
	return failure.cause.Error()
}

func (failure retryableSelectorFailure) Unwrap() error { return failure.cause }
func (retryableSelectorFailure) Retryable() bool       { return true }

func safeSelectorCall(call func() (SelectorResponse, error)) (response SelectorResponse, err error) {
	defer func() {
		if recover() != nil {
			response = SelectorResponse{}
			err = ErrSelectorPanic
		}
	}()
	return call()
}

func (runner *Runner) selectorLeaseDuration() time.Duration {
	const minimum = 2 * time.Minute
	duration := runner.selectorExecution.Timeout + 30*time.Second
	if duration < minimum {
		return minimum
	}
	return duration
}

func (runner *Runner) selectWithPolicy(
	ctx context.Context,
	request SelectorRequest,
) (SelectorResponse, error) {
	release, err := runner.selectorLimiter.acquire(ctx)
	if err != nil {
		return SelectorResponse{}, err
	}
	defer release()

	callCtx, cancel := context.WithTimeout(ctx, runner.selectorExecution.Timeout)
	response, callErr := safeSelectorCall(func() (SelectorResponse, error) {
		return runner.selector.Select(callCtx, request)
	})
	callContextErr := callCtx.Err()
	cancel()

	if callErr == nil {
		return response, nil
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return SelectorResponse{}, parentErr
	}
	if errors.Is(callContextErr, context.DeadlineExceeded) || errors.Is(callErr, context.DeadlineExceeded) {
		return SelectorResponse{}, retryableSelectorFailure{
			cause: errors.Join(ErrSelectorTimeout, context.DeadlineExceeded),
		}
	}
	return SelectorResponse{}, callErr
}

func selectorRetryDelay(policy SelectorExecutionPolicy, attempt int, identity string) time.Duration {
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
	digest := sha256.Sum256([]byte(identity + "\x00" + strconv.Itoa(attempt)))
	fraction := float64(binary.BigEndian.Uint64(digest[:8])) / float64(math.MaxUint64)
	return time.Duration(float64(delay) * (0.8 + fraction*0.4))
}
