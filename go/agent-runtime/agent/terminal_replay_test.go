package agent_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func terminalReplayFixture(t *testing.T, content, marker string) *model.ProviderReplay {
	t.Helper()
	raw, err := json.Marshal([]any{map[string]any{
		"type": "message", "phase": "final_answer", "id": marker,
		"content": []any{map[string]any{"type": "output_text", "text": content}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &model.ProviderReplay{Provider: "openai", Format: "responses.output.v1", Data: raw}
}

func assertTerminalReplayMessage(t *testing.T, snapshot kernel.Snapshot, content string, want *model.ProviderReplay) {
	t.Helper()
	var state struct {
		Messages []model.Message `json:"messages"`
	}
	if err := json.Unmarshal(snapshot.State, &state); err != nil {
		t.Fatal(err)
	}
	for _, message := range state.Messages {
		if message.Role == model.RoleAssistant && model.TextValue(message.Content) == content {
			replay := message.ProviderReplay
			if replay == nil || replay.Provider != want.Provider || replay.Format != want.Format || !bytes.Equal(replay.Data, want.Data) {
				t.Fatal("trusted assistant message lost or changed its provider replay")
			}
			return
		}
	}
	t.Fatal("trusted assistant message is missing")
}

func assertTerminalReplayNotPublic(t *testing.T, snapshot kernel.Snapshot, markers ...string) {
	t.Helper()
	view, err := agent.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range view.Messages {
		if message.ProviderReplay != nil {
			t.Fatal("public message exposed provider replay")
		}
	}
	for _, invocation := range view.ModelInvocations {
		if invocation.Response.ProviderReplay != nil {
			t.Fatal("public receipt exposed provider replay")
		}
		for _, message := range invocation.Request.Messages {
			if message.ProviderReplay != nil {
				t.Fatal("public frozen request exposed provider replay")
			}
		}
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range markers {
		if bytes.Contains(raw, []byte(marker)) {
			t.Fatal("private replay marker escaped the public view")
		}
	}
}

func TestTerminalProviderReplayPersistsWithoutAliasingOrPublicExposure(t *testing.T) {
	const content = `{"answer":"done"}`
	const marker = "private-terminal-evidence"
	replay := terminalReplayFixture(t, "\n"+content+"\n", marker)
	want := model.CloneProviderReplay(replay)
	provider := &structuredFixtureModel{supports: true, responses: []model.Response{
		{Content: "\n" + content + "\n", ProviderReplay: replay},
	}}
	runner, runtime := structuredRunner(t, provider)
	completed, err := runner.StartRun(t.Context(), structuredRequest("terminal-replay"))
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("terminal replay run did not complete: status=%s err=%v", completed.Run.Status, err)
	}
	stored, err := runtime.Load(t.Context(), completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalReplayMessage(t, stored, content, want)
	assertTerminalReplayNotPublic(t, stored, marker)

	// The model still owns its returned buffer. Later model-side changes must
	// not alter the committed assistant transcript.
	replay.Provider, replay.Format, replay.Data[0] = "changed", "changed", 'X'
	stored, err = runtime.Load(t.Context(), completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalReplayMessage(t, stored, content, want)
	assertTerminalReplayNotPublic(t, stored, marker)
}

func TestRejectedCompletionReplaySurvivesRestartAndRepair(t *testing.T) {
	const rejected, corrected = `{"answer":123}`, `{"answer":"fixed"}`
	const rejectedMarker, correctedMarker = "private-rejected-evidence", "private-corrected-evidence"
	rejectedReplay := terminalReplayFixture(t, "\n"+rejected+"\n", rejectedMarker)
	correctedReplay := terminalReplayFixture(t, corrected, correctedMarker)
	wantRejected, wantCorrected := model.CloneProviderReplay(rejectedReplay), model.CloneProviderReplay(correctedReplay)
	provider := &structuredFixtureModel{supports: true, responses: []model.Response{
		{Content: "\n" + rejected + "\n", ProviderReplay: rejectedReplay},
		{Content: corrected, ProviderReplay: correctedReplay},
	}}
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{Store: base, eventType: "agent.completion_corrected", afterCommit: true}
	first, err := agent.NewRunner(agent.Dependencies{Runtime: newModelInvocationRuntime(t, faults), Model: provider})
	if err != nil {
		t.Fatal(err)
	}
	request := structuredRequest("corrected-replay-restart")
	_, err = first.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedModelInvocationCrash) || len(provider.requests) != 1 {
		t.Fatalf("did not stop after the correction commit: calls=%d err=%v", len(provider.requests), err)
	}
	runtime := newModelInvocationRuntime(t, base)
	stored, err := runtime.Load(t.Context(), request.ID)
	if err != nil || stored.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("correction was not durable: status=%s err=%v", stored.Run.Status, err)
	}
	assertTerminalReplayMessage(t, stored, rejected, wantRejected)
	assertTerminalReplayNotPublic(t, stored, rejectedMarker)
	rejectedReplay.Data[0], rejectedReplay.Provider = 'X', "changed"

	second, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: provider})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := second.Resume(t.Context(), stored.Run.ID, stored.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(provider.requests) != 2 || completed.Run.ID != stored.Run.ID {
		t.Fatalf("repair failed or replayed a model call: status=%s calls=%d err=%v", completed.Run.Status, len(provider.requests), err)
	}
	var repairReplay *model.ProviderReplay
	for _, message := range provider.requests[1].Messages {
		if message.Role == model.RoleAssistant && model.TextValue(message.Content) == rejected {
			repairReplay = message.ProviderReplay
		}
	}
	if repairReplay == nil || repairReplay == rejectedReplay || !bytes.Equal(repairReplay.Data, wantRejected.Data) {
		t.Fatal("repair request lost the committed rejected-final replay")
	}
	repairReplay.Data[0], correctedReplay.Data[0] = 'X', 'X'
	stored, err = runtime.Load(t.Context(), completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalReplayMessage(t, stored, rejected, wantRejected)
	assertTerminalReplayMessage(t, stored, corrected, wantCorrected)
	assertTerminalReplayNotPublic(t, stored, rejectedMarker, correctedMarker)
}
