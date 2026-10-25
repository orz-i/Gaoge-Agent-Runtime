package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBaselineReportsActualTaskResponseWhenEncryptedReplayMissing(t *testing.T) {
	var physical atomic.Int32
	var probes atomic.Int32
	var tasks atomic.Int32
	toolProbeFixture := &baselineHTTPFixture{t: t, protocol: "openai"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+baselineHTTPFakeKey {
			t.Error("fake bearer credential missing")
		}
		var request struct {
			Stream  bool     `json:"stream"`
			Store   bool     `json:"store"`
			Include []string `json:"include"`
			Tools   []any    `json:"tools"`
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if isReplayProbeRequest(payload) {
			toolProbeFixture.respondToolReplayProbe(w, payload)
			return
		}
		if err := decodeTaskRequestPayload(payload, &request); err != nil {
			t.Error(err)
			return
		}
		if request.Store {
			t.Error("baseline lost stateless provider contract")
		}
		if !request.Stream {
			probes.Add(1)
			if len(request.Include) != 0 || len(request.Tools) != 0 {
				t.Error("preflight unexpectedly included tool/replay input")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"probe1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"ok\":true}"}]}],"usage":{"input_tokens":10,"output_tokens":3}}`)
			return
		}
		tasks.Add(1)
		if len(request.Include) != 1 || request.Include[0] != "reasoning.encrypted_content" || len(request.Tools) != 3 {
			t.Error("task request omitted requested encrypted replay or tools")
		}
		response := `{"id":"task1","status":"completed","output":[{"type":"reasoning","id":"reason1","summary":[{"type":"summary_text","text":"TOOL_REPLAY_MISSING"}]},{"type":"function_call","call_id":"call1","name":"lookup_release","arguments":"{}"}],"usage":{"input_tokens":14,"output_tokens":5}}`
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
	}))
	defer server.Close()
	config := baselineHTTPInput("openai", server.URL+"/task", true)
	code, events, output := executeHTTPBaseline(t, config)
	samples := baselineEventsOfType(events, "sample")
	if code == 0 || probes.Load() != 1 || tasks.Load() != 2 || physical.Load() != 5 || len(samples) != 2 {
		t.Fatalf("baseline made unexpected requests or discarded samples: probes=%d tasks=%d samples=%d code=%d", probes.Load(), tasks.Load(), len(samples), code)
	}
	for _, item := range samples {
		if item["status"] != "failed" || item["failureCode"] != "provider_replay_unavailable" ||
			item["providerHTTPRequests"] != float64(1) || item["lastHTTPStatus"] != float64(200) ||
			item["lastResponseKind"] != "event_stream" || item["toolCalls"] != float64(0) {
			t.Fatalf("task failure was not classified at the real provider boundary: %+v", item)
		}
		debug, ok := item["lastResponseDebug"].(map[string]any)
		if !ok || debug["capture"] != "complete" || !strings.Contains(debug["body"].(string), "TOOL_REPLAY_MISSING") {
			t.Fatalf("failing HTTP response was not preserved for inspection: %+v", debug)
		}
	}
	if !strings.Contains(output, "TOOL_REPLAY_MISSING") || strings.Contains(output, baselineHTTPFakeKey) {
		t.Fatal("output hid task response or disclosed the fake API key")
	}
}

func TestBaselineReportsSecondTaskHTTPResponseAfterSuccessfulLookup(t *testing.T) {
	var physical atomic.Int32
	var tasks atomic.Int32
	toolProbeFixture := &baselineHTTPFixture{t: t, protocol: "openai"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		var req struct {
			Stream  bool                         `json:"stream"`
			Input   []map[string]json.RawMessage `json:"input"`
			Include []string                     `json:"include"`
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if isReplayProbeRequest(payload) {
			toolProbeFixture.respondToolReplayProbe(w, payload)
			return
		}
		if err := decodeTaskRequestPayload(payload, &req); err != nil {
			t.Error(err)
			return
		}
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"probe","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"ok\":true}"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`)
			return
		}
		tasks.Add(1)
		seenToolResult, seenEncryptedReasoning := false, false
		for _, item := range req.Input {
			var kind string
			_ = json.Unmarshal(item["type"], &kind)
			if kind == "function_call_output" {
				seenToolResult = true
			}
			if kind == "reasoning" && string(item["encrypted_content"]) == `"encrypted-fixture"` {
				seenEncryptedReasoning = true
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if !seenToolResult {
			if len(req.Include) != 1 || req.Include[0] != "reasoning.encrypted_content" {
				t.Error("first tool call omitted replay inclusion")
			}
			response := `{"id":"lookup","status":"completed","output":[{"type":"reasoning","id":"reason1","encrypted_content":"encrypted-fixture","summary":[]},{"type":"function_call","name":"lookup_release","call_id":"lookup1","arguments":"{\"release\":\"R-17\"}"}],"usage":{"input_tokens":12,"output_tokens":8}}`
			_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
			return
		}
		if !seenEncryptedReasoning {
			t.Error("stateless continuation dropped encrypted reasoning evidence")
		}
		response := `{"id":"unsupported-final","status":"completed","output":[{"type":"UNSUPPORTED_SECOND_RESPONSE_MARKER"}],"usage":{"input_tokens":15,"output_tokens":4}}`
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
	}))
	defer server.Close()
	code, events, _ := executeHTTPBaseline(t, baselineHTTPInput("openai", server.URL+"/task", true))
	samples := baselineEventsOfType(events, "sample")
	if code == 0 || physical.Load() != 7 || tasks.Load() != 4 || len(samples) != 2 {
		t.Fatalf("unexpected task request/response sequence: physical=%d tasks=%d samples=%d", physical.Load(), tasks.Load(), len(samples))
	}
	for _, sample := range samples {
		if sample["status"] != "failed" || sample["failureCode"] != "provider_unsupported" ||
			sample["stage"] != "evidence" || sample["modelCalls"] != float64(2) ||
			sample["lookupCalls"] != float64(1) || sample["providerHTTPRequests"] != float64(2) ||
			sample["lastHTTPStatus"] != float64(200) || sample["lastResponseKind"] != "event_stream" {
			t.Fatalf("second model failure was misattributed: %+v", sample)
		}
		debug, ok := sample["lastResponseDebug"].(map[string]any)
		if !ok || !strings.Contains(debug["body"].(string), "UNSUPPORTED_SECOND_RESPONSE_MARKER") ||
			strings.Contains(debug["body"].(string), "encrypted-fixture") {
			t.Fatal("last response evidence belongs to prior successful request or leaked private replay")
		}
	}
}

func decodeTaskRequestPayload(source map[string]json.RawMessage, dest any) error {
	encoded, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, dest)
}
