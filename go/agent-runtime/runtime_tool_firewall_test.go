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
	testFirewallToolKey       = "mcp.search"
	testFirewallToolName      = "search"
	testToolOutputDeniedCode  = "tool_output_denied"
	testRejectedProviderValue = "provider leak"
	testOutputDeniedCallID    = "call_output_denied"
)

type recordingToolExecutor struct {
	calls         []ToolExecutionInput
	receiptCalls  []ToolExecutionInput
	output        string
	receiptResult ToolExecutionResult
	err           error
}

func TestNullOutputSchemaIsAbsentAfterSnapshotRoundTrip(t *testing.T) {
	inputSchema := json.RawMessage(`{"type":"object","additionalProperties":false}`)
	outputSchema := json.RawMessage(`null`)
	if err := validateToolContractSchemas(inputSchema, outputSchema); err != nil {
		t.Fatalf("validateToolContractSchemas() error = %v", err)
	}
	const providerOutput = "plain provider output"
	got, err := normalizeToolOutputAgainstSchema(providerOutput, outputSchema, "")
	if err != nil || got != providerOutput {
		t.Fatalf("normalizeToolOutputAgainstSchema() = %q, %v", got, err)
	}
}

func TestNullInputSchemaRemainsInvalid(t *testing.T) {
	err := validateToolContractSchemas(json.RawMessage(`null`), nil)
	if !errors.Is(err, ErrToolSchemaInvalid) {
		t.Fatalf("validateToolContractSchemas() error = %v", err)
	}
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

func TestToolInputEvaluationBlocksBeforeProviderExecution(t *testing.T) {
	repo := &durableFailureTestRepository{}
	executor := &recordingToolExecutor{output: `{"structuredContent":{"ok":true}}`}
	registry, err := NewEvaluationRegistry([]EvaluationRegistration{{
		Name: "tool_input_policy", Stages: []EvaluationStage{EvaluationStageToolInput}, Mode: EvaluationModeEnforce,
		Evaluator: evaluatorFunc(func(context.Context, EvaluationRequest) (EvaluationResult, error) {
			return EvaluationResult{Decision: EvaluationDecisionDeny, Code: "tool_input_denied", Message: "raw input must not persist"}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{cfg: StaticConfigProvider(Config{}), repo: repo, toolExecutor: executor, evaluations: registry, generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{})}
	tool, effective := frozenFirewallTestTool(t, json.RawMessage(`{"type":"object","additionalProperties":false}`), nil, valueNever4C6E2E88)
	run := firewallTestRun("run_input_evaluation")

	result, waiting, err := engine.executeFrozenRunTool(t.Context(), run, "step_1", effective, tool, ToolCall{ToolCallID: "call_input_denied", ToolName: tool.ModelName, ArgumentsJSON: `{}`})
	if err != nil || waiting {
		t.Fatalf("executeFrozenRunTool() waiting=%v error=%v", waiting, err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.calls))
	}
	if result.Status != valueErrorA8DE48C2 || !strings.Contains(result.Error, "tool_input_denied") || strings.Contains(result.Error, "raw input") {
		t.Fatalf("blocked input result = %#v", result)
	}
	assertFirewallToolEvents(t, repo.events, "call_input_denied")
	assertEvaluationEvent(t, repo.events, EvaluationStageToolInput, "tool_input_denied")
}

func TestToolOutputEvaluationBlocksRawProviderOutputPersistence(t *testing.T) {
	repo := &durableFailureTestRepository{}
	executor := &recordingToolExecutor{output: `{"structuredContent":{"secret":"` + testRejectedProviderValue + `"}}`}
	registry, err := NewEvaluationRegistry([]EvaluationRegistration{{
		Name: "tool_output_policy", Stages: []EvaluationStage{EvaluationStageToolOutput}, Mode: EvaluationModeEnforce,
		Evaluator: evaluatorFunc(func(context.Context, EvaluationRequest) (EvaluationResult, error) {
			return EvaluationResult{Decision: EvaluationDecisionDeny, Code: testToolOutputDeniedCode, Message: testRejectedProviderValue}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{cfg: StaticConfigProvider(Config{}), repo: repo, toolExecutor: executor, evaluations: registry, generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{})}
	inputSchema := json.RawMessage(`{"type":"object","additionalProperties":false}`)
	outputSchema := json.RawMessage(`{"type":"object","required":["secret"],"additionalProperties":false,"properties":{"secret":{"type":"string"}}}`)
	tool, effective := frozenFirewallTestTool(t, inputSchema, outputSchema, valueNever4C6E2E88)
	run := firewallTestRun("run_output_evaluation")

	result, waiting, err := engine.executeFrozenRunTool(t.Context(), run, "step_1", effective, tool, ToolCall{ToolCallID: testOutputDeniedCallID, ToolName: tool.ModelName, ArgumentsJSON: `{}`})
	if err != nil || waiting {
		t.Fatalf("executeFrozenRunTool() waiting=%v error=%v", waiting, err)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	if result.Status != valueErrorA8DE48C2 || !strings.Contains(result.Error, testToolOutputDeniedCode) {
		t.Fatalf("blocked output result = %#v", result)
	}
	assertNoEventPayloadContains(t, repo.events, testRejectedProviderValue)
	assertFirewallToolEvents(t, repo.events, testOutputDeniedCallID)
	assertEvaluationEvent(t, repo.events, EvaluationStageToolOutput, testToolOutputDeniedCode)
}

func assertNoEventPayloadContains(t *testing.T, events []model.Event, forbidden string) {
	t.Helper()
	for _, event := range events {
		combined := event.PayloadJSON + event.OutputJSON + event.ErrorJSON
		if strings.Contains(combined, forbidden) {
			t.Fatalf("rejected payload leaked into event: %#v", event)
		}
	}
}

func assertEvaluationEvent(t *testing.T, events []model.Event, stage EvaluationStage, code string) {
	t.Helper()
	for _, event := range events {
		if event.EventType == eventGuardrailEvaluated && strings.Contains(event.PayloadJSON, `"stage":"`+string(stage)+`"`) && strings.Contains(event.PayloadJSON, `"code":"`+code+`"`) {
			return
		}
	}
	t.Fatalf("evaluation event stage=%s code=%s missing: %#v", stage, code, events)
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

func (e *recordingToolExecutor) ExecuteWithReceipt(_ context.Context, input ToolExecutionInput) (ToolExecutionResult, error) {
	e.receiptCalls = append(e.receiptCalls, input)
	return e.receiptResult, e.err
}

func TestSnapshotResolvedRunToolRequiresProviderReceiptForWrites(t *testing.T) {
	tool, _ := frozenFirewallTestTool(t, json.RawMessage(`{"type":"object","additionalProperties":false}`), nil, valueAlways6FAD1299)
	tool.SideEffectLevel = ToolSideEffectWrite
	tool.IdempotencyMode = ToolIdempotencyRequestKey
	if _, err := snapshotResolvedRunTool(tool, 0, 1); !errors.Is(err, ErrRunToolProviderReceiptRequired) {
		t.Fatalf("write tool without provider receipt error = %v", err)
	}

	tool.IdempotencyMode = ToolIdempotencyProviderReceipt
	if _, err := snapshotResolvedRunTool(tool, 0, 1); err != nil {
		t.Fatalf("write tool with provider receipt error = %v", err)
	}
}

func TestFrozenWriteToolPersistsProviderExecutionReceipt(t *testing.T) {
	repo := &durableFailureTestRepository{}
	run := firewallTestRun("run_receipt")
	call := ToolCall{ToolCallID: "call_receipt", ToolName: testFirewallToolName, ArgumentsJSON: `{}`}
	requestID := run.RunID + ":tool:" + call.ToolCallID
	executor := &recordingToolExecutor{receiptResult: ToolExecutionResult{
		OutputJSON: `{"content":[],"structuredContent":{"ok":true}}`,
		Receipt:    ToolExecutionReceipt{RequestID: requestID, ProviderExecutionID: "provider_execution_1", Disposition: ToolReceiptCommitted},
	}}
	engine := &Engine{cfg: StaticConfigProvider(Config{}), repo: repo, toolExecutor: executor, generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{})}
	tool, _ := frozenFirewallTestTool(t, json.RawMessage(`{"type":"object","additionalProperties":false}`), nil, valueNever4C6E2E88)
	tool.SideEffectLevel = ToolSideEffectWrite
	tool.IdempotencyMode = ToolIdempotencyProviderReceipt
	policy, err := snapshotResolvedRunTool(tool, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	effective := effectiveTextRunConfig{MaxToolCalls: 4, ToolPolicies: []effectiveRunToolPolicy{policy}}

	result, waiting, err := engine.executeFrozenRunTool(t.Context(), run, "step_1", effective, tool, call)
	if err != nil || waiting || result.Status != valueSuccess4D886D19 {
		t.Fatalf("receipt tool result=%#v waiting=%v err=%v", result, waiting, err)
	}
	if len(executor.calls) != 0 || len(executor.receiptCalls) != 1 || executor.receiptCalls[0].RequestID != requestID {
		t.Fatalf("executor calls=%d receiptCalls=%#v", len(executor.calls), executor.receiptCalls)
	}
	assertProviderReceiptEvent(t, repo.events, call.ToolCallID, "provider_execution_1", ToolReceiptCommitted)
}

func assertProviderReceiptEvent(t *testing.T, events []model.Event, toolCallID, providerExecutionID, disposition string) {
	t.Helper()
	for _, event := range events {
		if event.EventType != valueToolCompleted8D0A12FD || event.ToolCallID != toolCallID {
			continue
		}
		if !strings.Contains(event.PayloadJSON, `"providerExecutionID":"`+providerExecutionID+`"`) || !strings.Contains(event.PayloadJSON, `"disposition":"`+disposition+`"`) {
			t.Fatalf("receipt missing from completed event: %s", event.PayloadJSON)
		}
		return
	}
	t.Fatal("tool.completed event missing")
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

func TestNormalizeToolArgumentsAgainstSchemaRepairsNestedProviderCasing(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"required":["operations"],
		"additionalProperties":false,
		"properties":{
			"operations":{
				"type":"array",
				"items":{
					"oneOf":[{
						"type":"object",
						"required":["targetKind","evidence"],
						"additionalProperties":false,
						"properties":{
							"targetKind":{"const":"foundation"},
							"evidence":{
								"type":"array",
								"items":{
									"type":"object",
									"required":["targetID"],
									"additionalProperties":false,
									"properties":{"targetID":{"type":"string"},"blockID":{"type":"string"}}
								}
							}
						}
					}]
				}
			}
		}
	}`)

	got, err := normalizeToolArgumentsAgainstSchema(
		`{"operations":[{"targetkind":"foundation","evidence":[{"targetid":"story_1","blockid":"block_1"}]}]}`,
		schema,
	)
	if err != nil {
		t.Fatalf("normalizeToolArgumentsAgainstSchema() error = %v", err)
	}
	want := `{"operations":[{"evidence":[{"blockID":"block_1","targetID":"story_1"}],"targetKind":"foundation"}]}`
	if got != want {
		t.Fatalf("canonical arguments = %s, want %s", got, want)
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
			if test.name == "missing required" && !strings.Contains(err.Error(), "required parameters are missing: target") {
				t.Fatalf("missing-required error = %v, want actionable field name", err)
			}
			if test.name == "unknown field" && !strings.Contains(err.Error(), "$/force: unexpected parameters are not allowed: force") {
				t.Fatalf("unknown-field error = %v, want actionable field name", err)
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

func TestNormalizeToolArgumentsAgainstSchemaReportsNestedLeafViolation(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"required":["operations"],
		"properties":{
			"operations":{
				"type":"array",
				"items":{
					"type":"object",
					"required":["after"],
					"properties":{"after":{"type":"object","minProperties":1}}
				}
			}
		}
	}`)
	_, err := normalizeToolArgumentsAgainstSchema(
		`{"operations":[{"after":{}}]}`,
		schema,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "$/operations/0/after") ||
		!strings.Contains(err.Error(), "object must include at least one property") {
		t.Fatalf("nested validation error = %v", err)
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
