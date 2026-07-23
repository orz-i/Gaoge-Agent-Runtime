package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	testReadToolName    = "story_get_document_blocks"
	testPublishToolName = "story_publish_change_set"
	testReadToolKey     = "story.read_blocks"
	testPublishToolKey  = "story.publish"
	testDSMLPublishText = `<|DSML|tool_calls><|DSML|invoke name="story_publish_change_set">`
	testOperationsJSON  = `{"operations":[]}`
	testStepAID         = "step_a"
	testSideEffectWrite = "write"
	testAskToolCallID   = "call_ask"
)

type scriptedLLMGateway struct {
	outputs []*GenerateOutput
	inputs  []GenerateInput
}

func (g *scriptedLLMGateway) PrepareTextRoute(context.Context, LLMRouteInput) (*LLMRoute, error) {
	return &LLMRoute{PlatformModelName: "test-model", Protocol: AdapterOpenAIResponses, UpstreamModel: "test-model"}, nil
}

func (g *scriptedLLMGateway) PrepareDefaultTextRoute(context.Context, LLMRouteInput) (*LLMRoute, error) {
	return g.PrepareTextRoute(context.Background(), LLMRouteInput{})
}

func (g *scriptedLLMGateway) GenerateText(_ context.Context, _ *LLMRoute, input GenerateInput) (*GenerateOutput, error) {
	g.inputs = append(g.inputs, input)
	if len(g.outputs) == 0 {
		return &GenerateOutput{Text: "fallback"}, nil
	}
	out := g.outputs[0]
	g.outputs = g.outputs[1:]
	return out, nil
}

func (g *scriptedLLMGateway) GenerateTextStream(context.Context, *LLMRoute, GenerateInput, func(GenerateStreamEvent) error) (*GenerateOutput, error) {
	return nil, errCategoryFBB8372B5B
}

type multiTurnRunRepo struct {
	Store
	events       []model.Event
	nextSeq      int64
	outputs      []model.OutputRef
	interactions []model.Interaction
	checkpoints  []model.Checkpoint
	snapshot     *model.ContextSnapshot
}

func (r *multiTurnRunRepo) AppendRunEvent(_ context.Context, item *model.Event) (*model.Event, bool, error) {
	r.nextSeq++
	saved := *item
	saved.Seq = r.nextSeq
	r.events = append(r.events, saved)
	return &saved, true, nil
}

func (r *multiTurnRunRepo) AppendRunEvents(_ context.Context, items []model.Event) ([]model.Event, error) {
	saved := make([]model.Event, 0, len(items))
	for _, item := range items {
		r.nextSeq++
		item.Seq = r.nextSeq
		r.events = append(r.events, item)
		saved = append(saved, item)
	}
	return saved, nil
}

func (r *multiTurnRunRepo) ListRunEventsAfter(_ context.Context, _ model.ActorRef, _ string, afterSeq int64, limit int) ([]model.Event, error) {
	result := make([]model.Event, 0)
	for _, event := range r.events {
		if int64(event.Seq) <= afterSeq {
			continue
		}
		result = append(result, event)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (r *multiTurnRunRepo) CountRunEventsByType(_ context.Context, _ model.ActorRef, _ string, eventTypes []string) (map[string]int, error) {
	counts := make(map[string]int, len(eventTypes))
	wanted := map[string]struct{}{}
	for _, eventType := range eventTypes {
		wanted[eventType] = struct{}{}
	}
	for _, event := range r.events {
		if _, ok := wanted[event.EventType]; ok {
			counts[event.EventType]++
		}
	}
	return counts, nil
}

func (r *multiTurnRunRepo) CreateRunInteractionBundle(_ context.Context, _ string, _ string, interaction *model.Interaction, checkpoint *model.Checkpoint, events []model.Event) ([]model.Event, error) {
	r.interactions = append(r.interactions, *interaction)
	r.checkpoints = append(r.checkpoints, *checkpoint)
	return r.AppendRunEvents(context.Background(), events)
}

func (r *multiTurnRunRepo) CommitRunToolResultBundle(_ context.Context, _ *model.Checkpoint, output *model.OutputRef, events []model.Event) (*model.OutputRef, []model.Event, bool, error) {
	saved, err := r.AppendRunEvents(context.Background(), events)
	if err != nil {
		return nil, nil, false, err
	}
	if output != nil {
		r.outputs = append(r.outputs, *output)
	}
	return output, saved, true, nil
}

func (r *multiTurnRunRepo) ListOutputs(_ context.Context, _ model.ActorRef, _ string) ([]model.OutputRef, error) {
	return append([]model.OutputRef(nil), r.outputs...), nil
}

func (r *multiTurnRunRepo) GetRunContextSnapshot(_ context.Context, _ model.ActorRef, _ string) (*model.ContextSnapshot, error) {
	return r.snapshot, nil
}

type scriptedWorkspace struct {
	calls []string
}

func (w *scriptedWorkspace) CompileWorkspace(context.Context, model.ActorRef, model.ThreadRef, *WorkspaceRequest, int) (*WorkspaceSnapshot, error) {
	return nil, ErrInvalidInput
}

func (w *scriptedWorkspace) ExecuteWorkspaceTool(_ context.Context, input WorkspaceToolExecution) (string, error) {
	w.calls = append(w.calls, input.ToolName)
	if input.ToolName == testPublishToolName {
		return `{
			"projection": {
				"kind": "story_change_set",
				"title": "Change Set ready",
				"summary": "Review and apply it.",
				"preview": {"artifactType":"change_set","storyID":"story_1"}
			}
		}`, nil
	}
	return `{"blocks":[]}`, nil
}

func TestExecuteRunStepAutoRecoversFromDSMLWithRequiredToolChoice(t *testing.T) {
	gateway := &scriptedLLMGateway{
		outputs: []*GenerateOutput{
			{Text: testDSMLPublishText},
			{Text: "Natural language recovery without protocol markup."},
		},
	}
	repo := &multiTurnRunRepo{}
	service := &Engine{
		repo:              repo,
		llmGateway:        gateway,
		generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{}),
	}
	run := model.Run{RunID: "run_auto_dsml", Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}, RequestID: "req_auto"}
	step := model.Step{StepID: "step_work", Title: "Draft", Description: "Write reply"}
	effective := effectiveTextRunConfig{MaxLLMCalls: 4, MaxToolCalls: 8}

	text, _, waiting, err := service.executeRunStep(context.Background(), run, step, effective, nil, nil, nil)
	if err != nil || waiting {
		t.Fatalf("executeRunStep err=%v waiting=%v", err, waiting)
	}
	if !strings.Contains(text, "Natural language recovery") {
		t.Fatalf("result = %q", text)
	}
	assertToolChoiceSequence(t, gateway.inputs, ToolChoiceAuto, ToolChoiceRequired)
	if !hasRunEventType(repo.events, valueModelToolProtocolRejected) {
		t.Fatal("expected model.tool_protocol_rejected event")
	}
}

func TestExecuteRunStepChangeSetMultiTurnDSMLThenNativeTools(t *testing.T) {
	gateway := &scriptedLLMGateway{
		outputs: []*GenerateOutput{
			{Text: testDSMLPublishText},
			{ToolCalls: []ToolCall{{ToolCallID: "call_read", ToolName: testReadToolName, ArgumentsJSON: `{}`}}},
			{ToolCalls: []ToolCall{{ToolCallID: "call_publish", ToolName: testPublishToolName, ArgumentsJSON: testOperationsJSON}}},
		},
	}
	repo := &multiTurnRunRepo{}
	workspace := &scriptedWorkspace{}
	service := &Engine{
		repo:              repo,
		llmGateway:        gateway,
		workspaces:        NewWorkspaceRegistry(map[string]WorkspaceProvider{testWorkspaceProviderKind: workspace}),
		generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{}),
	}
	run := model.Run{RunID: "run_cs_dsml", Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}, RequestID: "req_cs"}
	step := model.Step{StepID: "step_cs", Title: "Publish", Description: "Create change set"}
	readPolicy := storyToolPolicy(testReadToolKey, testReadToolName)
	publishPolicy := storyToolPolicy(testPublishToolKey, testPublishToolName)
	effective := changeSetRunConfig(readPolicy, publishPolicy)
	tools := map[string]ResolvedTool{
		testReadToolName:    policyToResolvedTool(readPolicy),
		testPublishToolName: policyToResolvedTool(publishPolicy),
	}

	text, _, waiting, err := service.executeRunStep(context.Background(), run, step, effective, tools, nil, nil)
	if err != nil || waiting {
		t.Fatalf("executeRunStep err=%v waiting=%v", err, waiting)
	}
	if !strings.Contains(text, "Change Set ready") {
		t.Fatalf("terminal text = %q", text)
	}
	assertAllToolChoicesRequired(t, gateway.inputs, 3)
	if !hasRunEventType(repo.events, valueModelToolProtocolRejected) {
		t.Fatal("expected protocol rejection event after DSML turn")
	}
	assertWorkspaceCallOrder(t, workspace.calls, testReadToolName, testPublishToolName)
}

func TestExecuteDirectStrategyStopsAtWaitingInteraction(t *testing.T) {
	policy := storyToolPolicy(testReadToolKey, testReadToolName)
	gateway := &scriptedLLMGateway{
		outputs: []*GenerateOutput{{
			ToolCalls: []ToolCall{{
				ToolCallID:    testAskToolCallID,
				ToolName:      runControlAskUser,
				ArgumentsJSON: `{"question":"Which track kind should be used?"}`,
			}},
		}},
	}
	run := model.Run{
		RunID:         "run_direct_waiting",
		Actor:         model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey},
		Thread:        model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey},
		RequestID:     "req_direct_waiting",
		CurrentStepID: "step_direct_waiting",
	}
	contextJSON, err := json.Marshal(textRunContextSnapshotPayload{
		SemanticVersion: RuntimeSnapshotVersion,
		RunID:           run.RunID,
		MessagePathHash: hashTextRunContextStrings(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	contextHash := sha256.Sum256(contextJSON)
	repo := &multiTurnRunRepo{
		snapshot: &model.ContextSnapshot{
			RunID:          run.RunID,
			SchemaVersion:  RuntimeSnapshotVersion,
			ThreadPathHash: hashTextRunContextStrings(nil),
			ContentJSON:    string(contextJSON),
			ContentHash:    hex.EncodeToString(contextHash[:]),
		},
	}
	service := &Engine{
		repo:              repo,
		llmGateway:        gateway,
		generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{}),
	}
	root := model.Step{StepID: "step_direct_waiting", Title: "Clarify", Description: "Ask for required input"}
	effective := effectiveTextRunConfig{
		SemanticVersion: RuntimeSnapshotVersion,
		Strategy:        TextRunStrategyDirect,
		MaxLLMCalls:     3,
		MaxToolCalls:    3,
		ToolKeys:        []string{policy.ToolKey},
		ToolPolicies:    []effectiveRunToolPolicy{policy},
	}

	service.executeDirectStrategy(context.Background(), run, root, effective, nil, nil, runUsage{})

	if len(repo.interactions) != 1 || len(repo.checkpoints) != 1 {
		t.Fatalf("interactions=%d checkpoints=%d, want one durable waiting bundle", len(repo.interactions), len(repo.checkpoints))
	}
	if !hasRunEventType(repo.events, "run.waiting_input") {
		t.Fatal("expected run.waiting_input event")
	}
	if hasRunEventType(repo.events, "message.delta") || hasRunEventType(repo.events, "step.completed") || hasRunEventType(repo.events, "run.completed") {
		t.Fatalf("waiting direct run emitted terminal events: %#v", repo.events)
	}
}

func TestPrepareRunStepExecutionRehydratesForcedToolChoice(t *testing.T) {
	repo := &multiTurnRunRepo{}
	gateway := &scriptedLLMGateway{}
	service := &Engine{repo: repo, llmGateway: gateway}
	run := model.Run{RunID: "run_rehydrate", Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}}
	step := model.Step{StepID: testStepAID, Title: "Work", Description: "Continue"}
	payload, err := json.Marshal(toolProtocolRejectedPayload(1, string(ModelTextToolProtocol)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = repo.AppendRunEvent(context.Background(), &model.Event{
		EventType:   valueModelToolProtocolRejected,
		StepID:      step.StepID,
		PayloadJSON: string(payload),
	}); err != nil {
		t.Fatal(err)
	}

	prepared, err := service.prepareRunStepExecution(context.Background(), run, step, effectiveTextRunConfig{MaxLLMCalls: 3}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.forceToolChoiceRequired {
		t.Fatal("expected forceToolChoiceRequired rehydrated from durable event")
	}
	if choice := toolChoiceForRunStep(effectiveTextRunConfig{}, prepared.forceToolChoiceRequired); choice.Mode != ToolChoiceRequired {
		t.Fatalf("tool choice = %#v", choice)
	}
	last := prepared.messages[len(prepared.messages)-1].Content
	if !strings.Contains(last, "provider-native") {
		t.Fatalf("expected correction prompt re-injected, last=%q", last)
	}
}

func TestStreamingDSMLDeltasNeverPublishMessageDelta(t *testing.T) {
	t.Parallel()
	var published []string
	collector := &runDeltaCollector{
		publishDelta: func(delta string) error {
			published = append(published, delta)
			return nil
		},
	}
	acceptStreamChunks(t, collector, "preface ", "<|", "DSML|tool", "_calls>", `<|DSML|invoke name="story_publish_change_set">`)
	if err := collector.flushFinal(); err != nil {
		t.Fatal(err)
	}
	assertNoProtocolInDeltas(t, published)
	if !collector.suppressed {
		t.Fatal("collector must suppress after protocol markup")
	}
}

func acceptStreamChunks(t *testing.T, collector *runDeltaCollector, chunks ...string) {
	t.Helper()
	for _, chunk := range chunks {
		if err := collector.accept(GenerateStreamEvent{Delta: chunk}); err != nil {
			t.Fatalf("accept(%q): %v", chunk, err)
		}
	}
}

func assertNoProtocolInDeltas(t *testing.T, published []string) {
	t.Helper()
	for _, delta := range published {
		lower := strings.ToLower(delta)
		if strings.Contains(lower, "dsml") || strings.Contains(delta, "tool_calls") || strings.Contains(delta, "story_publish") {
			t.Fatalf("protocol leaked into message.delta: %#v", published)
		}
	}
	if len(published) > 1 {
		t.Fatalf("unexpected deltas: %#v", published)
	}
	if len(published) == 1 && published[0] != "preface " {
		t.Fatalf("published = %#v", published)
	}
}

func assertToolChoiceSequence(t *testing.T, inputs []GenerateInput, want ...ToolChoiceMode) {
	t.Helper()
	if len(inputs) != len(want) {
		t.Fatalf("LLM calls = %d, want %d", len(inputs), len(want))
	}
	for index, mode := range want {
		if inputs[index].ToolChoice.Mode != mode {
			t.Fatalf("call%d tool choice = %#v, want %s", index+1, inputs[index].ToolChoice, mode)
		}
	}
}

func assertAllToolChoicesRequired(t *testing.T, inputs []GenerateInput, wantCalls int) {
	t.Helper()
	if len(inputs) != wantCalls {
		t.Fatalf("LLM calls = %d, want %d", len(inputs), wantCalls)
	}
	for index, input := range inputs {
		if input.ToolChoice.Mode != ToolChoiceRequired {
			t.Fatalf("call%d tool choice = %#v, want required", index+1, input.ToolChoice)
		}
	}
}

func assertWorkspaceCallOrder(t *testing.T, calls []string, want ...string) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("workspace calls = %#v, want %#v", calls, want)
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("workspace calls = %#v, want %#v", calls, want)
		}
	}
}

func changeSetRunConfig(policies ...effectiveRunToolPolicy) effectiveTextRunConfig {
	return effectiveTextRunConfig{
		MaxLLMCalls:  5,
		MaxToolCalls: 10,
		ToolPolicies: policies,
		Workspace: &WorkspaceSnapshot{
			ExpectedArtifact: testArtifactChangeSet,
			Request:          ResolvedWorkspaceContext{ResourceID: testStoryID, Type: testWorkspaceProviderKind},
			ContextBudget:    10_000,
			Policy:           testWorkspacePolicy(testArtifactChangeSet),
		},
	}
}

func storyToolPolicy(toolKey, modelName string) effectiveRunToolPolicy {
	policy := effectiveRunToolPolicy{
		ToolKey:            toolKey,
		ProviderKind:       testWorkspaceProviderKind,
		ProviderKey:        testWorkspaceProviderKind,
		ModelName:          modelName,
		OriginalName:       modelName,
		DefinitionVersion:  "v1",
		InputSchema:        json.RawMessage(`{"type":"object"}`),
		ExecutionMode:      valueLocalDispatchC00F9A8D,
		ApprovalCapability: valuePerCall065DDC2C,
		ApprovalMode:       valueNeverF5C79F24,
		RiskLevel:          valueLow9A37DEBA,
		SideEffectLevel:    testSideEffectWrite,
		RetryCount:         0,
		Concurrency:        1,
	}
	policy.Fingerprint = fingerprintRunToolSnapshot(policy)
	return policy
}

func policyToResolvedTool(policy effectiveRunToolPolicy) ResolvedTool {
	return ResolvedTool{
		ToolKey:            policy.ToolKey,
		ProviderKind:       policy.ProviderKind,
		ProviderKey:        policy.ProviderKey,
		ModelName:          policy.ModelName,
		OriginalName:       policy.OriginalName,
		DefinitionVersion:  policy.DefinitionVersion,
		InputSchema:        policy.InputSchema,
		ExecutionMode:      policy.ExecutionMode,
		ApprovalCapability: policy.ApprovalCapability,
		ApprovalMode:       policy.ApprovalMode,
		RiskLevel:          policy.RiskLevel,
		SideEffectLevel:    policy.SideEffectLevel,
	}
}

func hasRunEventType(events []model.Event, eventType string) bool {
	for _, event := range events {
		if event.EventType == eventType {
			return true
		}
	}
	return false
}
