// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueImage57AC75A4 = "image"
)

type messageBillingUpdateError struct {
	err error
}

func (e *messageBillingUpdateError) Error() string {
	return e.err.Error()
}

func (e *messageBillingUpdateError) Unwrap() error {
	return e.err
}

// RunBillingInput 描述一次消息发送对应的计费上下文。
type RunBillingInput struct {
	Actor             domain.ActorRef
	Thread            domain.ThreadRef
	PlatformModelName string
	ThreadModel       string
	ClientRunID       string
	Usage             TurnUsage
	Result            *RunMessageResult
}

// RunAuditInput 描述一次消息发送对应的审计上下文。
type RunAuditInput struct {
	Actor       domain.ActorRef
	Thread      domain.ThreadRef
	RequestID   string
	ClientIP    string
	UserAgent   string
	Action      string
	ContentType string
	Resources   []domain.ResourceRef
	Result      *RunMessageResult
}

type attachmentKindEntry struct {
	Kind     string `json:"kind"`
	MimeType string `json:"mime_type"`
}

// EnsureRunBillingAccess 校验本次发送在当前计费策略下是否可用。
func (s *Engine) EnsureRunBillingAccess(ctx context.Context, input RunBillingInput) error {
	if s.billingSvc == nil {
		return nil
	}
	return s.billingSvc.EnsureModelUsable(ctx, input.Actor, runBillingPlatformModelName(input), s.now())
}

// ReserveRunUsageBalance 在模型调用前执行按量预扣。
func (s *Engine) ReserveRunUsageBalance(ctx context.Context, input RunBillingInput) (*UsageBalanceReservation, bool, error) {
	if s.billingSvc == nil {
		return nil, false, nil
	}
	return s.billingSvc.ReserveUsageBalance(ctx, input.Actor, runBillingPlatformModelName(input), strings.TrimSpace(input.ClientRunID))
}

// ReleaseRunUsageReservation 在调用失败或计费失败时退回预扣。
func (s *Engine) ReleaseRunUsageReservation(ctx context.Context, reservation *UsageBalanceReservation, description string) error {
	if s.billingSvc == nil || reservation == nil {
		return nil
	}
	return s.billingSvc.ReleaseUsageBalanceReservation(ctx, reservation, description)
}

// RecordRunBilling 记录发送消息产生的用量账本，并把账单快照回写到 assistant 消息。
func (s *Engine) RecordRunBilling(
	ctx context.Context,
	input RunBillingInput,
	reservation *UsageBalanceReservation,
) (*UsageLedger, bool, error) {
	if s.billingSvc == nil || input.Result == nil {
		return nil, false, nil
	}
	usageLedger, found, err := s.buildRunUsageLedger(ctx, input)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	if err = s.billingSvc.RecordUsageWithReservation(ctx, usageLedger, reservation); err != nil {
		return nil, false, err
	}
	return usageLedger, true, nil
}

// ShouldReleaseRunUsageReservationAfterBillingError 判断计费失败后是否应退回预扣。
func ShouldReleaseRunUsageReservationAfterBillingError(err error) bool {
	var updateErr *messageBillingUpdateError
	return !errors.As(err, &updateErr)
}

// RecordRunAudit 记录发送消息审计日志。
func (s *Engine) RecordRunAudit(ctx context.Context, input RunAuditInput) {
	if s.auditWriter == nil || input.Result == nil {
		return
	}
	s.auditWriter.Write(
		ctx,
		strings.TrimSpace(input.RequestID),
		input.Actor,
		strings.TrimSpace(input.Action),
		input.Thread,
		strings.TrimSpace(input.ClientIP),
		strings.TrimSpace(input.UserAgent),
		map[string]interface{}{
			"content_type": strings.TrimSpace(input.ContentType),
			"resources":    len(input.Resources),
		},
	)
}

func (s *Engine) buildRunUsageLedger(ctx context.Context, input RunBillingInput) (*UsageLedger, bool, error) {
	result := input.Result
	if result == nil {
		return nil, false, nil
	}
	latencyMS := result.LatencyMS
	ledger, err := s.billingSvc.BuildUsageLedger(ctx, UsagePricingInput{
		IdempotencyKey:      strings.TrimSpace(input.ClientRunID),
		Actor:               input.Actor,
		Thread:              input.Thread,
		PlatformModelName:   runBillingPlatformModelName(input),
		RoutedBindingCode:   strings.TrimSpace(result.RoutedBindingCode),
		ProviderProtocol:    strings.TrimSpace(result.UpstreamProtocol),
		UpstreamName:        strings.TrimSpace(result.UpstreamName),
		UpstreamModelName:   strings.TrimSpace(result.UpstreamModelName),
		CacheTimeout:        messageCacheTimeout(result.EffectiveOptions),
		RequestSpeed:        messageRequestSpeed(result.EffectiveOptions),
		UsageSpeed:          strings.TrimSpace(result.UsageSpeed),
		RequestServiceTier:  messageRequestServiceTier(result.EffectiveOptions),
		UsageServiceTier:    strings.TrimSpace(result.UsageServiceTier),
		BillingRateClass:    strings.TrimSpace(result.BillingRateClass),
		InputTokens:         input.Usage.InputTokens,
		CacheReadTokens:     input.Usage.CacheReadTokens,
		CacheWriteTokens:    input.Usage.CacheWriteTokens,
		CacheWrite5mTokens:  result.CacheWrite5mTokens,
		CacheWrite1hTokens:  result.CacheWrite1hTokens,
		OutputTokens:        input.Usage.OutputTokens,
		ReasoningTokens:     input.Usage.ReasoningTokens,
		CallCount:           1,
		LatencyMS:           latencyMS,
		ServerSideToolUsage: result.ServerSideToolUsage,
		ServiceItems:        result.ServiceItems,
		RawUsageJSON:        result.RawUsageJSON,
		BillingAt:           result.StartedAt,
	})
	return ledger, err == nil && ledger != nil, err
}

func runBillingPlatformModelName(input RunBillingInput) string {
	if input.Result != nil {
		if value := strings.TrimSpace(input.Result.PlatformModelName); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(input.PlatformModelName); value != "" {
		return value
	}
	return strings.TrimSpace(input.ThreadModel)
}

func messageCacheTimeout(options map[string]interface{}) string {
	if len(options) == 0 {
		return "5m"
	}
	if value := strings.TrimSpace(stringOption(options, "cache_timeout")); value != "" {
		if strings.EqualFold(value, "1h") {
			return "1h"
		}
		return "5m"
	}
	if cacheControl, ok := options["cache_control"].(map[string]interface{}); ok {
		if value := strings.TrimSpace(stringOption(cacheControl, "ttl")); strings.EqualFold(value, "1h") {
			return "1h"
		}
	}
	return "5m"
}

func messageRequestSpeed(options map[string]interface{}) string {
	if len(options) == 0 {
		return ""
	}
	speed := strings.TrimSpace(stringOption(options, "speed"))
	if strings.EqualFold(speed, "fast") {
		return "fast"
	}
	return speed
}

func messageRequestServiceTier(options map[string]interface{}) string {
	if len(options) == 0 {
		return ""
	}
	return strings.TrimSpace(stringOption(options, "service_tier"))
}

func stringOption(options map[string]interface{}, key string) string {
	raw, ok := options[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return value
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func countAttachmentKinds(attachmentsJSON string) (int64, int64) {
	items := make([]attachmentKindEntry, 0)
	if err := json.Unmarshal([]byte(strings.TrimSpace(attachmentsJSON)), &items); err != nil {
		return 0, 0
	}

	var imageCount int64
	var fileCount int64
	for _, item := range items {
		switch NormalizeAttachmentKind(item.Kind, item.MimeType) {
		case valueImage57AC75A4:
			imageCount++
		default:
			fileCount++
		}
	}
	return imageCount, fileCount
}
