package openai_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// The public Responses API documents response.output_item.done as the
// authoritative encrypted reasoning item for stateless continuation.
// AEAD ciphertext may differ in a later response.completed projection;
// only item ID, non-encrypted payload, Tool identity and finalized arguments
// must still reconcile. The done ciphertext, never added/final ciphertext,
// must be the one sent to the next physical model request.
func TestStreamingReasoningDoneCiphertextOverridesFinalReencryptionForStatelessReplay(t *testing.T) {
	const done = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"authoritative-done-cipher"}`
	const final = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"different-terminal-cipher"}`
	const added = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"incomplete-added-cipher"}`
	actualRequests := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		actualRequests++
		if actualRequests == 1 {
			var wire struct {
				Store   bool     `json:"store"`
				Include []string `json:"include"`
			}
			if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
				t.Error(err)
			}
			if wire.Store || len(wire.Include) != 1 || wire.Include[0] != "reasoning.encrypted_content" {
				t.Error("stateless encryption was not requested")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			writeReasoningEvent(t, w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": json.RawMessage(added)})
			writeReasoningEvent(t, w, "response.output_item.done", doneReasoningEvent(done, 0))
			complete := `{"object":"response","id":"resp-reencryption","status":"completed","output":[` + final + `,` + streamToolCall + `],"usage":{"input_tokens":12,"output_tokens":6}}`
			writeReasoningEvent(t, w, "response.completed", map[string]any{"type": "response.completed", "response": json.RawMessage(complete)})
			return
		}
		var wire struct {
			Store   bool                         `json:"store"`
			Include []string                     `json:"include"`
			Input   []map[string]json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		if wire.Store || len(wire.Include) != 1 || wire.Include[0] != "reasoning.encrypted_content" ||
			len(wire.Input) != 4 || string(wire.Input[1]["encrypted_content"]) != `"authoritative-done-cipher"` {
			t.Error("continuation did not replay the exact authoritative done ciphertext")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeReasoningEvent(t, w, "response.completed", map[string]any{"type": "response.completed", "response": json.RawMessage(completedAnswer)})
	})
	req := reasoningRequest()
	first, err := client.GenerateStream(t.Context(), req, nil)
	if err != nil || first.ResponseID != "resp-reencryption" || first.Usage == nil ||
		len(first.ToolCalls) != 1 || first.ProviderReplay == nil {
		t.Fatalf("valid completed reasoning/tool pair rejected: response=%+v err=%v", first, err)
	}
	var replay []struct {
		Type             string `json:"type"`
		EncryptedContent string `json:"encrypted_content"`
	}
	if err := json.Unmarshal(first.ProviderReplay.Data, &replay); err != nil || len(replay) != 2 ||
		replay[0].EncryptedContent != "authoritative-done-cipher" {
		t.Fatal("opaque replay was not built from the exact completed reasoning item")
	}
	req.Messages = append(req.Messages, model.Message{Role: model.RoleAssistant,
		ToolCalls: first.ToolCalls, ProviderReplay: first.ProviderReplay},
		model.Message{Role: model.RoleTool, ToolCallID: first.ToolCalls[0].ID,
			Content: model.TextParts(`{"ok":true}`)})
	result, err := client.GenerateStream(t.Context(), req, nil)
	if err != nil || result.Content != "GAOGE_TOOL_OK" || actualRequests != 2 {
		t.Fatalf("stateless tool continuation failed: response=%+v err=%v requests=%d", result, err, actualRequests)
	}
}

func TestStreamingReasoningCiphertextDifferenceDoesNotPermitAnyOtherCompletedEvidenceConflict(t *testing.T) {
	const done = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"authoritative-done-cipher"}`
	const final = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"different"}],"encrypted_content":"different-terminal-cipher"}`
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeReasoningEvent(t, w, "response.output_item.done", doneReasoningEvent(done, 0))
		complete := `{"object":"response","id":"resp-bad-reasoning","status":"completed","output":[` + final + `,` + streamToolCall + `],"usage":{"input_tokens":12,"output_tokens":6}}`
		writeReasoningEvent(t, w, "response.completed", map[string]any{"type": "response.completed", "response": json.RawMessage(complete)})
	})
	result, err := client.GenerateStream(t.Context(), reasoningRequest(), nil)
	if !errors.Is(err, providerhttp.ErrProtocol) {
		t.Fatalf("reasoning metadata mutation was not blocked at protocol boundary: %v", err)
	}
	if len(result.ToolCalls) > 0 || result.ProviderReplay != nil || result.ResponseID != "" || result.Usage != nil {
		t.Fatal("reasoning metadata mutation granted physical Tool/replay authority")
	}
}

func TestStreamingReasoningDoneCiphertextAcrossFullThirteenEventToolTrace(t *testing.T) {
	const doneCipher = "stream-done-authority"
	const finalCipher = "independently-encrypted-final"
	calls := 0
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		identity := map[string]any{"id": "resp-13-events", "object": "response", "status": "in_progress"}
		writeReasoningEvent(t, w, "response.created", map[string]any{
			"type": "response.created", "response": identity, "sequence_number": 0,
		})
		writeReasoningEvent(t, w, "response.in_progress", map[string]any{
			"type": "response.in_progress", "response": identity, "sequence_number": 1,
		})
		reasoningAdded := map[string]any{
			"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": "partial-added",
		}
		reasoningDone := map[string]any{
			"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": doneCipher,
		}
		writeReasoningEvent(t, w, "response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": 0, "item": reasoningAdded, "sequence_number": 2,
		})
		writeReasoningEvent(t, w, "response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": 0, "item": reasoningDone, "sequence_number": 3,
		})
		writeReasoningEvent(t, w, "response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": 1, "sequence_number": 4,
			"item": map[string]any{"type": "function_call", "id": "fc_1", "status": "in_progress", "name": "lookup", "call_id": "call_1", "arguments": ""},
		})
		for index, delta := range []string{`{"`, "x", `":`, "1", "}"} {
			writeReasoningEvent(t, w, "response.function_call_arguments.delta", map[string]any{
				"type": "response.function_call_arguments.delta", "output_index": 1, "sequence_number": index + 5,
				"item_id": "fc_1", "delta": delta,
			})
		}
		writeReasoningEvent(t, w, "response.function_call_arguments.done", map[string]any{
			"type": "response.function_call_arguments.done", "item_id": "fc_1", "output_index": 1,
			"arguments": `{"x":1}`, "sequence_number": 10,
		})
		toolDone := map[string]any{
			"type": "function_call", "id": "fc_1", "status": "completed",
			"call_id": "call_1", "name": "lookup", "arguments": `{"x":1}`,
		}
		writeReasoningEvent(t, w, "response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": 1, "item": toolDone, "sequence_number": 11,
		})
		response := map[string]any{
			"id": "resp-13-events", "object": "response", "status": "completed",
			"output": []any{
				map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": finalCipher},
				toolDone,
			},
			"usage": map[string]any{"input_tokens": 113, "output_tokens": 33},
		}
		writeReasoningEvent(t, w, "response.completed", map[string]any{
			"type": "response.completed", "sequence_number": 12, "response": response,
		})
	})
	result, err := client.GenerateStream(t.Context(), reasoningRequest(), nil)
	if err != nil || calls != 1 || result.ResponseID != "resp-13-events" ||
		result.Usage == nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call_1" ||
		result.ProviderReplay == nil {
		t.Fatalf("13-event physical trace rejected: calls=%d result=%+v err=%v", calls, result, err)
	}
	var replay []struct {
		EncryptedContent string `json:"encrypted_content"`
	}
	if err := json.Unmarshal(result.ProviderReplay.Data, &replay); err != nil ||
		len(replay) != 2 || replay[0].EncryptedContent != doneCipher {
		t.Fatal("trace did not retain the exact authoritative done encryption")
	}
}
