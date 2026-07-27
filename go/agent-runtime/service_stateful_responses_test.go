// Package conversation owns conversation use cases and policy.
package agentruntime

import (
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueAssistant22F9B439 = "assistant"
	valueSystem72C2F39E    = "system"
	valueUserEEC6AB2C      = "user"
)

const (
	valueAnswer95CD14B0  = "answer"
	valueCall10A3D31FF   = "call_1"
	valueDefaultA93C534A = "default"
	valueFirst6074A28C   = "first"
	valueGpt55004D6907   = "gpt-5.5"
	valueHello636D88EC   = "hello"
	valueModelUpstream   = "model_upstream"
	valuePolicy44182DB1  = "policy"
	valueRun16BAF6D69    = "run_1"
	value第一轮29386C12     = "第一轮"
	value第一轮回答F760B219   = "第一轮回答"
)

func TestTextRunPromptSnapshotNeverDependsOnPreviousResponseID(t *testing.T) {
	messages := []Message{
		{Role: valueSystem72C2F39E, Content: valuePolicy44182DB1},
		{Role: valueUserEEC6AB2C, Content: valueFirst6074A28C},
		{Role: valueAssistant22F9B439, Content: valueAnswer95CD14B0},
		{Role: valueUserEEC6AB2C, Content: "next"},
	}
	input := PromptBuildInput{
		RunInput: RuntimeInput{
			Actor:     model.ActorRef{TenantID: valueTenantTest, ActorID: "actor_1"},
			Thread:    model.ThreadRef{Kind: threadKindConversation, ID: valueThread7},
			RequestID: valueRun16BAF6D69,
		},
		Thread: &ThreadSnapshot{
			Thread:             model.ThreadRef{Kind: threadKindConversation, ID: valueThread7},
			DefaultModel:       valueGpt55004D6907,
			ProviderResponseID: "resp_legacy",
		},
		Route: &LLMRoute{
			Protocol:                    AdapterOpenAIResponses,
			UpstreamRef:                 model.ResourceRef{Kind: valueModelUpstream, ID: "1"},
			UpstreamModel:               valueGpt55004D6907,
			PreviousResponseIDSupported: true,
		},
		BranchReason: valueDefaultA93C534A,
	}
	result := newRunPromptBuildResult(input, Config{}, PromptPlan{}, messages, RouteConfig{Endpoint: EndpointResponses}, nil, nil, nil, selectedToolRuntime{})

	if result.generateInput.PreviousResponseID != "" {
		t.Fatalf("Text Run must not depend on an upstream response chain, got %q", result.generateInput.PreviousResponseID)
	}
	if len(result.llmMessages) != len(messages) || len(result.generateInput.Messages) != 3 {
		t.Fatalf("expected a self-contained prompt history, got snapshot=%d request=%d", len(result.llmMessages), len(result.generateInput.Messages))
	}
	if result.statefulDecision.DisabledReason != "self_contained_text_run_snapshot" {
		t.Fatalf("unexpected durable prompt decision: %#v", result.statefulDecision)
	}
}

func TestSupportsPreviousResponseIDRouteOnlyAllowsOfficialOpenAIResponses(t *testing.T) {
	if !supportsPreviousResponseIDRoute(&LLMRoute{
		Protocol:                    AdapterOpenAIResponses,
		PreviousResponseIDSupported: true,
	}) {
		t.Fatalf("expected gateway-allowed route to support previous_response_id")
	}
	if supportsPreviousResponseIDRoute(&LLMRoute{
		Protocol:                    AdapterOpenAIResponses,
		PreviousResponseIDSupported: false,
	}) {
		t.Fatalf("expected gateway-denied route to disable previous_response_id")
	}
	if supportsPreviousResponseIDRoute(&LLMRoute{
		Protocol:                    AdapterOpenAIChatCompletions,
		PreviousResponseIDSupported: false,
	}) {
		t.Fatalf("expected non-Responses route to disable previous_response_id")
	}
}

func TestApplyOpenAIResponsesInstructionsOnlyForOfficialRoute(t *testing.T) {
	official := &LLMRoute{
		Protocol:                    AdapterOpenAIResponses,
		PreviousResponseIDSupported: true,
	}
	input := GenerateInput{
		Messages: []Message{
			{Role: valueSystem72C2F39E, Content: "platform policy"},
			{Role: valueUserEEC6AB2C, Content: valueHello636D88EC},
			{Role: valueSystem72C2F39E, Content: "final synthesis only"},
			{Role: "tool", ToolResults: []ToolResult{{ToolCallID: valueCall10A3D31FF, OutputJSON: `{"ok":true}`}}},
		},
	}

	applyOpenAIResponsesInstructions(official, EndpointResponses, &input)

	if input.Instructions != "platform policy\n\nfinal synthesis only" {
		t.Fatalf("expected extracted instructions, got %q", input.Instructions)
	}
	if len(input.Messages) != 2 || input.Messages[0].Role != valueUserEEC6AB2C || input.Messages[1].Role != "tool" {
		t.Fatalf("expected system messages removed from input, got %#v", input.Messages)
	}

	custom := &LLMRoute{
		Protocol:                    AdapterOpenAIResponses,
		PreviousResponseIDSupported: false,
	}
	compatInput := GenerateInput{Messages: []Message{{Role: valueSystem72C2F39E, Content: valuePolicy44182DB1}, {Role: valueUserEEC6AB2C, Content: valueHello636D88EC}}}
	applyOpenAIResponsesInstructions(custom, EndpointResponses, &compatInput)
	if compatInput.Instructions != "" || len(compatInput.Messages) != 2 {
		t.Fatalf("expected custom route to keep system messages, got %#v", compatInput)
	}
}

func TestPromptStateFingerprintMatchesPrefixAfterAssistantAppend(t *testing.T) {
	firstPrompt := []Message{
		{Role: valueSystem72C2F39E, Content: valuePolicy44182DB1},
		{Role: valueUserEEC6AB2C, Content: value第一轮29386C12},
	}
	stored := buildPromptStateFingerprint(promptStateFingerprintInput{
		Protocol:          AdapterOpenAIResponses,
		Endpoint:          EndpointResponses,
		UpstreamRef:       model.ResourceRef{Kind: valueModelUpstream, ID: "1"},
		UpstreamModel:     valueGpt55004D6907,
		PlatformModelName: valueGpt55004D6907,
		Messages:          appendAssistantStateMessage(firstPrompt, value第一轮回答F760B219),
		Tools: []ToolDefinition{
			{Name: "b", Description: "B", InputSchema: []byte(`{"type":"object"}`)},
			{Name: "a", Description: "A", InputSchema: []byte(`{"type":"object"}`)},
		},
	})
	secondPrompt := []Message{
		{Role: valueSystem72C2F39E, Content: valuePolicy44182DB1},
		{Role: valueUserEEC6AB2C, Content: value第一轮29386C12},
		{Role: valueAssistant22F9B439, Content: value第一轮回答F760B219},
		{Role: valueUserEEC6AB2C, Content: "第二轮"},
	}
	prefix := buildPromptStateFingerprint(promptStateFingerprintInput{
		Protocol:          AdapterOpenAIResponses,
		Endpoint:          EndpointResponses,
		UpstreamRef:       model.ResourceRef{Kind: valueModelUpstream, ID: "1"},
		UpstreamModel:     valueGpt55004D6907,
		PlatformModelName: valueGpt55004D6907,
		Messages:          promptStatePrefixMessages(secondPrompt),
		Tools: []ToolDefinition{
			{Name: "a", Description: "A", InputSchema: []byte(`{"type":"object"}`)},
			{Name: "b", Description: "B", InputSchema: []byte(`{"type":"object"}`)},
		},
	})

	if stored != prefix {
		t.Fatalf("expected state fingerprint to match next prompt prefix")
	}
}

func TestPromptStateFingerprintUsesRebuildableHistoryWhenCurrentUserHasDynamicContext(t *testing.T) {
	firstPrompt := []Message{
		{Role: valueSystem72C2F39E, Content: "<ctx><files><file name=\"A.md\">稳定文件</file></files></ctx>"},
		{Role: valueSystem72C2F39E, Content: "# tool_use\n- use tools only when useful"},
		{Role: valueUserEEC6AB2C, Content: "<ctx><rag><doc name=\"A.md\" i=\"1\">动态片段</doc></rag></ctx>\n\n<q>第一轮</q>"},
	}
	stored := buildPromptStateFingerprint(promptStateFingerprintInput{
		Protocol:          AdapterOpenAIResponses,
		Endpoint:          EndpointResponses,
		UpstreamRef:       model.ResourceRef{Kind: valueModelUpstream, ID: "1"},
		UpstreamModel:     valueGpt55004D6907,
		PlatformModelName: valueGpt55004D6907,
		Messages:          buildNextStatefulPrefixMessages(firstPrompt, value第一轮29386C12, value第一轮回答F760B219),
	})
	secondPrompt := []Message{
		{Role: valueSystem72C2F39E, Content: "<ctx><files><file name=\"A.md\">稳定文件</file></files></ctx>"},
		{Role: valueSystem72C2F39E, Content: "# tool_use\n- use tools only when useful"},
		{Role: valueUserEEC6AB2C, Content: value第一轮29386C12},
		{Role: valueAssistant22F9B439, Content: value第一轮回答F760B219},
		{Role: valueUserEEC6AB2C, Content: "<ctx><rag><doc name=\"A.md\" i=\"2\">新片段</doc></rag></ctx>\n\n<q>第二轮</q>"},
	}
	prefix := buildPromptStateFingerprint(promptStateFingerprintInput{
		Protocol:          AdapterOpenAIResponses,
		Endpoint:          EndpointResponses,
		UpstreamRef:       model.ResourceRef{Kind: valueModelUpstream, ID: "1"},
		UpstreamModel:     valueGpt55004D6907,
		PlatformModelName: valueGpt55004D6907,
		Messages:          promptStatePrefixMessages(secondPrompt),
	})

	if stored != prefix {
		t.Fatalf("expected dynamic first round to match rebuildable second prefix")
	}
}

func TestPromptStateFingerprintChangesWhenContextConfigChanges(t *testing.T) {
	messages := []Message{
		{Role: valueSystem72C2F39E, Content: valuePolicy44182DB1},
		{Role: valueUserEEC6AB2C, Content: value第一轮29386C12},
	}
	baseCfg := Config{
		Retrieval: RetrievalConfig{
			Enabled: true, Model: "embed-a", MinSimilarity: 0.45,
			EmbeddingOutputDimensions: 1536, EmbeddingNormalize: true,
		},
	}
	changedCfg := baseCfg
	changedCfg.Context.MessageEmbeddingEnabled = !baseCfg.Context.MessageEmbeddingEnabled

	first := buildPromptStateFingerprint(promptStateFingerprintInput{
		Protocol:          AdapterOpenAIResponses,
		Endpoint:          EndpointResponses,
		UpstreamRef:       model.ResourceRef{Kind: valueModelUpstream, ID: "1"},
		UpstreamModel:     valueGpt55004D6907,
		PlatformModelName: valueGpt55004D6907,
		ContextConfig:     buildPromptContextConfigSignature(baseCfg),
		Messages:          messages,
	})
	second := buildPromptStateFingerprint(promptStateFingerprintInput{
		Protocol:          AdapterOpenAIResponses,
		Endpoint:          EndpointResponses,
		UpstreamRef:       model.ResourceRef{Kind: valueModelUpstream, ID: "1"},
		UpstreamModel:     valueGpt55004D6907,
		PlatformModelName: valueGpt55004D6907,
		ContextConfig:     buildPromptContextConfigSignature(changedCfg),
		Messages:          messages,
	})

	if first == second {
		t.Fatal("expected context config change to invalidate state fingerprint")
	}
}
