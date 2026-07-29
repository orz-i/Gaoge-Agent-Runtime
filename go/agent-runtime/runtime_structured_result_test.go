package agentruntime

import (
	"context"
	"errors"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/modelcap"
)

type structuredResultGateway struct {
	capabilities string
	outputs      []*GenerateOutput
	inputs       []GenerateInput
}

func (g *structuredResultGateway) PrepareTextRoute(_ context.Context, input LLMRouteInput) (*LLMRoute, error) {
	return &LLMRoute{
		PlatformModelName:     input.PlatformModelName,
		UpstreamModel:         "structured-test-model",
		Protocol:              AdapterOpenAIChatCompletions,
		ModelCapabilitiesJSON: g.capabilities,
	}, nil
}

func (g *structuredResultGateway) PrepareDefaultTextRoute(ctx context.Context, input LLMRouteInput) (*LLMRoute, error) {
	return g.PrepareTextRoute(ctx, input)
}

func (g *structuredResultGateway) GenerateText(_ context.Context, _ *LLMRoute, input GenerateInput) (*GenerateOutput, error) {
	g.inputs = append(g.inputs, input)
	if len(g.outputs) == 0 {
		return nil, ErrUpstreamEmptyResponse
	}
	output := g.outputs[0]
	g.outputs = g.outputs[1:]
	return output, nil
}

func (g *structuredResultGateway) GenerateTextStream(_ context.Context, _ *LLMRoute, input GenerateInput, onEvent func(GenerateStreamEvent) error) (*GenerateOutput, error) {
	g.inputs = append(g.inputs, input)
	if len(g.outputs) == 0 {
		return nil, ErrUpstreamEmptyResponse
	}
	output := g.outputs[0]
	g.outputs = g.outputs[1:]
	if err := onEvent(GenerateStreamEvent{Delta: output.Text}); err != nil {
		return nil, err
	}
	return output, nil
}

func TestStructuredRunAnswerNegotiatesValidatesAndCorrectsOnce(t *testing.T) {
	gateway := &structuredResultGateway{
		capabilities: `{"structuredOutput":{"mode":"strict_json_schema"}}`,
		outputs: []*GenerateOutput{
			{Text: `{"value":"wrong"}`, Usage: Usage{InputTokens: 3, OutputTokens: 2}},
			{Text: `{"value":7}`, Usage: Usage{InputTokens: 4, OutputTokens: 3}},
		},
	}
	repo := &multiTurnRunRepo{}
	engine := &Engine{
		cfg:               StaticConfigProvider(Config{}),
		repo:              repo,
		llmGateway:        gateway,
		generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{}),
	}
	run := model.Run{
		RunID: "run_structured_result", RequestID: "request_structured_result",
		Actor:  model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey},
		Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey},
	}
	effective := effectiveTextRunConfig{
		PlatformModelName: "structured-test-model", MaxLLMCalls: 3,
		StructuredOutputSchema: []byte(`{
			"type":"object",
			"required":["value"],
			"additionalProperties":false,
			"properties":{"value":{"type":"integer"}}
		}`),
		ResultAttempts: 1,
		Options:        map[string]interface{}{plannerResponseFormatKey: map[string]interface{}{valueType5EE8C955: testLegacyPlannerOption}},
	}
	usage, _, text, err := engine.streamRunAnswer(
		t.Context(), run, "step_result", effective, "direct", "direct",
		[]Message{{Role: valueUser19341906, Content: "return a value"}}, "answer", false,
	)
	if err != nil {
		t.Fatalf("streamRunAnswer() error = %v", err)
	}
	assertStructuredCorrectionResult(t, gateway, repo, usage, text)
}

func assertStructuredCorrectionResult(t *testing.T, gateway *structuredResultGateway, repo *multiTurnRunRepo, usage Usage, resultText string) {
	t.Helper()
	if resultText != `{"value":7}` || usage.InputTokens != 7 || usage.OutputTokens != 5 {
		t.Fatalf("result text=%q usage=%#v", resultText, usage)
	}
	if len(gateway.inputs) != 2 {
		t.Fatalf("gateway calls = %d, want 2", len(gateway.inputs))
	}
	if countRunEvents(repo.events, eventLLMRouteSelected) != 2 || countRunEvents(repo.events, valueUsageUpdatedABC8B0B2) != 2 {
		t.Fatalf("route/usage events must match successful calls exactly once: %#v", repo.events)
	}
	assertStructuredGatewayFormats(t, gateway.inputs)
	if len(gateway.inputs[1].Messages) <= len(gateway.inputs[0].Messages) {
		t.Fatal("correction request did not include the invalid result and validation feedback")
	}
	assertRunEventCount(t, repo.events, "result.correction.started", 1)
	assertRunEventCount(t, repo.events, "message.delta", 1)
}

func assertStructuredGatewayFormats(t *testing.T, inputs []GenerateInput) {
	t.Helper()
	for index, input := range inputs {
		format, ok := input.Options[plannerResponseFormatKey].(map[string]interface{})
		if !ok || format[valueType5EE8C955] != plannerJSONSchemaType {
			t.Fatalf("call %d response format = %#v", index, input.Options[plannerResponseFormatKey])
		}
	}
}

func assertRunEventCount(t *testing.T, events []model.Event, eventType string, want int) {
	t.Helper()
	if got := countRunEvents(events, eventType); got != want {
		t.Fatalf("%s events = %d, want %d", eventType, got, want)
	}
}

type structuredOutputModeTest struct {
	name string
	mode modelcap.StructuredOutputMode
	want string
}

func TestStructuredRunOutputModesAndUnsupportedCapability(t *testing.T) {
	schema := []byte(`{"type":"object"}`)
	tests := []structuredOutputModeTest{
		{name: workflowPayloadStrict, mode: modelcap.StructuredOutputStrictJSONSchema, want: plannerJSONSchemaType},
		{name: valueObjectE97B31A9, mode: modelcap.StructuredOutputJSONObject, want: plannerJSONObjectType},
		{name: "json text", mode: modelcap.StructuredOutputJSONText},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertStructuredOutputMode(t, schema, test)
		})
	}
	if _, err := resolveStructuredRunOutput(&LLMRoute{ModelCapabilitiesJSON: `{}`}, schema); !errors.Is(err, ErrStructuredOutputUnsupported) {
		t.Fatalf("unsupported capability error = %v", err)
	}
}

func assertStructuredOutputMode(t *testing.T, schema []byte, test structuredOutputModeTest) {
	t.Helper()
	route := &LLMRoute{ModelCapabilitiesJSON: `{"structuredOutput":{"mode":"` + string(test.mode) + `"}}`}
	spec, err := resolveStructuredRunOutput(route, schema)
	if err != nil {
		t.Fatalf("resolveStructuredRunOutput() error = %v", err)
	}
	base := map[string]interface{}{plannerResponseFormatKey: map[string]interface{}{valueType5EE8C955: testLegacyPlannerOption}}
	options := applyStructuredRunOutput(base, spec)
	assertStructuredBaseOptionsUnchanged(t, base)
	format, exists := options[plannerResponseFormatKey].(map[string]interface{})
	if test.want == "" {
		if exists {
			t.Fatalf("json_text response format = %#v", format)
		}
		return
	}
	if !exists || format[valueType5EE8C955] != test.want {
		t.Fatalf("response format = %#v", options[plannerResponseFormatKey])
	}
}

func assertStructuredBaseOptionsUnchanged(t *testing.T, base map[string]interface{}) {
	t.Helper()
	baseResponseFormat, ok := base[plannerResponseFormatKey].(map[string]interface{})
	if !ok || baseResponseFormat[valueType5EE8C955] != testLegacyPlannerOption {
		t.Fatal("structured option negotiation mutated the frozen base options")
	}
}
