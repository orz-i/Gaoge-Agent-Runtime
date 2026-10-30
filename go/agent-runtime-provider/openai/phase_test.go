package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	phaseCommentaryItem = `{"type":"message","id":"progress-1","role":"assistant","phase":"commentary","status":"completed","content":[{"type":"output_text","text":"  Working.\n"}],"fixture_opaque":{"keep":true}}`
	phaseFinalItem      = `{"type":"message","id":"answer-1","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"{\"ok\":"},{"type":"output_text","text":"true}"}],"fixture_opaque":"retain"}`
	phaseCallOne        = `{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"x\":1}"}`
	phaseCallTwo        = `{"type":"function_call","call_id":"call-2","name":"lookup","arguments":"{\"x\":2}"}`
	phaseReasoning      = `{"type":"reasoning","id":"reason-1","summary":[],"encrypted_content":"opaque-fixture"}`
	phaseBareReasoning  = `{"type":"reasoning","id":"reason-1","summary":[]}`
)

func phaseResponse(items ...string) string {
	return `{"id":"response-phase","status":"completed","output":[` + strings.Join(items, ",") +
		`],"usage":{"input_tokens":31,"output_tokens":13}}`
}

func writePhaseResponse(w http.ResponseWriter, body string, stream bool) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, body)
}

func TestPhaseFinalProjectionAndManualReplayUnaryAndStreaming(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			calls := 0
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				var payload struct {
					Input  []json.RawMessage `json:"input"`
					Stream bool              `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload.Stream != stream {
					t.Error("wrong transport mode")
				}
				if calls == 1 {
					writePhaseResponse(w, phaseResponse(phaseBareReasoning, phaseCommentaryItem, phaseFinalItem), stream)
					return
				}
				if len(payload.Input) != 4 || string(payload.Input[1]) != phaseCommentaryItem ||
					string(payload.Input[2]) != phaseFinalItem {
					t.Errorf("manual replay changed phase, order or opaque message fields: %s", payload.Input)
				}
				writePhaseResponse(w, phaseResponse(phaseFinalItem), stream)
			})
			request := model.Request{
				Model: "gpt-4.1", Messages: []model.Message{{Role: model.RoleUser, Content: "answer"}},
				OutputContract: &model.OutputContract{Name: "phase_probe",
					Schema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean","enum":[true]}},"required":["ok"],"additionalProperties":false}`)},
			}
			generate := func(request model.Request) (model.Response, error) {
				if stream {
					return client.GenerateStream(t.Context(), request, nil)
				}
				return client.Generate(t.Context(), request)
			}
			first, err := generate(request)
			if err != nil || first.Content != `{"ok":true}` || first.ProviderReplay == nil || first.Refused ||
				first.Usage == nil || first.Usage.InputTokens != 31 {
				t.Fatalf("commentary polluted final output: response=%+v err=%v", first, err)
			}
			var replay []json.RawMessage
			if err = json.Unmarshal(first.ProviderReplay.Data, &replay); err != nil || len(replay) != 2 ||
				string(replay[0]) != phaseCommentaryItem || string(replay[1]) != phaseFinalItem {
				t.Fatalf("message phase lost or unsigned reasoning promoted to replay: %s error=%v", first.ProviderReplay.Data, err)
			}
			request.Messages = append(request.Messages,
				model.Message{Role: model.RoleAssistant, Content: first.Content, ProviderReplay: first.ProviderReplay},
				model.Message{Role: model.RoleUser, Content: "confirm"})
			for _, content := range []string{"forged", "Working." + first.Content} {
				tampered := model.CloneRequest(request)
				tampered.Messages[1].Content = content
				if _, err = generate(tampered); !errors.Is(err, openai.ErrUnsupported) || calls != 1 {
					t.Fatalf("altered or legacy-flattened phase transcript dispatched: err=%v calls=%d", err, calls)
				}
			}
			second, err := generate(request)
			if err != nil || second.Content != `{"ok":true}` || calls != 2 {
				t.Fatalf("phase-preserving continuation failed: response=%+v err=%v calls=%d", second, err, calls)
			}
		})
	}
}

func TestPhaseToolReplayWithoutReasoningKeepsEveryCallBound(t *testing.T) {
	calls := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writePhaseResponse(w, phaseResponse(phaseCommentaryItem, phaseCallOne, phaseCallTwo), false)
			return
		}
		var payload struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload.Input) != 6 || string(payload.Input[1]) != phaseCommentaryItem ||
			string(payload.Input[2]) != phaseCallOne || string(payload.Input[3]) != phaseCallTwo {
			t.Errorf("phase/tool replay changed original items: %s", payload.Input)
		}
		writePhaseResponse(w, phaseResponse(phaseFinalItem), false)
	})
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "lookup"}}, Tools: []tools.Definition{definition}}
	first, err := client.Generate(t.Context(), request)
	if err != nil || first.Content != "  Working.\n" || len(first.ToolCalls) != 2 || first.ProviderReplay == nil {
		t.Fatalf("intermediate commentary or tool replay lost: response=%+v error=%v", first, err)
	}
	request.Messages = append(request.Messages,
		model.Message{Role: model.RoleAssistant, Content: strings.TrimSpace(first.Content), ToolCalls: first.ToolCalls, ProviderReplay: first.ProviderReplay},
		model.Message{Role: model.RoleTool, ToolCallID: "call-1", Content: "{}"},
		model.Message{Role: model.RoleTool, ToolCallID: "call-2", Content: "{}"})
	for _, tc := range []struct {
		name   string
		mutate func(*model.Message)
	}{
		{"content", func(message *model.Message) { message.Content = "forged" }},
		{"count", func(message *model.Message) { message.ToolCalls = message.ToolCalls[:1] }},
		{"order", func(message *model.Message) {
			message.ToolCalls[0], message.ToolCalls[1] = message.ToolCalls[1], message.ToolCalls[0]
		}},
		{"id", func(message *model.Message) { message.ToolCalls[0].ID = "forged" }},
		{"name", func(message *model.Message) { message.ToolCalls[0].Name = "forged" }},
		{"arguments", func(message *model.Message) { message.ToolCalls[0].Arguments = json.RawMessage(`{"x":9}`) }},
		{"authority", func(message *model.Message) { message.ToolCalls[0].ToolKey = "" }},
		{"extra-raw-call", func(message *model.Message) {
			message.ProviderReplay.Data = json.RawMessage("[" + strings.Join([]string{phaseCommentaryItem, phaseCallOne, phaseCallTwo, phaseCallOne}, ",") + "]")
		}},
		{"hidden-final", func(message *model.Message) {
			message.ProviderReplay.Data = json.RawMessage("[" + strings.Join([]string{phaseFinalItem, phaseCallOne, phaseCallTwo}, ",") + "]")
		}},
		{"phase-erased", func(message *model.Message) {
			message.ProviderReplay.Data = json.RawMessage(strings.ReplaceAll(string(message.ProviderReplay.Data), `"phase":"commentary",`, ""))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tampered := model.CloneRequest(request)
			tc.mutate(&tampered.Messages[1])
			if _, err := client.Generate(t.Context(), tampered); !errors.Is(err, openai.ErrUnsupported) || calls != 1 {
				t.Fatalf("replay bypassed committed identity: err=%v calls=%d", err, calls)
			}
		})
	}
	last, err := client.Generate(t.Context(), request)
	if err != nil || last.Content != `{"ok":true}` || calls != 2 {
		t.Fatalf("valid phase/tool continuation failed: response=%+v err=%v calls=%d", last, err, calls)
	}
}

func TestPhaseToolReplayStillRequiresEncryptedReasoning(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%t", encrypted), func(t *testing.T) {
			calls := 0
			reasoning := phaseBareReasoning
			if encrypted {
				reasoning = phaseReasoning
			}
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				writePhaseResponse(w, phaseResponse(reasoning, phaseCommentaryItem, phaseCallOne), false)
			})
			request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "lookup"}}, Tools: []tools.Definition{definition}}
			response, err := client.Generate(t.Context(), request)
			if !encrypted {
				if !errors.Is(err, openai.ErrReplayUnavailable) {
					t.Fatalf("unsigned reasoning silently replayed: %v", err)
				}
				return
			}
			if err != nil || response.ProviderReplay == nil || !strings.Contains(string(response.ProviderReplay.Data), "opaque-fixture") {
				t.Fatalf("encrypted phase/tool evidence lost: %+v error=%v", response, err)
			}
			response.ProviderReplay.Data = json.RawMessage("[" + strings.Join([]string{phaseBareReasoning, phaseCommentaryItem, phaseCallOne}, ",") + "]")
			request.Messages = append(request.Messages,
				model.Message{Role: model.RoleAssistant, Content: response.Content, ToolCalls: response.ToolCalls, ProviderReplay: response.ProviderReplay})
			if _, err = client.Generate(t.Context(), request); !errors.Is(err, openai.ErrReplayUnavailable) || calls != 1 {
				t.Fatalf("phase bypassed reasoning evidence fence: error=%v calls=%d", err, calls)
			}
		})
	}
}

func TestPhaseRejectsAmbiguousOrIntermediateOnlyOutput(t *testing.T) {
	legacy := `{"type":"message","content":[{"type":"output_text","text":"legacy"}]}`
	refusal := `{"type":"message","phase":"commentary","content":[{"type":"refusal","refusal":"Cannot comply."}]}`
	for name, output := range map[string][]string{
		"commentary-only":     {phaseCommentaryItem},
		"commentary-json":     {strings.Replace(phaseFinalItem, "final_answer", "commentary", 1)},
		"unknown":             {strings.Replace(phaseFinalItem, "final_answer", "unknown_phase", 1)},
		"empty-phase":         {strings.Replace(phaseFinalItem, "final_answer", "", 1)},
		"invalid-phase-type":  {strings.Replace(phaseFinalItem, `"phase":"final_answer"`, `"phase":17`, 1)},
		"multiple-final":      {phaseFinalItem, phaseFinalItem},
		"mixed-legacy":        {legacy, phaseFinalItem},
		"mixed-null":          {strings.Replace(phaseCommentaryItem, `"phase":"commentary"`, `"phase":null`, 1), phaseFinalItem},
		"late-commentary":     {phaseFinalItem, phaseCommentaryItem},
		"final-plus-tool":     {phaseFinalItem, phaseCallOne},
		"refusal-plus-tool":   {refusal, phaseCallOne},
		"non-assistant-phase": {strings.Replace(phaseFinalItem, `"role":"assistant"`, `"role":"user"`, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writePhaseResponse(w, phaseResponse(output...), false)
			})
			_, err := client.Generate(context.Background(), model.Request{
				Messages: []model.Message{{Role: model.RoleUser, Content: "test"}}, Tools: []tools.Definition{definition},
			})
			if !errors.Is(err, openai.ErrUnsupported) {
				t.Fatalf("ambiguous phase was silently repaired: error=%v", err)
			}
		})
	}
}

func TestPhaseRefusalCannotHideBehindValidFinalJSON(t *testing.T) {
	refusal := `{"type":"message","phase":"commentary","content":[{"type":"refusal","refusal":"Cannot comply."}]}`
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writePhaseResponse(w, phaseResponse(refusal, phaseFinalItem), false)
	})
	response, err := client.Generate(t.Context(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "test"}}})
	if err != nil || !response.Refused || response.Content != `{"ok":true}` {
		t.Fatalf("phase selection concealed a refusal: response=%+v err=%v", response, err)
	}
}

func TestPhaseAbsenceAndNullPreserveLegacyProjection(t *testing.T) {
	for _, phase := range []string{"", `"phase":null,`} {
		client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writePhaseResponse(w, phaseResponse(
				`{"type":"message",`+phase+`"content":[{"type":"output_text","text":"old "}]}`,
				`{"type":"message",`+phase+`"content":[{"type":"output_text","text":"answer"}]}`), false)
		})
		response, err := client.Generate(t.Context(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "test"}}})
		if err != nil || response.Content != "old answer" || response.ProviderReplay != nil {
			t.Fatalf("legacy projection changed: response=%+v error=%v", response, err)
		}
	}
}
