// Package conversation owns conversation use cases and policy.
package agentruntime

import (
	"testing"
)

const (
	valueAnthropicMessagesACE75DA8 = "anthropic_messages"
	valueHigh1C664D5D              = "high"
	valueOpenaiResponses5FEBF24A   = "openai_responses"
	valueOverride13385125          = "override"
	valueTypeDD5D141B              = "type"
)

const (
	valueTextCA1039B4                  = "1024x1024"
	valueAnthropicA8BF84BF             = "anthropic"
	valueAuto993D732F                  = "auto"
	valueB64JsonDF70BFB1               = "b64_json"
	valueClaudeC77E9FD8                = "claude"
	valueCodeExecution8AA29571         = "code_execution"
	valueEffort941F75BE                = "effort"
	valueGemini50CFE9D9                = "gemini"
	valueGeminiGenerateContent84E8ECBC = "gemini_generate_content"
	valueGrok05C426D3                  = "grok"
	valueImage0F4A1483                 = "image"
	valueLow9A37DEBA                   = "low"
	valueMedium1C46A438                = "medium"
	valueModel887D785E                 = "model"
	valueNameFCC768B9                  = "name"
	valueOpenai06D9A1C9                = "openai"
	valuePriority55AB769A              = "priority"
	valuePrompt5197D752                = "prompt"
	valueQualityE279DDAC               = "quality"
	valueReasoningF71D8C63             = "reasoning"
	valueSearchContextSize28B2ED02     = "search_context_size"
	valueServiceTier9D8E449B           = "service_tier"
	valueStream0E431F91                = "stream"
	valueTemperatureE5A80009           = "temperature"
	valueTextD154430C                  = "text"
	valueThinking6A44E9A1              = "thinking"
	valueTools7DC9A8AC                 = "tools"
	valueUnknownAEB30783               = "unknown"
	valueWebSearchF9D8B2A2             = "web_search"
	valueWebSearchPreviewB3B80B45      = "web_search_preview"
	valueWebp42DA7FBB                  = "webp"
	valueXaiResponsesB8DD0673          = "xai_responses"
)

func TestModelOptionPolicyProtocolKeyNormalizesProviderAliases(t *testing.T) {
	tests := map[string]string{
		"xai":                  valueXaiResponsesB8DD0673,
		valueGrok05C426D3:      valueXaiResponsesB8DD0673,
		valueAnthropicA8BF84BF: valueAnthropicMessagesACE75DA8,
		valueClaudeC77E9FD8:    valueAnthropicMessagesACE75DA8,
		"google":               valueGeminiGenerateContent84E8ECBC,
		valueGemini50CFE9D9:    valueGeminiGenerateContent84E8ECBC,
		valueOpenai06D9A1C9:    valueOpenaiResponses5FEBF24A,
	}

	for protocol, expected := range tests {
		if got := modelOptionPolicyProtocolKey(protocol); got != expected {
			t.Fatalf("expected %s to normalize to %s, got %s", protocol, expected, got)
		}
	}
}

func TestFilterModelOptionsAllowlistUsesDefaultAndProtocolPaths(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueTemperatureE5A80009: 0.7,
		valueServiceTier9D8E449B: "PRIORITY",
		valueModel887D785E:       valueOverride13385125,
		valueReasoningF71D8C63: map[string]interface{}{
			valueEffort941F75BE: valueHigh1C664D5D,
			"summary":           valueAuto993D732F,
			"extra":             true,
		},
		valueTextD154430C: map[string]interface{}{
			"verbosity": valueLow9A37DEBA,
		},
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}, AdapterOpenAIResponses, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  `{"default":["reasoning.effort"]}`,
	})

	if filtered[valueTemperatureE5A80009] != 0.7 {
		t.Fatalf("expected temperature to pass, got %#v", filtered)
	}
	if filtered[valueServiceTier9D8E449B] != valuePriority55AB769A {
		t.Fatalf("expected service_tier to pass, got %#v", filtered)
	}
	if _, ok := filtered[valueModel887D785E]; ok {
		t.Fatalf("expected model to be denied, got %#v", filtered)
	}
	reasoning, ok := filtered[valueReasoningF71D8C63].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning map, got %#v", filtered[valueReasoningF71D8C63])
	}
	if reasoning[valueEffort941F75BE] != valueHigh1C664D5D || reasoning["summary"] != valueAuto993D732F {
		t.Fatalf("expected allowed reasoning fields, got %#v", reasoning)
	}
	if _, ok := reasoning["extra"]; ok {
		t.Fatalf("expected unlisted reasoning.extra to be removed, got %#v", reasoning)
	}
	if _, ok := filtered["stream_options"]; ok {
		t.Fatalf("expected chat-only stream_options to be removed for responses, got %#v", filtered)
	}
}

func TestFilterModelOptionsAppliesCapabilityDefaultOptions(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueReasoningF71D8C63: map[string]interface{}{
			valueEffort941F75BE: valueHigh1C664D5D,
		},
	}, AdapterOpenAIResponses, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{
			"defaultOptions": {
				"reasoning": {"effort": "medium", "summary": "auto"},
				"text": {"verbosity": "low"},
				"model": "blocked"
			}
		}`,
	})

	reasoning, ok := filtered[valueReasoningF71D8C63].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning map, got %#v", filtered[valueReasoningF71D8C63])
	}
	if reasoning[valueEffort941F75BE] != valueHigh1C664D5D || reasoning["summary"] != valueAuto993D732F {
		t.Fatalf("expected explicit option to override default and default summary to remain, got %#v", reasoning)
	}
	text, ok := filtered[valueTextD154430C].(map[string]interface{})
	if !ok {
		t.Fatalf("expected text map, got %#v", filtered[valueTextD154430C])
	}
	if text["verbosity"] != valueLow9A37DEBA {
		t.Fatalf("expected default text verbosity to pass, got %#v", filtered)
	}
	if _, ok := filtered[valueModel887D785E]; ok {
		t.Fatalf("expected hard-denied default option to be removed, got %#v", filtered)
	}
}

func TestFilterModelOptionsAppliesLockedCapabilityDefaultOptions(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueReasoningF71D8C63: map[string]interface{}{
			valueEffort941F75BE: valueHigh1C664D5D,
		},
		valueTextD154430C: map[string]interface{}{
			"verbosity": valueHigh1C664D5D,
		},
	}, AdapterOpenAIResponses, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{
			"defaultOptions": {
				"reasoning": {"effort": "low"},
				"text": {"verbosity": "low"},
				"previous_response_id": "resp_blocked"
			},
			"lockedOptionPaths": ["reasoning.effort", "text.verbosity", "previous_response_id"]
		}`,
	})

	reasoning, ok := filtered[valueReasoningF71D8C63].(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning map, got %#v", filtered[valueReasoningF71D8C63])
	}
	if reasoning[valueEffort941F75BE] != valueLow9A37DEBA {
		t.Fatalf("expected locked default reasoning effort to override explicit option, got %#v", reasoning)
	}
	text, ok := filtered[valueTextD154430C].(map[string]interface{})
	if !ok {
		t.Fatalf("expected text map, got %#v", filtered[valueTextD154430C])
	}
	if text["verbosity"] != valueLow9A37DEBA {
		t.Fatalf("expected locked default text verbosity to override explicit option, got %#v", text)
	}
	if _, ok := filtered["previous_response_id"]; ok {
		t.Fatalf("expected hard-denied locked default option to be removed, got %#v", filtered)
	}
}

func TestFilterModelOptionsOnlyInjectsDefaultToolsFromDefaultOptions(t *testing.T) {
	allowedOnly := filterModelOptions(nil, AdapterOpenAIResponses, modelOptionPolicyConfig{
		Mode:                  modelOptionPolicyAllowlist,
		AllowedPathsJSON:      DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:       DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{"nativeToolKeys":["openai.web_search_preview"]}`,
	})
	if _, ok := allowedOnly[valueTools7DC9A8AC]; ok {
		t.Fatalf("expected nativeToolKeys to allow but not inject tools, got %#v", allowedOnly)
	}

	withDefault := filterModelOptions(nil, AdapterOpenAIResponses, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{
			"defaultOptions": {
				"tools": [{"type": "web_search_preview", "search_context_size": "low"}]
			}
		}`,
	})
	tools, ok := withDefault[valueTools7DC9A8AC].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected default tool to be injected through capability defaults, got %#v", withDefault)
	}
	if tools[0][valueTypeDD5D141B] != valueWebSearchPreviewB3B80B45 || tools[0][valueSearchContextSize28B2ED02] != valueLow9A37DEBA {
		t.Fatalf("expected default tool parameters to pass, got %#v", tools[0])
	}
}

func TestFilterModelOptionsInjectsLockedDefaultToolsThroughCapabilities(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueTools7DC9A8AC: []interface{}{
			map[string]interface{}{valueTypeDD5D141B: valueWebSearchPreviewB3B80B45, valueSearchContextSize28B2ED02: valueHigh1C664D5D},
		},
	}, AdapterOpenAIResponses, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{
			"defaultOptions": {
				"tools": [{"type": "web_search_preview", "search_context_size": "low"}]
			},
			"lockedOptionPaths": ["tools"]
		}`,
	})

	tools, ok := filtered[valueTools7DC9A8AC].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected locked default tool to be injected through capabilities, got %#v", filtered)
	}
	if tools[0][valueTypeDD5D141B] != valueWebSearchPreviewB3B80B45 || tools[0][valueSearchContextSize28B2ED02] != valueLow9A37DEBA {
		t.Fatalf("expected locked default tool to override explicit tool parameters, got %#v", tools[0])
	}
}

func TestFilterModelOptionsRejectsUnsupportedOpenAIServiceTier(t *testing.T) {
	for _, serviceTier := range []string{valueAuto993D732F, "scale", valueUnknownAEB30783} {
		t.Run(serviceTier, func(t *testing.T) {
			filtered := filterModelOptions(map[string]interface{}{
				valueTemperatureE5A80009: 0.7,
				valueServiceTier9D8E449B: serviceTier,
			}, AdapterOpenAIResponses, modelOptionPolicyConfig{
				Mode:             modelOptionPolicyAllowlist,
				AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
				DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
			})

			if _, ok := filtered[valueServiceTier9D8E449B]; ok {
				t.Fatalf("expected unsupported service_tier to be removed, got %#v", filtered)
			}
			if filtered[valueTemperatureE5A80009] != 0.7 {
				t.Fatalf("expected other allowed options to remain, got %#v", filtered)
			}
		})
	}
}

func TestFilterModelOptionsKeepsOpenRouterChatServiceTierOutOfDefaultAllowlist(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueServiceTier9D8E449B: valuePriority55AB769A,
		"reasoning_effort":       valueHigh1C664D5D,
	}, AdapterOpenRouterChat, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
	})

	if _, ok := filtered[valueServiceTier9D8E449B]; ok {
		t.Fatalf("expected OpenRouter Chat service_tier to stay outside the default allowlist, got %#v", filtered)
	}
	if filtered["reasoning_effort"] != valueHigh1C664D5D {
		t.Fatalf("expected OpenRouter Chat reasoning_effort to pass, got %#v", filtered)
	}
}

func TestFilterModelOptionsDenylistAllowsUnlistedAndRemovesDenied(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueTemperatureE5A80009: 0.2,
		"custom_vendor_option":   true,
		"previous_response_id":   "resp_123",
		valueReasoningF71D8C63: map[string]interface{}{
			valueEffort941F75BE: valueHigh1C664D5D,
		},
	}, AdapterXAIResponses, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyDenylist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  `{"default":["reasoning.effort"]}`,
	})

	if filtered["custom_vendor_option"] != true {
		t.Fatalf("expected custom option to pass in denylist mode, got %#v", filtered)
	}
	if _, ok := filtered["previous_response_id"]; ok {
		t.Fatalf("expected previous_response_id to be hard denied, got %#v", filtered)
	}
	if reasoning, ok := filtered[valueReasoningF71D8C63].(map[string]interface{}); ok {
		if _, ok := reasoning[valueEffort941F75BE]; ok {
			t.Fatalf("expected configured deny path removed, got %#v", filtered)
		}
	}
}

func TestFilterModelOptionsOpenAIChatCompletionsAllowsThinkingType(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueThinking6A44E9A1: map[string]interface{}{
			valueTypeDD5D141B: "enabled",
			"budget_tokens":   1024,
		},
	}, AdapterOpenAIChatCompletions, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
	})

	thinking, ok := filtered[valueThinking6A44E9A1].(map[string]interface{})
	if !ok {
		t.Fatalf("expected thinking map, got %#v", filtered[valueThinking6A44E9A1])
	}
	if thinking[valueTypeDD5D141B] != "enabled" {
		t.Fatalf("expected thinking.type to pass for chat completions, got %#v", filtered)
	}
	if _, ok := thinking["budget_tokens"]; ok {
		t.Fatalf("expected unlisted thinking.budget_tokens to be removed for chat completions, got %#v", filtered)
	}
}

func TestFilterModelOptionsPreservesNativeToolAcrossConfiguredProtocols(t *testing.T) {
	capabilitiesJSON := `{
		"nativeTools": [
			{
				"key": "openai.web_search",
				"protocols": ["openai_chat_completions", "openai_responses"],
				"type": "web_search",
				"enabled": true,
				"payload": {"type": "web_search"}
			}
		]
	}`
	for _, adapter := range []string{AdapterOpenAIChatCompletions, AdapterOpenAIResponses} {
		t.Run(adapter, func(t *testing.T) {
			filtered := filterModelOptions(map[string]interface{}{
				valueTools7DC9A8AC: []interface{}{
					map[string]interface{}{
						valueTypeDD5D141B:              valueWebSearchF9D8B2A2,
						valueSearchContextSize28B2ED02: valueLow9A37DEBA,
					},
				},
			}, adapter, modelOptionPolicyConfig{
				Mode:                  modelOptionPolicyAllowlist,
				AllowedPathsJSON:      `{"default":[]}`,
				DeniedPathsJSON:       DefaultModelOptionDeniedPathsJSON(),
				ModelCapabilitiesJSON: capabilitiesJSON,
			})

			tools, ok := filtered[valueTools7DC9A8AC].([]map[string]interface{})
			if !ok || len(tools) != 1 {
				t.Fatalf("expected one official tool for %s, got %#v", adapter, filtered)
			}
			if tools[0][valueTypeDD5D141B] != valueWebSearchF9D8B2A2 || tools[0][valueSearchContextSize28B2ED02] != valueLow9A37DEBA {
				t.Fatalf("expected web_search parameters to pass for %s, got %#v", adapter, tools[0])
			}
		})
	}
}

func TestFilterModelOptionsDerivesNativeToolKeysFromCapabilityDefaultTools(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		"store": false,
		valueTools7DC9A8AC: []interface{}{
			map[string]interface{}{
				valueTypeDD5D141B:            "x_search",
				"enable_image_understanding": true,
			},
			map[string]interface{}{
				valueTypeDD5D141B:            valueWebSearchF9D8B2A2,
				"enable_image_understanding": true,
			},
			map[string]interface{}{
				valueTypeDD5D141B: "code_interpreter",
				"container": map[string]interface{}{
					valueTypeDD5D141B: valueAuto993D732F,
				},
			},
		},
	}, AdapterXAIResponses, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: `{"default":["store"]}`,
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{
			"defaultOptions": {
				"tools": [
					{"type": "x_search"},
					{"type": "web_search"},
					{"type": "code_interpreter"}
				]
			}
		}`,
	})

	tools, ok := filtered[valueTools7DC9A8AC].([]map[string]interface{})
	if !ok || len(tools) != 3 {
		t.Fatalf("expected native tool keys to be derived from capability default tools, got %#v", filtered)
	}
	if tools[0][valueTypeDD5D141B] != "x_search" || tools[0]["enable_image_understanding"] != true {
		t.Fatalf("expected derived x_search to preserve parameters, got %#v", tools[0])
	}
	if tools[1][valueTypeDD5D141B] != valueWebSearchF9D8B2A2 || tools[1]["enable_image_understanding"] != true {
		t.Fatalf("expected derived web_search to preserve parameters, got %#v", tools[1])
	}
	container, ok := tools[2]["container"].(map[string]interface{})
	if tools[2][valueTypeDD5D141B] != "code_interpreter" || !ok || container[valueTypeDD5D141B] != valueAuto993D732F {
		t.Fatalf("expected derived code_interpreter to preserve parameters, got %#v", tools[2])
	}
}

func TestFilterModelOptionsPreservesNativeToolsForcedByModelCapabilitiesAcrossProtocol(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueServiceTier9D8E449B: valuePriority55AB769A,
		valueTools7DC9A8AC: []interface{}{
			map[string]interface{}{valueTypeDD5D141B: valueWebSearchPreviewB3B80B45, valueSearchContextSize28B2ED02: valueMedium1C46A438},
		},
	}, AdapterOpenAIResponses, modelOptionPolicyConfig{
		Mode:                  modelOptionPolicyAllowlist,
		AllowedPathsJSON:      DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:       DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{"nativeToolKeys":["openai.web_search_preview"]}`,
	})

	if filtered[valueServiceTier9D8E449B] != valuePriority55AB769A {
		t.Fatalf("expected response option to pass, got %#v", filtered)
	}
	tools, ok := filtered[valueTools7DC9A8AC].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected forced native tool to pass across protocol, got %#v", filtered)
	}
	if tools[0][valueTypeDD5D141B] != valueWebSearchPreviewB3B80B45 {
		t.Fatalf("expected canonical web_search_preview tool, got %#v", tools[0])
	}
	if tools[0][valueSearchContextSize28B2ED02] != valueMedium1C46A438 {
		t.Fatalf("expected forced native tool parameters to pass, got %#v", tools[0])
	}
}

func TestFilterModelOptionsGoogleAllowsGenerationConfigAndGoogleSearch(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		"generationConfig": map[string]interface{}{
			valueTemperatureE5A80009: 0.5,
			"maxOutputTokens":        512,
		},
		valueTools7DC9A8AC: []interface{}{
			map[string]interface{}{"google_search": map[string]interface{}{}},
		},
	}, AdapterGoogleGenerateContent, modelOptionPolicyConfig{
		Mode:                  modelOptionPolicyAllowlist,
		AllowedPathsJSON:      DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:       DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{"nativeToolKeys":["google.google_search"]}`,
	})

	assertGoogleGenerationConfig(t, filtered)
	tools := assertSingleGoogleSearchTool(t, filtered)
	if _, ok := tools[0][valueTypeDD5D141B]; ok {
		t.Fatalf("expected google_search tool without type, got %#v", tools)
	}
	if _, ok := tools[0]["google_search"]; !ok {
		t.Fatalf("expected google_search tool, got %#v", tools)
	}
}

func assertGoogleGenerationConfig(t *testing.T, filtered map[string]interface{}) map[string]interface{} {
	t.Helper()
	generationConfig, ok := filtered["generationConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected generationConfig map, got %#v", filtered)
	}
	if generationConfig[valueTemperatureE5A80009] != 0.5 || generationConfig["maxOutputTokens"] != 512 {
		t.Fatalf("expected text generation config, got %#v", generationConfig)
	}
	return generationConfig
}

func assertSingleGoogleSearchTool(t *testing.T, filtered map[string]interface{}) []map[string]interface{} {
	t.Helper()
	tools, ok := filtered[valueTools7DC9A8AC].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one normalized google_search tool, got %#v", tools)
	}
	return tools
}

func TestFilterModelOptionsPreservesGoogleSearchParameters(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueTools7DC9A8AC: []interface{}{
			map[string]interface{}{
				"google_search": map[string]interface{}{
					"searchTypes": map[string]interface{}{
						"webSearch":   map[string]interface{}{},
						"imageSearch": map[string]interface{}{},
					},
				},
			},
		},
	}, AdapterGoogleGenerateContent, modelOptionPolicyConfig{
		Mode:                  modelOptionPolicyAllowlist,
		AllowedPathsJSON:      DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:       DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{"nativeToolKeys":["google.google_search"]}`,
	})

	tools, ok := filtered[valueTools7DC9A8AC].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected google_search tool, got %#v", filtered)
	}
	googleSearch, ok := tools[0]["google_search"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected google_search object, got %#v", tools[0])
	}
	searchTypes, ok := googleSearch["searchTypes"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected searchTypes object, got %#v", googleSearch)
	}
	if _, ok := searchTypes["webSearch"]; !ok {
		t.Fatalf("expected webSearch to pass, got %#v", tools)
	}
	if _, ok := searchTypes["imageSearch"]; !ok {
		t.Fatalf("expected imageSearch to pass, got %#v", tools)
	}
}

func TestFilterModelOptionsPreservesGoogleNativeToolFieldPayloads(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueTools7DC9A8AC: []interface{}{
			map[string]interface{}{valueCodeExecution8AA29571: map[string]interface{}{}},
			map[string]interface{}{"url_context": map[string]interface{}{}},
		},
	}, AdapterGoogleGenerateContent, modelOptionPolicyConfig{
		Mode:                  modelOptionPolicyAllowlist,
		AllowedPathsJSON:      DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:       DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{"nativeToolKeys":["google.code_execution","google.url_context"]}`,
	})

	tools, ok := filtered[valueTools7DC9A8AC].([]map[string]interface{})
	if !ok || len(tools) != 2 {
		t.Fatalf("expected Google native tools, got %#v", filtered)
	}
	for _, key := range []string{valueCodeExecution8AA29571, "url_context"} {
		found := false
		for _, tool := range tools {
			if _, ok := tool[valueTypeDD5D141B]; ok {
				t.Fatalf("expected Google native tool without type, got %#v", tool)
			}
			if _, ok := tool[key]; ok {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected Google %s tool, got %#v", key, tools)
		}
	}
}

func TestFilterModelOptionsMergesGoogleNativeToolEmptyObjectPayloads(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueTools7DC9A8AC: []interface{}{
			map[string]interface{}{valueCodeExecution8AA29571: map[string]interface{}{}},
		},
	}, AdapterGoogleGenerateContent, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
		ModelCapabilitiesJSON: `{
			"nativeTools": [
				{
					"key": "google.code_execution",
					"protocols": ["gemini_generate_content"],
					"type": "code_execution",
					"payload": {"code_execution": {}}
				}
			]
		}`,
	})

	tools, ok := filtered[valueTools7DC9A8AC].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected google code_execution tool, got %#v", filtered)
	}
	if _, ok := tools[0][valueCodeExecution8AA29571].(map[string]interface{}); !ok {
		t.Fatalf("expected code_execution empty object to pass, got %#v", tools[0])
	}
}

func TestFilterModelOptionsOpenAIResponsesAllowsTextParams(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueServiceTier9D8E449B: valuePriority55AB769A,
		"reasoning":              map[string]interface{}{"effort": "medium"},
		valuePrompt5197D752:      valueOverride13385125,
		valueStream0E431F91:      true,
	}, AdapterOpenAIResponses, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
	})

	if filtered[valueServiceTier9D8E449B] != valuePriority55AB769A {
		t.Fatalf("expected response params to pass, got %#v", filtered)
	}
	if _, ok := filtered[valuePrompt5197D752]; ok {
		t.Fatalf("expected prompt override to be hard denied, got %#v", filtered)
	}
	if _, ok := filtered[valueStream0E431F91]; ok {
		t.Fatalf("expected stream override to be hard denied, got %#v", filtered)
	}
}

func TestFilterModelOptionsOpenAIChatAllowsSamplingParams(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		valueTemperatureE5A80009: 0.2,
		"presence_penalty":       0.5,
		valuePrompt5197D752:      valueOverride13385125,
		valueStream0E431F91:      true,
	}, AdapterOpenAIChatCompletions, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
	})

	if filtered[valueTemperatureE5A80009] != 0.2 || filtered["presence_penalty"] != 0.5 {
		t.Fatalf("expected chat params to pass, got %#v", filtered)
	}
	for _, key := range []string{valuePrompt5197D752, valueStream0E431F91} {
		if _, ok := filtered[key]; ok {
			t.Fatalf("expected %s to be hard denied, got %#v", key, filtered)
		}
	}
}

func TestFilterModelOptionsXAIResponsesAllowsReasoningParams(t *testing.T) {
	filtered := filterModelOptions(map[string]interface{}{
		"reasoning":          map[string]interface{}{"effort": "high"},
		valuePrompt5197D752:  valueOverride13385125,
		valueStream0E431F91:  true,
		valueQualityE279DDAC: valueHigh1C664D5D,
	}, AdapterXAIResponses, modelOptionPolicyConfig{
		Mode:             modelOptionPolicyAllowlist,
		AllowedPathsJSON: DefaultModelOptionAllowedPathsJSON(),
		DeniedPathsJSON:  DefaultModelOptionDeniedPathsJSON(),
	})

	reasoning, ok := filtered["reasoning"].(map[string]interface{})
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("expected xAI reasoning params to pass, got %#v", filtered)
	}
	for _, key := range []string{valuePrompt5197D752, valueStream0E431F91, valueQualityE279DDAC} {
		if _, ok := filtered[key]; ok {
			t.Fatalf("expected %s to be removed, got %#v", key, filtered)
		}
	}
}

func TestFilterModelOptionsForwardsRawProviderToolCandidates(t *testing.T) {
	rawTools := []interface{}{
		map[string]interface{}{valueTypeDD5D141B: "web_search_20260209", "max_uses": float64(3), valueModel887D785E: "must-be-filtered-by-gateway"},
		map[string]interface{}{valueTypeDD5D141B: "external_function", valueNameFCC768B9: "client_tool"},
	}
	filtered := filterModelOptions(
		map[string]interface{}{valueTemperatureE5A80009: float64(0.4), valueTools7DC9A8AC: rawTools},
		valueAnthropicMessagesACE75DA8,
		modelOptionPolicyConfig{
			Mode:                  modelOptionPolicyAllowlist,
			AllowedPathsJSON:      `{"anthropic_messages":["temperature"]}`,
			ModelCapabilitiesJSON: `{"nativeToolKeys":[]}`,
		},
	)
	tools := providerToolOptionPayloads(filtered[valueTools7DC9A8AC])
	if len(tools) != 2 {
		t.Fatalf("expected raw tool candidates to be forwarded, got %#v", filtered)
	}
	if tools[0][valueModel887D785E] != "must-be-filtered-by-gateway" || tools[1][valueNameFCC768B9] != "client_tool" {
		t.Fatalf("conversation interpreted provider tool payloads: %#v", tools)
	}
}
