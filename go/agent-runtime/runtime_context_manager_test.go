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
	contextTestDecision   = "decision"
	contextTestImageMIME  = "image/png"
	contextTestOld        = "old"
	contextTestToolCallID = "call-1"
)

var errContextSummaryUnavailable = errors.New("summary unavailable")

type fixedContextTokenCounter struct {
	result ContextTokenCountResult
	err    error
}

type fixedContextManagerThreadSource struct {
	messages []ContextMessage
	request  LoadThreadPathRequest
}

func (source *fixedContextManagerThreadSource) ResolveThread(context.Context, ResolveThreadRequest) (ThreadSnapshot, error) {
	return ThreadSnapshot{}, nil
}

func (source *fixedContextManagerThreadSource) LoadThreadPath(_ context.Context, request LoadThreadPathRequest) (ThreadPath, error) {
	source.request = request
	messages := append([]ContextMessage(nil), source.messages...)
	if request.MaxDepth > 0 && len(messages) > request.MaxDepth {
		messages = messages[len(messages)-request.MaxDepth:]
	}
	return ThreadPath{Messages: messages}, nil
}

type contextManagerRepo struct {
	*multiTurnRunRepo
	snapshots  []model.ContextSnapshot
	artifacts  []model.ContextArtifact
	checkpoint *model.Checkpoint
}

func (repo *contextManagerRepo) GetRunContextSnapshot(_ context.Context, _ model.ActorRef, _ string) (*model.ContextSnapshot, error) {
	if len(repo.snapshots) == 0 {
		return nil, ErrNotFound
	}
	latest := repo.snapshots[len(repo.snapshots)-1]
	return &latest, nil
}

func (repo *contextManagerRepo) CreateContextSnapshotBundle(_ context.Context, snapshot *model.ContextSnapshot, artifacts []model.ContextArtifact, checkpoint *model.Checkpoint, events []model.Event) ([]model.Event, error) {
	for _, existing := range repo.snapshots {
		if existing.SnapshotID == snapshot.SnapshotID {
			if existing.ContentHash == snapshot.ContentHash && existing.Revision == snapshot.Revision {
				return nil, nil
			}
			return nil, ErrDuplicate
		}
	}
	repo.snapshots = append(repo.snapshots, *snapshot)
	repo.artifacts = append(repo.artifacts, artifacts...)
	if checkpoint != nil {
		copied := *checkpoint
		repo.checkpoint = &copied
	}
	return repo.AppendRunEvents(context.Background(), events)
}

func (repo *contextManagerRepo) ListRecentContextArtifactsByKind(_ context.Context, _ model.ActorRef, _ model.ThreadRef, kind model.ContextArtifactKind, limit int) ([]model.ContextArtifact, error) {
	result := make([]model.ContextArtifact, 0)
	for index := len(repo.artifacts) - 1; index >= 0; index-- {
		if repo.artifacts[index].Kind != kind {
			continue
		}
		result = append(result, repo.artifacts[index])
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (repo *contextManagerRepo) GetRunEvent(_ context.Context, _ model.ActorRef, runID, eventID string) (*model.Event, error) {
	for index := range repo.events {
		if repo.events[index].RunID == runID && repo.events[index].EventID == eventID {
			event := repo.events[index]
			return &event, nil
		}
	}
	return nil, ErrNotFound
}

func (counter fixedContextTokenCounter) CountContextTokens(context.Context, ContextTokenCountInput) (ContextTokenCountResult, error) {
	return counter.result, counter.err
}

func TestEstimateGenerateInputTokensAccountsForCompleteRequest(t *testing.T) {
	base := GenerateInput{Messages: []Message{{Role: valueUser81BE622D, Content: valueHello636D88EC}}}
	baseTokens := estimateGenerateInputTokens(base)
	inputs := []GenerateInput{
		{Messages: []Message{{Role: valueUser81BE622D, Content: valueHello636D88EC, Parts: []ContentPart{{Kind: ContentPartImage, MimeType: contextTestImageMIME, Data: []byte(strings.Repeat("x", 512))}}}}},
		{Messages: []Message{{Role: valueAssistantCE8D479A, Content: valueHello636D88EC, ReasoningContent: strings.Repeat("reasoning ", 40)}}},
		{Messages: []Message{{Role: valueAssistantCE8D479A, Content: valueHello636D88EC, ToolCalls: []ToolCall{{ToolCallID: contextTestToolCallID, ToolName: valueLookupE85B2FAE, ArgumentsJSON: strings.Repeat("argument", 40), ThoughtSignature: "signature"}}}}},
		{Messages: []Message{{Role: valueToolCCF14517, ToolResults: []ToolResult{{ToolCallID: contextTestToolCallID, ToolName: valueLookupE85B2FAE, OutputJSON: strings.Repeat("result", 80), Status: valueSuccess4D886D19}}}}},
		{Messages: base.Messages, Instructions: strings.Repeat("instruction ", 40)},
		{Messages: base.Messages, Tools: []ToolDefinition{{Name: valueLookupE85B2FAE, Description: strings.Repeat("description ", 40), InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}}},
		{Messages: base.Messages, HostedTools: []HostedTool{{ToolKey: "web", Protocol: "responses", Payload: map[string]interface{}{"domains": []string{"example.com"}, "depth": 5}}}},
		{Messages: base.Messages, Options: map[string]interface{}{valueReasoningF71D8C63: map[string]interface{}{valueEffort941F75BE: valueHighB19D217F}, "max_output_tokens": 2048}},
	}
	for index, input := range inputs {
		if got := estimateGenerateInputTokens(input); got <= baseTokens {
			t.Fatalf("case %d token estimate = %d, base = %d", index, got, baseTokens)
		}
	}
}

func TestCountContextTokensAppliesSafetyOnlyToEstimatedCounts(t *testing.T) {
	estimatedEngine := &Engine{}
	estimated := estimatedEngine.countContextTokens(context.Background(), nil, GenerateInput{Messages: []Message{{Role: valueUser81BE622D, Content: strings.Repeat("a", 400)}}}, 15)
	if estimated.Source != ContextTokenCountEstimated || estimated.AdjustedTokens != (estimated.Tokens*115+99)/100 {
		t.Fatalf("estimated count = %+v", estimated)
	}
	exactEngine := &Engine{contextTokenCounter: fixedContextTokenCounter{result: ContextTokenCountResult{Tokens: 42}}}
	exact := exactEngine.countContextTokens(context.Background(), nil, GenerateInput{}, 15)
	if exact.Source != ContextTokenCountExact || exact.Tokens != 42 {
		t.Fatalf("exact count = %+v", exact)
	}
	assessment := exactEngine.assessContextBudget(context.Background(), nil, effectiveTextRunConfig{PlatformModelName: "gpt-4o", Context: ContextConfig{MaxInputTokens: 1000}}, GenerateInput{})
	if assessment.HardInputTokens != 1000 || assessment.AdjustedTokenEstimate != 42 || assessment.TokenCountSource != ContextTokenCountExact {
		t.Fatalf("exact assessment = %+v", assessment)
	}
}

func TestContextBudgetUsesSmallerModelOrServerLimitAndIndependentByteLimit(t *testing.T) {
	route := &LLMRoute{PlatformModelName: "custom-small", ModelCapabilitiesJSON: `{"contextWindow":20000,"maxOutputTokens":1000}`}
	if got := contextHardInputBudget(ContextConfig{MaxInputTokens: 5_000}, route, "custom-small"); got != 5_000 {
		t.Fatalf("server-limited hard budget = %d", got)
	}
	if got := contextHardInputBudget(ContextConfig{MaxInputTokens: 10_000}, route, "custom-small"); got != 6_000 {
		t.Fatalf("model-limited hard budget = %d", got)
	}
	if got := contextSoftInputBudget(5_000, 80); got != 4_000 {
		t.Fatalf("soft budget = %d", got)
	}
	assessment := ContextBudgetAssessment{HardInputTokens: 1_000, AdjustedTokenEstimate: 900, SerializedBytes: contextTransportByteBudget(1_000) + 1}
	if contextInputWithinHardBudget(assessment) {
		t.Fatal("serialized byte overflow passed the independent hard limit")
	}
}

func TestContextSummaryCutPreservesRecentCompleteTurns(t *testing.T) {
	messages := make([]ContextMessage, 0, 40)
	for turn := 0; turn < 20; turn++ {
		messages = append(messages,
			ContextMessage{Role: valueUser81BE622D, Content: "question", Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "u" + string(rune('a'+turn))}},
			ContextMessage{Role: valueAssistantCE8D479A, Content: "answer", Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "a" + string(rune('a'+turn))}},
		)
	}
	cut := contextSummaryCut(messages, 8)
	if cut != 24 || len(messages[cut:]) != 16 || messages[cut].Role != valueUser81BE622D {
		t.Fatalf("cut = %d, retained = %d", cut, len(messages[cut:]))
	}
}

func TestContextSummaryCutPreservesEightCompleteTurnsAndCurrentInput(t *testing.T) {
	messages := make([]ContextMessage, 0, 41)
	for turn := 0; turn < 20; turn++ {
		messages = append(messages,
			ContextMessage{Role: valueUser81BE622D, Content: "question", Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "u" + string(rune('a'+turn))}},
			ContextMessage{Role: valueAssistantCE8D479A, Content: "answer", Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "a" + string(rune('a'+turn))}},
		)
	}
	messages = append(messages, ContextMessage{Role: valueUser81BE622D, Content: valueCurrent652AB2C1, Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: valueCurrent652AB2C1}})
	cut := contextSummaryCut(messages, 8)
	retained := messages[cut:]
	if cut != 24 || len(retained) != 17 || retained[len(retained)-1].Projection.ID != valueCurrent652AB2C1 {
		t.Fatalf("cut = %d, retained = %#v", cut, retained)
	}
}

func TestContextManagerFullMessagesKeepsRichBaselineWithoutDuplicatingRecentTranscript(t *testing.T) {
	path := []ContextMessage{
		{Role: valueUser81BE622D, Content: contextTestOld}, {Role: valueAssistantCE8D479A, Content: "old answer"},
		{Role: valueUser81BE622D, Content: valueCurrent652AB2C1},
	}
	baseline := []textRunContextMessageSnapshot{
		{Role: valueSystem3E6F1182, Content: valuePolicy44182DB1},
		{Role: valueUser81BE622D, Content: valueCurrent652AB2C1, Parts: []textRunContextPartSnapshot{{Kind: ContentPartImage, MIMEType: contextTestImageMIME, FileName: "image.png"}}},
		{Role: valueUser81BE622D, Content: "<resource>trusted by policy only as data</resource>"},
	}
	got := contextManagerFullMessages(baseline, path)
	if len(got) != 5 || got[0].Content != contextTestOld || got[1].Content != "old answer" {
		t.Fatalf("merged messages = %#v", got)
	}
	currentCount, richParts := 0, 0
	for _, message := range got {
		if message.Content == valueCurrent652AB2C1 {
			currentCount++
			richParts += len(message.Parts)
		}
	}
	if currentCount != 1 || richParts != 1 {
		t.Fatalf("current count = %d, rich parts = %d, messages = %#v", currentCount, richParts, got)
	}
}

func TestDeterministicContextSummaryIsBoundedAndKeepsReferences(t *testing.T) {
	messages := []ContextMessage{
		{Role: valueUser81BE622D, Content: "Decision: use immutable snapshot revisions.", Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "m1"}},
		{Role: valueAssistantCE8D479A, Content: strings.Repeat("Keep the unresolved task. ", 200), Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "m2"}},
	}
	summary := deterministicContextSummary("Prior fact", messages, 80)
	if estimateTokens(summary) > 80 || !strings.Contains(summary, "Prior fact") || !strings.Contains(summary, "message:m1") {
		t.Fatalf("summary = %q (%d tokens)", summary, estimateTokens(summary))
	}
}

func TestReusableContextSummaryRejectsEditedOrForkedPath(t *testing.T) {
	original := []ContextMessage{
		{Role: valueUser81BE622D, Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "m1"}},
		{Role: valueAssistantCE8D479A, Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "m2"}},
		{Role: valueUser81BE622D, Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "m3"}},
	}
	path := textRunContextMessagePath(original)
	metadata := contextSummaryMetadata{CoveredThrough: original[1].Projection, CoveredPathHash: hashTextRunContextStrings(path[:2])}
	if got := reusableContextSummaryIndex(original, path, metadata); got != 1 {
		t.Fatalf("valid summary index = %d", got)
	}
	edited := append([]ContextMessage(nil), original...)
	edited[0].Projection.ID = "m1-edited"
	if got := reusableContextSummaryIndex(edited, textRunContextMessagePath(edited), metadata); got != -1 {
		t.Fatalf("edited path reused summary at %d", got)
	}
	forked := append([]ContextMessage(nil), original...)
	forked[1].Projection.ID = "m2-fork"
	if got := reusableContextSummaryIndex(forked, textRunContextMessagePath(forked), metadata); got != -1 {
		t.Fatalf("forked path reused summary at %d", got)
	}
}

func TestCompactOversizedToolResultsCreatesArtifactReference(t *testing.T) {
	run := model.Run{RunID: "run-tool-budget", OutputProjection: model.ProjectionRef{Kind: valueMessage69246916, ID: "out"}}
	content := strings.Repeat("tool-output-", 2000)
	messages := []Message{{Role: valueToolCCF14517, ToolResults: []ToolResult{{ToolCallID: contextTestToolCallID, ToolName: valueLookupE85B2FAE, OutputJSON: content, Status: valueSuccess4D886D19}}}}
	compacted, artifacts, actions := compactOversizedToolResults(run, messages, 4_000, nil)
	if len(artifacts) != 1 || len(actions) != 1 || artifacts[0].Content != content || artifacts[0].Kind != model.ContextArtifactToolResult {
		t.Fatalf("artifacts = %+v, actions = %+v", artifacts, actions)
	}
	if !strings.Contains(compacted[0].ToolResults[0].OutputJSON, artifacts[0].ArtifactID) || !strings.Contains(compacted[0].ToolResults[0].OutputJSON, artifacts[0].ContentHash) {
		t.Fatalf("compacted result = %q", compacted[0].ToolResults[0].OutputJSON)
	}
	if messages[0].ToolResults[0].OutputJSON != content {
		t.Fatal("compaction mutated caller-owned messages")
	}
}

func TestGenerateInputGuardRejectsRequiredContentBeforeUpstream(t *testing.T) {
	engine := &Engine{}
	run := model.Run{RunID: "run-required-budget"}
	effective := effectiveTextRunConfig{PlatformModelName: "gpt-4o", Context: ContextConfig{MaxInputTokens: 100, EstimateSafetyPercent: 15}}
	input := GenerateInput{Messages: []Message{{Role: valueUser81BE622D, Content: strings.Repeat("required-current-input ", 200)}}}
	_, assessment, err := engine.enforceGenerateInputBudget(context.Background(), run, effective, nil, input)
	if !errors.Is(err, ErrContextBudgetExceeded) || assessment.AdjustedTokenEstimate <= assessment.HardInputTokens {
		t.Fatalf("guard = assessment %+v, err %v", assessment, err)
	}
}

func TestNormalizeContextConfigDefaultsToManaged(t *testing.T) {
	got := normalizeContextConfig(ContextConfig{})
	if got.ManagementMode != ContextManagementManaged || got.MaxTurns != 48 || got.MaxMessages != 20 || got.PreserveRecentTurns != 8 || got.SoftLimitPercent != 80 || got.SummaryMaxTokens != 1024 || got.EstimateSafetyPercent != 15 {
		t.Fatalf("defaults = %+v", got)
	}
	legacy := normalizeContextConfig(ContextConfig{ManagementMode: ContextManagementLegacy})
	if legacy.ManagementMode != ContextManagementLegacy {
		t.Fatalf("legacy mode = %q", legacy.ManagementMode)
	}
}

type managedContextTestFixture struct {
	run          model.Run
	baseline     model.ContextSnapshot
	repo         *contextManagerRepo
	threadSource *fixedContextManagerThreadSource
	gateway      *scriptedLLMGateway
	engine       *Engine
	effective    effectiveTextRunConfig
	checkpoint   model.Checkpoint
	current      ContextMessage
}

func newManagedContextTestFixture(t *testing.T) managedContextTestFixture {
	t.Helper()
	allMessages := make([]ContextMessage, 0, 201)
	for turn := 0; turn < 100; turn++ {
		allMessages = append(allMessages,
			ContextMessage{Role: valueUser81BE622D, Content: "question " + string(rune(0x1000+turn)), Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "user-" + string(rune(0x1000+turn))}},
			ContextMessage{Role: valueAssistantCE8D479A, Content: "answer " + string(rune(0x2000+turn)), Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "assistant-" + string(rune(0x2000+turn))}},
		)
	}
	current := ContextMessage{Role: valueUser81BE622D, Content: "current request must remain", Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "current-input"}}
	allMessages = append(allMessages, current)
	baselineMessages := make([]textRunContextMessageSnapshot, 0, 19)
	for _, message := range allMessages[len(allMessages)-19:] {
		baselineMessages = append(baselineMessages, textRunContextMessageSnapshot{Role: message.Role, Content: message.Content})
	}
	run := model.Run{
		RunID: "run-managed-100-turns", RequestID: "request-managed-100-turns", CurrentStepID: "step-root",
		Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey},
		InputProjection: current.Projection, OutputProjection: model.ProjectionRef{Kind: valueMessage69246916, ID: valueOutput6DD2E13C},
	}
	payload := textRunContextSnapshotPayload{SemanticVersion: RuntimeSnapshotVersion, RunID: run.RunID, Messages: baselineMessages}
	content, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	baseline := model.ContextSnapshot{
		SnapshotID: contextSnapshotID(run.RunID, 1), RunID: run.RunID, Revision: 1, ManagementStatus: model.ContextManagementStatusBaseline,
		Actor: run.Actor, Thread: run.Thread, InputProjection: run.InputProjection, SchemaVersion: RuntimeSnapshotVersion,
		ContentJSON: string(content), ContentHash: hashTextRunContextStrings([]string{string(content)}),
	}
	repo := &contextManagerRepo{multiTurnRunRepo: &multiTurnRunRepo{}, snapshots: []model.ContextSnapshot{baseline}}
	threadSource := &fixedContextManagerThreadSource{messages: allMessages}
	gateway := &scriptedLLMGateway{outputs: []*GenerateOutput{{Text: "semantic summary", Usage: Usage{InputTokens: 120, OutputTokens: 8}}}}
	engine := &Engine{repo: repo, threadContext: threadSource, llmGateway: gateway, generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{})}
	effective := effectiveTextRunConfig{
		PlatformModelName: testRouteModelName, MaxLLMCalls: 2,
		Context: ContextConfig{ManagementMode: ContextManagementManaged, MaxTurns: 48, MaxMessages: 20, PreserveRecentTurns: 8, MaxInputTokens: 100_000, SummaryMaxTokens: 128},
	}
	return managedContextTestFixture{
		run: run, baseline: baseline, repo: repo, threadSource: threadSource, gateway: gateway, engine: engine, effective: effective,
		checkpoint: model.Checkpoint{CheckpointID: "checkpoint-initial", RunID: run.RunID, StepID: run.CurrentStepID, Kind: runCheckpointInitialContext}, current: current,
	}
}

func assertManagedContextDurability(t *testing.T, fixture managedContextTestFixture) {
	t.Helper()
	assertContextTest(t, fixture.threadSource.request.MaxDepth == 97, "max depth = %d", fixture.threadSource.request.MaxDepth)
	assertContextTest(t, len(fixture.repo.snapshots) == 2, "snapshots = %#v", fixture.repo.snapshots)
	assertContextTest(t, fixture.repo.snapshots[1].Revision == 2, "snapshots = %#v", fixture.repo.snapshots)
	assertContextTest(t, fixture.repo.snapshots[1].SupersedesSnapshotID == fixture.baseline.SnapshotID, "snapshots = %#v", fixture.repo.snapshots)
	assertContextTest(t, len(fixture.repo.artifacts) == 1, "artifacts = %#v", fixture.repo.artifacts)
	assertContextTest(t, fixture.repo.artifacts[0].Kind == model.ContextArtifactSummary, "artifacts = %#v", fixture.repo.artifacts)
	assertContextTest(t, fixture.repo.artifacts[0].ArtifactID != "", "artifacts = %#v", fixture.repo.artifacts)
	assertContextTest(t, len(fixture.gateway.inputs) == 1, "summary inputs = %#v", fixture.gateway.inputs)
	assertContextTest(t, fixture.gateway.inputs[0].DisableTools, "summary inputs = %#v", fixture.gateway.inputs)
	assertContextTest(t, fixture.gateway.inputs[0].RequestID != "", "summary inputs = %#v", fixture.gateway.inputs)
	assertContextTest(t, countRunEvents(fixture.repo.events, valueUsageUpdatedABC8B0B2) == 1, "events = %#v", fixture.repo.events)
	assertContextTest(t, countRunEvents(fixture.repo.events, "context.compacted") == 1, "events = %#v", fixture.repo.events)
	assertContextTest(t, countRunEvents(fixture.repo.events, "context.management_completed") == 1, "events = %#v", fixture.repo.events)
	assertContextTest(t, fixture.repo.checkpoint != nil, "checkpoint = %#v", fixture.repo.checkpoint)
	assertContextTest(t, fixture.repo.checkpoint.ContextSnapshotID == fixture.repo.snapshots[1].SnapshotID, "checkpoint = %#v", fixture.repo.checkpoint)
	assertContextTest(t, fixture.repo.checkpoint.Status == model.CheckpointConsumed, "checkpoint = %#v", fixture.repo.checkpoint)
}

func assertManagedContextMessages(t *testing.T, fixture managedContextTestFixture) {
	t.Helper()
	managed := decodeManagedContextPayload(t, fixture.repo.snapshots[1])
	assertContextTest(t, managed.Management != nil, "management = %#v", managed.Management)
	assertContextTest(t, managed.Management.LoadedMessageCount == 97, "management = %#v", managed.Management)
	assertContextTest(t, managed.Management.RetainedMessageCount == 17, "management = %#v", managed.Management)
	assertContextTest(t, managed.Management.SummarizedMessageCount == 80, "management = %#v", managed.Management)
	completeTurns, currentInputs := managedContextMessageCounts(managed.Messages, fixture.current.Content)
	assertContextTest(t, completeTurns == 8, "managed messages retain complete turns=%d current=%d: %#v", completeTurns, currentInputs, managed.Messages)
	assertContextTest(t, currentInputs == 1, "managed messages retain complete turns=%d current=%d: %#v", completeTurns, currentInputs, managed.Messages)
}

func decodeManagedContextPayload(t *testing.T, snapshot model.ContextSnapshot) textRunContextSnapshotPayload {
	t.Helper()
	var managed textRunContextSnapshotPayload
	if err := json.Unmarshal([]byte(snapshot.ContentJSON), &managed); err != nil {
		t.Fatal(err)
	}
	return managed
}

func managedContextMessageCounts(messages []textRunContextMessageSnapshot, currentContent string) (int, int) {
	completeTurns, currentInputs := 0, 0
	for index, message := range messages {
		if message.Content == currentContent {
			currentInputs++
		}
		if message.Role == valueUser81BE622D && index+1 < len(messages) && messages[index+1].Role == valueAssistantCE8D479A {
			completeTurns++
		}
	}
	return completeTurns, currentInputs
}

func assertContextTest(t *testing.T, condition bool, format string, args ...interface{}) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

func TestManagedContextCompactsLongBranchIntoImmutableRevisionAndIsRetrySafe(t *testing.T) {
	fixture := newManagedContextTestFixture(t)
	if err := fixture.engine.manageInitialRunContext(context.Background(), fixture.run, fixture.effective, fixture.checkpoint); err != nil {
		t.Fatal(err)
	}
	assertManagedContextDurability(t, fixture)
	assertManagedContextMessages(t, fixture)

	// A continuation retry reads the latest managed revision and neither calls
	// the summary model nor appends another immutable snapshot.
	if err := fixture.engine.manageInitialRunContext(context.Background(), fixture.run, fixture.effective, fixture.checkpoint); err != nil {
		t.Fatal(err)
	}
	if len(fixture.repo.snapshots) != 2 || len(fixture.gateway.inputs) != 1 || countRunEvents(fixture.repo.events, valueUsageUpdatedABC8B0B2) != 1 {
		t.Fatalf("retry changed durable state: snapshots=%d calls=%d events=%#v", len(fixture.repo.snapshots), len(fixture.gateway.inputs), fixture.repo.events)
	}
}

func TestSemanticSummaryFailureConsumesOneLLMCallAndFallsBackIdempotently(t *testing.T) {
	repo := &contextManagerRepo{multiTurnRunRepo: &multiTurnRunRepo{}}
	gateway := &scriptedLLMGateway{errors: []error{errContextSummaryUnavailable}}
	engine := &Engine{repo: repo, llmGateway: gateway, generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{})}
	run := model.Run{
		RunID: "run-summary-failure", CurrentStepID: "step", Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey},
		Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey},
	}
	effective := effectiveTextRunConfig{PlatformModelName: testRouteModelName, MaxLLMCalls: 2, Context: ContextConfig{MaxInputTokens: 10_000}}
	path := []ContextMessage{{Role: valueUser81BE622D, Content: "fact", Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "m1"}}, {Role: valueAssistantCE8D479A, Content: contextTestDecision, Projection: model.ProjectionRef{Kind: valueMessage69246916, ID: "m2"}}}
	route, _ := gateway.PrepareTextRoute(context.Background(), LLMRouteInput{})
	first := engine.generateContextSummary(context.Background(), run, effective, route, "stable-path-hash", "", path, 128)
	second := engine.generateContextSummary(context.Background(), run, effective, route, "stable-path-hash", "", path, 128)
	if !first.Fallback || !second.Fallback || len(gateway.inputs) != 1 {
		t.Fatalf("results=(%+v, %+v), calls=%d", first, second, len(gateway.inputs))
	}
	if countRunEvents(repo.events, eventLLMRouteSelected) != 1 || countRunEvents(repo.events, valueUsageUpdatedABC8B0B2) != 1 {
		t.Fatalf("events = %#v", repo.events)
	}
}

func TestLLMCallBudgetCountsDispatchedRouteWhenUsageWasNotRecorded(t *testing.T) {
	repo := &multiTurnRunRepo{}
	engine := &Engine{repo: repo, generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{})}
	run := model.Run{RunID: "run-route-only-budget", Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}}
	route := &LLMRoute{PlatformModelName: valueModel22D48A8A, UpstreamName: "upstream", BindingCode: "binding", UpstreamModel: valueModel22D48A8A, Protocol: AdapterOpenAIResponses}
	if err := engine.recordRunLLMRouteSelected(context.Background(), run, "step", "generation", route, "request-1"); err != nil {
		t.Fatal(err)
	}
	effective := effectiveTextRunConfig{MaxLLMCalls: 2}
	if err := engine.ensureRunCallBudgetWithReserve(context.Background(), run, effective, true, 1); err != nil {
		t.Fatalf("last main call should remain available: %v", err)
	}
	if err := engine.ensureRunCallBudgetWithReserve(context.Background(), run, effective, true, 2); err == nil {
		t.Fatal("route-selected attempt was not counted against MaxLLMCalls")
	}
}
