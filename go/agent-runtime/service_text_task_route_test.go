// Package conversation owns conversation use cases and policy.
package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueOpenaiResponses72E1C081 = "openai_responses"
)

const (
	valueGpt5MiniB408026C = "gpt-5-mini"
	valueGrok4362DC6DA2   = "grok-4.3"
	valueUser576741BA     = "user"
)

const (
	valueCurrent652AB2C1  = "current"
	valueDefaultCDEF8D02  = "default"
	valueFallbackD1751217 = "fallback"
	valueHelloAA54638B    = "hello"
	valueMediumFFDF3807   = "medium"
	valueSystemB3D0189B   = "system"
)

type textTaskGatewayStub struct {
	routes       map[string]*LLMRoute
	defaultRoute *LLMRoute
	fail         map[string]error
}

func (r *textTaskGatewayStub) PrepareTextRoute(_ context.Context, input LLMRouteInput) (*LLMRoute, error) {
	if err := r.fail[input.PlatformModelName]; err != nil {
		return nil, err
	}
	route := r.routes[input.PlatformModelName]
	if route == nil {
		return nil, errCategory0977450825
	}
	return route, nil
}

func (r *textTaskGatewayStub) PrepareDefaultTextRoute(context.Context, LLMRouteInput) (*LLMRoute, error) {
	if r.defaultRoute == nil {
		return nil, errCategory662E668475
	}
	return r.defaultRoute, nil
}

func (r *textTaskGatewayStub) GenerateText(context.Context, *LLMRoute, GenerateInput) (*GenerateOutput, error) {
	return nil, errCategoryFBB8372B5B
}

func (r *textTaskGatewayStub) GenerateTextStream(context.Context, *LLMRoute, GenerateInput, func(GenerateStreamEvent) error) (*GenerateOutput, error) {
	return nil, errCategoryFBB8372B5B
}

func TestResolveTextTaskRouteCandidatesFollowUsesCurrentThenDefault(t *testing.T) {
	service := &Engine{llmGateway: &textTaskGatewayStub{
		routes: map[string]*LLMRoute{
			valueGrok4362DC6DA2: {PlatformModelName: valueGrok4362DC6DA2, BindingCode: valueCurrent652AB2C1, Protocol: "xai_responses", UpstreamModel: valueGrok4362DC6DA2},
		},
		defaultRoute: &LLMRoute{PlatformModelName: valueGpt5MiniB408026C, BindingCode: valueDefaultCDEF8D02, Protocol: valueOpenaiResponses72E1C081, UpstreamModel: valueGpt5MiniB408026C},
	}}

	routes, err := service.resolveTextTaskRouteCandidates(context.Background(), textTaskFollowModel, valueGrok4362DC6DA2, domain.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, domain.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}, "")
	if err != nil {
		t.Fatalf("resolve candidates: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("expected current and default routes, got %#v", routes)
	}
	if routes[0].BindingCode != valueCurrent652AB2C1 || routes[1].BindingCode != valueDefaultCDEF8D02 {
		t.Fatalf("unexpected route order: %#v", routes)
	}
}

func TestResolveTextTaskRouteCandidatesSpecifiedModelDoesNotAddDefault(t *testing.T) {
	service := &Engine{llmGateway: &textTaskGatewayStub{
		routes: map[string]*LLMRoute{
			valueGpt5MiniB408026C: {PlatformModelName: valueGpt5MiniB408026C, BindingCode: "specified", Protocol: valueOpenaiResponses72E1C081, UpstreamModel: valueGpt5MiniB408026C},
		},
		defaultRoute: &LLMRoute{PlatformModelName: valueFallbackD1751217, BindingCode: valueDefaultCDEF8D02, Protocol: valueOpenaiResponses72E1C081, UpstreamModel: valueFallbackD1751217},
	}}

	routes, err := service.resolveTextTaskRouteCandidates(context.Background(), valueGpt5MiniB408026C, valueGrok4362DC6DA2, domain.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, domain.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}, "")
	if err != nil {
		t.Fatalf("resolve candidates: %v", err)
	}
	if len(routes) != 1 || routes[0].BindingCode != "specified" {
		t.Fatalf("expected only specified route, got %#v", routes)
	}
}

func TestResolveTextTaskRouteCandidatesFollowFallsBackWhenCurrentRouteFails(t *testing.T) {
	service := &Engine{llmGateway: &textTaskGatewayStub{
		routes: map[string]*LLMRoute{},
		fail: map[string]error{
			valueGrok4362DC6DA2: errCategory9052EE7B1E,
		},
		defaultRoute: &LLMRoute{PlatformModelName: valueGpt5MiniB408026C, BindingCode: valueDefaultCDEF8D02, Protocol: valueOpenaiResponses72E1C081, UpstreamModel: valueGpt5MiniB408026C},
	}}

	routes, err := service.resolveTextTaskRouteCandidates(context.Background(), textTaskFollowModel, valueGrok4362DC6DA2, domain.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, domain.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}, "")
	if err != nil {
		t.Fatalf("resolve candidates: %v", err)
	}
	if len(routes) != 1 || routes[0].BindingCode != valueDefaultCDEF8D02 {
		t.Fatalf("expected default route after current route failure, got %#v", routes)
	}
}

func TestBuildTextTaskGenerateInputAppliesDefaultsAndInstructions(t *testing.T) {
	route := &LLMRoute{
		Protocol:                    AdapterOpenAIResponses,
		PreviousResponseIDSupported: true,
		ModelCapabilitiesJSON:       `{"defaultOptions":{"reasoning":{"effort":"medium"}}}`,
	}
	input := buildTextTaskGenerateInput(route, Config{
		Execution: ExecutionConfig{ModelOptions: ModelOptionConfig{
			Mode: modelOptionPolicyAllowlist,
			AllowedPaths: `{
			"openai_responses": ["reasoning.effort"]
		}`,
			DeniedPaths: DefaultModelOptionDeniedPathsJSON(),
		}},
	}, []Message{
		{Role: valueSystemB3D0189B, Content: "summarize carefully"},
		{Role: valueUser576741BA, Content: valueHelloAA54638B},
	})

	if input.Instructions != "summarize carefully" {
		t.Fatalf("expected official Responses instructions, got %q", input.Instructions)
	}
	if len(input.Messages) != 1 || input.Messages[0].Role != valueUser576741BA {
		t.Fatalf("expected system message to be removed from input, got %#v", input.Messages)
	}
	reasoning, ok := input.Options["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning options, got %#v", input.Options)
	}
	if reasoning["effort"] != valueMediumFFDF3807 {
		t.Fatalf("expected default reasoning effort, got %#v", input.Options)
	}
}

func TestBuildTextTaskGenerateInputInlinesSystemWhenCapabilitiesDisableSystemPrompt(t *testing.T) {
	route := &LLMRoute{
		Protocol:                    AdapterOpenAIResponses,
		PreviousResponseIDSupported: true,
		ModelCapabilitiesJSON:       `{"supportsSystemPrompt":false}`,
	}
	input := buildTextTaskGenerateInput(route, Config{}, []Message{
		{Role: valueSystemB3D0189B, Content: "title only"},
		{Role: valueUser576741BA, Content: valueHelloAA54638B},
	})

	if input.Instructions != "" {
		t.Fatalf("expected no native instructions for inline-user capability, got %q", input.Instructions)
	}
	if len(input.Messages) != 1 || input.Messages[0].Role != valueUser576741BA {
		t.Fatalf("expected one inlined user message, got %#v", input.Messages)
	}
	content := input.Messages[0].Content
	if !strings.Contains(content, "<system_instructions>") || !strings.Contains(content, "title only") || !strings.Contains(content, valueHelloAA54638B) {
		t.Fatalf("expected system prompt to be inlined into user message, got %q", content)
	}
}

var (
	errCategory9052EE7B1E = errors.New("current route unavailable")
	errCategory662E668475 = errors.New("default route not found")
	errCategoryFBB8372B5B = errors.New("not implemented")
	errCategory0977450825 = errors.New("route not found")
)
