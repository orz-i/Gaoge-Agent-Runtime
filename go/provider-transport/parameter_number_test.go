package transport

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderJSONNumberParametersReachTextRequests(t *testing.T) {
	options := map[string]any{
		valueTemperature75242E23: json.Number("0.2"), valueTopPC744CA21: json.Number("0.9"),
		valueMaxOutputTokens5044A27A: json.Number("2048.0"), valueSeedC8D041FC: json.Number("9007199254740993"),
	}
	input := GenerateInput{Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}}, Options: options}
	chat := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "test-chat", EndpointChatCompletions, input, false)
	assertExactProviderNumber(t, chat, valueTemperature75242E23, "0.2")
	assertExactProviderNumber(t, chat, valueTopPC744CA21, "0.9")
	assertExactProviderNumber(t, chat, valueMaxCompletionTokensF93E57D2, "2048")
	assertExactProviderNumber(t, chat, valueSeedC8D041FC, "9007199254740993")
	responses := mustBuildRequestBody(t, AdapterOpenAIResponses, "test-responses", EndpointResponses, input, false)
	assertExactProviderNumber(t, responses, valueMaxOutputTokens5044A27A, "2048")
	input.Options = map[string]any{valueMaxTokensC696F5D6: json.Number("2048"), valueTemperature75242E23: json.Number("0.2"), valueTopPC744CA21: json.Number("0.9")}
	anthropic, err := buildAnthropicRequestBody("test-anthropic", input, false)
	require.NoError(t, err)
	assertExactProviderNumber(t, anthropic, valueMaxTokensC696F5D6, "2048")
	assertExactProviderNumber(t, anthropic, valueTemperature75242E23, "0.2")
	assertExactProviderNumber(t, anthropic, valueTopPC744CA21, "0.9")
	input.Options = map[string]any{
		"max_output_tokens": json.Number("2048"),
		"seed":              json.Number("9007199254740993"),
	}
	gemini, err := buildGeminiInteractionsRequestBody("gemini-3.7-flash", input, false)
	require.NoError(t, err)
	generation, ok := gemini[interactionKeyGenerationConfig].(map[string]any)
	require.True(t, ok)
	assertExactProviderNumber(t, generation, "max_output_tokens", "2048")
	assertExactProviderNumber(t, generation, "seed", "9007199254740993")
}

func TestProviderJSONNumberParametersReachMediaRequests(t *testing.T) {
	input := GenerateInput{Messages: []Message{{Role: valueUser2B36BE02, Content: "reviewed visual prompt"}},
		Options: map[string]any{valueSeedC8D041FC: json.Number("9007199254740993"), "duration": json.Number("5"), "fps": json.Number("24"), "guidance_scale": json.Number("2.5")}}
	image, err := buildVolcengineImageGenerationRequestBody("test-image", input)
	require.NoError(t, err)
	assertExactProviderNumber(t, image, valueSeedC8D041FC, "9007199254740993")
	assertExactProviderNumber(t, image, "guidance_scale", "2.5")
	video, prompt, err := buildVolcengineVideoRequestBody("test-video", input)
	require.NoError(t, err)
	require.Equal(t, input.Messages[0].Content, prompt)
	assertExactProviderNumber(t, video, valueSeedC8D041FC, "9007199254740993")
	assertExactProviderNumber(t, video, "duration", "5")
	assertExactProviderNumber(t, video, "fps", "24")
}

func assertExactProviderNumber(t *testing.T, payload map[string]any, key, expected string) {
	t.Helper()
	encoded, err := json.Marshal(payload[key])
	require.NoError(t, err)
	require.Equal(t, []byte(expected), encoded)
}

func TestProviderJSONNumberIntegerConversionDoesNotRoundOrAllocateHugeExponent(t *testing.T) {
	for _, spelling := range []string{"9007199254740993", "9007199254740993.0", "9.007199254740993e15"} {
		value, ok := modelParamIntValue(map[string]any{valueSeedC8D041FC: json.Number(spelling)}, valueSeedC8D041FC)
		require.True(t, ok)
		require.Equal(t, "9007199254740993", strconv.Itoa(value))
	}
	for _, spelling := range []string{"1e999999999", "9223372036854775808", "NaN"} {
		_, ok := modelParamIntValue(map[string]any{valueSeedC8D041FC: json.Number(spelling)}, valueSeedC8D041FC)
		require.False(t, ok, spelling)
	}
}

func TestQuotedProviderSingleAttemptDisablesAutomaticUsageCompatibilityRetry(t *testing.T) {
	for _, protocol := range []string{AdapterOpenAIChatCompletions, AdapterOpenRouterChat} {
		t.Run(protocol, func(t *testing.T) {
			var includeUsageValues []interface{}
			server := newAutoUsageRetryTestServer(t, &includeUsageValues)
			defer server.Close()
			_, err := newTestProviderClient().GenerateStream(context.Background(), RouteConfig{
				Protocol: protocol, BaseURL: server.URL, UpstreamModel: "test-single-attempt", SingleProviderAttempt: true,
			}, GenerateInput{Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}}}, nil)
			require.Error(t, err)
			require.Len(t, includeUsageValues, 1, "a bound call cannot spend again to remove stream_options")
		})
	}
}
