package agentruntime

import (
	"encoding/json"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestTruncatePublicRunProgressUsesUnicodeRuneLimit(t *testing.T) {
	value := strings.Repeat("界", 319) + "👩‍💻extra"
	got := truncatePublicRunProgress(value)
	if len([]rune(got)) != 320 || !strings.HasSuffix(got, "…") {
		t.Fatalf("progress length=%d suffix=%q", len([]rune(got)), got[len(got)-3:])
	}
	if truncatePublicRunProgress("  ready  ") != "ready" {
		t.Fatal("progress should be trimmed")
	}
}

func TestPublicRunProgressEventIDDeduplicatesAcrossSteps(t *testing.T) {
	first := publicRunProgressEventID("run_progress", "正在检查结果")
	second := publicRunProgressEventID("run_progress", "  正在检查结果  ")
	if first != second {
		t.Fatalf("same public progress should share one durable event id: %q != %q", first, second)
	}
	if first == publicRunProgressEventID("run_other", "正在检查结果") {
		t.Fatal("progress event ids must remain isolated by run")
	}
}

func TestPublicRunProgressFromModelTextSuppressesToolAssociatedText(t *testing.T) {
	if got := publicRunProgressFromModelText("Looking up the scene blocks next.", true); got != "" {
		t.Fatalf("tool-associated text leaked to progress: %q", got)
	}
	if got := publicRunProgressFromModelText("<tool_call>story_publish_change_set</tool_call>", false); got != "" {
		t.Fatalf("tool_call markup leaked to progress: %q", got)
	}
	if got := publicRunProgressFromModelText("<｜DSML｜tool_calls>invoke</｜DSML｜tool_calls>", false); got != "" {
		t.Fatalf("DSML markup leaked to progress: %q", got)
	}
	if got := publicRunProgressFromModelText("<|DSML|tool_calls><|DSML|invoke name=\"story_publish_change_set\">", false); got != "" {
		t.Fatalf("ASCII DSML markup leaked to progress: %q", got)
	}
	if got := publicRunProgressFromModelText("  Checking continuity facts.  ", false); got != "Checking continuity facts." {
		t.Fatalf("safe commentary progress = %q", got)
	}
}

func TestClassifyModelTextDetectsProtocolAndPublicLanguage(t *testing.T) {
	if classifyModelText("") != ModelTextEmpty {
		t.Fatal("empty")
	}
	if classifyModelText("Continue the scene carefully.") != ModelTextPublic {
		t.Fatal("public")
	}
	if classifyModelText(`<|DSML|tool_calls><|DSML|invoke name="story_publish_change_set">`) != ModelTextToolProtocol {
		t.Fatal("dsml protocol")
	}
	// Angle brackets in normal prose/code must not be over-blocked.
	if classifyModelText("Use the <div> tag in HTML examples.") != ModelTextPublic {
		t.Fatal("html prose should stay public")
	}
}

func TestToolChoiceForRunStepRequiresNativeCallsForChangeSet(t *testing.T) {
	choice := toolChoiceForRunStep(effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{ExpectedArtifact: testArtifactChangeSet, Policy: testWorkspacePolicy(testArtifactChangeSet)}}, false)
	if choice.Mode != ToolChoiceRequired {
		t.Fatalf("change_set tool choice = %#v", choice)
	}
	choice = toolChoiceForRunStep(effectiveTextRunConfig{}, false)
	if choice.Mode != ToolChoiceAuto {
		t.Fatalf("reply tool choice = %#v", choice)
	}
	choice = toolChoiceForRunStep(effectiveTextRunConfig{}, true)
	if choice.Mode != ToolChoiceRequired {
		t.Fatalf("forced required = %#v", choice)
	}
}

func TestToolProtocolRejectedPayloadForcesRequired(t *testing.T) {
	t.Parallel()
	payload := toolProtocolRejectedPayload(2, string(ModelTextToolProtocol))
	if payload[valueRetryCount] != 2 {
		t.Fatalf("retryCount = %#v", payload[valueRetryCount])
	}
	if payload[valueNextToolChoice] != string(ToolChoiceRequired) {
		t.Fatalf("nextToolChoice = %#v", payload[valueNextToolChoice])
	}
	if payload[valueReasonB5B063AA] != string(ModelTextToolProtocol) {
		t.Fatalf("reason = %#v", payload[valueReasonB5B063AA])
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !eventForcesToolChoiceRequired(string(raw)) {
		t.Fatal("expected required force from payload")
	}
	if eventForcesToolChoiceRequired(`{"nextToolChoice":"auto"}`) {
		t.Fatal("auto must not force required")
	}
}

func TestForceToolChoiceRequiredFromEventsIsDurablePerStep(t *testing.T) {
	t.Parallel()
	events := []model.Event{
		{EventType: valueModelToolProtocolRejected, StepID: "step_a", PayloadJSON: `{"nextToolChoice":"required","retryCount":1}`},
		{EventType: "progress.created", StepID: "step_a", PayloadJSON: `{}`},
		{EventType: valueModelToolProtocolRejected, StepID: "step_b", PayloadJSON: `{"nextToolChoice":"auto"}`},
	}
	if !forceToolChoiceRequiredFromEvents(events, "step_a") {
		t.Fatal("step_a should rehydrate required")
	}
	if forceToolChoiceRequiredFromEvents(events, "step_b") {
		t.Fatal("step_b auto payload must not force required")
	}
	if forceToolChoiceRequiredFromEvents(events, "step_missing") {
		t.Fatal("unrelated step must not force required")
	}
}

func TestRequiredToolCallFailureAssistantContentIsSafe(t *testing.T) {
	effective := effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{Policy: WorkspacePolicy{Failure: WorkspaceFailurePolicy{RequiredToolCallAssistantContent: "工作区工具调用格式无效，未发布所需产物。"}}}}
	raw, err := json.Marshal(effective)
	if err != nil {
		t.Fatal(err)
	}
	content := (&Engine{}).failedAssistantContent(t.Context(), model.Run{RunConfigSnapshotJSON: string(raw)}, errRequiredToolCallNotProduced)
	if content == "" || looksLikeToolProtocolText(content) {
		t.Fatalf("unsafe failure content: %q", content)
	}
	if runFailureCode(errRequiredToolCallNotProduced) != "required_tool_call_not_parsed" {
		t.Fatalf("error code = %q", runFailureCode(errRequiredToolCallNotProduced))
	}
}
