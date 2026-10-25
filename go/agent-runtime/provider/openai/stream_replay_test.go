package openai_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/openai"
)

const (
	doneReasoning  = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"sealed-from-done"}`
	finalReasoning = `{"type":"reasoning","id":"rs_1","summary":[]}`
	streamToolCall = `{"type":"function_call","id":"fc_1","status":"completed","name":"lookup","call_id":"call_1","arguments":"{\"x\":1}"}`
)

func writeReasoningEvent(t *testing.T, w http.ResponseWriter, kind string, contents any) {
	t.Helper()
	frame, err := json.Marshal(contents)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, frame)
}

func streamReasoningResponse(t *testing.T, w http.ResponseWriter, finalReason string, doneEvents []map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	// Added items can contain incomplete encrypted payloads. They must
	// never be used as authoritative replay.
	writeReasoningEvent(t, w, "response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"partial-not-safe"}`),
	})
	for _, event := range doneEvents {
		writeReasoningEvent(t, w, "response.output_item.done", event)
	}
	complete := `{"id":"resp-stream-replay","status":"completed","output":[` + finalReason + "," + streamToolCall + `],"usage":{"input_tokens":12,"output_tokens":6}}`
	writeReasoningEvent(t, w, "response.completed", map[string]any{
		"type": "response.completed", "response": json.RawMessage(complete),
	})
}

func doneReasoningEvent(item string, index int) map[string]any {
	return map[string]any{
		"type": "response.output_item.done", "output_index": index,
		"item": json.RawMessage(item),
	}
}

func TestStreamingReplayUsesCompletedReasoningItemWhenFinalOmitsEncryption(t *testing.T) {
	physicalRequests := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		physicalRequests++
		if physicalRequests == 1 {
			streamReasoningResponse(t, w, finalReasoning, []map[string]any{doneReasoningEvent(doneReasoning, 0)})
			return
		}
		var payload struct {
			Store   bool                         `json:"store"`
			Include []string                     `json:"include"`
			Input   []map[string]json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Store || len(payload.Include) != 1 ||
			payload.Include[0] != "reasoning.encrypted_content" ||
			len(payload.Input) != 4 || string(payload.Input[1]["encrypted_content"]) != `"sealed-from-done"` {
			t.Errorf("completed encrypted reasoning not replayed faithfully: %+v", payload.Input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeReasoningEvent(t, w, "response.completed", map[string]any{
			"type": "response.completed", "response": json.RawMessage(completedAnswer),
		})
	})
	req := reasoningRequest()
	first, err := client.GenerateStream(t.Context(), req, nil)
	if err != nil || len(first.ToolCalls) != 1 || first.ProviderReplay == nil {
		t.Fatalf("completed SSE reasoning was not accepted: %+v, %v", first, err)
	}
	if !strings.Contains(string(first.ProviderReplay.Data), "sealed-from-done") ||
		strings.Contains(string(first.ProviderReplay.Data), "partial-not-safe") {
		t.Fatal("provider replay did not use the authoritative completed reasoning")
	}
	req.Messages = append(req.Messages, model.Message{
		Role: model.RoleAssistant, Content: first.Content,
		ToolCalls: first.ToolCalls, ProviderReplay: first.ProviderReplay,
	}, model.Message{Role: model.RoleTool, ToolCallID: first.ToolCalls[0].ID, Content: `{"ok":true}`})
	result, err := client.GenerateStream(t.Context(), req, nil)
	if err != nil || result.Content != "GAOGE_TOOL_OK" || physicalRequests != 2 {
		t.Fatalf("encrypted Tool context could not resume: response=%+v err=%v calls=%d", result, err, physicalRequests)
	}
}

func TestStreamingReplayRejectsAbsentOrContradictoryCompletedItems(t *testing.T) {
	invalidTests := []struct {
		name          string
		finalItem     string
		done          []map[string]any
		missingReplay bool
	}{
		{name: "missing-both", finalItem: finalReasoning, missingReplay: true},
		{name: "wrong-index", finalItem: finalReasoning, done: []map[string]any{doneReasoningEvent(doneReasoning, 1)}},
		{name: "too-large-index", finalItem: finalReasoning, done: []map[string]any{doneReasoningEvent(doneReasoning, 1024)}},
		{name: "different-ID", finalItem: finalReasoning, done: []map[string]any{doneReasoningEvent(strings.Replace(doneReasoning, "rs_1", "other-reasoning", 1), 0)}},
		{name: "different-summary", finalItem: finalReasoning, done: []map[string]any{doneReasoningEvent(strings.Replace(doneReasoning, "[]", `[{"type":"summary_text","text":"different"}]`, 1), 0)}},
		{name: "duplicate-index", finalItem: finalReasoning, done: []map[string]any{doneReasoningEvent(doneReasoning, 0), doneReasoningEvent(doneReasoning, 0)}},
		{name: "different-encryption", finalItem: strings.Replace(finalReasoning, `"summary":[]`, `"summary":[],"encrypted_content":"other"`, 1), done: []map[string]any{doneReasoningEvent(doneReasoning, 0)}},
		{name: "added-is-not-completed", finalItem: finalReasoning, missingReplay: true},
		{name: "done-missing-index", finalItem: finalReasoning, done: []map[string]any{{"type": "response.output_item.done", "item": json.RawMessage(doneReasoning)}}},
	}
	for _, tc := range invalidTests {
		t.Run(tc.name, func(t *testing.T) {
			physicalRequests := 0
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				physicalRequests++
				streamReasoningResponse(t, w, tc.finalItem, tc.done)
			})
			reply, err := client.GenerateStream(t.Context(), reasoningRequest(), nil)
			if err == nil || len(reply.ToolCalls) != 0 || reply.ProviderReplay != nil || physicalRequests != 1 {
				t.Fatalf("inconsistent encrypted reasoning passed: response=%+v err=%v calls=%d", reply, err, physicalRequests)
			}
			if tc.missingReplay && !errors.Is(err, openai.ErrReplayUnavailable) {
				t.Errorf("missing completed encrypted reasoning should fail explicitly, got %v", err)
			}
		})
	}
}
