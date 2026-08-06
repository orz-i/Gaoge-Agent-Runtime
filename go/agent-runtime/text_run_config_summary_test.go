package agentruntime

import (
	"encoding/json"
	"testing"
)

const textRunSummaryToolKeysField = "toolKeys"

func TestSummarizeTextRunConfigKeepsCollectionDTOsNonNull(t *testing.T) {
	// Empty free-chat policies still append both runtime control tools.
	summary := summarizeTextRunConfig(effectiveTextRunConfig{}, "")
	payload, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"skillRefs",
		textRunSummaryToolKeysField,
		"localToolKeys",
		"unavailableSkillRefs",
		"unavailableToolKeys",
	} {
		items, ok := document[field].([]any)
		if !ok || len(items) != 0 {
			t.Fatalf("%s = %#v, want an empty JSON array", field, document[field])
		}
	}
	names, ok := document["providerToolNames"].([]any)
	if !ok || len(names) != 2 {
		t.Fatalf("providerToolNames = %#v, want runtime control tools only", document["providerToolNames"])
	}
}

func TestSummarizeTextRunConfigOmitsControlsForExplicitStoryArtifacts(t *testing.T) {
	for _, contract := range []string{testArtifactChangeSet, testArtifactReview} {
		summary := summarizeTextRunConfig(effectiveTextRunConfig{
			Workspace: &WorkspaceSnapshot{ExpectedArtifact: contract, Policy: testWorkspacePolicy(contract)},
			ToolPolicies: []effectiveRunToolPolicy{
				{ToolKey: testStoryGetManifestKey, ModelName: testStoryGetManifestName, ExecutionMode: valueLocalDispatchC00F9A8D},
			},
		}, "")
		if len(summary.ProviderToolNames) != 1 || summary.ProviderToolNames[0] != testStoryGetManifestName {
			t.Fatalf("contract %s providerToolNames = %#v, want story tools only", contract, summary.ProviderToolNames)
		}
	}
	replySummary := summarizeTextRunConfig(effectiveTextRunConfig{
		Workspace: &WorkspaceSnapshot{ExpectedArtifact: valueArtifactReply1A2B3C4D, Policy: testWorkspacePolicy(testReplyContract)},
	}, "")
	if len(replySummary.ProviderToolNames) != 1 || replySummary.ProviderToolNames[0] != runControlAskUser {
		t.Fatalf("reply providerToolNames = %#v, want run_ask_user only", replySummary.ProviderToolNames)
	}
}

func TestSummarizeTextRunConfigProviderToolNamesUseModelNames(t *testing.T) {
	const (
		testStoryGetSelectionKey  = "story.get_selection"
		testStoryGetSelectionName = "story_get_selection"
	)
	summary := summarizeTextRunConfig(effectiveTextRunConfig{
		ToolKeys: []string{testStoryGetManifestKey, testStoryGetSelectionKey},
		ToolPolicies: []effectiveRunToolPolicy{
			{ToolKey: testStoryGetManifestKey, ModelName: testStoryGetManifestName, ExecutionMode: valueLocalDispatchC00F9A8D},
			{ToolKey: testStoryGetSelectionKey, ModelName: testStoryGetSelectionName, ExecutionMode: valueLocalDispatchC00F9A8D},
		},
	}, "")
	if len(summary.ProviderToolNames) != 4 {
		t.Fatalf("providerToolNames = %#v, want policy tools + runtime controls", summary.ProviderToolNames)
	}
	if summary.ProviderToolNames[0] != testStoryGetManifestName || summary.ProviderToolNames[1] != testStoryGetSelectionName {
		t.Fatalf("providerToolNames = %#v, want model function names first", summary.ProviderToolNames)
	}
	if summary.ProviderToolNames[2] != runControlAskUser || summary.ProviderToolNames[3] != runControlPublishOutput {
		t.Fatalf("providerToolNames missing runtime controls: %#v", summary.ProviderToolNames)
	}
	if summary.LocalToolKeys[0] != testStoryGetManifestKey {
		t.Fatalf("localToolKeys should remain catalog keys: %#v", summary.LocalToolKeys)
	}
	if summary.ProviderToolPayloadBytes != 0 {
		t.Fatalf("providerToolPayloadBytes without protocol should be 0, got %d", summary.ProviderToolPayloadBytes)
	}
	if summary.ProviderPayloadObserved {
		t.Fatal("providerPayloadObserved without protocol should be false")
	}
}

func TestSummarizeTextRunConfigMeasuresProviderWirePayload(t *testing.T) {
	const testToolDescription = "story manifest tool"
	summary := summarizeTextRunConfig(effectiveTextRunConfig{
		ToolPolicies: []effectiveRunToolPolicy{
			{ToolKey: testStoryGetManifestKey, ModelName: testStoryGetManifestName, Description: testToolDescription, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), ExecutionMode: valueLocalDispatchC00F9A8D},
		},
	}, protocolGoogleGenerateContent)
	if summary.ProviderToolPayloadBytes <= 0 {
		t.Fatalf("providerToolPayloadBytes = %d, want wire measure > 0", summary.ProviderToolPayloadBytes)
	}
	if !summary.ProviderPayloadObserved {
		t.Fatal("providerPayloadObserved should be true when wire measure succeeds")
	}
	if summary.ToolSchemaBytes <= 0 {
		t.Fatalf("toolSchemaBytes = %d", summary.ToolSchemaBytes)
	}
	// Gemini wraps tools in functionDeclarations; size should be set and final names include controls.
	if len(summary.ProviderToolNames) != 3 {
		t.Fatalf("providerToolNames = %#v", summary.ProviderToolNames)
	}
}
