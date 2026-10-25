package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestOpenAIToolReplayAdmissionFailsBeforeAnyTaskWhenEncryptionAbsent(t *testing.T) {
	var total atomic.Int32
	var forcedProbe atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		total.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+baselineHTTPFakeKey {
			t.Fatal("probe omitted fake credentials")
		}
		var req struct {
			Stream  bool     `json:"stream"`
			Store   bool     `json:"store"`
			Include []string `json:"include"`
			Tools   []struct {
				Name string `json:"name"`
			} `json:"tools"`
			ToolChoice string `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Store {
			t.Error("probe unexpectedly stores remote responses")
		}
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "plain-schema", "status": "completed", "output": []any{
					map[string]any{"type": "message", "role": "assistant", "content": []any{
						map[string]string{"type": "output_text", "text": `{"ok":true}`},
					}},
				},
				"usage": map[string]int{"input_tokens": 10, "output_tokens": 2},
			})
			return
		}
		forcedProbe.Add(1)
		if req.ToolChoice != "required" || len(req.Tools) != 1 || req.Tools[0].Name != replayProbeToolName ||
			len(req.Include) != 1 || req.Include[0] != "reasoning.encrypted_content" {
			t.Errorf("tool replay request not enforcing its stated contract: %+v", req)
		}
		reply := map[string]any{
			"id": "unsupported-replay", "status": "completed", "output": []any{
				map[string]any{"type": "reasoning", "id": "reason-1", "summary": []any{}},
				map[string]any{"type": "function_call", "call_id": "call1", "name": replayProbeToolName,
					"arguments": `{"value":"probe"}`},
			}, "usage": map[string]int{"input_tokens": 12, "output_tokens": 5},
		}
		event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": reply})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", event)
	}))
	defer server.Close()

	code, events, _ := executeHTTPBaseline(t, baselineHTTPInput("openai", server.URL+"/tool-admission", true))
	preflights := baselineEventsOfType(events, "preflight")
	samples := baselineEventsOfType(events, "sample")
	if code == 0 || total.Load() != 2 || forcedProbe.Load() != 1 || len(samples) != 0 || len(preflights) != 1 {
		t.Fatalf("tool admission did not stop the tasks: calls=%d forced=%d events=%+v", total.Load(), forcedProbe.Load(), events)
	}
	preflight := preflights[0]
	tool, ok := preflight["toolProbe"].(map[string]any)
	if preflight["status"] != "failed" || preflight["failureCode"] != "provider_replay_unavailable" ||
		preflight["httpStatus"] != float64(200) || preflight["responseKind"] != "json" ||
		!ok || tool["status"] != "failed" || tool["stage"] != "tool_call" ||
		tool["httpStatus"] != float64(200) || tool["responseKind"] != "event_stream" ||
		tool["modelCalls"] != float64(1) || tool["httpRequests"] != float64(1) ||
		tool["reasoningSeen"] != true || tool["encryptedReplaySeen"] != false {
		t.Fatalf("tool replay admission result lost its evidence: %+v", preflight)
	}
	debug, ok := tool["responseDebug"].(map[string]any)
	if !ok || debug["capture"] != "complete" ||
		debug["bodyTruncated"] != false || debug["normalizedAnswer"] != "" {
		t.Fatalf("failed tool probe did not preserve its bounded original wire body: %+v", debug)
	}
	summary := baselineEventsOfType(events, "summary")
	if len(summary) != 1 || summary[0]["attempted"] != float64(0) ||
		summary[0]["skipped"] != float64(2) {
		t.Fatalf("skipped task samples falsely counted as task attempts: %+v", summary)
	}
}
