// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueSystemBE377EF8 = "system"
)

const textTaskFollowModel = "follow"

// resolveTextTaskRouteCandidates 返回内部文本任务的候选路由。
// 指定模型只使用指定路由；follow 先使用当前会话模型，再使用默认聊天路由作为任务兜底。
func (s *Engine) resolveTextTaskRouteCandidates(ctx context.Context, configured string, threadModel string, actor domain.ActorRef, thread domain.ThreadRef, requestID string) ([]*LLMRoute, error) {
	if s.llmGateway == nil {
		return nil, ErrModelRouteNotConfigured
	}
	value := strings.TrimSpace(configured)
	if value != "" && !strings.EqualFold(value, textTaskFollowModel) {
		route, err := s.resolveExplicitTextTaskRoute(ctx, value, actor, thread, requestID)
		if err != nil {
			return nil, err
		}
		return []*LLMRoute{route}, nil
	}

	routes, routeErr := s.resolveFollowTextTaskRoutes(ctx, threadModel, actor, thread, requestID)
	routes, err := s.appendDefaultTextTaskRoute(ctx, routes, actor, thread, requestID)
	if err != nil {
		return nil, err
	}
	if len(routes) > 0 {
		return routes, nil
	}
	if routeErr != nil {
		return nil, fmt.Errorf("text task route resolve: %w", routeErr)
	}
	return nil, ErrModelRouteNotConfigured
}

func (s *Engine) resolveFollowTextTaskRoutes(ctx context.Context, threadModel string, actor domain.ActorRef, thread domain.ThreadRef, requestID string) ([]*LLMRoute, error) {
	routes := make([]*LLMRoute, 0, 2)
	// follow 只在当前会话模型本身具备聊天路由时直接复用；图片、视频等非文本模型不参与内部文本任务。
	route, found, err := s.resolveCurrentTextTaskRoute(ctx, threadModel, actor, thread, requestID)
	if err != nil {
		return routes, err
	}
	if found {
		routes = append(routes, route)
	}
	return routes, nil
}

func (s *Engine) resolveExplicitTextTaskRoute(ctx context.Context, modelName string, actor domain.ActorRef, thread domain.ThreadRef, requestID string) (*LLMRoute, error) {
	route, err := s.llmGateway.PrepareTextRoute(ctx, textTaskRouteInput(modelName, actor, thread, requestID))
	if err != nil {
		return nil, fmt.Errorf("text task route resolve: %w", err)
	}
	return route, nil
}

func (s *Engine) resolveCurrentTextTaskRoute(ctx context.Context, threadModel string, actor domain.ActorRef, thread domain.ThreadRef, requestID string) (*LLMRoute, bool, error) {
	modelName := strings.TrimSpace(threadModel)
	if modelName == "" {
		return nil, false, nil
	}
	route, err := s.llmGateway.PrepareTextRoute(ctx, textTaskRouteInput(modelName, actor, thread, requestID))
	return route, err == nil, err
}

func textTaskRouteInput(modelName string, actor domain.ActorRef, thread domain.ThreadRef, requestID string) LLMRouteInput {
	return LLMRouteInput{
		PlatformModelName: modelName,
		TaskType:          LLMTaskTypeText,
		Scope:             LLMRouteScopeInternal,
		Actor:             actor,
		Thread:            thread,
		RequestID:         strings.TrimSpace(requestID),
	}
}

func (s *Engine) appendDefaultTextTaskRoute(
	ctx context.Context,
	routes []*LLMRoute,
	actor domain.ActorRef,
	thread domain.ThreadRef,
	requestID string,
) ([]*LLMRoute, error) {
	route, err := s.llmGateway.PrepareDefaultTextRoute(ctx, LLMRouteInput{
		TaskType:  LLMTaskTypeText,
		Scope:     LLMRouteScopeInternal,
		Actor:     actor,
		Thread:    thread,
		RequestID: strings.TrimSpace(requestID),
	})
	if err != nil {
		return routes, defaultTextTaskRouteError(routes, err)
	}
	if !textTaskRouteExists(routes, route) {
		routes = append(routes, route)
	}
	return routes, nil
}

func defaultTextTaskRouteError(routes []*LLMRoute, err error) error {
	if len(routes) == 0 {
		return fmt.Errorf("default text task route resolve: %w", err)
	}
	return nil
}

func textTaskRouteExists(routes []*LLMRoute, route *LLMRoute) bool {
	if route == nil {
		return true
	}
	for _, item := range routes {
		if item == nil {
			continue
		}
		if strings.TrimSpace(item.BindingCode) != "" && strings.TrimSpace(item.BindingCode) == strings.TrimSpace(route.BindingCode) {
			return true
		}
		if strings.TrimSpace(item.PlatformModelName) == strings.TrimSpace(route.PlatformModelName) &&
			strings.TrimSpace(item.Protocol) == strings.TrimSpace(route.Protocol) &&
			strings.TrimSpace(item.UpstreamModel) == strings.TrimSpace(route.UpstreamModel) {
			return true
		}
	}
	return false
}

// buildTextTaskGenerateInput 为标题、标签、压缩等内部文本任务统一应用模型能力策略。
func buildTextTaskGenerateInput(route *LLMRoute, cfg Config, messages []Message) GenerateInput {
	if route == nil {
		return GenerateInput{Messages: cloneLLMMessages(messages)}
	}
	input := GenerateInput{
		Messages: normalizeTextTaskSystemMessages(route, messages),
		Options: filterModelOptions(nil, route.Protocol, modelOptionPolicyConfig{
			Mode:                  cfg.Execution.ModelOptions.Mode,
			AllowedPathsJSON:      cfg.Execution.ModelOptions.AllowedPaths,
			DeniedPathsJSON:       cfg.Execution.ModelOptions.DeniedPaths,
			ModelCapabilitiesJSON: route.ModelCapabilitiesJSON,
		}),
	}
	applyOpenAIResponsesInstructions(route, routeEndpoint(route), &input)
	return input
}

// normalizeTextTaskSystemMessages 按模型能力决定内部文本任务是否需要把 system 指令降级进 user 消息。
func normalizeTextTaskSystemMessages(route *LLMRoute, messages []Message) []Message {
	if route == nil || !shouldInlineSystemPromptToUser(*route) {
		return cloneLLMMessages(messages)
	}
	var systemText strings.Builder
	remaining := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != valueSystemBE377EF8 {
			remaining = append(remaining, message)
			continue
		}
		text := strings.TrimSpace(systemInstructionText(message))
		if text == "" {
			continue
		}
		if systemText.Len() > 0 {
			systemText.WriteString("\n\n")
		}
		systemText.WriteString(text)
	}
	if systemText.Len() == 0 {
		return remaining
	}
	return inlineSystemPromptIntoLatestUserMessage(remaining, systemText.String())
}
