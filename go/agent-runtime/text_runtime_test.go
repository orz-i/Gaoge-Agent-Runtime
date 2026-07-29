package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/modelcap"
)

const (
	testWorkspaceProviderKind              = "story"
	testArtifactChangeSet                  = "change_set"
	testArtifactReview                     = "review"
	valueTitle142B09AD                     = "title"
	durableFailureCall1                    = "call_1"
	durableFailureCall2                    = "call_2"
	durableFailureTool                     = "story_publish_change_set"
	testStoryID                            = "story_1"
	testReplyContract                      = "reply"
	testErrorCodeWorkspaceArgumentsInvalid = "workspace_arguments_invalid"
	testLegacyPlannerOption                = "legacy"
)

var (
	errStaleWorkspaceSnapshot   = errors.New("stale snapshot")
	errProviderInvalidInput     = errors.New("provider-specific invalid input")
	errProviderRevisionConflict = errors.New("story revision conflict")
)

func testWorkspacePolicy(expected string) WorkspacePolicy {
	policy := WorkspacePolicy{ArtifactResourceField: "storyID", SerializeToolProtocols: []string{protocolGoogleGenerateContent}}
	switch expected {
	case testArtifactChangeSet, testArtifactReview:
		policy.RequiredArtifact = true
		policy.TerminalArtifactTypes = []string{expected}
	case valueAuto60DC1905:
		policy.TerminalArtifactTypes = []string{testArtifactChangeSet, testArtifactReview}
		policy.AllowAskUser = true
		policy.AllowPublishOutput = true
	case testReplyContract:
		policy.AllowAskUser = true
	default:
		policy.AllowAskUser = true
		policy.AllowPublishOutput = true
	}
	return policy
}

func TestPlannerUnsupportedCapabilityNeverCallsGateway(t *testing.T) {
	gateway := &scriptedLLMGateway{}
	engine := &Engine{llmGateway: gateway}
	_, err := engine.generatePlanAttempt(
		t.Context(),
		model.Run{RunID: "run_unsupported_plan"},
		effectiveTextRunConfig{},
		&LLMRoute{UpstreamModel: valueModel22D48A8A, Protocol: AdapterOpenAIChatCompletions, ModelCapabilitiesJSON: `{}`},
		1,
		"",
		false,
		nil,
	)
	if !errors.Is(err, errPlannerStructuredOutputUnsupported) {
		t.Fatalf("planner unsupported error = %v", err)
	}
	if len(gateway.inputs) != 0 {
		t.Fatalf("unsupported Planner reached the provider: %#v", gateway.inputs)
	}
}

func TestPlannerGenerateFailurePersistsRoute(t *testing.T) {
	gatewayErr := &UpstreamError{StatusCode: 503, Message: "upstream unavailable", Body: "no available L1 node"}
	gateway := &scriptedLLMGateway{errors: []error{gatewayErr}}
	repo := &multiTurnRunRepo{}
	engine := &Engine{repo: repo, llmGateway: gateway, generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{})}
	run := model.Run{
		RunID:         "run_planner_route_failure",
		CurrentStepID: "step_planner",
		Actor:         model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey},
		Thread:        model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey},
	}
	route, err := gateway.PrepareTextRoute(t.Context(), LLMRouteInput{PlatformModelName: testRouteModelName})
	if err != nil {
		t.Fatal(err)
	}
	route.ModelCapabilitiesJSON = `{"structuredOutput":{"mode":"json_object"}}`
	_, err = engine.generatePlanAttempt(
		t.Context(),
		run,
		effectiveTextRunConfig{PlatformModelName: testRouteModelName, PlanMaxSteps: 2, MaxLLMCalls: 4},
		route,
		1,
		"",
		false,
		nil,
	)
	if !errors.Is(err, gatewayErr) {
		t.Fatalf("planner error = %v", err)
	}
	if countRunEvents(repo.events, eventLLMRouteSelected) != 1 || countRunEvents(repo.events, valueUsageUpdatedABC8B0B2) != 0 {
		t.Fatalf("planner failure events = %#v", repo.events)
	}
}

func TestPlannerRequestNegotiatesStructuredOutputModes(t *testing.T) {
	effective := effectiveTextRunConfig{
		PlanMaxSteps: 3,
		Options: map[string]interface{}{
			plannerResponseFormatKey:     map[string]interface{}{"type": testLegacyPlannerOption},
			plannerResponseJSONSchemaKey: map[string]interface{}{testLegacyPlannerOption: true},
		},
	}
	tests := []struct {
		name string
		mode modelcap.StructuredOutputMode
		want string
	}{
		{name: "strict schema", mode: modelcap.StructuredOutputStrictJSONSchema, want: plannerJSONSchemaType},
		{name: "json object", mode: modelcap.StructuredOutputJSONObject, want: plannerJSONObjectType},
		{name: "json text", mode: modelcap.StructuredOutputJSONText},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := buildPlannerRequest("run_mode", "goal", effective, 1, "", false, 3, test.mode)
			if _, exists := request.Options[plannerResponseJSONSchemaKey]; exists {
				t.Fatal("response_json_schema must be removed")
			}
			format, exists := request.Options[plannerResponseFormatKey].(map[string]interface{})
			if test.want == "" {
				if exists {
					t.Fatalf("json_text leaked response_format: %#v", format)
				}
				return
			}
			if !exists || format[valueType5EE8C955] != test.want {
				t.Fatalf("response_format = %#v", request.Options[plannerResponseFormatKey])
			}
		})
	}
}

func TestPlannerStructuredOutputRejectsUnconfiguredRoutes(t *testing.T) {
	tests := []struct {
		name  string
		route *LLMRoute
	}{
		{name: "nil route"},
		{name: "absent capability", route: &LLMRoute{UpstreamModel: valueModel22D48A8A, Protocol: AdapterOpenAIChatCompletions, ModelCapabilitiesJSON: `{}`}},
		{name: "explicit unsupported", route: &LLMRoute{UpstreamModel: valueModel22D48A8A, Protocol: AdapterOpenAIChatCompletions, ModelCapabilitiesJSON: `{"structuredOutput":{"mode":"unsupported"}}`}},
		{name: "invalid capability", route: &LLMRoute{UpstreamModel: valueModel22D48A8A, Protocol: AdapterOpenAIChatCompletions, ModelCapabilitiesJSON: `{"structuredOutput":{"mode":"yaml"}}`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := plannerStructuredOutput(test.route); !errors.Is(err, errPlannerStructuredOutputUnsupported) {
				t.Fatalf("planner capability error = %v", err)
			}
		})
	}
}

func (r *durableFailureTestRepository) AppendRunEvents(_ context.Context, events []model.Event) ([]model.Event, error) {
	saved := append([]model.Event(nil), events...)
	for index := range saved {
		r.nextSeq++
		saved[index].Seq = r.nextSeq
	}
	r.events = append(r.events, saved...)
	return saved, nil
}

func (r *durableFailureTestRepository) CountRunEventsByType(_ context.Context, _ model.ActorRef, _ string, eventTypes []string) (map[string]int, error) {
	wanted := make(map[string]struct{}, len(eventTypes))
	for _, eventType := range eventTypes {
		wanted[eventType] = struct{}{}
	}
	counts := make(map[string]int, len(wanted))
	for _, event := range r.events {
		if _, ok := wanted[event.EventType]; ok {
			counts[event.EventType]++
		}
	}
	return counts, nil
}

const (
	valueKey50D45996    = "key"
	valueLookupE85B2FAE = "lookup"
	valueRevise90165516 = "revise"
)

const (
	valueActionBE52611B            = "action"
	valueAlways55D4AEEB            = "always"
	valueCheckpointCreatedEEEB685F = "checkpoint.created"
	valueDescriptionC75D7BBB       = "description"
	valueInteraction947864A7       = "interaction"
	valueNeverC7701C8C             = "never"
	valuePrompt390B7B69            = "prompt"
	valueProviderId4F1796AF        = "provider-id"
	valueRoot809EA865              = "root"
	valueRunCancelledD8649EC6      = "run.cancelled"
	valueRunCompletedCFE4A298      = "run.completed"
	valueRunFailed0B4285FA         = "run.failed"
	valueRunPreparingD85F5CAE      = "run.preparing"
	valueRunResumedBED4B593        = "run.resumed"
	valueRunStartedBBF53B65        = "run.started"
	valueRunSuspended4193AEC2      = "run.suspended"
	valueRunWaitingInputE28A9912   = "run.waiting_input"
	valueStep83D64102              = "step"
	valueStepResumedFD23056A       = "step.resumed"
	valueToolCompleted1C0A7F48     = "tool.completed"
	valueToolFailedE0DC1562        = "tool.failed"
)

func TestParseAndValidatePlanAcceptsDAG(t *testing.T) {
	payload, err := parseAndValidatePlan(`{
  "summary":"ship safely",
  "steps":[
    {"key":"research","title":"Research","description":"collect facts","dependsOn":[],"approvalRequired":false,"expectedTools":[],"resourceRefs":[]},
    {"key":"implement","title":"Implement","description":"make the change","dependsOn":["research"],"approvalRequired":true,"expectedTools":[],"resourceRefs":[]}
  ]
}`, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Steps) != 2 || payload.Steps[1].DependsOn[0] != "research" {
		t.Fatalf("unexpected plan: %#v", payload)
	}
}

func TestParseAndValidatePlanCanonicalizesOmittedEmptyCollections(t *testing.T) {
	payload, err := parseAndValidatePlan(`{
  "summary":"revise safely",
  "steps":[
    {"key":"revise","title":"Revise","description":"apply the feedback","dependencies":[]}
  ]
}`, 12)
	if err != nil {
		t.Fatal(err)
	}
	step := payload.Steps[0]
	if step.DependsOn == nil || step.ExpectedTools == nil || step.ResourceRefs == nil || !step.ApprovalRequired {
		t.Fatalf("planner collections were not canonicalized: %#v", step)
	}
	if _, err = parseAndValidatePlan(`{"summary":"invalid","steps":[{"key":"a","title":"A"}]}`, 12); err == nil {
		t.Fatal("missing scalar description must remain invalid")
	}
}

func TestParseAndValidatePlanNormalizesPlanSummary(t *testing.T) {
	payload, err := parseAndValidatePlan(`{"planSummary":"Use local evidence","steps":[{"key":"collect","title":"Collect","description":"Collect evidence","dependsOn":[],"approvalRequired":false,"expectedTools":[],"resourceRefs":[]}]}`, 3)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Summary != "Use local evidence" {
		t.Fatalf("summary = %q", payload.Summary)
	}
}

func TestParseAndValidatePlanRejectsConflictingSummaryAlias(t *testing.T) {
	_, err := parseAndValidatePlan(`{"summary":"first","planSummary":"second","steps":[{"key":"collect","title":"Collect","description":"Collect evidence","dependsOn":[],"approvalRequired":false,"expectedTools":[],"resourceRefs":[]}]}`, 3)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("error = %v, want conflicting alias", err)
	}
}

func TestTopologicallySortRunStepsHonorsDependencies(t *testing.T) {
	steps := []model.Step{
		{StepID: "step_synthesis", StepIndex: 1, DependsOnJSON: `["step_research"]`},
		{StepID: "step_research", StepIndex: 2, DependsOnJSON: `[]`},
		{StepID: "step_publish", StepIndex: 3, DependsOnJSON: `["step_synthesis"]`},
	}
	ordered, err := topologicallySortRunSteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	if ordered[0].StepID != "step_research" || ordered[1].StepID != "step_synthesis" || ordered[2].StepID != "step_publish" {
		t.Fatalf("unexpected topological order: %#v", ordered)
	}
}

func TestPlannerRequestUsesRunScopedIdempotencyKey(t *testing.T) {
	effective := effectiveTextRunConfig{PlanMaxSteps: 12}
	planner := buildPlannerRequest("run_123", "goal", effective, 2, "", false, 3, modelcap.StructuredOutputStrictJSONSchema)
	repair := buildPlannerRequest("run_123", "goal", effective, 2, "fix", true, 2, modelcap.StructuredOutputStrictJSONSchema)
	if planner.RequestID != "run_123:planner:2" || repair.RequestID != "run_123:planner-repair:2" {
		t.Fatalf("unexpected planner request IDs: %q %q", planner.RequestID, repair.RequestID)
	}
	if _, duplicated := planner.Options[plannerResponseJSONSchemaKey]; duplicated {
		t.Fatal("planner must not send both response_format and response_json_schema to Gemini")
	}
	if _, ok := planner.Options[plannerResponseFormatKey]; !ok {
		t.Fatal("planner response_format is missing")
	}
	if len(planner.Messages) != 1 || strings.Contains(planner.Messages[0].Content, `"mode"`) || !strings.Contains(planner.Messages[0].Content, "最终呈现由系统 synthesis 完成") {
		t.Fatalf("planned strategy guidance is invalid: %#v", planner.Messages)
	}
}

func TestParseAndValidatePlanRejectsInvalidGraphs(t *testing.T) {
	tests := map[string]string{
		"duplicate":          `{"summary":"x","steps":[{"key":"a","title":"A","description":"a","dependsOn":[],"approvalRequired":false,"expectedTools":[],"resourceRefs":[]},{"key":"a","title":"B","description":"b","dependsOn":[],"approvalRequired":false,"expectedTools":[],"resourceRefs":[]}]}`,
		"missing dependency": `{"summary":"x","steps":[{"key":"a","title":"A","description":"a","dependsOn":["missing"],"approvalRequired":false,"expectedTools":[],"resourceRefs":[]}]}`,
		"cycle":              `{"summary":"x","steps":[{"key":"a","title":"A","description":"a","dependsOn":["b"],"approvalRequired":false,"expectedTools":[],"resourceRefs":[]},{"key":"b","title":"B","description":"b","dependsOn":["a"],"approvalRequired":false,"expectedTools":[],"resourceRefs":[]}]}`,
		"too many":           `{"summary":"x","steps":[{"key":"a","title":"A","description":"a","dependsOn":[],"approvalRequired":false,"expectedTools":[],"resourceRefs":[]},{"key":"b","title":"B","description":"b","dependsOn":[],"approvalRequired":false,"expectedTools":[],"resourceRefs":[]}]}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			maxSteps := 12
			if name == "too many" {
				maxSteps = 1
			}
			if _, err := parseAndValidatePlan(raw, maxSteps); err == nil {
				t.Fatal("expected invalid plan to be rejected")
			}
		})
	}
}

func TestPlannerCannotDowngradePlannedStrategy(t *testing.T) {
	if _, err := parseAndValidatePlan(`{"mode":"direct","summary":"answer directly","steps":[]}`, 3); err == nil {
		t.Fatal("planner must not be able to change the immutable run strategy")
	}
	if _, err := parseAndValidatePlan(`{"strategy":"direct","summary":"answer directly","steps":[]}`, 3); err == nil {
		t.Fatal("planner strategy fields must be rejected")
	}
}

func TestPlanBudgetReservesRoutingAndSynthesis(t *testing.T) {
	effective := effectiveTextRunConfig{MaxLLMCalls: 5, PlanMaxSteps: 12}
	if got, err := planMaxForNextPlanningCall(effective, 0); err != nil || got != 3 {
		t.Fatalf("initial plan max = %d, err = %v; want 3", got, err)
	}
	if got, err := planMaxForNextPlanningCall(effective, 1); err != nil || got != 2 {
		t.Fatalf("repair plan max = %d, err = %v; want 2", got, err)
	}
	if _, err := planMaxForNextPlanningCall(effective, 3); !errors.Is(err, errPlanBudgetExceeded) {
		t.Fatalf("planned route without step budget error = %v", err)
	}
}

func TestAutoStrategyIntentPolicy(t *testing.T) {
	for _, goal := range []string{
		"请先询问我希望的输出风格",
		"create a plan and wait for my approval",
		"请分步骤完成调研",
		"请制定恰好两步计划，第一步分析问题，第二步给出结论",
		"请分两个阶段完成调研并总结",
		"complete this task in two steps",
	} {
		if !textRunRequiresPlannedIntent(goal) {
			t.Fatalf("goal must require planned execution: %q", goal)
		}
	}
	for _, goal := range []string{"什么是项目计划？", "what does a project plan contain?", "不要制定多步计划，直接回答", "do not create a plan; answer directly"} {
		if textRunRequiresPlannedIntent(goal) {
			t.Fatalf("descriptive plan question must remain eligible for adaptive routing: %q", goal)
		}
	}
	if !textRunRequiresPlannedIntent("不要制定计划，但先询问我希望的输出风格") {
		t.Fatal("HITL intent must take precedence over a no-plan modifier")
	}
}

func TestPlannerSchemaMatchesRequiredStepFields(t *testing.T) {
	request := buildPlannerRequest("run_schema", "比较两种数据库", effectiveTextRunConfig{PlanMaxSteps: 3}, 1, "", false, 3, modelcap.StructuredOutputStrictJSONSchema)
	responseFormat := requireStringMap(t, request.Options, plannerResponseFormatKey)
	jsonSchema := requireStringMap(t, responseFormat, plannerJSONSchemaType)
	schema := requireStringMap(t, jsonSchema, "schema")
	properties := requireStringMap(t, schema, "properties")
	steps := requireStringMap(t, properties, "steps")
	items := requireStringMap(t, steps, "items")
	stepProperties := requireStringMap(t, items, "properties")
	for _, field := range []string{valueKey50D45996, valueTitle142B09AD, valueDescriptionC75D7BBB} {
		definition, ok := stepProperties[field].(map[string]interface{})
		if !ok {
			t.Fatalf("%s definition missing", field)
		}
		if definition["minLength"] != 1 {
			t.Fatalf("%s minLength = %#v; want 1", field, definition["minLength"])
		}
	}
	if !strings.Contains(request.Messages[0].Content, "key、title、description 都必须是非空字符串") {
		t.Fatal("planner prompt must repeat the non-empty step invariant")
	}
}

func requireStringMap(t *testing.T, parent map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	value, ok := parent[key].(map[string]interface{})
	if !ok {
		t.Fatalf("%s missing", key)
	}
	return value
}

func TestOutputLimitAllowsFiftiethAndRejectsFiftyFirst(t *testing.T) {
	current := make([]model.OutputRef, 49)
	for index := range current {
		current[index] = model.OutputRef{OutputID: fmt.Sprintf("output_%d", index), Version: 1}
	}
	if err := enforceOutputLimit(current, "output_50", 50); err != nil {
		t.Fatalf("50th output: err=%v", err)
	}
	currentWithFiftieth := make([]model.OutputRef, 50)
	copy(currentWithFiftieth, current)
	currentWithFiftieth[49] = model.OutputRef{OutputID: "output_50", Version: 1}
	if err := enforceOutputLimit(currentWithFiftieth, "output_51", 50); err == nil {
		t.Fatal("51st output must be rejected")
	}
	if err := enforceOutputLimit(currentWithFiftieth, "output_50", 50); err != nil {
		t.Fatalf("output version update must not consume a new identity: %v", err)
	}
}

func TestFrozenRunToolPolicyIncludesExecutionLimitsInFingerprint(t *testing.T) {
	policy := effectiveRunToolPolicy{ToolKey: "mcp.lookup", ProviderKind: "mcp", ProviderKey: valueLookupE85B2FAE, ModelName: valueLookupE85B2FAE, OriginalName: valueLookupE85B2FAE, DefinitionVersion: "v1", InputSchema: json.RawMessage(`{"type":"object"}`), ExecutionMode: "local_dispatch", ApprovalCapability: "per_call", ApprovalMode: valueAlways55D4AEEB, RiskLevel: "low", SideEffectLevel: "read", RetryCount: 2, Concurrency: 3}
	policy.Fingerprint = fingerprintRunToolSnapshot(policy)
	effective := effectiveTextRunConfig{ToolPolicies: []effectiveRunToolPolicy{policy}}
	loaded, ok := frozenRunToolPolicy(effective, policy.ToolKey)
	if !ok || loaded.RetryCount != 2 || loaded.Concurrency != 3 {
		t.Fatalf("frozen policy = %#v, ok=%v", loaded, ok)
	}
	effective.ToolPolicies[0].RetryCount++
	if _, ok = frozenRunToolPolicy(effective, policy.ToolKey); ok {
		t.Fatal("mutated execution limits must invalidate the frozen tool fingerprint")
	}
}

func TestFrozenRunToolPolicyFingerprintSurvivesSnapshotRoundTrip(t *testing.T) {
	policy := effectiveRunToolPolicy{ToolKey: "mcp.lookup", ProviderKind: "mcp", ProviderKey: valueLookupE85B2FAE, ModelName: valueLookupE85B2FAE, OriginalName: valueLookupE85B2FAE, DefinitionVersion: "v1", InputSchema: json.RawMessage(`{"type":"object"}`), ExecutionMode: "local_dispatch", ApprovalCapability: "per_call", ApprovalMode: valueNeverC7701C8C, RiskLevel: "low", SideEffectLevel: "read", HostedVariants: cloneHostedToolVariants(nil), Concurrency: 1}
	policy.Fingerprint = fingerprintRunToolSnapshot(policy)
	encoded, err := json.Marshal(effectiveTextRunConfig{SemanticVersion: RuntimeSnapshotVersion, ToolKeys: []string{policy.ToolKey}, ToolPolicies: []effectiveRunToolPolicy{policy}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded effectiveTextRunConfig
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err = (&Engine{}).resolveRunTools(t.Context(), model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, decoded); err != nil {
		t.Fatalf("round-tripped tool snapshot was rejected: %v", err)
	}
}

func TestNormalizeRunToolCallsCreatesStableUniqueIDs(t *testing.T) {
	calls := []ToolCall{
		{ToolName: runControlPublishOutput, ArgumentsJSON: `{"title":"one"}`},
		{ToolCallID: valueProviderId4F1796AF, ToolName: valueLookupE85B2FAE, ArgumentsJSON: `{}`},
		{ToolCallID: valueProviderId4F1796AF, ToolName: valueLookupE85B2FAE, ArgumentsJSON: `{"page":2}`},
	}
	first := normalizeRunToolCalls("run_1", "step_1", 2, calls)
	second := normalizeRunToolCalls("run_1", "step_1", 2, calls)
	if first[0].ToolCallID == "" || first[0].ToolCallID != second[0].ToolCallID {
		t.Fatalf("generated tool call ID is not stable: %#v %#v", first, second)
	}
	if first[1].ToolCallID != valueProviderId4F1796AF || first[2].ToolCallID == valueProviderId4F1796AF || first[2].ToolCallID == first[0].ToolCallID {
		t.Fatalf("tool call IDs are not unique: %#v", first)
	}
}

func TestSerializeStoryProviderToolCallsKeepsFirstOnly(t *testing.T) {
	storyGoogle := effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{Request: ResolvedWorkspaceContext{Type: testWorkspaceProviderKind}, Policy: testWorkspacePolicy("")}}
	if !shouldSerializeWorkspaceToolCalls(storyGoogle, protocolGoogleGenerateContent) {
		t.Fatal("expected Story+google_generate_content to serialize tool calls")
	}
	if shouldSerializeWorkspaceToolCalls(storyGoogle, "openai_responses") {
		t.Fatal("non-google protocols must not serialize")
	}
	if shouldSerializeWorkspaceToolCalls(effectiveTextRunConfig{}, protocolGoogleGenerateContent) {
		t.Fatal("non-story workspace must not serialize")
	}
	calls := []ToolCall{
		{ToolCallID: "a", ToolName: "story_list_entities"},
		{ToolCallID: "b", ToolName: "story_list_units"},
		{ToolCallID: "c", ToolName: "story_get_continuity"},
	}
	got := serializeWorkspaceToolCalls(calls)
	if len(got) != 1 || got[0].ToolCallID != "a" {
		t.Fatalf("serializeWorkspaceToolCalls = %#v, want first call only", got)
	}
	if len(serializeWorkspaceToolCalls(calls[:1])) != 1 {
		t.Fatal("single call must pass through")
	}
}

func TestPlanFailureCodesAreStable(t *testing.T) {
	if got := runFailureCode(fmt.Errorf("%w: too many steps", errPlanBudgetExceeded)); got != "plan_budget_exceeded" {
		t.Fatalf("budget code = %q", got)
	}
	if got := runFailureCode(fmt.Errorf("%w: malformed", errPlanInvalid)); got != "plan_invalid" {
		t.Fatalf("invalid code = %q", got)
	}
	if got := runFailureCode(fmt.Errorf("%w: unavailable", errPlannerStructuredOutputUnsupported)); got != "planner_structured_output_unsupported" {
		t.Fatalf("structured output code = %q", got)
	}
	if got := runFailureCode(fmt.Errorf("%w: invalid document", errRepeatedDeterministicWorkspaceToolFailure)); got != "repeated_deterministic_tool_failure" {
		t.Fatalf("repeated tool failure code = %q", got)
	}
	workspaceArg := NewWorkspaceError(WorkspaceErrorClassification{
		Kind:          WorkspaceErrorInvalidInput,
		Code:          testErrorCodeWorkspaceArgumentsInvalid,
		Message:       "workspace arguments invalid",
		Diagnostic:    "title is required and must not be empty",
		Deterministic: true,
	}, ErrInvalidInput)
	if got := runFailureCode(workspaceArg); got != testErrorCodeWorkspaceArgumentsInvalid {
		t.Fatalf("workspace args code = %q", got)
	}
	if got := runFailureCode(fmt.Errorf("%w: %w", errRepeatedDeterministicWorkspaceToolFailure, workspaceArg)); got != testErrorCodeWorkspaceArgumentsInvalid {
		t.Fatalf("repeated workspace args code = %q", got)
	}
	conflict := NewWorkspaceError(WorkspaceErrorClassification{Kind: WorkspaceErrorConflict, Code: "workspace_revision_conflict"}, errStaleWorkspaceSnapshot)
	if got := runFailureCode(conflict); got != "workspace_revision_conflict" {
		t.Fatalf("workspace conflict code = %q", got)
	}
	if got := classifyRunErrorCode(workspaceArg); got != testErrorCodeWorkspaceArgumentsInvalid {
		t.Fatalf("classifyRunErrorCode = %q", got)
	}
}

func TestWorkspaceErrorPreservesDiagnosticAndIsDeterministic(t *testing.T) {
	err := NewWorkspaceError(
		WorkspaceErrorClassification{Kind: WorkspaceErrorInvalidInput, Message: "workspace arguments invalid", Diagnostic: "operation 1: evidence is required", Deterministic: true},
		errProviderInvalidInput,
	)
	if err.Error() != "operation 1: evidence is required" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatal("invalid workspace error must preserve ErrInvalidInput semantics")
	}
	if !deterministicWorkspaceToolFailure(err) {
		t.Fatal("wrapped publisher validation must stay deterministic")
	}
	if !err.DeterministicToolFailure() {
		t.Fatal("DeterministicToolFailure() = false")
	}
}

func TestWorkspaceConflictPreservesSourceStaleSemantics(t *testing.T) {
	err := NewWorkspaceError(
		WorkspaceErrorClassification{Kind: WorkspaceErrorConflict, Code: "workspace_revision_conflict"},
		errProviderRevisionConflict,
	)
	if !errors.Is(err, ErrWorkspaceSourceStale) {
		t.Fatal("workspace conflict must preserve ErrWorkspaceSourceStale semantics")
	}
	if errors.Is(err, ErrInvalidInput) {
		t.Fatal("workspace conflict must not be classified as invalid input")
	}
}

type markedDeterministicFailure struct{ message string }

func (e markedDeterministicFailure) Error() string                { return e.message }
func (markedDeterministicFailure) DeterministicToolFailure() bool { return true }

func TestDeterministicStoryFailureFingerprintIsSemanticAndStable(t *testing.T) {
	tool := ResolvedTool{ProviderKind: testWorkspaceProviderKind, ProviderKey: testWorkspaceProviderKind, ToolKey: "story.publish", ModelName: durableFailureTool, OriginalName: durableFailureTool}
	effective := effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{ExpectedArtifact: "change_set"}}
	err := fmt.Errorf("operation 1: %w", markedDeterministicFailure{message: "invalid  Foundation\nfield"})
	first := deterministicToolFailureFingerprint(tool, ToolCall{ToolCallID: durableFailureCall1, ArgumentsJSON: `{"b":2,"a":1}`}, err, effective)
	second := deterministicToolFailureFingerprint(tool, ToolCall{ToolCallID: durableFailureCall2, ArgumentsJSON: `{ "a": 1, "b": 2 }`}, err, effective)
	if first == "" || first != second {
		t.Fatalf("semantic fingerprints differ: %q %q", first, second)
	}
	changedArguments := deterministicToolFailureFingerprint(tool, ToolCall{ToolCallID: "call_3", ArgumentsJSON: `{"a":2,"b":2}`}, err, effective)
	changedError := deterministicToolFailureFingerprint(tool, ToolCall{ArgumentsJSON: `{"a":1,"b":2}`}, markedDeterministicFailure{message: "another error"}, effective)
	changedContract := deterministicToolFailureFingerprint(tool, ToolCall{ArgumentsJSON: `{"a":1,"b":2}`}, err, effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{ExpectedArtifact: "review"}})
	if first == changedArguments || first == changedError || first == changedContract {
		t.Fatalf("material changes reused fingerprint: %q %q %q %q", first, changedArguments, changedError, changedContract)
	}
	if !deterministicWorkspaceToolFailure(err) {
		t.Fatal("marked Story validation failure was not classified as deterministic")
	}
	if deterministicWorkspaceToolFailure(ErrInvalidInput) || deterministicWorkspaceToolFailure(context.DeadlineExceeded) {
		t.Fatal("non-Story or transient failure was classified as deterministic")
	}
}

func TestTerminalWorkspaceArtifactResultCompletesContractWithoutAnotherLLMCall(t *testing.T) {
	effective := effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{
		ExpectedArtifact: testArtifactChangeSet,
		Request:          ResolvedWorkspaceContext{ResourceID: testStoryID},
		Policy:           testWorkspacePolicy(testArtifactChangeSet),
	}}
	results := []ToolResult{{OutputJSON: `{
		"projection": {
			"kind": "story_change_set",
			"title": "Change Set ready",
			"summary": "Review and apply it.",
			"preview": {"artifactType":"change_set","storyID":"story_1"}
		}
	}`}}

	text, terminal := terminalWorkspaceArtifactResult(effective, results)
	if !terminal || text != "Change Set ready\n\nReview and apply it." {
		t.Fatalf("terminal artifact result = (%q, %t)", text, terminal)
	}

	for name, mutated := range map[string]effectiveTextRunConfig{
		"different contract": {Workspace: &WorkspaceSnapshot{ExpectedArtifact: testArtifactReview, Request: ResolvedWorkspaceContext{ResourceID: testStoryID}, Policy: testWorkspacePolicy(testArtifactReview)}},
		"different story":    {Workspace: &WorkspaceSnapshot{ExpectedArtifact: testArtifactChangeSet, Request: ResolvedWorkspaceContext{ResourceID: "story_2"}, Policy: testWorkspacePolicy(testArtifactChangeSet)}},
		"reply contract":     {Workspace: &WorkspaceSnapshot{ExpectedArtifact: testReplyContract, Request: ResolvedWorkspaceContext{ResourceID: testStoryID}, Policy: testWorkspacePolicy(testReplyContract)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, terminal := terminalWorkspaceArtifactResult(mutated, results); terminal {
				t.Fatal("mismatched artifact was terminal")
			}
		})
	}
}

func TestTerminalWorkspaceArtifactResultCompletesAutoContract(t *testing.T) {
	for _, artifactType := range []string{testArtifactChangeSet, testArtifactReview} {
		t.Run(artifactType, func(t *testing.T) {
			effective := effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{ExpectedArtifact: valueAuto60DC1905, Request: ResolvedWorkspaceContext{ResourceID: testStoryID}, Policy: testWorkspacePolicy(valueAuto60DC1905)}}
			result := ToolResult{OutputJSON: fmt.Sprintf(`{"projection":{"kind":"story_artifact","title":"Ready","summary":"Review it.","preview":{"artifactType":%q,"storyID":%q}}}`, artifactType, testStoryID)}
			if text, terminal := terminalWorkspaceArtifactResult(effective, []ToolResult{result}); !terminal || text != "Ready\n\nReview it." {
				t.Fatalf("auto artifact result = (%q, %t)", text, terminal)
			}
		})
	}
}

func TestRequiresWorkspaceArtifact(t *testing.T) {
	for _, contract := range []string{testArtifactChangeSet, testArtifactReview} {
		if !requiresWorkspaceArtifact(effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{ExpectedArtifact: contract, Policy: testWorkspacePolicy(contract)}}) {
			t.Fatalf("contract %q was not required", contract)
		}
	}
	for _, contract := range []string{"", valueAuto60DC1905, testReplyContract} {
		if requiresWorkspaceArtifact(effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{ExpectedArtifact: contract, Policy: testWorkspacePolicy(contract)}}) {
			t.Fatalf("contract %q was unexpectedly required", contract)
		}
	}
	if requiresWorkspaceArtifact(effectiveTextRunConfig{}) {
		t.Fatal("missing workspace unexpectedly required an artifact")
	}
}

func TestDurableFailureCountSurvivesCallIDChanges(t *testing.T) {
	events := []model.Event{
		{EventType: valueToolFailedFB145984, ToolCallID: durableFailureCall1, PayloadJSON: `{"failureFingerprint":"same","repeatCount":1}`},
		{EventType: valueToolFailedFB145984, ToolCallID: durableFailureCall2, PayloadJSON: `{"failureFingerprint":"different","repeatCount":1}`},
		{EventType: valueToolFailedFB145984, ToolCallID: "call_after_resume", PayloadJSON: `{"failureFingerprint":"same","repeatCount":1,"retryable":true}`},
	}
	if got := advanceConsecutiveFailureCount(events, "same", 0); got != 1 {
		t.Fatalf("materially changed failure did not reset count: %d", got)
	}
	events = append(events, model.Event{EventType: valueToolFailedFB145984, ToolCallID: "call_after_resume_2", PayloadJSON: `{"failureFingerprint":"same","repeatCount":2,"retryable":false}`})
	if got := advanceConsecutiveFailureCount(events, "same", 0); got != maxIdenticalDeterministicToolFailures {
		t.Fatalf("resumed durable failures = %d", got)
	}
	events = append(events, model.Event{EventType: valueToolCompleted8D0A12FD, ToolCallID: "call_ok"})
	if got := advanceConsecutiveFailureCount(events, "same", 0); got != 0 {
		t.Fatalf("successful correction did not reset count: %d", got)
	}
}

type durableFailureTestRepository struct {
	Store
	events  []model.Event
	nextSeq int64
}

func (r *durableFailureTestRepository) ListRunEventsAfter(_ context.Context, _ model.ActorRef, _ string, afterSeq int64, limit int) ([]model.Event, error) {
	result := make([]model.Event, 0)
	for _, event := range r.events {
		if int64(event.Seq) <= afterSeq {
			continue
		}
		result = append(result, event)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (r *durableFailureTestRepository) CommitRunToolResultBundle(_ context.Context, _ *model.Checkpoint, output *model.OutputRef, events []model.Event) (*model.OutputRef, []model.Event, bool, error) {
	saved, err := r.AppendRunEvents(context.Background(), events)
	if err != nil {
		return nil, nil, false, err
	}
	return output, saved, true, nil
}

func TestCommitFrozenToolResultBreaksOnSecondDurableFailure(t *testing.T) {
	repo := &durableFailureTestRepository{}
	service := &Engine{
		repo:              repo,
		generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{}),
	}
	run := model.Run{RunID: "run_durable_failure", Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}}
	tool := ResolvedTool{ProviderKind: testWorkspaceProviderKind, ProviderKey: testWorkspaceProviderKind, ToolKey: "story.publish", ModelName: durableFailureTool, OriginalName: durableFailureTool}
	effective := effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{ExpectedArtifact: "change_set"}}
	executionErr := markedDeterministicFailure{message: "invalid Foundation field"}

	_, _, err := service.commitFrozenToolResult(t.Context(), run, "step_1", effective, tool, ToolCall{ToolCallID: durableFailureCall1, ArgumentsJSON: `{"operations":[]}`}, "", 0, ToolExecutionReceipt{}, executionErr)
	if err != nil {
		t.Fatalf("first failure returned terminal error: %v", err)
	}
	_, _, err = service.commitFrozenToolResult(t.Context(), run, "step_1", effective, tool, ToolCall{ToolCallID: durableFailureCall2, ArgumentsJSON: `{"operations":[]}`}, "", 0, ToolExecutionReceipt{}, executionErr)
	if !errors.Is(err, errRepeatedDeterministicWorkspaceToolFailure) {
		t.Fatalf("second failure error = %v", err)
	}

	assertDurableFailureEvents(t, repo.events)
}

func assertDurableFailureEvents(t *testing.T, events []model.Event) {
	t.Helper()
	failed := make([]model.Event, 0, 2)
	for _, event := range events {
		if event.EventType == valueToolFailedFB145984 {
			failed = append(failed, event)
		}
	}
	if len(failed) != 2 {
		t.Fatalf("durable tool failures = %d, want 2", len(failed))
	}
	for index, event := range failed {
		var payload struct {
			Fingerprint string `json:"failureFingerprint"`
			RepeatCount int    `json:"repeatCount"`
			Retryable   bool   `json:"retryable"`
		}
		if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil || payload.Fingerprint == "" || payload.RepeatCount != index+1 || payload.Retryable != (index == 0) {
			t.Fatalf("failure payload %d = %s", index+1, event.PayloadJSON)
		}
	}
}

func TestNormalizeRunInteractionResponseIsCanonical(t *testing.T) {
	left, _, err := normalizeRunInteractionResponse(map[string]interface{}{"feedback": "change", valueActionBE52611B: valueRevise90165516})
	if err != nil {
		t.Fatal(err)
	}
	right, _, err := normalizeRunInteractionResponse(map[string]interface{}{valueActionBE52611B: valueRevise90165516, "feedback": "change"})
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("normalized response must be stable: %s != %s", left, right)
	}
}

func TestRunEventSummaryIsBoundedWithoutLosingStepPayload(t *testing.T) {
	long := strings.Repeat("结", 300)
	event := newRunEvent(model.Run{RunID: "run_test"}, "step.completed", "step_test", long, map[string]interface{}{"resultSummary": long}, nil)
	if len([]rune(event.Summary)) != 255 {
		t.Fatalf("event summary length = %d", len([]rune(event.Summary)))
	}
	if !strings.Contains(event.PayloadJSON, long) {
		t.Fatal("full step result must remain in the durable payload")
	}
}

func TestRunEventStatusUsesUnifiedLifecycle(t *testing.T) {
	want := map[string]string{
		valueRunStartedBBF53B65:      model.RunStatusRunning,
		valueRunPreparingD85F5CAE:    model.RunStatusPreparing,
		valueRunWaitingInputE28A9912: model.RunStatusWaitingInput,
		valueRunSuspended4193AEC2:    model.RunStatusSuspended,
		valueRunCompletedCFE4A298:    model.RunStatusCompleted,
		valueRunFailed0B4285FA:       model.RunStatusFailed,
		valueRunCancelledD8649EC6:    model.RunStatusCancelled,
	}
	for eventType, status := range want {
		if actual := runEventStatus(eventType); actual != status {
			t.Fatalf("runEventStatus(%q) = %q, want %q", eventType, actual, status)
		}
	}
}

func TestRunInitialCheckpointResumeAlsoResumesRootStep(t *testing.T) {
	run := model.Run{RunID: "run_resume", CurrentStepID: "step_root"}
	checkpoint := model.Checkpoint{CheckpointID: "checkpoint_initial", Kind: "initial_context"}
	successor := model.Checkpoint{CheckpointID: "checkpoint_successor", StepID: run.CurrentStepID, Kind: "resume_execution"}
	events := runExplicitResumeEvents(run, checkpoint, successor, model.RunStatusPreparing, []string{run.CurrentStepID}, runContinuationStartPlanning)
	if len(events) != 3 {
		t.Fatalf("resume event count = %d, want 3", len(events))
	}
	if events[0].EventType != valueCheckpointCreatedEEEB685F || !strings.Contains(events[0].PayloadJSON, successor.CheckpointID) {
		t.Fatalf("unexpected successor event: %#v", events[0])
	}
	if events[1].EventType != valueStepResumedFD23056A || events[1].StepID != run.CurrentStepID {
		t.Fatalf("initial checkpoint must resume the orchestration step: %#v", events[1])
	}
	if events[2].EventType != valueRunResumedBED4B593 || events[2].Status != model.RunStatusPreparing {
		t.Fatalf("unexpected run resume event: %#v", events[2])
	}
}

func TestRunContinuationCheckpointIsFailClosedAndTamperEvident(t *testing.T) {
	run := model.Run{RunID: "run_continuation"}
	toolArgs := json.RawMessage(`{"query":"safe"}`)
	toolFingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte("mcp.search\x00search\x00"+canonicalRunJSON(toolArgs))))
	frozenInteraction := &runFrozenInteraction{InteractionID: "interaction_renew", Type: model.InteractionAskUser, StepID: valueStep83D64102, ToolCallID: "call_ask", Request: json.RawMessage(`{"question":"Scope?"}`), ResponseSchema: json.RawMessage(runInteractionResponseSchema(model.InteractionAskUser))}
	frozenInteraction.Fingerprint = runInteractionSnapshotFingerprint(*frozenInteraction)
	cases := []runContinuation{
		{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: "run_continuation:start", Type: runContinuationStartPlanning, TargetStatus: model.RunStatusPreparing, StepID: valueRoot809EA865, NextRevision: 1},
		{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: "run_continuation:resolve:answer", Type: runContinuationContinuePlan, TargetStatus: model.RunStatusRunning, StepID: valueStep83D64102, DurableToolResult: &runDurableToolResult{ToolCallID: "call_answer", EventType: valueToolCompleted1C0A7F48}},
		{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: "run_continuation:resolve:revise", Type: runContinuationReplan, TargetStatus: model.RunStatusPreparing, InteractionID: valueInteraction947864A7, PlanID: "plan", StepID: valueRoot809EA865, SourceStepID: valueStep83D64102, NextRevision: 2, Feedback: "revise safely"},
		{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: "run_continuation:resolve:tool", Type: runContinuationExecuteApprovedTool, TargetStatus: model.RunStatusRunning, InteractionID: valueInteraction947864A7, StepID: valueStep83D64102, FrozenToolCall: &runFrozenToolCall{ToolKey: "mcp.search", ToolName: "search", OriginalName: "search", ToolCallID: "call_tool", Arguments: toolArgs, Fingerprint: toolFingerprint}},
		{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: "run_continuation:interaction:interaction_renew", Type: runContinuationRenewInteraction, TargetStatus: model.RunStatusWaitingInput, InteractionID: "interaction_renew", StepID: valueStep83D64102, FrozenInteraction: frozenInteraction},
	}
	for _, continuation := range cases {
		checkpoint := *newRunContinuationCheckpoint(run, continuation.StepID, "runtime", continuation)
		decoded, err := decodeRunContinuation(checkpoint)
		if err != nil || decoded.Type != continuation.Type {
			t.Fatalf("continuation %s decode=%#v err=%v", continuation.Type, decoded, err)
		}
	}
	missing := *newRunCheckpoint(run, valueRoot809EA865, "runtime", map[string]interface{}{"legacy": true})
	if _, err := decodeRunContinuation(missing); !errors.Is(err, ErrRunSnapshotIncompatible) {
		t.Fatalf("missing continuation error=%v", err)
	}
	tampered := *newRunContinuationCheckpoint(run, valueRoot809EA865, "runtime", cases[0])
	tampered.ResumeStateJSON = strings.Replace(tampered.ResumeStateJSON, `"nextRevision":1`, `"nextRevision":2`, 1)
	if _, err := decodeRunContinuation(tampered); !errors.Is(err, ErrRunSnapshotIncompatible) {
		t.Fatalf("tampered continuation error=%v", err)
	}
	unsealed := *newRunContinuationCheckpoint(run, valueRoot809EA865, "runtime", cases[0])
	unsealed.ManifestHash = ""
	if _, err := decodeRunContinuation(unsealed); !errors.Is(err, ErrRunSnapshotIncompatible) {
		t.Fatalf("unsealed continuation error=%v", err)
	}
	wrongRun := *newRunContinuationCheckpoint(run, valueRoot809EA865, "runtime", cases[0])
	wrongRun.RunID = "run_other"
	if _, err := decodeRunContinuation(wrongRun); !errors.Is(err, ErrRunSnapshotIncompatible) {
		t.Fatalf("wrong run continuation error=%v", err)
	}
	legacyState, err := json.Marshal(map[string]interface{}{"continuation": cases[0]})
	if err != nil {
		t.Fatal(err)
	}
	legacy := *newRunContinuationCheckpoint(run, valueRoot809EA865, "runtime", cases[0])
	legacy.ResumeStateJSON = string(legacyState)
	legacy.ManifestHash = fmt.Sprintf("%x", sha256.Sum256(legacyState))
	if _, err = decodeRunContinuation(legacy); !errors.Is(err, ErrRunSnapshotIncompatible) {
		t.Fatalf("legacy continuation error=%v", err)
	}
}

func TestRunWaitingInteractionsFreezeRenewalContinuation(t *testing.T) {
	run := model.Run{RunID: "run_interaction_renewal"}
	for _, kind := range []string{model.InteractionSubmitPlan, model.InteractionApproveStep, model.InteractionApproveTool, model.InteractionApproveToolSet, model.InteractionAskUser} {
		t.Run(kind, func(t *testing.T) {
			assertWaitingInteractionFrozen(t, run, kind)
		})
	}
}

func assertWaitingInteractionFrozen(t *testing.T, run model.Run, kind string) {
	t.Helper()
	interaction := newRunInteraction(run, "step_waiting", kind, map[string]interface{}{valuePrompt390B7B69: "durable request"}, 24)
	if waitingInteractionHasToolCall(kind) {
		interaction.ToolCallID = "call_waiting"
	}
	checkpoint, err := newRunInteractionCheckpoint(run, interaction, "waiting_input")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRunContinuation(*checkpoint)
	assertRenewalContinuation(t, decoded, err)
	assertFrozenInteractionSnapshot(t, interaction, kind, decoded)
}

func waitingInteractionHasToolCall(kind string) bool {
	return kind == model.InteractionApproveTool || kind == model.InteractionAskUser
}

func assertRenewalContinuation(t *testing.T, decoded runContinuation, err error) {
	t.Helper()
	if err != nil || decoded.Type != runContinuationRenewInteraction || decoded.TargetStatus != model.RunStatusWaitingInput || decoded.FrozenInteraction == nil {
		t.Fatalf("renewal continuation=%#v err=%v", decoded, err)
	}
}

func assertFrozenInteractionSnapshot(t *testing.T, interaction *model.Interaction, kind string, decoded runContinuation) {
	t.Helper()
	frozen := decoded.FrozenInteraction
	if frozen.InteractionID != interaction.InteractionID || frozen.Type != kind || frozen.StepID != interaction.StepID || frozen.ToolCallID != interaction.ToolCallID || frozen.Fingerprint != runInteractionSnapshotFingerprint(*frozen) {
		t.Fatalf("waiting interaction was not frozen completely: interaction=%#v continuation=%#v", interaction, decoded)
	}
}

func TestRunInteractionResolutionBuildsCompleteContinuation(t *testing.T) {
	run := model.Run{RunID: "run_resolution", CurrentPlanID: "plan_current"}
	segmentKey := run.RunID + ":resolve:test"
	base := model.Interaction{InteractionID: valueInteraction947864A7, StepID: valueStep83D64102}
	approvedPlan := base
	approvedPlan.Type = model.InteractionSubmitPlan
	continuation, err := buildRunResolutionContinuation(run, approvedPlan, approvedPlan.StepID, segmentKey, "", 0, false, nil)
	assertApprovedPlanContinuation(t, continuation, err)
	ask := base
	ask.Type, ask.ToolCallID = model.InteractionAskUser, "call_ask"
	continuation, err = buildRunResolutionContinuation(run, ask, ask.StepID, segmentKey, "", 0, false, nil)
	assertAskUserContinuation(t, continuation, ask.ToolCallID, err)
	denied := base
	denied.Type, denied.ToolCallID = model.InteractionApproveTool, "call_denied"
	continuation, err = buildRunResolutionContinuation(run, denied, denied.StepID, segmentKey, "", 0, false, nil)
	assertDeniedToolContinuation(t, continuation, err)
	arguments := json.RawMessage(`{"path":"report"}`)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte("mcp.writer\x00writer\x00"+canonicalRunJSON(arguments))))
	frozen := &runFrozenToolCall{ToolKey: "mcp.writer", ToolName: "writer", OriginalName: "write_file", ToolCallID: "call_approved", Arguments: arguments, Fingerprint: fingerprint}
	continuation, err = buildRunResolutionContinuation(run, denied, denied.StepID, segmentKey, "", 0, true, frozen)
	assertApprovedToolContinuation(t, continuation, frozen.ToolCallID, err)
	revise := base
	revise.Type = model.InteractionApproveStep
	continuation, err = buildRunResolutionContinuation(run, revise, valueRoot809EA865, segmentKey, "keep the durable answer", 3, false, nil)
	assertRevisionContinuation(t, continuation, revise.StepID, err)
}

func assertApprovedPlanContinuation(t *testing.T, continuation runContinuation, err error) {
	t.Helper()
	if err != nil || continuation.Type != runContinuationContinuePlan || continuation.TargetStatus != model.RunStatusRunning {
		t.Fatalf("approved plan continuation=%#v err=%v", continuation, err)
	}
}

func assertAskUserContinuation(t *testing.T, continuation runContinuation, toolCallID string, err error) {
	t.Helper()
	if err != nil || continuation.DurableToolResult == nil || continuation.DurableToolResult.ToolCallID != toolCallID || continuation.DurableToolResult.EventType != valueToolCompleted1C0A7F48 {
		t.Fatalf("ask_user continuation=%#v err=%v", continuation, err)
	}
}

func assertDeniedToolContinuation(t *testing.T, continuation runContinuation, err error) {
	t.Helper()
	if err != nil || continuation.DurableToolResult == nil || continuation.DurableToolResult.EventType != valueToolFailedE0DC1562 {
		t.Fatalf("denied tool continuation=%#v err=%v", continuation, err)
	}
}

func assertApprovedToolContinuation(t *testing.T, continuation runContinuation, toolCallID string, err error) {
	t.Helper()
	if err != nil || continuation.Type != runContinuationExecuteApprovedTool || continuation.FrozenToolCall == nil || continuation.FrozenToolCall.ToolCallID != toolCallID {
		t.Fatalf("approved tool continuation=%#v err=%v", continuation, err)
	}
}

func assertRevisionContinuation(t *testing.T, continuation runContinuation, sourceStepID string, err error) {
	t.Helper()
	if err != nil || continuation.Type != runContinuationReplan || continuation.NextRevision != 3 || continuation.Feedback != "keep the durable answer" || continuation.StepID != valueRoot809EA865 || continuation.SourceStepID != sourceStepID {
		t.Fatalf("revision continuation=%#v err=%v", continuation, err)
	}
}

func TestRunStepExecutionModeContinuesCurrentRunningStep(t *testing.T) {
	current := model.Step{StepID: "step_current", Status: model.RunStatusRunning}
	if execute, appendStarted := runStepExecutionMode(current, current.StepID); !execute || appendStarted {
		t.Fatalf("current running step mode = (%v, %v), want (true, false)", execute, appendStarted)
	}
	if execute, _ := runStepExecutionMode(current, "step_other"); execute {
		t.Fatal("a stale non-current running step must not be executed")
	}
	queued := model.Step{StepID: "step_queued", Status: model.RunStatusQueued}
	if execute, appendStarted := runStepExecutionMode(queued, current.StepID); !execute || !appendStarted {
		t.Fatalf("queued step mode = (%v, %v), want (true, true)", execute, appendStarted)
	}
}
