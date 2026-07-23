package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	testFirewallToolKey  = "mcp.search"
	testFirewallToolName = "search"
)

type recordingToolExecutor struct {
	calls  []ToolExecutionInput
	output string
	err    error
}

func TestResolvedToolCallRejectsInvalidArgumentsBeforeApprovalOrExecution(t *testing.T) {
	repo := &durableFailureTestRepository{}
	executor := &recordingToolExecutor{output: `{"structuredContent":{"ok":true}}`}
	engine := &Engine{
		cfg:               StaticConfigProvider(Config{}),
		repo:              repo,
		toolExecutor:      executor,
		generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{}),
	}
	inputSchema := json.RawMessage(`{
		"type":"object",
		"required":["query"],
		"additionalProperties":false,
		"properties":{"query":{"type":"string","minLength":1}}
	}`)
	tool, effective := frozenFirewallTestTool(t, inputSchema, nil, valueAlways6FAD1299)
	run := firewallTestRun("run_invalid_arguments")
	step := model.Step{StepID: "step_1"}

	result, waiting, err := engine.handleResolvedRunToolCall(
		t.Context(), run, step, effective, map[string]ResolvedTool{tool.ModelName: tool},
		ToolCall{ToolCallID: "call_invalid", ToolName: tool.ModelName, ArgumentsJSON: `{"unexpected":true}`},
	)
	if err != nil || waiting {
		t.Fatalf("handleResolvedRunToolCall() waiting=%v error=%v", waiting, err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.calls))
	}
	if result.Status != valueErrorA8DE48C2 || !strings.Contains(result.Error, "tool arguments validation failed") {
		t.Fatalf("invalid argument result = %#v", result)
	}
	assertFirewallToolEvents(t, repo.events, "call_invalid")
}

func TestFrozenToolExecutionRejectsInvalidOutputAfterProviderSuccess(t *testing.T) {
	repo := &durableFailureTestRepository{}
	executor := &recordingToolExecutor{output: `{"content":[],"structuredContent":{"ok":"yes"}}`}
	engine := &Engine{
		cfg:               StaticConfigProvider(Config{}),
		repo:              repo,
		toolExecutor:      executor,
		generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{}),
	}
	inputSchema := json.RawMessage(`{"type":"object","additionalProperties":false}`)
	outputSchema := json.RawMessage(`{
		"type":"object",
		"required":["ok"],
		"additionalProperties":false,
		"properties":{"ok":{"type":"boolean"}}
	}`)
	tool, effective := frozenFirewallTestTool(t, inputSchema, outputSchema, valueNever4C6E2E88)
	run := firewallTestRun("run_invalid_output")

	result, waiting, err := engine.executeFrozenRunTool(
		t.Context(), run, "step_1", effective, tool,
		ToolCall{ToolCallID: "call_invalid_output", ToolName: tool.ModelName, ArgumentsJSON: `{}`},
	)
	if err != nil || waiting {
		t.Fatalf("executeFrozenRunTool() waiting=%v error=%v", waiting, err)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	if result.Status != valueErrorA8DE48C2 || !strings.Contains(result.Error, "tool output validation failed") {
		t.Fatalf("invalid output result = %#v", result)
	}
	if strings.Contains(result.OutputJSON, `"ok":"yes"`) {
		t.Fatalf("invalid provider output escaped firewall: %s", result.OutputJSON)
	}
	assertFirewallToolEvents(t, repo.events, "call_invalid_output")
}

func frozenFirewallTestTool(t *testing.T, inputSchema, outputSchema json.RawMessage, approvalMode string) (ResolvedTool, effectiveTextRunConfig) {
	t.Helper()
	tool := ResolvedTool{
		ToolKey:            testFirewallToolKey,
		ProviderKind:       valueMcp75675BED,
		ProviderKey:        testFirewallToolName,
		ModelName:          testFirewallToolName,
		OriginalName:       testFirewallToolName,
		DefinitionVersion:  "v1",
		InputSchema:        append(json.RawMessage(nil), inputSchema...),
		OutputSchema:       append(json.RawMessage(nil), outputSchema...),
		ExecutionMode:      valueLocalDispatch71FF6D47,
		ApprovalCapability: valuePerCall2570116D,
		ApprovalMode:       approvalMode,
		RiskLevel:          valueLow9A37DEBA,
		SideEffectLevel:    valueRead3A612695,
	}
	policy, err := snapshotResolvedRunTool(tool, 0, 1)
	if err != nil {
		t.Fatalf("snapshotResolvedRunTool() error = %v", err)
	}
	return tool, effectiveTextRunConfig{MaxToolCalls: 4, ToolPolicies: []effectiveRunToolPolicy{policy}}
}

func firewallTestRun(runID string) model.Run {
	return model.Run{
		RunID:  runID,
		Actor:  model.ActorRef{TenantID: "tenant_test", ActorID: "actor_test"},
		Thread: model.ThreadRef{Kind: threadKindConversation, ID: "thread_test"},
	}
}

func assertFirewallToolEvents(t *testing.T, events []model.Event, toolCallID string) {
	t.Helper()
	started, failed := false, false
	for _, event := range events {
		if event.ToolCallID != toolCallID {
			continue
		}
		started = started || event.EventType == valueToolStartedB113F313
		failed = failed || event.EventType == valueToolFailedFB145984
	}
	if !started || !failed {
		t.Fatalf("tool lifecycle events started=%v failed=%v events=%#v", started, failed, events)
	}
}

func (e *recordingToolExecutor) Execute(_ context.Context, input ToolExecutionInput) (string, error) {
	e.calls = append(e.calls, input)
	return e.output, e.err
}

func TestNormalizeToolArgumentsAgainstSchemaCanonicalizesAndValidates(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"required":["query","limit"],
		"additionalProperties":false,
		"properties":{
			"query":{"type":"string","minLength":2,"maxLength":20},
			"limit":{"type":"integer","minimum":1,"maximum":50},
			"kinds":{"type":"array","maxItems":2,"uniqueItems":true,"items":{"enum":["fact","entity"]}}
		}
	}`)

	got, err := normalizeToolArgumentsAgainstSchema(`{"limit":10,"query":"canon","kinds":["entity","fact"]}`, schema)
	if err != nil {
		t.Fatalf("normalizeToolArgumentsAgainstSchema() error = %v", err)
	}
	if got != `{"kinds":["entity","fact"],"limit":10,"query":"canon"}` {
		t.Fatalf("canonical arguments = %s", got)
	}
}

func TestSnapshotResolvedRunToolRejectsInvalidContracts(t *testing.T) {
	base := ResolvedTool{
		ToolKey:            testFirewallToolKey,
		ProviderKind:       valueMcp75675BED,
		ProviderKey:        testFirewallToolName,
		ModelName:          testFirewallToolName,
		OriginalName:       testFirewallToolName,
		DefinitionVersion:  "v1",
		InputSchema:        json.RawMessage(`{"type":"object"}`),
		ExecutionMode:      valueLocalDispatch71FF6D47,
		ApprovalCapability: valuePerCall2570116D,
		SideEffectLevel:    valueRead3A612695,
	}

	invalidInput := base
	invalidInput.InputSchema = json.RawMessage(`{"type":7}`)
	if _, err := snapshotResolvedRunTool(invalidInput, 0, 1); !errors.Is(err, ErrRunEnvironmentUnavailable) || !errors.Is(err, ErrToolSchemaInvalid) {
		t.Fatalf("invalid input schema error = %v", err)
	}

	invalidOutput := base
	invalidOutput.OutputSchema = json.RawMessage(`{"$ref":"https://example.com/output.json"}`)
	if _, err := snapshotResolvedRunTool(invalidOutput, 0, 1); !errors.Is(err, ErrRunEnvironmentUnavailable) || !errors.Is(err, ErrToolSchemaInvalid) {
		t.Fatalf("invalid output schema error = %v", err)
	}
}

func TestNormalizeToolArgumentsAgainstSchemaRejectsContractViolations(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"required":["target"],
		"additionalProperties":false,
		"properties":{"target":{"type":"string","pattern":"^story_"}}
	}`)
	tests := []struct {
		name string
		raw  string
	}{
		{name: "missing required", raw: `{}`},
		{name: "unknown field", raw: `{"target":"story_1","force":true}`},
		{name: "pattern mismatch", raw: `{"target":"unit_1"}`},
		{name: "root array", raw: `[]`},
		{name: "invalid json", raw: `{bad`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeToolArgumentsAgainstSchema(test.raw, schema)
			if !errors.Is(err, ErrToolArgumentsInvalid) {
				t.Fatalf("error = %v, want ErrToolArgumentsInvalid", err)
			}
		})
	}
}

func TestNormalizeToolArgumentsAgainstSchemaSupportsLocalRefsAndOneOf(t *testing.T) {
	schema := json.RawMessage(`{
		"$defs":{"identifier":{"type":"string","minLength":1}},
		"type":"object",
		"required":["target","operation"],
		"additionalProperties":false,
		"properties":{
			"target":{"$ref":"#/$defs/identifier"},
			"operation":{"oneOf":[{"const":"read"},{"const":"write"}]}
		}
	}`)
	if _, err := normalizeToolArgumentsAgainstSchema(`{"target":"story_1","operation":"write"}`, schema); err != nil {
		t.Fatalf("valid local ref contract rejected: %v", err)
	}
	if _, err := normalizeToolArgumentsAgainstSchema(`{"target":"story_1","operation":"delete"}`, schema); !errors.Is(err, ErrToolArgumentsInvalid) {
		t.Fatalf("oneOf violation error = %v", err)
	}
}

func TestNormalizeToolOutputAgainstSchema(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"required":["ok","count"],
		"additionalProperties":false,
		"properties":{"ok":{"type":"boolean"},"count":{"type":"integer","minimum":0}}
	}`)
	got, err := normalizeToolOutputAgainstSchema(`{"count":2,"ok":true}`, schema, "story")
	if err != nil {
		t.Fatalf("normalizeToolOutputAgainstSchema() error = %v", err)
	}
	if got != `{"count":2,"ok":true}` {
		t.Fatalf("canonical output = %s", got)
	}
	if _, err = normalizeToolOutputAgainstSchema(`{"count":-1,"ok":true}`, schema, "story"); !errors.Is(err, ErrToolOutputInvalid) {
		t.Fatalf("output violation error = %v", err)
	}
}

func TestNormalizeMCPToolOutputValidatesStructuredContentAndPreservesEnvelope(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"required":["ok","count"],
		"additionalProperties":false,
		"properties":{"ok":{"type":"boolean"},"count":{"type":"integer","minimum":0}}
	}`)
	raw := `{"content":[{"type":"text","text":"done"}],"structuredContent":{"count":2,"ok":true}}`
	got, err := normalizeToolOutputAgainstSchema(raw, schema, valueMcp75675BED)
	if err != nil {
		t.Fatalf("normalizeToolOutputAgainstSchema() error = %v", err)
	}
	if got != `{"content":[{"text":"done","type":"text"}],"structuredContent":{"count":2,"ok":true}}` {
		t.Fatalf("canonical MCP output = %s", got)
	}
	if _, err = normalizeToolOutputAgainstSchema(`{"content":[]}`, schema, valueMcp75675BED); !errors.Is(err, ErrToolOutputInvalid) {
		t.Fatalf("missing structuredContent error = %v", err)
	}
}

func TestNormalizeToolArgumentsPreservesLargeIntegerPrecision(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["revision"],"properties":{"revision":{"type":"integer"}}}`)
	const raw = `{"revision":9007199254740993}`
	got, err := normalizeToolArgumentsAgainstSchema(raw, schema)
	if err != nil {
		t.Fatalf("normalizeToolArgumentsAgainstSchema() error = %v", err)
	}
	if got != raw {
		t.Fatalf("large integer changed: %s", got)
	}
}

func TestToolContractRejectsExternalReferences(t *testing.T) {
	schema := json.RawMessage(`{"$ref":"https://example.com/schema.json"}`)
	_, err := normalizeToolArgumentsAgainstSchema(`{}`, schema)
	if !errors.Is(err, ErrToolSchemaInvalid) {
		t.Fatalf("error = %v, want ErrToolSchemaInvalid", err)
	}
}

func TestToolContractRejectsInvalidSchema(t *testing.T) {
	_, err := normalizeToolArgumentsAgainstSchema(`{}`, json.RawMessage(`{"type":7}`))
	if !errors.Is(err, ErrToolSchemaInvalid) {
		t.Fatalf("error = %v, want ErrToolSchemaInvalid", err)
	}
}
