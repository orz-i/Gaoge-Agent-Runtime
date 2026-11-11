package transport

import "testing"

const (
	valueName9C913FDF                 = "name"
	valueOutputTextF077F256           = "output_text"
	valuePromptCacheRetention8CA8CD4C = "prompt_cache_retention"
	valueResponseFormat8FC81B36       = "response_format"
	valueSchema88EB438D               = "schema"
	valueStopA99407D1                 = "stop"
	valueType246D1704                 = "type"
	valueUser2B36BE02                 = "user"
	valueVerbosity9061A658            = "verbosity"
)

const (
	valueTextA9C18C6C                       = "24h"
	valueEND4BD1AC14                        = "END"
	valueTEXTB911CD5F                       = "TEXT"
	valueAdaptive6FD768C8                   = "adaptive"
	valueAnswerC6D9EEFD                     = "answer"
	valueAssistant55D42475                  = "assistant"
	valueAuto84006CFC                       = "auto"
	valueBudgetTokens7B3D07EE               = "budget_tokens"
	valueCacheTimeoutE8E0EF1D               = "cache_timeout"
	valueCandidateCount3D192A7E             = "candidate_count"
	valueCodeInterpreter59DEE28E            = "code_interpreter"
	valueCompletedC0D53520                  = "completed"
	valueContents1C7BCADE                   = "contents"
	valueDebugC1F5C7D0                      = "debug"
	valueDisabledA7F0C0E2                   = "disabled"
	valueEffortC1E1EB4B                     = "effort"
	valueEnableThinking4E6D6E90             = "enable_thinking"
	valueEnabledDFFEEB45                    = "enabled"
	valueEphemeral34FB8464                  = "ephemeral"
	valueFastF700BBFA                       = "fast"
	valueFlexFDF83CA6                       = "flex"
	valueFrequencyPenaltyA032F3A1           = "frequency_penalty"
	valueGenerationConfig8EB05844           = "generationConfig"
	valueHelloEE9FC27E                      = "hello"
	valueHighD260C831                       = "high"
	valueInMemory1FA8DAAA                   = "in-memory"
	valueIncludeEE400095                    = "include"
	valueIncludeThoughts80DC204C            = "includeThoughts"
	valueIncludeThoughts5FF82B9D            = "include_thoughts"
	valueIncludeUsage5E9B8ABF               = "include_usage"
	valueInput5B3C51F4                      = "input"
	valueInputText5C80732A                  = "input_text"
	valueInstructions7609DA2E               = "instructions"
	valueJsonA18C6EEB                       = "json"
	valueJsonObject2DBE4D4A                 = "json_object"
	valueJsonSchema6579F439                 = "json_schema"
	valueLogprobs649B31BD                   = "logprobs"
	valueLow9925FBD7                        = "low"
	valueMaxOutputTokens4CE535AC            = "maxOutputTokens"
	valueMaxCompletionTokensB564C8E0        = "max_completion_tokens"
	valueMaxOutputTokens849E4630            = "max_output_tokens"
	valueMaxTokensC696F5D6                  = "max_tokens"
	valueMemorySaveB5407D87                 = "memory.save"
	valueMessageA35DE1E0                    = "message"
	valueMessages84CF5AF0                   = "messages"
	valueMetadata7B48D589                   = "metadata"
	valueModel191CB21F                      = "model"
	valueObject93F91238                     = "object"
	valueOmitted5D064241                    = "omitted"
	valueOverride76742FCF                   = "override"
	valuePmpt123CA6C2D32                    = "pmpt_123"
	valuePresencePenalty75E93E70            = "presence_penalty"
	valuePriorityFD0A2E1A                   = "priority"
	valuePrompt5E27DBCE                     = "prompt"
	valuePromptCacheAA3C7767                = "prompt_cache"
	valuePropertiesEB0801DD                 = "properties"
	valueReasoning5BCA7A7C                  = "reasoning"
	valueReasoningEncryptedContent7FEFEAFD  = "reasoning.encrypted_content"
	valueResponseMimeTypeDDF1A58A           = "responseMimeType"
	valueResponseLogprobsE76AF6B4           = "response_logprobs"
	valueSeedF601F196                       = "seed"
	valueStopSequences49A5E276              = "stopSequences"
	valueStream7534BDDA                     = "stream"
	valueStreamOptionsBCDF09B6              = "stream_options"
	valueStrictA33B9FE3                     = "strict"
	valueSystem5F0CEAEC                     = "system"
	valueTemperatureD5412760                = "temperature"
	valueTextEFD60C8A                       = "text"
	valueThinking0C77F201                   = "thinking"
	valueThinkingBudgetCDB10200             = "thinkingBudget"
	valueThinkingConfig98104B00             = "thinkingConfig"
	valueThinkingBudget78D0050C             = "thinking_budget"
	valueThinkingLevelBE370EB3              = "thinking_level"
	valueTool18FAFDB8                       = "tool"
	valueTools794D5C06                      = "tools"
	valueTopK3DF8C9DA                       = "topK"
	valueTopP3BA0FFF3                       = "topP"
	valueTopK7EE01BEF                       = "top_k"
	valueTopPF4196172                       = "top_p"
	valueWebSearch923D4575                  = "web_search"
	valueWebSearch20250305386AE0B8          = "web_search_20250305"
	valueWebSearchCallActionSources8CF2A2B9 = "web_search_call.action.sources"
	valueXSearch7796EE5B                    = "x_search"
)

func TestBuildOpenAIChatCompletionsMinimalStreamRequestIncludesUsage(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "gpt-5", EndpointChatCompletions, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
	}, true)

	expectedKeys := map[string]struct{}{
		valueModel191CB21F:         {},
		valueMessages84CF5AF0:      {},
		valueStream7534BDDA:        {},
		valueStreamOptionsBCDF09B6: {},
	}
	assertOnlyPayloadKeys(t, payload, expectedKeys)
	streamOptions, ok := payload[valueStreamOptionsBCDF09B6].(map[string]interface{})
	if !ok || streamOptions[valueIncludeUsage5E9B8ABF] != true {
		t.Fatalf("expected stream usage enabled by default, got %#v", payload[valueStreamOptionsBCDF09B6])
	}
}

func TestBuildOpenAIResponsesForwardsGPT56ReasoningAndExplicitPromptCache(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5.6-sol", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Options: map[string]interface{}{
			"reasoning": map[string]interface{}{
				"context":          "all_turns",
				"effort":           testValueMedium,
				interactionKeyMode: "pro",
			},
			"prompt_cache_key": "gaoge_ctx_gpt56",
			"prompt_cache_options": map[string]interface{}{
				interactionKeyMode: "explicit",
				"ttl":              "30m",
			},
		},
	}, false)

	reasoning, ok := payload["reasoning"].(map[string]interface{})
	requireCondition(t, ok, "expected reasoning object, got %#v", payload["reasoning"])
	requireEqual(t, reasoning["context"], "all_turns", "expected reasoning.context, got %#v", reasoning["context"])
	requireEqual(t, reasoning["effort"], "medium", "expected reasoning.effort, got %#v", reasoning["effort"])
	requireEqual(t, reasoning["mode"], "pro", "expected reasoning.mode, got %#v", reasoning["mode"])
	requireEqual(t, payload["prompt_cache_key"], "gaoge_ctx_gpt56", "expected prompt_cache_key, got %#v", payload["prompt_cache_key"])
	cache, ok := payload["prompt_cache_options"].(map[string]interface{})
	requireCondition(t, ok, "expected prompt_cache_options object, got %#v", payload["prompt_cache_options"])
	requireEqual(t, cache[interactionKeyMode], "explicit", "expected prompt cache mode, got %#v", cache[interactionKeyMode])
	requireEqual(t, cache["ttl"], "30m", "expected prompt cache ttl, got %#v", cache["ttl"])
}

func TestBuildOpenAIResponsesMinimalRequestHasOnlyProtocolDefaults(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
	}, true)

	expectedKeys := map[string]struct{}{
		valueModel191CB21F:   {},
		valueInput5B3C51F4:   {},
		valueStream7534BDDA:  {},
		valueIncludeEE400095: {},
	}
	assertOnlyPayloadKeys(t, payload, expectedKeys)
	include, ok := payload[valueIncludeEE400095].([]string)
	if !ok || len(include) != 1 || include[0] != valueReasoningEncryptedContent7FEFEAFD {
		t.Fatalf("expected responses encrypted reasoning include only, got %#v", payload[valueIncludeEE400095])
	}
}

func TestBuildOpenAIResponsesOmitsChatCompletionTokenAliases(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Options: map[string]interface{}{
			valueMaxOutputTokens849E4630:     64,
			valueMaxCompletionTokensB564C8E0: 64,
			valueMaxTokensC696F5D6:           64,
		},
	}, false)

	requireEqual(t, payload[valueMaxOutputTokens849E4630], 64, "expected max_output_tokens, got %#v", payload[valueMaxOutputTokens849E4630])
	if _, ok := payload[valueMaxCompletionTokensB564C8E0]; ok {
		t.Fatalf("expected Responses payload to omit max_completion_tokens, got %#v", payload)
	}
	if _, ok := payload[valueMaxTokensC696F5D6]; ok {
		t.Fatalf("expected Responses payload to omit max_tokens, got %#v", payload)
	}
}

func TestBuildOpenAIResponsesForwardsPromptCacheKey(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Options: map[string]interface{}{
			"prompt_cache_key": "gaoge_ctx_test",
		},
	}, false)

	requireEqual(t, payload["prompt_cache_key"], "gaoge_ctx_test", "expected prompt_cache_key, got %#v", payload["prompt_cache_key"])
}

func TestBuildOpenAIResponsesUsesOutputTextForAssistantHistory(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5", EndpointResponses, GenerateInput{
		Messages: []Message{
			{Role: valueSystem5F0CEAEC, Content: "follow the rules"},
			{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E},
			{Role: valueAssistant55D42475, Content: "hi there"},
			{
				Role: valueAssistant55D42475,
				Parts: []ContentPart{
					{Kind: ContentPartText, Text: "part answer"},
				},
			},
			{Role: valueUser2B36BE02, Content: "next"},
		},
	}, true)

	inputItems, ok := payload[valueInput5B3C51F4].([]map[string]interface{})
	if !ok || len(inputItems) != 5 {
		t.Fatalf("expected responses input items, got %#v", payload[valueInput5B3C51F4])
	}
	expectedTypes := []string{valueInputText5C80732A, valueInputText5C80732A, valueOutputTextF077F256, valueOutputTextF077F256, valueInputText5C80732A}
	for i, expected := range expectedTypes {
		content, ok := inputItems[i]["content"].([]map[string]interface{})
		if !ok || len(content) != 1 {
			t.Fatalf("expected content item at %d, got %#v", i, inputItems[i]["content"])
		}
		if content[0][valueType246D1704] != expected {
			t.Fatalf("expected content type %q at %d, got %#v", expected, i, content[0][valueType246D1704])
		}
	}
}

func TestBuildOpenRouterResponsesAddsRequiredHistoryMessageFields(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenRouterResponses, "openai/gpt-oss-120b:free", EndpointResponses, GenerateInput{
		Messages: []Message{
			{Role: valueUser2B36BE02, Content: "你好"},
			{Role: valueAssistant55D42475, Content: "你好！有什么我可以帮忙的吗？"},
			{Role: valueUser2B36BE02, Content: "你是谁？"},
		},
	}, true)
	assertOpenRouterResponsesHistoryPayload(t, payload)
}

func assertOpenRouterResponsesHistoryPayload(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	inputItems, ok := payload[valueInput5B3C51F4].([]map[string]interface{})
	requireCondition(t, ok && len(inputItems) == 3, "expected three openrouter responses input items, got %#v", payload[valueInput5B3C51F4])
	requireCondition(t, inputItems[0][valueType246D1704] == valueMessageA35DE1E0 && inputItems[0]["role"] == valueUser2B36BE02, "expected user message item, got %#v", inputItems[0])
	assistant := inputItems[1]
	requireCondition(t, assistant[valueType246D1704] == valueMessageA35DE1E0 && assistant["role"] == valueAssistant55D42475, "expected assistant message item, got %#v", assistant)
	requireCondition(t, assistant["id"] != "" && assistant["status"] == valueCompletedC0D53520, "expected assistant id/status for openrouter history, got %#v", assistant)
	content, ok := assistant["content"].([]map[string]interface{})
	requireCondition(t, ok && len(content) > 0, "expected assistant content, got %#v", assistant)
	requireEqual(t, content[0][valueType246D1704], valueOutputTextF077F256, "expected output_text content, got %#v", content[0])
	if _, ok := payload[valueIncludeEE400095]; ok {
		t.Fatalf("expected no openai-only default include for openrouter responses, got %#v", payload[valueIncludeEE400095])
	}
}

func TestBuildOpenAIResponsesPlaylistHistoryMatchesOfficialContentTypes(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5.5", EndpointResponses, GenerateInput{
		Messages: []Message{
			{Role: valueUser2B36BE02, Content: "这是我最近听的歌，你根据文件告诉我不同歌手的占比"},
			{Role: valueAssistant55D42475, Content: "根据你文件里的 11 首歌，我按“歌手参与次数”统计。"},
			{Role: valueUser2B36BE02, Content: "<ctx>\n<files>\n<file name=\"My_FreeText_Playlist_Missing.csv\">Track name\tArtist name\n\t天后 (Live) - 薛之谦</file>\n</files>\n</ctx>\n\n<q>推荐一些流行的哥</q>"},
		},
		Options: map[string]interface{}{
			valueIncludeEE400095: []interface{}{valueReasoningEncryptedContent7FEFEAFD},
		},
	}, true)

	inputItems, ok := payload[valueInput5B3C51F4].([]map[string]interface{})
	if !ok || len(inputItems) != 3 {
		t.Fatalf("expected three responses input items, got %#v", payload[valueInput5B3C51F4])
	}
	expectedRoles := []string{valueUser2B36BE02, valueAssistant55D42475, valueUser2B36BE02}
	expectedTypes := []string{valueInputText5C80732A, valueOutputTextF077F256, valueInputText5C80732A}
	for i := range expectedRoles {
		if inputItems[i]["role"] != expectedRoles[i] {
			t.Fatalf("expected role %q at %d, got %#v", expectedRoles[i], i, inputItems[i])
		}
		content, ok := inputItems[i]["content"].([]map[string]interface{})
		if !ok || len(content) != 1 {
			t.Fatalf("expected single content item at %d, got %#v", i, inputItems[i]["content"])
		}
		if content[0][valueType246D1704] != expectedTypes[i] {
			t.Fatalf("expected content type %q at %d, got %#v", expectedTypes[i], i, content[0])
		}
	}
}

func TestBuildAnthropicMinimalRequestHasOnlyRequiredDefaults(t *testing.T) {
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4", GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
	}, true)

	expectedKeys := map[string]struct{}{
		valueModel191CB21F:     {},
		valueMaxTokensC696F5D6: {},
		valueMessages84CF5AF0:  {},
		valueStream7534BDDA:    {},
		"cache_control":        {},
	}
	assertOnlyPayloadKeys(t, payload, expectedKeys)
	if payload[valueMaxTokensC696F5D6] != anthropicDefaultMaxTokens {
		t.Fatalf("expected default max_tokens, got %#v", payload[valueMaxTokensC696F5D6])
	}
	cacheControl, ok := payload["cache_control"].(map[string]interface{})
	if !ok || cacheControl[valueType246D1704] != valueEphemeral34FB8464 {
		t.Fatalf("expected default cache_control, got %#v", payload["cache_control"])
	}
}

func assertOnlyPayloadKeys(t *testing.T, payload map[string]interface{}, expected map[string]struct{}) {
	t.Helper()
	if len(payload) != len(expected) {
		t.Fatalf("expected only keys %v, got %#v", mapKeys(expected), payload)
	}
	for key := range expected {
		if _, ok := payload[key]; !ok {
			t.Fatalf("expected key %q in payload %#v", key, payload)
		}
	}
}

func mapKeys(items map[string]struct{}) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	return keys
}

func TestBuildOpenAIChatCompletionsRequestBodyPromptCacheRetention(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "gpt-5", EndpointChatCompletions, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Options: map[string]interface{}{
			valueMaxOutputTokens849E4630:      2048,
			valuePromptCacheRetention8CA8CD4C: valueTextA9C18C6C,
			valueTemperatureD5412760:          0.2,
			valueTopPF4196172:                 0.8,
			valueFrequencyPenaltyA032F3A1:     0.4,
			valuePresencePenalty75E93E70:      0.3,
			valueSeedF601F196:                 1234,
			valueStopA99407D1:                 []interface{}{valueEND4BD1AC14, "STOP"},
			valueResponseFormat8FC81B36:       valueJsonA18C6EEB,
		},
	}, false)
	assertOpenAIChatPromptCachePayload(t, payload)
}

func assertOpenAIChatPromptCachePayload(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	requireEqual(t, payload[valueMaxCompletionTokensB564C8E0], 2048, "expected max_completion_tokens, got %#v", payload[valueMaxCompletionTokensB564C8E0])
	requireEqual(t, payload[valueTemperatureD5412760], 0.2, "expected temperature=0.2, got %#v", payload[valueTemperatureD5412760])
	requireEqual(t, payload[valueTopPF4196172], 0.8, "expected top_p=0.8, got %#v", payload[valueTopPF4196172])
	requireEqual(t, payload[valueFrequencyPenaltyA032F3A1], 0.4, "expected frequency_penalty=0.4, got %#v", payload[valueFrequencyPenaltyA032F3A1])
	requireEqual(t, payload[valuePresencePenalty75E93E70], 0.3, "expected presence_penalty=0.3, got %#v", payload[valuePresencePenalty75E93E70])
	requireEqual(t, payload[valueSeedF601F196], 1234, "expected seed=1234, got %#v", payload[valueSeedF601F196])
	requireEqual(t, payload[valuePromptCacheRetention8CA8CD4C], valueTextA9C18C6C, "expected prompt_cache_retention=24h, got %#v", payload[valuePromptCacheRetention8CA8CD4C])
	stops, ok := payload[valueStopA99407D1].([]string)
	requireCondition(t, ok && len(stops) == 2 && stops[0] == valueEND4BD1AC14 && stops[1] == "STOP", "expected stop sequence list, got %#v", payload[valueStopA99407D1])
	responseFormat, ok := payload[valueResponseFormat8FC81B36].(map[string]string)
	requireCondition(t, ok && responseFormat[valueType246D1704] == valueJsonObject2DBE4D4A, "expected json_object response_format, got %#v", payload[valueResponseFormat8FC81B36])
}

func TestBuildOpenAIChatCompletionsProviderOptionsMergeAndProtectSystemFields(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "gpt-5", EndpointChatCompletions, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Options: map[string]interface{}{
			"service_tier": valuePriorityFD0A2E1A,
			valueMetadata7B48D589: map[string]interface{}{
				"tenant": "gaoge",
			},
			valueModel191CB21F:         "attacker-model",
			valueMessages84CF5AF0:      []interface{}{},
			valueStream7534BDDA:        false,
			valueStreamOptionsBCDF09B6: map[string]interface{}{valueIncludeUsage5E9B8ABF: false},
		},
	}, true)

	if payload[valueModel191CB21F] != "gpt-5" {
		t.Fatalf("expected protected model, got %#v", payload[valueModel191CB21F])
	}
	if payload[valueStream7534BDDA] != true {
		t.Fatalf("expected protected stream=true, got %#v", payload[valueStream7534BDDA])
	}
	streamOptions, ok := payload[valueStreamOptionsBCDF09B6].(map[string]interface{})
	if !ok || streamOptions[valueIncludeUsage5E9B8ABF] != false {
		t.Fatalf("expected provider stream_options, got %#v", payload[valueStreamOptionsBCDF09B6])
	}
	if payload["service_tier"] != valuePriorityFD0A2E1A {
		t.Fatalf("expected provider option service_tier, got %#v", payload["service_tier"])
	}
	metadata, ok := payload[valueMetadata7B48D589].(map[string]interface{})
	if !ok || metadata["tenant"] != "gaoge" {
		t.Fatalf("expected provider metadata merge, got %#v", payload[valueMetadata7B48D589])
	}
}

func TestBuildOpenAIChatCompletionsStructuredOutputsAndVerbosity(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "gpt-5.1", EndpointChatCompletions, GenerateInput{
		Messages: []Message{{Role: "developer", Content: "Return valid JSON."}, {Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Options: map[string]interface{}{
			valueVerbosity9061A658: valueLow9925FBD7,
			valueResponseFormat8FC81B36: map[string]interface{}{
				valueType246D1704:   valueJsonSchema6579F439,
				valueName9C913FDF:   valueAnswerC6D9EEFD,
				valueSchema88EB438D: map[string]interface{}{valueType246D1704: valueObject93F91238},
				valueStrictA33B9FE3: true,
			},
		},
	}, false)
	assertOpenAIChatStructuredOutputPayload(t, payload)
}

func assertOpenAIChatStructuredOutputPayload(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	requireEqual(t, payload[valueVerbosity9061A658], valueLow9925FBD7, "expected chat verbosity=low, got %#v", payload[valueVerbosity9061A658])
	messages, ok := payload[valueMessages84CF5AF0].([]map[string]interface{})
	requireCondition(t, ok && len(messages) > 0, "expected chat messages, got %#v", payload)
	requireEqual(t, messages[0]["role"], "developer", "expected developer role to be preserved, got %#v", messages[0])
	format, ok := payload[valueResponseFormat8FC81B36].(map[string]interface{})
	requireCondition(t, ok && format[valueType246D1704] == valueJsonSchema6579F439, "expected chat response_format json_schema, got %#v", payload[valueResponseFormat8FC81B36])
	jsonSchema, ok := format[valueJsonSchema6579F439].(map[string]interface{})
	requireCondition(t, ok && jsonSchema[valueName9C913FDF] == valueAnswerC6D9EEFD && jsonSchema[valueStrictA33B9FE3] == true, "expected chat json_schema wrapper, got %#v", format[valueJsonSchema6579F439])
	if _, ok := format[valueSchema88EB438D]; ok {
		t.Fatalf("expected schema under json_schema wrapper, got %#v", format)
	}
}

func TestBuildOpenAIChatCompletionsNativeToolOptions(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "mimo-v2.5-pro", EndpointChatCompletions, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: "please introduce Jun Lei"}},
		Options: map[string]interface{}{
			valueTools794D5C06: []interface{}{
				map[string]interface{}{
					valueType246D1704: valueWebSearch923D4575,
					"max_keyword":     float64(3),
					"force_search":    true,
					"limit":           float64(1),
					"user_location": map[string]interface{}{
						valueType246D1704: "approximate",
						"country":         "China",
						"region":          "Hubei",
						"city":            "Wuhan",
					},
				},
			},
			valueMaxCompletionTokensB564C8E0: float64(1024),
			valueTemperatureD5412760:         float64(1),
			valueTopPF4196172:                float64(0.95),
			valueStopA99407D1:                nil,
			valueFrequencyPenaltyA032F3A1:    float64(0),
			valuePresencePenalty75E93E70:     float64(0),
			valueThinking0C77F201: map[string]interface{}{
				valueType246D1704: valueDisabledA7F0C0E2,
			},
		},
	}, true)

	if payload[valueMaxCompletionTokensB564C8E0] != 1024 {
		t.Fatalf("expected max_completion_tokens=1024, got %#v", payload[valueMaxCompletionTokensB564C8E0])
	}
	if payload[valueStopA99407D1] != nil {
		t.Fatalf("expected stop=null, got %#v", payload[valueStopA99407D1])
	}
	streamOptions, ok := payload[valueStreamOptionsBCDF09B6].(map[string]interface{})
	if !ok || streamOptions[valueIncludeUsage5E9B8ABF] != true {
		t.Fatalf("expected stream usage enabled by default, got %#v", payload[valueStreamOptionsBCDF09B6])
	}
	thinking, ok := payload[valueThinking0C77F201].(map[string]interface{})
	if !ok || thinking[valueType246D1704] != valueDisabledA7F0C0E2 {
		t.Fatalf("expected native thinking config, got %#v", payload[valueThinking0C77F201])
	}
	tools, ok := payload[valueTools794D5C06].([]map[string]interface{})
	if !ok || len(tools) != 1 || tools[0][valueType246D1704] != valueWebSearch923D4575 {
		t.Fatalf("expected native web_search tool, got %#v", payload[valueTools794D5C06])
	}
}

func TestBuildOpenAIResponsesRequestBodyWebSearchAndPromptCacheRetention(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Options: map[string]interface{}{
			valueMaxOutputTokens849E4630:      2048,
			valuePromptCacheRetention8CA8CD4C: valueInMemory1FA8DAAA,
			valueTemperatureD5412760:          0.3,
			valueTopPF4196172:                 0.7,
			valueVerbosity9061A658:            valueHighD260C831,
			valueResponseFormat8FC81B36:       valueJsonA18C6EEB,
			valueWebSearch923D4575:            true,
		},
	}, false)
	assertOpenAIResponsesWebSearchPayload(t, payload)
}

func assertOpenAIResponsesWebSearchPayload(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	requireEqual(t, payload[valueMaxOutputTokens849E4630], 2048, "expected max_output_tokens, got %#v", payload[valueMaxOutputTokens849E4630])
	requireEqual(t, payload[valueTemperatureD5412760], 0.3, "expected temperature=0.3, got %#v", payload[valueTemperatureD5412760])
	requireEqual(t, payload[valueTopPF4196172], 0.7, "expected top_p=0.7, got %#v", payload[valueTopPF4196172])
	text, ok := payload[valueTextEFD60C8A].(map[string]interface{})
	requireCondition(t, ok, "expected text config, got %#v", payload[valueTextEFD60C8A])
	requireEqual(t, text[valueVerbosity9061A658], valueHighD260C831, "expected verbosity=high, got %#v", text[valueVerbosity9061A658])
	format, ok := text["format"].(map[string]string)
	requireCondition(t, ok && format[valueType246D1704] == valueJsonObject2DBE4D4A, "expected text.format=json_object, got %#v", text["format"])
	tools, ok := payload[valueTools794D5C06].([]map[string]interface{})
	requireCondition(t, ok && len(tools) == 1 && tools[0][valueType246D1704] == valueWebSearch923D4575, "expected web_search tool, got %#v", payload[valueTools794D5C06])
	include, ok := payload[valueIncludeEE400095].([]string)
	requireCondition(t, ok && len(include) == 2 && include[0] == valueReasoningEncryptedContent7FEFEAFD && include[1] == valueWebSearchCallActionSources8CF2A2B9, "expected web search sources include, got %#v", payload[valueIncludeEE400095])
	requireEqual(t, payload[valuePromptCacheRetention8CA8CD4C], valueInMemory1FA8DAAA, "expected prompt_cache_retention=in-memory, got %#v", payload[valuePromptCacheRetention8CA8CD4C])
}

func TestBuildOpenAIResponsesRequestBodyMergesIncludeReasoningAndStructuredText(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: "return json"}},
		Options: map[string]interface{}{
			valueIncludeEE400095: []interface{}{"file_search_call.results"},
			"reasoning_effort":   "medium",
			"reasoning_summary":  valueAuto84006CFC,
			valueResponseFormat8FC81B36: map[string]interface{}{
				valueType246D1704:   valueJsonSchema6579F439,
				valueName9C913FDF:   valueAnswerC6D9EEFD,
				valueSchema88EB438D: map[string]interface{}{valueType246D1704: valueObject93F91238},
				valueStrictA33B9FE3: true,
			},
		},
	}, true)
	assertOpenAIResponsesIncludeAndStructuredText(t, payload)
}

func assertOpenAIResponsesIncludeAndStructuredText(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	include, ok := payload[valueIncludeEE400095].([]string)
	requireCondition(t, ok && len(include) == 2 && include[0] == valueReasoningEncryptedContent7FEFEAFD && include[1] == "file_search_call.results", "expected default and configured include values, got %#v", payload[valueIncludeEE400095])
	reasoning, ok := payload[valueReasoning5BCA7A7C].(map[string]interface{})
	requireCondition(t, ok && reasoning[valueEffortC1E1EB4B] == "medium" && reasoning["summary"] == valueAuto84006CFC, "expected reasoning effort and summary, got %#v", payload[valueReasoning5BCA7A7C])
	text, ok := payload[valueTextEFD60C8A].(map[string]interface{})
	requireCondition(t, ok, "expected text config, got %#v", payload[valueTextEFD60C8A])
	format, ok := text["format"].(map[string]interface{})
	requireCondition(t, ok && format[valueType246D1704] == valueJsonSchema6579F439 && format[valueName9C913FDF] == valueAnswerC6D9EEFD && format[valueStrictA33B9FE3] == true, "expected response_format mapped to text.format, got %#v", text["format"])
	if _, ok := payload[valueResponseFormat8FC81B36]; ok {
		t.Fatalf("expected responses response_format to stay under text.format, got %#v", payload[valueResponseFormat8FC81B36])
	}
}

func TestBuildOpenAIResponsesNestedProviderOptionsMergeAndOfficialFields(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5", EndpointResponses, GenerateInput{
		RequestID: "req-123",
		Messages:  []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Options: map[string]interface{}{
			valueVerbosity9061A658: valueLow9925FBD7,
			valueTextEFD60C8A: map[string]interface{}{
				"format": map[string]interface{}{valueType246D1704: valueJsonSchema6579F439},
			},
			valueMetadata7B48D589: map[string]interface{}{
				"user_tag": valueDebugC1F5C7D0,
			},
			valueStreamOptionsBCDF09B6: map[string]interface{}{
				valueIncludeUsage5E9B8ABF: true,
				"include_obfuscation":     true,
			},
			valueInput5B3C51F4:        []interface{}{},
			valueInstructions7609DA2E: "official developer instructions",
			valuePrompt5E27DBCE: map[string]interface{}{
				"id": valuePmpt123CA6C2D32,
			},
		},
	}, true)
	assertOpenAIResponsesNestedProviderOptions(t, payload)
}

func assertOpenAIResponsesNestedProviderOptions(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	text, ok := payload[valueTextEFD60C8A].(map[string]interface{})
	requireCondition(t, ok, "expected text config, got %#v", payload[valueTextEFD60C8A])
	requireEqual(t, text[valueVerbosity9061A658], valueLow9925FBD7, "expected normalized verbosity to remain, got %#v", text)
	format, ok := text["format"].(map[string]interface{})
	requireCondition(t, ok && format[valueType246D1704] == valueJsonSchema6579F439, "expected nested text.format merge, got %#v", text["format"])
	metadata, ok := payload[valueMetadata7B48D589].(map[string]interface{})
	requireCondition(t, ok && metadata["user_tag"] == valueDebugC1F5C7D0, "expected official metadata to pass through, got %#v", payload[valueMetadata7B48D589])
	if _, ok := payload[valueInput5B3C51F4].([]map[string]interface{}); !ok {
		t.Fatalf("expected protected input messages, got %#v", payload[valueInput5B3C51F4])
	}
	requireEqual(t, payload[valueInstructions7609DA2E], "official developer instructions", "expected official instructions to pass through, got %#v", payload[valueInstructions7609DA2E])
	prompt, ok := payload[valuePrompt5E27DBCE].(map[string]interface{})
	requireCondition(t, ok && prompt["id"] == valuePmpt123CA6C2D32, "expected official prompt to pass through, got %#v", payload[valuePrompt5E27DBCE])
	streamOptions, ok := payload[valueStreamOptionsBCDF09B6].(map[string]interface{})
	requireCondition(t, ok && streamOptions["include_obfuscation"] == true, "expected official responses stream_options, got %#v", payload[valueStreamOptionsBCDF09B6])
	if _, ok := streamOptions[valueIncludeUsage5E9B8ABF]; ok {
		t.Fatalf("expected chat-only include_usage to be omitted for responses, got %#v", streamOptions)
	}
}

func TestBuildOpenAIResponsesUsesManagedInstructions(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5", EndpointResponses, GenerateInput{
		Messages:     []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Instructions: "managed developer instructions",
		Options: map[string]interface{}{
			valueInstructions7609DA2E: "provider override",
			valueMetadata7B48D589:     map[string]interface{}{"trace": "ok"},
			valuePrompt5E27DBCE:       map[string]interface{}{"id": valuePmpt123CA6C2D32},
		},
	}, true)

	if payload[valueInstructions7609DA2E] != "managed developer instructions" {
		t.Fatalf("expected managed instructions, got %#v", payload[valueInstructions7609DA2E])
	}
	metadata, ok := payload[valueMetadata7B48D589].(map[string]interface{})
	if !ok || metadata["trace"] != "ok" {
		t.Fatalf("expected metadata to remain available, got %#v", payload[valueMetadata7B48D589])
	}
	prompt, ok := payload[valuePrompt5E27DBCE].(map[string]interface{})
	if !ok || prompt["id"] != valuePmpt123CA6C2D32 {
		t.Fatalf("expected prompt to remain available, got %#v", payload[valuePrompt5E27DBCE])
	}
}

func TestBuildXAIResponsesOmitsUnsupportedSystemMetadata(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterXAIResponses, "grok-4.3", EndpointResponses, GenerateInput{
		RequestID: "req-123",
		Messages:  []Message{{Role: valueUser2B36BE02, Content: "上海今天的天气"}},
		Options: map[string]interface{}{
			valueMetadata7B48D589:     map[string]interface{}{"user_tag": valueDebugC1F5C7D0},
			valueInstructions7609DA2E: "custom instructions",
			valuePrompt5E27DBCE:       map[string]interface{}{"id": valuePmpt123CA6C2D32},
			valueTools794D5C06: []interface{}{
				map[string]interface{}{valueType246D1704: valueXSearch7796EE5B},
			},
		},
	}, true)

	if _, ok := payload[valueMetadata7B48D589]; ok {
		t.Fatalf("expected xAI responses payload to omit unsupported metadata, got %#v", payload[valueMetadata7B48D589])
	}
	if _, ok := payload[valueInstructions7609DA2E]; ok {
		t.Fatalf("expected xAI responses payload to omit unsupported instructions, got %#v", payload[valueInstructions7609DA2E])
	}
	if _, ok := payload[valuePrompt5E27DBCE]; ok {
		t.Fatalf("expected xAI responses payload to omit unsupported prompt, got %#v", payload[valuePrompt5E27DBCE])
	}
	include, ok := payload[valueIncludeEE400095].([]string)
	if !ok || len(include) != 1 || include[0] != valueReasoningEncryptedContent7FEFEAFD {
		t.Fatalf("expected xAI responses encrypted reasoning include, got %#v", payload[valueIncludeEE400095])
	}
	tools, ok := payload[valueTools794D5C06].([]map[string]interface{})
	if !ok || len(tools) != 1 || tools[0][valueType246D1704] != valueXSearch7796EE5B {
		t.Fatalf("expected x_search tool to be preserved, got %#v", payload[valueTools794D5C06])
	}
}

func TestBuildXAIResponsesWebSearchRequestHasNoExtraIncludes(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterXAIResponses, "grok-4.3", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: "今日新闻？"}},
		Options: map[string]interface{}{
			valueTools794D5C06: []interface{}{
				map[string]interface{}{valueType246D1704: valueWebSearch923D4575},
			},
		},
	}, true)

	include, ok := payload[valueIncludeEE400095].([]string)
	if !ok {
		t.Fatalf("expected include list, got %#v", payload[valueIncludeEE400095])
	}
	expectedInclude := []string{
		valueReasoningEncryptedContent7FEFEAFD,
		valueWebSearchCallActionSources8CF2A2B9,
	}
	if len(include) != len(expectedInclude) {
		t.Fatalf("expected include %v, got %#v", expectedInclude, include)
	}
	for index, value := range expectedInclude {
		if include[index] != value {
			t.Fatalf("expected include[%d]=%q, got %#v", index, value, include)
		}
	}
	for _, key := range []string{valueMetadata7B48D589, valueReasoning5BCA7A7C, valueTextEFD60C8A, valueStreamOptionsBCDF09B6, valuePromptCacheRetention8CA8CD4C, valueTemperatureD5412760, valueTopPF4196172, valueMaxOutputTokens849E4630} {
		if _, ok := payload[key]; ok {
			t.Fatalf("expected no auto-added %s, got %#v", key, payload[key])
		}
	}
}

func TestBuildXAIResponsesIncludesSupportedNativeToolOutputs(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterXAIResponses, "grok-4.3", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: "搜索新闻"}},
		Options: map[string]interface{}{
			valueIncludeEE400095: []interface{}{"custom.include"},
			valueTools794D5C06: []interface{}{
				map[string]interface{}{valueType246D1704: valueXSearch7796EE5B},
				map[string]interface{}{valueType246D1704: valueWebSearch923D4575},
				map[string]interface{}{valueType246D1704: valueCodeInterpreter59DEE28E},
			},
		},
	}, true)

	include, ok := payload[valueIncludeEE400095].([]string)
	if !ok {
		t.Fatalf("expected include list, got %#v", payload[valueIncludeEE400095])
	}
	tools, ok := payload[valueTools794D5C06].([]map[string]interface{})
	if !ok || len(tools) != 3 || tools[0][valueType246D1704] != valueXSearch7796EE5B || tools[1][valueType246D1704] != valueWebSearch923D4575 || tools[2][valueType246D1704] != valueCodeInterpreter59DEE28E {
		t.Fatalf("expected xAI native tools to be preserved, got %#v", payload[valueTools794D5C06])
	}
	expected := []string{
		valueReasoningEncryptedContent7FEFEAFD,
		valueWebSearchCallActionSources8CF2A2B9,
		"code_interpreter_call.outputs",
		"custom.include",
	}
	if len(include) != len(expected) {
		t.Fatalf("expected include values %v, got %#v", expected, include)
	}
	for index, value := range expected {
		if include[index] != value {
			t.Fatalf("expected include[%d]=%q, got %#v", index, value, include)
		}
	}
}

func TestBuildAnthropicRequestBodyWebSearchAndPromptCache(t *testing.T) {
	longSystem := make([]byte, 4096)
	for i := range longSystem {
		longSystem[i] = 'a'
	}
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4", GenerateInput{
		Messages: []Message{
			{Role: valueSystem5F0CEAEC, Content: string(longSystem)},
			{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E},
		},
		Options: map[string]interface{}{
			valueMaxOutputTokens849E4630: 4096,
			valuePromptCacheAA3C7767:     true,
			valueTemperatureD5412760:     0.5,
			valueTopPF4196172:            0.9,
			valueTopK7EE01BEF:            64,
			valueStopA99407D1:            valueEND4BD1AC14,
			valueWebSearch923D4575:       true,
		},
	}, false)
	assertAnthropicWebSearchPromptCachePayload(t, payload, len(longSystem))
}

func assertAnthropicWebSearchPromptCachePayload(t *testing.T, payload map[string]interface{}, systemLength int) {
	t.Helper()
	requireEqual(t, payload[valueMaxTokensC696F5D6], 4096, "expected max_tokens=4096, got %#v", payload[valueMaxTokensC696F5D6])
	requireEqual(t, payload[valueTemperatureD5412760], 0.5, "expected temperature=0.5, got %#v", payload[valueTemperatureD5412760])
	requireEqual(t, payload[valueTopPF4196172], 0.9, "expected top_p=0.9, got %#v", payload[valueTopPF4196172])
	requireEqual(t, payload[valueTopK7EE01BEF], 64, "expected top_k=64, got %#v", payload[valueTopK7EE01BEF])
	stops, ok := payload["stop_sequences"].([]string)
	requireCondition(t, ok && len(stops) == 1 && stops[0] == valueEND4BD1AC14, "expected stop_sequences, got %#v", payload["stop_sequences"])
	tools, ok := payload[valueTools794D5C06].([]map[string]interface{})
	requireCondition(t, ok && len(tools) == 1 && tools[0][valueType246D1704] == valueWebSearch20250305386AE0B8, "expected anthropic web search tool, got %#v", payload[valueTools794D5C06])
	requireEqual(t, tools[0][valueName9C913FDF], valueWebSearch923D4575, "expected anthropic web search tool name, got %#v", tools[0])
	if _, ok := tools[0]["allowed_callers"]; ok {
		t.Fatalf("expected legacy anthropic web search to avoid direct caller override, got %#v", tools[0])
	}
	system, ok := payload[valueSystem5F0CEAEC].(string)
	requireCondition(t, ok && len(system) == systemLength, "expected plain system string, got %#v", payload[valueSystem5F0CEAEC])
	cacheControl, ok := payload["cache_control"].(map[string]interface{})
	requireCondition(t, ok && cacheControl[valueType246D1704] == valueEphemeral34FB8464, "expected top-level cache_control, got %#v", payload["cache_control"])
}

func TestBuildAnthropicRequestBodyPromptCacheDisabled(t *testing.T) {
	longSystem := make([]byte, 4096)
	for i := range longSystem {
		longSystem[i] = 'a'
	}
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4", GenerateInput{
		Messages: []Message{
			{Role: valueSystem5F0CEAEC, Content: string(longSystem)},
			{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E},
		},
		Options: map[string]interface{}{
			valuePromptCacheAA3C7767: false,
		},
	}, false)

	system, ok := payload[valueSystem5F0CEAEC].(string)
	if !ok || len(system) != len(longSystem) {
		t.Fatalf("expected plain system string when prompt cache disabled, got %#v", payload[valueSystem5F0CEAEC])
	}
	if _, ok := payload["cache_control"]; ok {
		t.Fatalf("expected cache_control omitted when prompt cache disabled, got %#v", payload["cache_control"])
	}
}

func TestBuildAnthropicRequestBodyFastMode(t *testing.T) {
	payload := mustBuildAnthropicRequestBody(t, "claude-opus-4-6", GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Options: map[string]interface{}{
			"speed": valueFastF700BBFA,
		},
	}, false)

	if payload["speed"] != valueFastF700BBFA {
		t.Fatalf("expected speed=fast, got %#v", payload["speed"])
	}
}

func TestBuildAnthropicRequestBodyPromptCacheEnabledByDefault(t *testing.T) {
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4", GenerateInput{
		Messages: []Message{
			{Role: valueSystem5F0CEAEC, Content: "short system"},
			{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E},
		},
	}, false)

	cacheControl, ok := payload["cache_control"].(map[string]interface{})
	if !ok || cacheControl[valueType246D1704] != valueEphemeral34FB8464 {
		t.Fatalf("expected default top-level cache_control, got %#v", payload["cache_control"])
	}
}

func TestBuildAnthropicRequestBodySystemBlockCacheControl(t *testing.T) {
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4", GenerateInput{
		Messages: []Message{
			{Role: valueSystem5F0CEAEC, Content: "stable file context", CacheControl: &CacheControl{Type: valueEphemeral34FB8464}},
			{Role: valueUser2B36BE02, Content: "<ctx><rag>dynamic</rag></ctx><q>hello</q>"},
		},
		Options: map[string]interface{}{
			valueCacheTimeoutE8E0EF1D: "1h",
		},
	}, false)

	system, ok := payload[valueSystem5F0CEAEC].([]map[string]interface{})
	if !ok || len(system) != 1 {
		t.Fatalf("expected system blocks, got %#v", payload[valueSystem5F0CEAEC])
	}
	cacheControl, ok := system[0]["cache_control"].(map[string]interface{})
	if !ok || cacheControl[valueType246D1704] != valueEphemeral34FB8464 || cacheControl["ttl"] != "1h" {
		t.Fatalf("expected system block cache_control ttl=1h, got %#v", system[0])
	}
	if _, ok := payload["cache_control"]; ok {
		t.Fatalf("expected top-level cache_control omitted with explicit block hint, got %#v", payload["cache_control"])
	}
}

func TestBuildAnthropicRequestBodyOpenWebUIAliases(t *testing.T) {
	longSystem := make([]byte, 4096)
	for i := range longSystem {
		longSystem[i] = 'a'
	}
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4-6", GenerateInput{
		Messages: []Message{
			{Role: valueSystem5F0CEAEC, Content: string(longSystem)},
			{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E},
		},
		Options: map[string]interface{}{
			valueMaxTokensC696F5D6:      64000,
			valueEnableThinking4E6D6E90: true,
			"thinking_display":          valueOmitted5D064241,
			valueEffortC1E1EB4B:         "xhigh",
			"enable_cache":              true,
			valueCacheTimeoutE8E0EF1D:   "1h",
		},
	}, false)
	assertAnthropicOpenWebUIAliasesPayload(t, payload, len(longSystem))
}

func assertAnthropicOpenWebUIAliasesPayload(t *testing.T, payload map[string]interface{}, systemLength int) {
	t.Helper()
	requireEqual(t, payload[valueMaxTokensC696F5D6], 64000, "expected max_tokens=64000, got %#v", payload[valueMaxTokensC696F5D6])
	thinking, ok := payload[valueThinking0C77F201].(map[string]interface{})
	requireCondition(t, ok && thinking[valueType246D1704] == valueAdaptive6FD768C8 && thinking["display"] == valueOmitted5D064241, "expected alias thinking config, got %#v", payload[valueThinking0C77F201])
	outputConfig, ok := payload["output_config"].(map[string]interface{})
	requireCondition(t, ok && outputConfig[valueEffortC1E1EB4B] == "xhigh", "expected output_config.effort from alias, got %#v", payload["output_config"])
	system, ok := payload[valueSystem5F0CEAEC].(string)
	requireCondition(t, ok && len(system) == systemLength, "expected plain system string, got %#v", payload[valueSystem5F0CEAEC])
	cacheControl, ok := payload["cache_control"].(map[string]interface{})
	requireCondition(t, ok && cacheControl[valueType246D1704] == valueEphemeral34FB8464 && cacheControl["ttl"] == "1h", "expected top-level cache_control ttl=1h, got %#v", payload["cache_control"])
}

func TestBuildAnthropicRequestBodyAliasOverridesOfficialThinking(t *testing.T) {
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4-6", GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: valueHelloEE9FC27E}},
		Options: map[string]interface{}{
			valueThinking0C77F201:       map[string]interface{}{valueType246D1704: valueDisabledA7F0C0E2, "display": "summarized"},
			valueEnableThinking4E6D6E90: true,
			"thinking_display":          valueOmitted5D064241,
			"output_config":             map[string]interface{}{valueEffortC1E1EB4B: valueHighD260C831},
			valueEffortC1E1EB4B:         "max",
		},
	}, false)

	thinking, ok := payload[valueThinking0C77F201].(map[string]interface{})
	if !ok || thinking[valueType246D1704] != valueAdaptive6FD768C8 || thinking["display"] != valueOmitted5D064241 {
		t.Fatalf("expected alias to override thinking type/display, got %#v", payload[valueThinking0C77F201])
	}
	outputConfig, ok := payload["output_config"].(map[string]interface{})
	if !ok || outputConfig[valueEffortC1E1EB4B] != valueHighD260C831 {
		t.Fatalf("expected explicit output_config.effort to win, got %#v", payload["output_config"])
	}
}

func TestBuildAnthropicRequestBodyStructuredOutputThinkingAndToolChoice(t *testing.T) {
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4-5", GenerateInput{
		Messages: []Message{{Role: valueUser2B36BE02, Content: "return json"}},
		Options: map[string]interface{}{
			valueMaxOutputTokens849E4630: 4096,
			valueThinking0C77F201: map[string]interface{}{
				valueType246D1704:         valueEnabledDFFEEB45,
				valueBudgetTokens7B3D07EE: float64(2048),
			},
			"tool_choice":               valueMemorySaveB5407D87,
			valueResponseFormat8FC81B36: map[string]interface{}{valueType246D1704: valueJsonObject2DBE4D4A},
		},
	}, false)
	assertAnthropicStructuredOutputPayload(t, payload)
}

func assertAnthropicStructuredOutputPayload(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	thinking, ok := payload[valueThinking0C77F201].(map[string]interface{})
	requireCondition(t, ok && thinking[valueType246D1704] == valueEnabledDFFEEB45 && thinking[valueBudgetTokens7B3D07EE] == float64(2048), "expected Anthropic thinking object, got %#v", payload[valueThinking0C77F201])
	toolChoice, ok := payload["tool_choice"].(map[string]interface{})
	requireCondition(t, ok && toolChoice[valueType246D1704] == valueTool18FAFDB8 && toolChoice[valueName9C913FDF] == valueMemorySaveB5407D87, "expected Anthropic named tool_choice, got %#v", payload["tool_choice"])
	outputConfig, ok := payload["output_config"].(map[string]interface{})
	requireCondition(t, ok, "expected output_config, got %#v", payload["output_config"])
	format, ok := outputConfig["format"].(map[string]interface{})
	requireCondition(t, ok && format[valueType246D1704] == valueJsonObject2DBE4D4A, "expected response_format mapped to output_config.format, got %#v", payload["output_config"])
	if _, ok := payload[valueResponseFormat8FC81B36]; ok {
		t.Fatalf("expected response_format not to be sent as top-level Anthropic param, got %#v", payload[valueResponseFormat8FC81B36])
	}
}
