package agentruntime

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

const (
	testStoryPublishChangeSetKey  = "story.publish_change_set"
	testStoryPublishChangeSetName = "story_publish_change_set"
	testStoryGetManifestKey       = "story.get_manifest"
	testStoryGetManifestName      = "story_get_manifest"
	testStoryStagedWrite          = "staged_write"
	testStoryContinuityToolKey    = "story.continuity"
)

func TestApplyWorkspaceToolDefinitionsOverlaysSchemaAndFingerprint(t *testing.T) {
	fullSchema := json.RawMessage(`{"type":"object","properties":{"operations":{"items":{"anyOf":[{"properties":{"type":{"enum":["create_entity"]}}},{"properties":{"type":{"enum":["patch_foundation_fields"]}}}]}}}}`)
	narrowSchema := json.RawMessage(`{"type":"object","required":["title","summary","operations"],"properties":{"operations":{"items":{"anyOf":[{"properties":{"type":{"enum":["patch_foundation_fields"]}}},{"properties":{"type":{"enum":["patch_foundation_blocks"]}}}]}}}}`)
	policy := effectiveRunToolPolicy{
		ToolKey: testStoryPublishChangeSetKey, ProviderKind: testWorkspaceProviderKind, ProviderKey: testWorkspaceProviderKind,
		ModelName: testStoryPublishChangeSetName, OriginalName: testStoryPublishChangeSetName, Description: "catalog full",
		DefinitionVersion: "v1", InputSchema: fullSchema, ExecutionMode: valueLocalDispatch71FF6D47,
		ApprovalCapability: valuePerCall2570116D, ApprovalMode: valueNever4C6E2E88, RiskLevel: valueLow9A37DEBA,
		SideEffectLevel: testStoryStagedWrite, RetryCount: 1, Concurrency: 1,
	}
	policy.Fingerprint = fingerprintRunToolSnapshot(policy)
	before := policy.Fingerprint

	readPolicy := effectiveRunToolPolicy{
		ToolKey: testStoryGetManifestKey, ProviderKind: testWorkspaceProviderKind, ProviderKey: testWorkspaceProviderKind,
		ModelName: testStoryGetManifestName, OriginalName: testStoryGetManifestName, Description: valueRead3A612695,
		DefinitionVersion: "v1", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		ExecutionMode: valueLocalDispatch71FF6D47, ApprovalCapability: valuePerCall2570116D, ApprovalMode: valueNever4C6E2E88,
		RiskLevel: valueLow9A37DEBA, SideEffectLevel: valueRead3A612695, RetryCount: 1, Concurrency: 1,
	}
	readPolicy.Fingerprint = fingerprintRunToolSnapshot(readPolicy)

	tools := []WorkspaceToolDefinition{
		{ToolKey: testStoryPublishChangeSetKey, Name: testStoryPublishChangeSetName, Description: "workspace narrow foundation", InputSchema: narrowSchema, SideEffectLevel: testStoryStagedWrite},
		{ToolKey: testStoryGetManifestKey, Name: testStoryGetManifestName, Description: valueRead3A612695, InputSchema: readPolicy.InputSchema, SideEffectLevel: valueRead3A612695},
	}
	got, err := applyWorkspaceToolDefinitions([]effectiveRunToolPolicy{policy, readPolicy}, tools)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("policies = %d", len(got))
	}
	if got[0].Description != "workspace narrow foundation" {
		t.Fatalf("description = %q", got[0].Description)
	}
	if string(got[0].InputSchema) != string(narrowSchema) {
		t.Fatalf("schema not overlaid: %s", got[0].InputSchema)
	}
	if got[0].Fingerprint == "" || got[0].Fingerprint == before {
		t.Fatalf("fingerprint not recomputed: before=%q after=%q", before, got[0].Fingerprint)
	}
	if strings.Contains(string(got[0].InputSchema), "create_entity") {
		t.Fatalf("full catalog operation leaked into workspace schema: %s", got[0].InputSchema)
	}
	if !strings.Contains(string(got[0].InputSchema), "patch_foundation_fields") {
		t.Fatalf("foundation op missing: %s", got[0].InputSchema)
	}
}

func TestFrozenWorkspaceIsClonedAndValidated(t *testing.T) {
	input := &WorkspaceSnapshot{
		SchemaVersion: RuntimeSnapshotVersion,
		Request:       ResolvedWorkspaceContext{SchemaVersion: RuntimeSnapshotVersion, Type: testWorkspaceProviderKind, ResourceID: "story_1", Revision: 9},
		Revision:      9, SnapshotID: "snapshot_9", StateHash: "state_9", ContentJSON: `{"storyID":"story_1"}`,
		Tools: []WorkspaceToolDefinition{{ToolKey: testStoryGetManifestKey, Name: testStoryGetManifestName, InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	engine := &Engine{}
	got, found, err := engine.compileTextRunWorkspace(t.Context(), StartTextRunInput{ThreadScope: testWorkspaceProviderKind, FrozenWorkspace: input}, "model")
	if err != nil || !found || got.SnapshotID != input.SnapshotID {
		t.Fatalf("frozen workspace = %#v, %t, %v", got, found, err)
	}
	got.Tools[0].InputSchema[0] = '['
	if input.Tools[0].InputSchema[0] != '{' {
		t.Fatal("frozen workspace was not deep cloned")
	}
}

func TestAgentManifestToolsNarrowWorkspaceTools(t *testing.T) {
	got := intersectRuntimeStrings(
		[]string{testAgentStoryReadTool, testPublishToolKey, testStoryContinuityToolKey},
		[]string{testAgentStoryReadTool, testStoryContinuityToolKey, "outside.tool"},
	)
	if !slices.Equal(got, []string{testAgentStoryReadTool, testStoryContinuityToolKey}) {
		t.Fatalf("intersection = %#v", got)
	}
}

func TestApplyWorkspaceToolDefinitionsRequiresEveryWorkspaceTool(t *testing.T) {
	policy := effectiveRunToolPolicy{
		ToolKey: testStoryGetManifestKey, ProviderKind: testWorkspaceProviderKind, ProviderKey: testWorkspaceProviderKind,
		ModelName: testStoryGetManifestName, OriginalName: testStoryGetManifestName, Description: valueRead3A612695,
		DefinitionVersion: "v1", InputSchema: json.RawMessage(`{"type":"object"}`), ExecutionMode: valueLocalDispatch71FF6D47,
		ApprovalCapability: valuePerCall2570116D, ApprovalMode: valueNever4C6E2E88, RiskLevel: valueLow9A37DEBA,
		SideEffectLevel: valueRead3A612695, RetryCount: 0, Concurrency: 1,
	}
	policy.Fingerprint = fingerprintRunToolSnapshot(policy)
	tools := []WorkspaceToolDefinition{
		{ToolKey: testStoryGetManifestKey, Name: testStoryGetManifestName, Description: valueRead3A612695, InputSchema: policy.InputSchema},
		{ToolKey: testStoryPublishChangeSetKey, Name: testStoryPublishChangeSetName, Description: "write", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	if _, err := applyWorkspaceToolDefinitions([]effectiveRunToolPolicy{policy}, tools); !errors.Is(err, ErrRunToolUnavailable) {
		t.Fatalf("error = %v, want ErrRunToolUnavailable", err)
	}
}

func TestWorkspaceToolKeysComeFromProviderContract(t *testing.T) {
	got := workspaceSnapshotToolKeys(&WorkspaceSnapshot{
		Request: ResolvedWorkspaceContext{Type: testWorkspaceProviderKind},
		Tools:   []WorkspaceToolDefinition{{ToolKey: "story.get_selection", Name: "story_get_selection"}, {ToolKey: testStoryPublishChangeSetKey, Name: testStoryPublishChangeSetName}},
	})
	if len(got) != 2 {
		t.Fatalf("keys = %v", got)
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "story.get_selection") || !strings.Contains(joined, testStoryPublishChangeSetKey) {
		t.Fatalf("keys = %v", got)
	}
}
