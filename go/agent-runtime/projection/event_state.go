package projection

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

var errInvalidRunEventProjection = errors.New("runtime event projection: invalid run")

const (
	eventMessageDelta = "message.delta"
	eventUsageUpdated = "usage.updated"
	eventToolStarted  = "tool.started"
)

type usagePayload struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, ReasoningTokens int64
	UpstreamName, BindingCode, UpstreamModel, Protocol                            string
}

// ApplyEvent updates the mutable Run and Step read models from one newly
// accepted durable event. Callers must invoke it only after event idempotency
// has established that the event is new.
func ApplyEvent(run *domain.Run, step *domain.Step, event domain.Event) error {
	if run == nil || strings.TrimSpace(event.RunID) == "" || event.RunID != run.RunID {
		return errInvalidRunEventProjection
	}
	applyRunStatus(run, event)
	if err := applyRunMetrics(run, event); err != nil {
		return err
	}
	if step != nil && step.RunID == run.RunID && step.StepID == event.StepID {
		applyStepStatus(step, event)
	}
	return nil
}

func applyRunStatus(run *domain.Run, event domain.Event) {
	if status := projectedRunStatus(event.EventType); status != "" {
		run.Status = status
	}
	if strings.HasPrefix(event.EventType, "step.") && strings.TrimSpace(event.StepID) != "" {
		run.CurrentStepID = event.StepID
	}
	if strings.HasPrefix(event.EventType, "run.") && strings.TrimSpace(event.Summary) != "" {
		run.StatusReason = event.Summary
	}
	if terminalRunStatus(run.Status) {
		endedAt := eventTime(event)
		run.EndedAt = &endedAt
		run.TotalLatencyMS = nonnegativeDurationMS(run.StartedAt, endedAt)
	}
}

func projectedRunStatus(eventType string) string {
	switch eventType {
	case "run.preparing":
		return domain.RunStatusPreparing
	case "run.started", "run.resumed":
		return domain.RunStatusRunning
	case "run.waiting_input":
		return domain.RunStatusWaitingInput
	case "run.waiting_handoff":
		return domain.RunStatusWaitingHandoff
	case "run.waiting_timer":
		return domain.RunStatusWaitingTimer
	case "run.cancelling":
		return domain.RunStatusCancelling
	case "run.compensating":
		return domain.RunStatusCompensating
	case "run.suspended":
		return domain.RunStatusSuspended
	case "run.completed":
		return domain.RunStatusCompleted
	case "run.failed":
		return domain.RunStatusFailed
	case "run.cancelled":
		return domain.RunStatusCancelled
	default:
		return ""
	}
}

func applyRunMetrics(run *domain.Run, event domain.Event) error {
	switch event.EventType {
	case eventMessageDelta:
		if run.FirstTokenLatencyMS == 0 {
			run.FirstTokenLatencyMS = nonnegativeDurationMS(run.StartedAt, eventTime(event))
		}
	case eventUsageUpdated:
		var payload usagePayload
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
			return fmt.Errorf("runtime event projection: decode usage for %s: %w", run.RunID, err)
		}
		run.InputTokens += payload.InputTokens
		run.OutputTokens += payload.OutputTokens
		run.CacheReadTokens += payload.CacheReadTokens
		run.CacheWriteTokens += payload.CacheWriteTokens
		run.ReasoningTokens += payload.ReasoningTokens
		run.LLMCallsCount++
		if strings.TrimSpace(payload.BindingCode) != "" {
			run.RoutedBindingCode = payload.BindingCode
		}
		if strings.TrimSpace(payload.UpstreamModel) != "" {
			run.UpstreamModelName = payload.UpstreamModel
		}
		if strings.TrimSpace(payload.Protocol) != "" {
			run.ProviderProtocol = payload.Protocol
		}
	case eventToolStarted:
		run.ToolCallsCount++
	}
	return nil
}

func applyStepStatus(step *domain.Step, event domain.Event) {
	switch event.EventType {
	case "step.created":
		step.Status = domain.RunStatusQueued
	case "step.started", "step.resumed":
		step.Status = domain.RunStatusRunning
		if step.StartedAt.IsZero() {
			step.StartedAt = eventTime(event)
		}
		step.EndedAt = nil
	case "step.waiting_input":
		step.Status = domain.RunStatusWaitingInput
	case "step.waiting_handoff":
		step.Status = domain.RunStatusWaitingHandoff
	case "step.waiting_timer":
		step.Status = domain.RunStatusWaitingTimer
	case "step.compensating":
		step.Status = domain.RunStatusCompensating
	case "step.suspended":
		step.Status = domain.RunStatusSuspended
		setStepEnd(step, event)
	case "step.completed":
		step.Status = domain.RunStatusCompleted
		step.ResultSummary = strings.TrimSpace(event.Summary)
		setStepEnd(step, event)
	case "step.failed":
		step.Status = domain.RunStatusFailed
		step.ResultSummary = strings.TrimSpace(event.Summary)
		step.ErrorJSON = event.ErrorJSON
		setStepEnd(step, event)
	case "step.cancelled":
		step.Status = domain.RunStatusCancelled
		step.ResultSummary = strings.TrimSpace(event.Summary)
		setStepEnd(step, event)
	case "step.skipped":
		step.Status = "skipped"
		step.ResultSummary = strings.TrimSpace(event.Summary)
		setStepEnd(step, event)
	}
}

func setStepEnd(step *domain.Step, event domain.Event) {
	value := eventTime(event)
	step.EndedAt = &value
}

func eventTime(event domain.Event) time.Time {
	if event.EndedAt != nil {
		return *event.EndedAt
	}
	if !event.StartedAt.IsZero() {
		return event.StartedAt
	}
	if !event.CreatedAt.IsZero() {
		return event.CreatedAt
	}
	return time.Now()
}

func terminalRunStatus(status string) bool {
	return status == domain.RunStatusCompleted || status == domain.RunStatusFailed || status == domain.RunStatusCancelled
}

func nonnegativeDurationMS(start, end time.Time) int64 {
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}
