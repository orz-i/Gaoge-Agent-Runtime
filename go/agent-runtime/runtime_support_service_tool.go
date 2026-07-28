// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

// ExecuteToolInput 定义工具执行入参。
type ExecuteToolInput struct {
	Actor           domain.ActorRef
	Thread          domain.ThreadRef
	RequestID       string
	ToolKey         string
	ProviderKind    string
	ProviderKey     string
	ToolName        string
	ArgumentsJSON   string
	ExecutionLimits *TextRunExecutionLimits
	OnAttemptFailed func(attempt, maxAttempts int, err error) error
}

func callReceiptToolWithRetry(ctx context.Context, executor ReceiptToolExecutor, input ToolExecutionInput, retryCount int, onAttemptFailed func(int, int, error) error) (ToolExecutionResult, error) {
	if retryCount < 0 {
		retryCount = 0
	}
	var lastErr error
	var lastResult ToolExecutionResult
	for attempt := 0; attempt <= retryCount; attempt++ {
		result, err := executor.ExecuteWithReceipt(ctx, input)
		result.Attempts = attempt + 1
		lastResult = result
		if err == nil {
			return result, nil
		}
		lastErr = err
		if onAttemptFailed != nil {
			if observeErr := onAttemptFailed(attempt+1, retryCount+1, err); observeErr != nil {
				return lastResult, observeErr
			}
		}
		if attempt >= retryCount {
			break
		}
		timer := time.NewTimer(time.Duration(100*(attempt+1)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return lastResult, ctx.Err()
		case <-timer.C:
		}
	}
	return lastResult, lastErr
}

func (s *Engine) executeReceiptWithToolLimiter(ctx context.Context, limit int, fn func() (ToolExecutionResult, error)) (ToolExecutionResult, error) {
	if fn == nil {
		return ToolExecutionResult{}, errCategoryD364A2A615
	}
	if limit <= 0 {
		return fn()
	}
	limiter := s.getToolLimiter(limit)
	select {
	case limiter <- struct{}{}:
		defer func() { <-limiter }()
		return fn()
	case <-ctx.Done():
		return ToolExecutionResult{}, ctx.Err()
	}
}

func (s *Engine) executeToolCallWithReceipt(ctx context.Context, input ExecuteToolInput) (ToolExecutionResult, error) {
	toolName := strings.TrimSpace(input.ToolName)
	if toolName == "" {
		return ToolExecutionResult{}, errCategory3A5F699D5F
	}
	if strings.TrimSpace(input.ToolKey) == "" || strings.TrimSpace(input.ProviderKey) == "" {
		return ToolExecutionResult{}, withErrorMessage(errCategory0B02F88F59, fmt.Sprintf("tool %s is not enabled for this run", toolName))
	}
	executor, ok := s.toolExecutor.(ReceiptToolExecutor)
	if !ok {
		return ToolExecutionResult{}, ErrRunToolProviderReceiptRequired
	}
	cfg := s.cfg.Snapshot()
	retryCount := cfg.Tools.RetryCount
	limit := cfg.Tools.MaxConcurrentCalls
	if input.ExecutionLimits != nil {
		retryCount = input.ExecutionLimits.ToolRetryCount
		limit = input.ExecutionLimits.ToolConcurrency
	}
	if limit <= 0 {
		limit = 8
	}
	return s.executeReceiptWithToolLimiter(ctx, limit, func() (ToolExecutionResult, error) {
		return callReceiptToolWithRetry(ctx, executor, ToolExecutionInput{
			ToolKey:       strings.TrimSpace(input.ToolKey),
			ProviderKind:  strings.TrimSpace(input.ProviderKind),
			ProviderKey:   strings.TrimSpace(input.ProviderKey),
			ToolName:      toolName,
			ArgumentsJSON: strings.TrimSpace(input.ArgumentsJSON),
			Actor:         input.Actor,
			Thread:        input.Thread,
			RequestID:     strings.TrimSpace(input.RequestID),
		}, retryCount, input.OnAttemptFailed)
	})
}

func (s *Engine) executeToolCall(ctx context.Context, input ExecuteToolInput) (ToolExecutionResult, error) {
	toolName := strings.TrimSpace(input.ToolName)
	if toolName == "" {
		return ToolExecutionResult{}, errCategory3A5F699D5F
	}
	if strings.TrimSpace(input.ToolKey) == "" || strings.TrimSpace(input.ProviderKey) == "" {
		return ToolExecutionResult{}, withErrorMessage(errCategory0B02F88F59, fmt.Sprintf("tool %s is not enabled for this run", toolName))
	}
	if s.toolExecutor == nil {
		return ToolExecutionResult{}, errCategoryF1B7A5D95D
	}
	cfg := s.cfg.Snapshot()
	retryCount := cfg.Tools.RetryCount
	limit := cfg.Tools.MaxConcurrentCalls
	if input.ExecutionLimits != nil {
		retryCount = input.ExecutionLimits.ToolRetryCount
		limit = input.ExecutionLimits.ToolConcurrency
	}
	if limit <= 0 {
		limit = 8
	}

	return s.executeReceiptWithToolLimiter(ctx, limit, func() (ToolExecutionResult, error) {
		return s.callMCPWithRetry(ctx, ToolExecutionInput{
			ToolKey:       strings.TrimSpace(input.ToolKey),
			ProviderKind:  strings.TrimSpace(input.ProviderKind),
			ProviderKey:   strings.TrimSpace(input.ProviderKey),
			ToolName:      toolName,
			ArgumentsJSON: strings.TrimSpace(input.ArgumentsJSON),
			Actor:         input.Actor,
			Thread:        input.Thread,
			RequestID:     strings.TrimSpace(input.RequestID),
		}, retryCount, input.OnAttemptFailed)
	})
}

func (s *Engine) resolveMaxToolCallsPerRun() int {
	maxCalls := s.cfg.Snapshot().Execution.MaxToolCalls
	if maxCalls <= 0 {
		maxCalls = 8
	}
	if maxCalls > 64 {
		maxCalls = 64
	}
	return maxCalls
}

func (s *Engine) resolveMaxSelectedToolsPerMessage() int {
	maxTools := s.cfg.Snapshot().Tools.MaxSelectedPerRun
	if maxTools <= 0 {
		maxTools = DefaultMCPMaxSelectedToolsPerMessage
	}
	if maxTools > MaxMCPSelectedToolsPerMessage {
		maxTools = MaxMCPSelectedToolsPerMessage
	}
	return maxTools
}

// ValidateSelectedToolKeys validates the unified per-message Tool selection limit.
func (s *Engine) ValidateSelectedToolKeys(toolKeys []string) error {
	if len(toolKeys) > s.resolveMaxSelectedToolsPerMessage() {
		return ErrTooManySelectedTools
	}
	return nil
}

func (s *Engine) resolveMaxLLMCallsPerRun() int {
	maxCalls := s.cfg.Snapshot().Execution.MaxLLMCalls
	if maxCalls <= 0 {
		maxCalls = 5
	}
	if maxCalls < 2 {
		maxCalls = 2
	}
	if maxCalls > 32 {
		maxCalls = 32
	}
	return maxCalls
}

func (s *Engine) getToolLimiter(limit int) chan struct{} {
	if limit <= 0 {
		limit = 1
	}
	if value, ok := s.toolLimiters.Load(limit); ok {
		if limiter, castOK := value.(chan struct{}); castOK {
			return limiter
		}
	}
	created := make(chan struct{}, limit)
	actual, _ := s.toolLimiters.LoadOrStore(limit, created)
	limiter, ok := actual.(chan struct{})
	if !ok {
		return created
	}
	return limiter
}

func (s *Engine) callMCPWithRetry(
	ctx context.Context,
	input ToolExecutionInput,
	retryCount int,
	onAttemptFailed func(attempt, maxAttempts int, err error) error,
) (ToolExecutionResult, error) {
	if retryCount < 0 {
		retryCount = 0
	}

	var lastErr error
	var lastResult ToolExecutionResult
	for attempt := 0; attempt <= retryCount; attempt++ {
		output, err := s.toolExecutor.Execute(ctx, input)
		lastResult = ToolExecutionResult{OutputJSON: output, Attempts: attempt + 1}
		if err == nil {
			return lastResult, nil
		}
		lastErr = err
		if onAttemptFailed != nil {
			if observeErr := onAttemptFailed(attempt+1, retryCount+1, err); observeErr != nil {
				return lastResult, observeErr
			}
		}
		if attempt >= retryCount {
			break
		}

		backoff := time.Duration(100*(attempt+1)) * time.Millisecond
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return lastResult, ctx.Err()
		case <-timer.C:
		}
	}
	return lastResult, lastErr
}

var (
	errCategory0B02F88F59 = errors.New("error category 0B02F88F59")
	errCategoryD364A2A615 = errors.New("tool execution function is nil")
	errCategoryF1B7A5D95D = errors.New("tool executor is not configured")
	errCategory3A5F699D5F = errors.New("tool name is required")
)
