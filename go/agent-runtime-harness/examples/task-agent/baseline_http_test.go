package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	baselineHTTPFakeKey    = "fake_baseline_key_123"
	baselineHTTPCommentary = " \nWorking.\n "
)

var baselineToolNames = map[string]string{
	"lookup_release":        lookupKey,
	"read_context_artifact": harness.ContextArtifactToolKey,
	"save_draft":            draftKey,
}

// The server decodes the real provider request and derives every task action
// from those messages. No source fixture or scripted cursor bypasses HTTP.
type baselineHTTPFixture struct {
	t                   *testing.T
	protocol            string
	path                string
	model               string
	wrongFinal          bool
	mu                  sync.Mutex
	probes              int
	streams             int
	answers             []taskAnswer
	phaseReplayRequests int
}

func (fixture *baselineHTTPFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != fixture.path || r.URL.RawQuery != "" {
		fixture.t.Errorf("unexpected endpoint: method=%s path=%s", r.Method, r.URL.Path)
	}
	if fixture.protocol == "openai" {
		if r.Header.Get("Authorization") != "Bearer "+baselineHTTPFakeKey || r.Header.Get("X-Api-Key") != "" {
			fixture.t.Error("OpenAI request used the wrong authentication contract")
		}
	} else if r.Header.Get("X-Api-Key") != baselineHTTPFakeKey || r.Header.Get("Anthropic-Version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
		fixture.t.Error("Anthropic request used the wrong authentication contract")
	}
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		fixture.t.Error(err)
		http.Error(w, "invalid fixture request", http.StatusBadRequest)
		return
	}
	var modelID string
	var stream bool
	if err := json.Unmarshal(payload["model"], &modelID); err != nil || modelID != fixture.model {
		fixture.t.Errorf("model not preserved: model=%q error=%v", modelID, err)
	}
	if err := json.Unmarshal(payload["stream"], &stream); err != nil {
		fixture.t.Errorf("missing explicit stream setting: %v", err)
	}
	fixture.assertContract(payload, stream)
	fixture.mu.Lock()
	if stream {
		fixture.streams++
	} else {
		fixture.probes++
	}
	fixture.mu.Unlock()

	response := model.Response{Content: `{"ok":true}`}
	if stream {
		if r.Header.Get("Accept") != "text/event-stream" {
			fixture.t.Error("streaming request omitted SSE Accept")
		}
		request, err := fixture.decodeRequest(payload)
		if err != nil {
			fixture.t.Error(err)
			http.Error(w, "invalid fixture transcript", http.StatusBadRequest)
			return
		}
		response, err = (scriptedModel{}).Generate(r.Context(), request)
		if err != nil {
			fixture.t.Error(err)
			http.Error(w, "invalid task evidence", http.StatusBadRequest)
			return
		}
		if len(response.ToolCalls) == 0 {
			var answer taskAnswer
			if err = json.Unmarshal([]byte(response.Content), &answer); err != nil {
				fixture.t.Error(err)
			}
			fixture.mu.Lock()
			fixture.answers = append(fixture.answers, answer)
			fixture.mu.Unlock()
			if fixture.wrongFinal {
				answer.DeploymentCode = "INVENTED"
				content, marshalErr := json.Marshal(answer)
				if marshalErr != nil {
					fixture.t.Error(marshalErr)
				}
				response.Content = string(content)
			}
		}
	}
	fixture.writeResponse(w, response, stream)
}

func (fixture *baselineHTTPFixture) assertContract(payload map[string]json.RawMessage, stream bool) {
	fixture.t.Helper()
	ceilingKey := "max_output_tokens"
	if fixture.protocol == "anthropic" {
		ceilingKey = "max_tokens"
	}
	var ceiling int64
	if err := json.Unmarshal(payload[ceilingKey], &ceiling); err != nil || ceiling <= 0 || ceiling > 512 || stream && ceiling != 512 {
		fixture.t.Errorf("provider output ceiling not enforced: got=%d error=%v", ceiling, err)
	}
	if fixture.protocol == "openai" {
		var text struct {
			Format struct {
				Type   string          `json:"type"`
				Strict bool            `json:"strict"`
				Schema json.RawMessage `json:"schema"`
			} `json:"format"`
		}
		if err := json.Unmarshal(payload["text"], &text); err != nil || text.Format.Type != "json_schema" || !text.Format.Strict || !json.Valid(text.Format.Schema) {
			fixture.t.Errorf("native strict output missing: %v", err)
		}
		fixture.assertSchema(text.Format.Schema, stream)
		var store bool
		if err := json.Unmarshal(payload["store"], &store); err != nil || store {
			fixture.t.Errorf("stateless Responses request missing: %v", err)
		}
		return
	}
	var output struct {
		Format struct {
			Type   string          `json:"type"`
			Schema json.RawMessage `json:"schema"`
		} `json:"format"`
	}
	if err := json.Unmarshal(payload["output_config"], &output); err != nil || output.Format.Type != "json_schema" || !json.Valid(output.Format.Schema) {
		fixture.t.Errorf("native Anthropic JSON Schema output missing: %v", err)
	}
	fixture.assertSchema(output.Format.Schema, stream)
}

func (fixture *baselineHTTPFixture) assertSchema(schema json.RawMessage, stream bool) {
	fixture.t.Helper()
	expected := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean","enum":[true]}},"required":["ok"],"additionalProperties":false}`)
	if stream {
		expected = answerSchema
	}
	var actual, want any
	if err := json.Unmarshal(schema, &actual); err != nil {
		fixture.t.Error(err)
		return
	}
	if err := json.Unmarshal(expected, &want); err != nil {
		fixture.t.Error(err)
		return
	}
	if !reflect.DeepEqual(actual, want) {
		fixture.t.Error("provider request changed the explicit strict output contract")
	}
}

type baselineOpenAIItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Role      model.Role      `json:"role"`
	Phase     string          `json:"phase"`
	Status    string          `json:"status"`
	Opaque    string          `json:"fixture_opaque"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Output    string          `json:"output"`
}

type baselineAnthropicBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Text      string          `json:"text"`
	ToolUseID string          `json:"tool_use_id"`
	Content   string          `json:"content"`
}

func (fixture *baselineHTTPFixture) decodeRequest(payload map[string]json.RawMessage) (model.Request, error) {
	var request model.Request
	if fixture.protocol == "openai" {
		var items []baselineOpenAIItem
		if err := json.Unmarshal(payload["input"], &items); err != nil {
			return request, err
		}
		var replayedPhase bool
		for _, item := range items {
			switch item.Type {
			case "":
				var content string
				if err := json.Unmarshal(item.Content, &content); err != nil {
					return request, err
				}
				request.Messages = append(request.Messages, model.Message{Role: item.Role, Content: content})
			case "message":
				if item.Phase != "commentary" || item.Role != model.RoleAssistant || item.Status != "completed" ||
					item.ID != baselineHTTPFakeKey+"_commentary" || item.Opaque != baselineHTTPFakeKey+"_opaque" {
					return request, errors.New("native phase replay lost its original metadata")
				}
				var content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(item.Content, &content); err != nil || len(content) != 1 ||
					content[0].Type != "output_text" || content[0].Text != baselineHTTPCommentary {
					return request, errors.New("native phase replay changed its message content")
				}
				request.Messages = append(request.Messages, model.Message{Role: item.Role, Content: content[0].Text})
				replayedPhase = true
			case "function_call":
				key, ok := baselineToolNames[item.Name]
				if !ok {
					return request, fmt.Errorf("unexpected tool name %q", item.Name)
				}
				request.Messages = append(request.Messages, model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{
					ID: item.CallID, Name: item.Name, ToolKey: key, Arguments: json.RawMessage(item.Arguments),
				}}})
			case "function_call_output":
				request.Messages = append(request.Messages, model.Message{Role: model.RoleTool, ToolCallID: item.CallID, Content: item.Output})
			default:
				return request, fmt.Errorf("unexpected Responses input type %q", item.Type)
			}
		}
		if replayedPhase {
			fixture.mu.Lock()
			fixture.phaseReplayRequests++
			fixture.mu.Unlock()
		}
		return request, nil
	}
	var system string
	if len(payload["system"]) != 0 {
		if err := json.Unmarshal(payload["system"], &system); err != nil {
			return request, err
		}
		request.Messages = append(request.Messages, model.Message{Role: model.RoleSystem, Content: system})
	}
	var messages []struct {
		Role    model.Role      `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(payload["messages"], &messages); err != nil {
		return request, err
	}
	for _, message := range messages {
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			request.Messages = append(request.Messages, model.Message{Role: message.Role, Content: text})
			continue
		}
		var blocks []baselineAnthropicBlock
		if err := json.Unmarshal(message.Content, &blocks); err != nil {
			return request, err
		}
		for _, block := range blocks {
			switch block.Type {
			case "text":
				request.Messages = append(request.Messages, model.Message{Role: message.Role, Content: block.Text})
			case "tool_use":
				key, ok := baselineToolNames[block.Name]
				if !ok {
					return request, fmt.Errorf("unexpected tool name %q", block.Name)
				}
				request.Messages = append(request.Messages, model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{
					ID: block.ID, Name: block.Name, ToolKey: key, Arguments: block.Input,
				}}})
			case "tool_result":
				request.Messages = append(request.Messages, model.Message{Role: model.RoleTool, ToolCallID: block.ToolUseID, Content: block.Content})
			default:
				return request, fmt.Errorf("unexpected Messages content type %q", block.Type)
			}
		}
	}
	return request, nil
}

func baselineProviderToolName(call tools.Call) string {
	for name, key := range baselineToolNames {
		if key == call.ToolKey {
			return name
		}
	}
	return ""
}

func baselineOpenAIPhaseMessage(phase, content string) map[string]any {
	return map[string]any{
		"type": "message", "id": baselineHTTPFakeKey + "_" + phase, "role": "assistant", "phase": phase,
		"status": "completed", "fixture_opaque": baselineHTTPFakeKey + "_opaque",
		"content": []any{map[string]string{"type": "output_text", "text": content}},
	}
}

func (fixture *baselineHTTPFixture) writeResponse(w http.ResponseWriter, response model.Response, stream bool) {
	usage := map[string]int{"input_tokens": 31, "output_tokens": 13}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
	}
	if fixture.protocol == "openai" {
		output := make([]any, 0, len(response.ToolCalls)+2)
		output = append(output, baselineOpenAIPhaseMessage("commentary", baselineHTTPCommentary))
		for _, call := range response.ToolCalls {
			output = append(output, map[string]any{
				"type": "function_call", "call_id": baselineHTTPFakeKey + "_" + call.ID, "name": baselineProviderToolName(call), "arguments": string(call.Arguments),
			})
		}
		if response.Content != "" {
			output = append(output, baselineOpenAIPhaseMessage("final_answer", response.Content))
		}
		body := map[string]any{"id": baselineHTTPFakeKey + "_response", "status": "completed", "output": output, "usage": usage}
		if !stream {
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		baselineSSE(fixture.t, w, "response.completed", map[string]any{"type": "response.completed", "response": body})
		return
	}
	content := make([]any, 0, len(response.ToolCalls)+1)
	for _, call := range response.ToolCalls {
		content = append(content, map[string]any{
			"type": "tool_use", "id": baselineHTTPFakeKey + "_" + call.ID, "name": baselineProviderToolName(call), "input": call.Arguments,
		})
	}
	if response.Content != "" {
		content = append(content, map[string]string{"type": "text", "text": response.Content})
	}
	stop := "end_turn"
	if len(response.ToolCalls) > 0 {
		stop = "tool_use"
	}
	if !stream {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": baselineHTTPFakeKey + "_message", "stop_reason": stop, "content": content, "usage": usage})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	baselineSSE(fixture.t, w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": baselineHTTPFakeKey + "_message", "usage": usage}})
	for index, block := range content {
		baselineSSE(fixture.t, w, "content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": block})
		baselineSSE(fixture.t, w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
	}
	baselineSSE(fixture.t, w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 13}})
	baselineSSE(fixture.t, w, "message_stop", map[string]string{"type": "message_stop"})
}

func baselineSSE(t *testing.T, w io.Writer, event string, payload any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Error(err)
		return
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded); err != nil {
		t.Error(err)
	}
}

func baselineHTTPInput(protocol, endpoint string, allowUnknown bool) map[string]any {
	return map[string]any{
		"schemaVersion": 1, "provider": protocol, "endpoint": endpoint, "apiKey": baselineHTTPFakeKey,
		"model": "private/baseline-verified", "contextTokens": 16384, "maxOutputTokens": 512,
		"repetitions": 1, "allowUnknownStructuredModel": allowUnknown,
	}
}

func executeHTTPBaseline(t *testing.T, input map[string]any) (int, []map[string]any, string) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	// Exercise the default command path without a response-debug opt-in flag.
	code := runCommand(t.Context(), []string{"-mode=baseline-stdin"}, bytes.NewReader(data), &output)
	var events []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	for {
		var event map[string]any
		err = decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("baseline did not emit JSON events: %v", err)
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatal("baseline emitted no status events")
	}
	if strings.Contains(output.String(), baselineHTTPFakeKey) {
		t.Fatal("baseline events disclosed the configured API key")
	}
	return code, events, output.String()
}

func baselineEventsOfType(events []map[string]any, kind string) []map[string]any {
	var matching []map[string]any
	for _, event := range events {
		if event["type"] != kind {
			continue
		}
		if kind == "sample" || kind == "summary" {
			payload, _ := event[kind].(map[string]any)
			matching = append(matching, payload)
		} else {
			matching = append(matching, event)
		}
	}
	return matching
}

func baselineHTTPResponseDebug(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	debug, ok := event["responseDebug"].(map[string]any)
	if !ok || len(debug) != 7 {
		t.Fatalf("failed preflight omitted its bounded response debug fields: %+v", debug)
	}
	capture, ok := debug["capture"].(string)
	if !ok || capture != "complete" && capture != "unavailable" && capture != "too_large" && capture != "incomplete" && capture != "read_error" {
		t.Fatalf("unexpected response capture state: %+v", debug)
	}
	body, bodyOK := debug["body"].(string)
	answer, answerOK := debug["normalizedAnswer"].(string)
	captured, capturedOK := debug["capturedBytes"].(float64)
	if !bodyOK || !answerOK || !capturedOK || captured < 0 || captured > 256<<10 || captured != float64(int(captured)) ||
		len(body) > 256<<10 || len(answer) > 64<<10 || !utf8.ValidString(body) || !utf8.ValidString(answer) {
		t.Fatal("response diagnostics changed their byte or UTF-8 bounds")
	}
	for _, field := range []string{"bodyTruncated", "answerTruncated", "redacted"} {
		if _, boolean := debug[field].(bool); !boolean {
			t.Fatalf("response diagnostics omitted boolean %s", field)
		}
	}
	for _, value := range []string{body, answer} {
		if strings.Contains(value, baselineHTTPFakeKey) {
			t.Fatal("response diagnostics disclosed the configured API key")
		}
		// JSON escapes must not conceal a leaked key from the output check.
		var decoded any
		if json.Unmarshal([]byte(value), &decoded) == nil && baselineHTTPContainsSecret(decoded) {
			t.Fatal("response diagnostics retained a JSON-escaped credential")
		}
	}
	return debug
}

func baselineHTTPContainsSecret(value any) bool {
	switch value := value.(type) {
	case string:
		return strings.Contains(value, baselineHTTPFakeKey)
	case []any:
		for _, child := range value {
			if baselineHTTPContainsSecret(child) {
				return true
			}
		}
	case map[string]any:
		for key, child := range value {
			if strings.Contains(key, baselineHTTPFakeKey) || baselineHTTPContainsSecret(child) {
				return true
			}
		}
	}
	return false
}

func baselineHTTPAssertUnavailableResponseDebug(t *testing.T, event map[string]any) {
	t.Helper()
	debug := baselineHTTPResponseDebug(t, event)
	want := map[string]any{
		"capture": "unavailable", "body": "", "capturedBytes": float64(0), "bodyTruncated": false,
		"normalizedAnswer": "", "answerTruncated": false, "redacted": false,
	}
	if !reflect.DeepEqual(debug, want) {
		t.Fatalf("missing HTTP response acquired invented diagnostic content: %+v", debug)
	}
}

func baselineHTTPAssertTextOnlyInResponseDebug(t *testing.T, events []map[string]any, marker string) {
	t.Helper()
	for _, event := range events {
		withoutDebug := make(map[string]any, len(event))
		for key, value := range event {
			if key != "responseDebug" {
				withoutDebug[key] = value
			}
		}
		raw, err := json.Marshal(withoutDebug)
		if err != nil || strings.Contains(string(raw), marker) {
			t.Fatal("response text escaped its explicitly authorized debug fields")
		}
	}
}

func TestBaselineHTTPFailedProbeShowsOriginalAnswerAndNormalizedText(t *testing.T) {
	const answer = " \nThe check passed in a plain sentence.\n "
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			payload := map[string]any{
				"id":    "response_" + baselineHTTPFakeKey,
				"usage": map[string]int{"input_tokens": 31, "output_tokens": 13},
			}
			if protocol == "openai" {
				payload["status"] = "completed"
				payload["output"] = []any{map[string]any{
					"type": "message", "role": "assistant",
					"content": []any{map[string]string{"type": "output_text", "text": answer}},
				}}
			} else {
				payload["stop_reason"] = "end_turn"
				payload["content"] = []any{map[string]string{"type": "text", "text": answer}}
			}
			wire, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			// A mixed JSON escape must be redacted after decoding too.
			wire = []byte(strings.ReplaceAll(string(wire), baselineHTTPFakeKey, "\\u0066"+baselineHTTPFakeKey[1:]))
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				var sent map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&sent); err != nil {
					t.Error(err)
					return
				}
				(&baselineHTTPFixture{t: t, protocol: protocol}).assertContract(sent, false)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(wire)
			}))
			defer server.Close()
			code, events, _ := executeHTTPBaseline(t, baselineHTTPInput(protocol, server.URL+"/readable-failure", true))
			preflight := baselineEventsOfType(events, "preflight")
			if code == 0 || calls.Load() != 1 || len(preflight) != 1 || len(baselineEventsOfType(events, "sample")) != 0 ||
				preflight[0]["failureCode"] != "strict_schema_probe_failed" ||
				preflight[0]["httpStatus"] != float64(http.StatusOK) || preflight[0]["responseKind"] != "json" {
				t.Fatalf("readable diagnostics changed strict failure or executed extra work: code=%d calls=%d events=%+v", code, calls.Load(), events)
			}
			debug := baselineHTTPResponseDebug(t, preflight[0])
			if debug["capture"] != "complete" || debug["capturedBytes"] != float64(len(wire)) ||
				debug["bodyTruncated"] != false || debug["answerTruncated"] != false || debug["redacted"] != true ||
				debug["normalizedAnswer"] != answer || !strings.Contains(debug["body"].(string), "The check passed in a plain sentence.") {
				t.Fatalf("failed probe did not show its actual response and exact normalized answer: %+v", debug)
			}
			var received struct {
				Output []struct {
					Content []struct{ Text string }
				}
				Content []struct{ Text string }
			}
			if err = json.Unmarshal([]byte(debug["body"].(string)), &received); err != nil {
				t.Fatal("redacted complete JSON response became unreadable")
			}
			if protocol == "openai" {
				if len(received.Output) != 1 || len(received.Output[0].Content) != 1 || received.Output[0].Content[0].Text != answer {
					t.Fatal("response debugging replaced the original OpenAI answer")
				}
			} else if len(received.Content) != 1 || received.Content[0].Text != answer {
				t.Fatal("response debugging replaced the original Anthropic answer")
			}
			baselineHTTPAssertTextOnlyInResponseDebug(t, events, "The check passed in a plain sentence.")
		})
	}
}

func TestBaselineHTTPAdaptersCompleteBothCases(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			fixture := &baselineHTTPFixture{
				t: t, protocol: protocol, path: "/private-gateway/v3/task-endpoint", model: "private/baseline-verified",
			}
			server := httptest.NewServer(fixture)
			defer server.Close()
			code, events, _ := executeHTTPBaseline(t, baselineHTTPInput(protocol, server.URL+fixture.path, true))
			if code != 0 {
				t.Fatalf("real adapter baseline failed: code=%d events=%+v", code, events)
			}
			preflight := baselineEventsOfType(events, "preflight")
			if len(baselineEventsOfType(events, "start")) != 1 || len(preflight) != 1 || preflight[0]["status"] != "passed" {
				t.Fatalf("start/preflight envelope missing: %+v", events)
			}
			for _, field := range []string{"schemaCheck", "wireCheck", "responseDebug"} {
				if _, diagnosed := preflight[0][field]; diagnosed {
					t.Fatal("successful strict preflight emitted failure diagnostics")
				}
			}
			samples := baselineEventsOfType(events, "sample")
			if len(samples) != 2 {
				t.Fatalf("fixed two-case denominator changed: %+v", samples)
			}
			for _, sample := range samples {
				if sample["status"] != "passed" {
					t.Fatalf("sample did not pass: %+v", sample)
				}
				duration, ok := sample["durationMs"].(float64)
				if !ok || duration < 0 || duration >= float64(leaseDuration.Milliseconds()) {
					t.Fatalf("sample latency appears to include the synthetic lease clock: %+v", sample["durationMs"])
				}
				usage, ok := sample["usage"].(map[string]any)
				if !ok || usage["knownCalls"] != float64(4) || usage["missingCalls"] != float64(0) || usage["complete"] != true {
					t.Fatalf("sample usage did not reflect exactly four settled receipts: %+v", usage)
				}
				known, ok := usage["known"].(map[string]any)
				if !ok || known["inputTokens"] != float64(124) || known["outputTokens"] != float64(52) {
					t.Fatalf("probe or stream observations contaminated sample usage: %+v", known)
				}
			}
			summaries := baselineEventsOfType(events, "summary")
			if len(summaries) != 1 || summaries[0]["requested"] != float64(2) ||
				summaries[0]["attempted"] != float64(2) || summaries[0]["passed"] != float64(2) ||
				summaries[0]["failed"] != float64(0) || summaries[0]["skipped"] != float64(0) ||
				summaries[0]["successRate"] != float64(1) {
				t.Fatalf("successful summary changed: %+v", summaries)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.probes != 1 || fixture.streams != 8 || len(fixture.answers) != 2 {
				t.Fatalf("provider path was bypassed or retried: probes=%d streams=%d answers=%+v", fixture.probes, fixture.streams, fixture.answers)
			}
			if protocol == "openai" && fixture.phaseReplayRequests != 6 {
				t.Fatalf("tool continuation or recovered request lost phase replay: got=%d want=6", fixture.phaseReplayRequests)
			}
			first, second := fixture.answers[0], fixture.answers[1]
			if first.DraftID != "draft-1" || second.DraftID != "draft-1" ||
				first.DeploymentCode == second.DeploymentCode || first.Window == second.Window ||
				first.ArtifactID == second.ArtifactID || first.ArtifactID == "" || second.ArtifactID == "" {
				t.Fatalf("cases did not independently recover distinct persisted evidence: %+v", fixture.answers)
			}
		})
	}
}

func TestBaselineHTTPPhasePreflightKeepsStrictSchemaDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		check   map[string]any
	}{
		{
			name: "false_literal", content: `{"ok":false}`,
			check: map[string]any{
				"jsonValid": true, "jsonType": "object", "okPresent": true, "okType": "boolean",
				"okIsTrue": false, "extraPropertyCount": float64(0),
			},
		},
		{
			name: "plain_secret", content: "private-schema-probe-" + baselineHTTPFakeKey,
			check: map[string]any{"jsonValid": false, "jsonType": "invalid"},
		},
		{
			name:    "secret_key_and_value",
			content: fmt.Sprintf(`{"ok":%q,%q:%q}`, baselineHTTPFakeKey, baselineHTTPFakeKey, "private-schema-probe-"+baselineHTTPFakeKey),
			check: map[string]any{
				"jsonValid": true, "jsonType": "object", "okPresent": true, "okType": "string",
				"extraPropertyCount": float64(1),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				baselineHTTPAssertStrictProbeRequest(t, r)
				w.Header().Set("Content-Type", "application/json")
				// Even a schema-valid commentary is not the terminal answer.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": baselineHTTPFakeKey + "_response", "status": "completed",
					"output": []any{
						baselineOpenAIPhaseMessage("commentary", `{"ok":true}`),
						baselineOpenAIPhaseMessage("final_answer", tc.content),
					},
				})
			}))
			defer server.Close()
			code, events, _ := executeHTTPBaseline(t, baselineHTTPInput("openai", server.URL+"/phase-probe", true))
			if code == 0 || calls.Load() != 1 || len(baselineEventsOfType(events, "sample")) != 0 {
				t.Fatalf("invalid final answer passed or retried: code=%d calls=%d events=%+v", code, calls.Load(), events)
			}
			baselineHTTPAssertTextOnlyInResponseDebug(t, events, "private-schema-probe")
			preflight := baselineEventsOfType(events, "preflight")
			if len(preflight) != 1 || preflight[0]["status"] != "failed" || preflight[0]["failureCode"] != "strict_schema_probe_failed" ||
				preflight[0]["httpStatus"] != float64(http.StatusOK) || preflight[0]["responseKind"] != "json" {
				t.Fatalf("invalid final answer lost its failure classification: %+v", preflight)
			}
			debug := baselineHTTPResponseDebug(t, preflight[0])
			if debug["redacted"] != true || debug["normalizedAnswer"] == "" ||
				!strings.Contains(tc.content, baselineHTTPFakeKey) && debug["normalizedAnswer"] != tc.content {
				t.Fatalf("failure debug lost the actual answer or key redaction: %+v", debug)
			}
			want := map[string]any{
				"contentPresent": true, "schemaValid": false, "continue": false,
				"toolCallCount": float64(0), "hostedToolCallCount": float64(0), "artifactCount": float64(0),
			}
			for key, value := range tc.check {
				want[key] = value
			}
			got, ok := preflight[0]["schemaCheck"].(map[string]any)
			if !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("schema diagnostics changed or exposed extra data: got=%+v want=%+v", got, want)
			}
			validJSON, _ := tc.check["jsonValid"].(bool)
			jsonParts := 0
			if validJSON {
				jsonParts = 1
			}
			baselineHTTPAssertWireCheck(t, preflight[0]["wireCheck"], baselineHTTPWireFormat("missing", false, false),
				baselineHTTPWireMessage(0, "commentary", 1, 1, 1, true, true, false),
				baselineHTTPWireMessage(1, "final_answer", 1, jsonParts, 0, validJSON, false, true),
			)
			summary := baselineEventsOfType(events, "summary")
			if len(summary) != 1 || summary[0]["attempted"] != float64(0) || summary[0]["skipped"] != float64(2) || summary[0]["successRate"] != nil {
				t.Fatalf("invalid strict preflight changed the task denominator: %+v", summary)
			}
		})
	}
}

func baselineHTTPAssertStrictProbeRequest(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer "+baselineHTTPFakeKey {
		t.Error("probe request changed its method or authentication contract")
	}
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Error("observer consumed or changed the actual request body")
		return
	}
	(&baselineHTTPFixture{t: t, protocol: "openai"}).assertContract(payload, false)
	var input []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	prompt := preflightPrompt
	if json.Unmarshal(payload["input"], &input) != nil || len(input) != 1 || input[0].Role != "user" || input[0].Content != prompt {
		t.Error("wire diagnostics changed the strict probe instructions")
	}
	var streaming bool
	if json.Unmarshal(payload["stream"], &streaming) != nil || streaming || len(payload["tools"]) != 0 {
		t.Error("wire diagnostics changed the single tool-free unary probe")
	}
}

func baselineHTTPWireFormat(kind string, strict, matches bool) map[string]any {
	return map[string]any{"type": kind, "strictEnabled": strict, "schemaMatchesProbe": matches}
}

func baselineHTTPWireMessage(index int, phase string, textParts, jsonParts, schemaParts int, jsonValid, schemaValid, matches bool) map[string]any {
	return map[string]any{
		"index": float64(index), "phase": phase, "textPartCount": float64(textParts),
		"refusalPartCount": float64(0), "unknownPartCount": float64(0),
		"jsonValidPartCount": float64(jsonParts), "schemaValidPartCount": float64(schemaParts),
		"jsonValid": jsonValid, "schemaValid": schemaValid, "matchesNormalized": matches,
		"leadingBOM": false, "codeFence": false,
	}
}

func baselineHTTPAssertWireCheck(t *testing.T, actual any, responseFormat map[string]any, messages ...map[string]any) {
	t.Helper()
	wireMessages := make([]any, len(messages))
	for index, message := range messages {
		wireMessages[index] = message
	}
	want := map[string]any{
		"request": map[string]any{
			"capture": "complete", "format": baselineHTTPWireFormat("json_schema", true, true),
		},
		"response": map[string]any{
			"capture": "complete", "status": "completed", "format": responseFormat,
			"outputItemCount": float64(len(messages)), "messageCount": float64(len(messages)),
			"messagesTruncated": false, "messages": wireMessages,
		},
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("wire evidence changed or retained unexpected provider data: got=%+v want=%+v", actual, want)
	}
}

func TestBaselineHTTPWireEvidenceSeparatesFinalTextAndUnphasedMessages(t *testing.T) {
	plain := "private-schema-probe-" + baselineHTTPFakeKey
	legacyFirst := baselineOpenAIPhaseMessage("commentary", plain)
	delete(legacyFirst, "phase")
	legacySecond := baselineOpenAIPhaseMessage("final_answer", "{\"ok\":true}")
	delete(legacySecond, "phase")
	for _, tc := range []struct {
		name           string
		output         []any
		echoFormat     map[string]any
		responseFormat map[string]any
		messages       []map[string]any
	}{
		{
			name:           "single_final_plain_without_format_echo",
			output:         []any{baselineOpenAIPhaseMessage("final_answer", plain)},
			responseFormat: baselineHTTPWireFormat("missing", false, false),
			messages:       []map[string]any{baselineHTTPWireMessage(0, "final_answer", 1, 0, 0, false, false, true)},
		},
		{
			name:           "single_final_plain_with_text_format_echo",
			output:         []any{baselineOpenAIPhaseMessage("final_answer", plain)},
			echoFormat:     map[string]any{"type": "text"},
			responseFormat: baselineHTTPWireFormat("text", false, false),
			messages:       []map[string]any{baselineHTTPWireMessage(0, "final_answer", 1, 0, 0, false, false, true)},
		},
		{
			name:           "strict_format_echo_does_not_validate_content",
			output:         []any{baselineOpenAIPhaseMessage("final_answer", plain)},
			echoFormat:     map[string]any{"type": "json_schema", "strict": true, "schema": probeSchema},
			responseFormat: baselineHTTPWireFormat("json_schema", true, true),
			messages:       []map[string]any{baselineHTTPWireMessage(0, "final_answer", 1, 0, 0, false, false, true)},
		},
		{
			name:           "unknown_format_type_does_not_echo_private_value",
			output:         []any{baselineOpenAIPhaseMessage("final_answer", plain)},
			echoFormat:     map[string]any{"type": baselineHTTPFakeKey, "strict": baselineHTTPFakeKey, "schema": map[string]string{baselineHTTPFakeKey: baselineHTTPFakeKey}},
			responseFormat: baselineHTTPWireFormat("other", false, false),
			messages:       []map[string]any{baselineHTTPWireMessage(0, "final_answer", 1, 0, 0, false, false, true)},
		},
		{
			name:           "two_unphased_messages_remain_ambiguous",
			output:         []any{legacyFirst, legacySecond},
			responseFormat: baselineHTTPWireFormat("missing", false, false),
			messages: []map[string]any{
				baselineHTTPWireMessage(0, "absent", 1, 0, 0, false, false, false),
				baselineHTTPWireMessage(1, "absent", 1, 1, 1, true, true, false),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				baselineHTTPAssertStrictProbeRequest(t, r)
				body := map[string]any{
					"id": baselineHTTPFakeKey + "_response", "status": "completed", "output": tc.output,
				}
				if tc.echoFormat != nil {
					body["text"] = map[string]any{"format": tc.echoFormat}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			code, events, _ := executeHTTPBaseline(t, baselineHTTPInput("openai", server.URL+"/wire-probe", true))
			if code == 0 || calls.Load() != 1 || len(baselineEventsOfType(events, "sample")) != 0 {
				t.Fatalf("wire observer retried or accepted an invalid answer: code=%d calls=%d", code, calls.Load())
			}
			baselineHTTPAssertTextOnlyInResponseDebug(t, events, "private-schema-probe")
			preflight := baselineEventsOfType(events, "preflight")
			if len(preflight) != 1 || preflight[0]["failureCode"] != "strict_schema_probe_failed" {
				t.Fatalf("wire evidence changed strict failure classification: %+v", preflight)
			}
			check, ok := preflight[0]["schemaCheck"].(map[string]any)
			if !ok || check["jsonValid"] != false || check["schemaValid"] != false {
				t.Fatalf("invalid normalized text was repaired: %+v", check)
			}
			baselineHTTPAssertWireCheck(t, preflight[0]["wireCheck"], tc.responseFormat, tc.messages...)
			debug := baselineHTTPResponseDebug(t, preflight[0])
			if !strings.Contains(debug["body"].(string), "private-schema-probe-") ||
				!strings.Contains(debug["normalizedAnswer"].(string), "private-schema-probe-") || debug["redacted"] != true {
				t.Fatalf("authorized response text was hidden instead of redacted: %+v", debug)
			}
		})
	}
}

func TestBaselineHTTPWireObserverPreservesJSONSplitAcrossTextParts(t *testing.T) {
	final := baselineOpenAIPhaseMessage("final_answer", "")
	final["content"] = []any{
		map[string]string{"type": "output_text", "text": " \n{\"ok\":"},
		map[string]string{"type": "output_text", "text": "true}\n "},
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		baselineHTTPAssertStrictProbeRequest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": baselineHTTPFakeKey + "_response", "status": "completed",
			"output": []any{baselineOpenAIPhaseMessage("commentary", "{\"ok\":true}"), final},
		})
	}))
	defer server.Close()
	input := unitBaselineInput()
	input.Endpoint, input.APIKey = server.URL+"/split-probe", baselineHTTPFakeKey
	event := baselinePreflight(t.Context(), input.settings())
	if event.Status != "passed" || event.FailureCode != "" || calls.Load() != 1 {
		t.Fatalf("valid combined JSON was rejected or retried: status=%s failure=%s calls=%d", event.Status, event.FailureCode, calls.Load())
	}
	raw, err := json.Marshal(event)
	if err != nil || bytes.Contains(raw, []byte(baselineHTTPFakeKey)) {
		t.Fatal("successful probe exposed private response evidence")
	}
	var saved map[string]any
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"schemaCheck", "wireCheck", "responseDebug"} {
		if _, present := saved[field]; present {
			t.Fatalf("successful fragmented JSON acquired failure diagnostics: %s", field)
		}
	}
}

func TestBaselineHTTPUnknownCapabilityMakesNoRequest(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			code, events, _ := executeHTTPBaseline(t, baselineHTTPInput(protocol, server.URL+"/must-not-call", false))
			preflight := baselineEventsOfType(events, "preflight")
			if code == 0 || calls.Load() != 0 || len(preflight) != 1 || preflight[0]["failureCode"] != "capability_unavailable" ||
				len(baselineEventsOfType(events, "sample")) != 0 {
				t.Fatalf("unknown model bypassed capability gate: code=%d calls=%d events=%+v", code, calls.Load(), events)
			}
			baselineHTTPAssertUnavailableResponseDebug(t, preflight[0])
			summary := baselineEventsOfType(events, "summary")
			if len(summary) != 1 || summary[0]["attempted"] != float64(0) || summary[0]["skipped"] != float64(2) || summary[0]["successRate"] != nil {
				t.Fatalf("unattempted cases became model failures or successes: %+v", summary)
			}
		})
	}
}

func TestBaselineHTTPFailureCannotEchoCredentials(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]string{
						"code": baselineHTTPFakeKey, "type": baselineHTTPFakeKey,
						"message": "private-upstream-payload-" + baselineHTTPFakeKey,
					},
				})
			}))
			defer server.Close()
			code, events, _ := executeHTTPBaseline(t, baselineHTTPInput(protocol, server.URL+"/private-api", true))
			if code == 0 || calls.Load() != 1 || len(baselineEventsOfType(events, "sample")) != 0 {
				t.Fatalf("HTTP failure was retried or became a sample: code=%d calls=%d events=%+v", code, calls.Load(), events)
			}
			baselineHTTPAssertTextOnlyInResponseDebug(t, events, "private-upstream-payload")
			preflight := baselineEventsOfType(events, "preflight")
			if len(preflight) != 1 || preflight[0]["status"] != "failed" ||
				preflight[0]["httpStatus"] != float64(http.StatusUnauthorized) || preflight[0]["responseKind"] != "json" {
				t.Fatalf("provider refusal diagnostics were not reported: %+v", preflight)
			}
			debug := baselineHTTPResponseDebug(t, preflight[0])
			if !strings.Contains(debug["body"].(string), "private-upstream-payload-") ||
				debug["normalizedAnswer"] != "" || debug["redacted"] != true {
				t.Fatalf("HTTP error message was not visible with credentials redacted: %+v", debug)
			}
		})
	}
}

func TestBaselineHTTPRedirectDoesNotForwardCredentials(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()
	targetURL := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	var sourceCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+baselineHTTPFakeKey {
			t.Error("source did not receive the configured fake credential")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.Redirect(w, r, targetURL+"/redirected", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	code, events, _ := executeHTTPBaseline(t, baselineHTTPInput("openai", source.URL+"/original", true))
	if code == 0 || sourceCalls.Load() != 1 || targetCalls.Load() != 0 || len(baselineEventsOfType(events, "sample")) != 0 {
		t.Fatalf("redirect escaped the selected endpoint: code=%d source=%d target=%d events=%+v", code, sourceCalls.Load(), targetCalls.Load(), events)
	}
	preflight := baselineEventsOfType(events, "preflight")
	if len(preflight) != 1 || preflight[0]["status"] != "failed" ||
		preflight[0]["httpStatus"] != float64(http.StatusTemporaryRedirect) || preflight[0]["responseKind"] != "html" {
		t.Fatalf("redirect diagnostics did not describe the original response: %+v", preflight)
	}
}

func TestBaselineHTTPHTMLRootPageFailsPreflight(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/" {
					t.Errorf("root endpoint changed: method=%s path=%s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "<html><body>private-login-page-"+baselineHTTPFakeKey+"</body></html>")
			}))
			defer server.Close()
			code, events, _ := executeHTTPBaseline(t, baselineHTTPInput(protocol, server.URL, true))
			if code == 0 || calls.Load() != 1 || len(baselineEventsOfType(events, "sample")) != 0 {
				t.Fatalf("HTML root response was retried or ran task samples: code=%d calls=%d events=%+v", code, calls.Load(), events)
			}
			baselineHTTPAssertTextOnlyInResponseDebug(t, events, "private-login-page")
			preflight := baselineEventsOfType(events, "preflight")
			if len(preflight) != 1 || preflight[0]["status"] != "failed" || preflight[0]["failureCode"] != "provider_failed" ||
				preflight[0]["httpStatus"] != float64(http.StatusOK) || preflight[0]["responseKind"] != "html" {
				t.Fatalf("HTML response was not distinguished from a valid API result: %+v", preflight)
			}
			debug := baselineHTTPResponseDebug(t, preflight[0])
			if debug["capture"] != "complete" || !strings.Contains(debug["body"].(string), "<html><body>private-login-page-") ||
				debug["normalizedAnswer"] != "" || debug["redacted"] != true {
				t.Fatalf("non-JSON response was not visible with credentials redacted: %+v", debug)
			}
			summary := baselineEventsOfType(events, "summary")
			if len(summary) != 1 || summary[0]["attempted"] != float64(0) || summary[0]["skipped"] != float64(2) || summary[0]["successRate"] != nil {
				t.Fatalf("failed HTML preflight changed the task denominator: %+v", summary)
			}
		})
	}
}

type baselineHTTPTransportTimeout string

func (failure baselineHTTPTransportTimeout) Error() string { return string(failure) }
func (baselineHTTPTransportTimeout) Timeout() bool         { return true }
func (baselineHTTPTransportTimeout) Temporary() bool       { return true }

func TestBaselineHTTPTransportTimeoutPreservesSafeClassification(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			// runBaseline constructs the real provider client. Keep this fixture
			// sequential and restore the default transport before any other test.
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			http.DefaultTransport = preflightRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				if request.Context().Err() != nil {
					t.Error("transport timeout fixture unexpectedly used an expired context")
				}
				if request.Method != http.MethodPost {
					t.Error("preflight did not use a provider POST")
				}
				if protocol == "openai" {
					if request.Header.Get("Authorization") != "Bearer "+baselineHTTPFakeKey {
						t.Error("OpenAI client omitted the configured fake credential")
					}
				} else if request.Header.Get("X-Api-Key") != baselineHTTPFakeKey {
					t.Error("Anthropic client omitted the configured fake credential")
				}
				return nil, baselineHTTPTransportTimeout("private-transport-timeout-" + baselineHTTPFakeKey)
			})
			code, events, output := executeHTTPBaseline(t, baselineHTTPInput(protocol, "https://provider.invalid/timeout-fixture", true))
			if code == 0 || calls.Load() != 1 || strings.Contains(output, "private-transport-timeout") ||
				len(baselineEventsOfType(events, "sample")) != 0 {
				t.Fatalf("transport timeout leaked, retried or ran samples: code=%d calls=%d events=%+v", code, calls.Load(), events)
			}
			preflight := baselineEventsOfType(events, "preflight")
			if len(preflight) != 1 || preflight[0]["status"] != "failed" || preflight[0]["failureCode"] != "model.upstream_timeout" {
				t.Fatalf("typed transport timeout lost its safe classification: %+v", preflight)
			}
			baselineHTTPAssertUnavailableResponseDebug(t, preflight[0])
			for _, field := range []string{"httpStatus", "responseKind", "schemaCheck", "wireCheck", "usage"} {
				if _, present := preflight[0][field]; present {
					t.Fatalf("transport timeout fabricated %s evidence: %+v", field, preflight)
				}
			}
			summary := baselineEventsOfType(events, "summary")
			if len(summary) != 1 || summary[0]["requested"] != float64(2) || summary[0]["attempted"] != float64(0) ||
				summary[0]["passed"] != float64(0) || summary[0]["failed"] != float64(0) ||
				summary[0]["skipped"] != float64(2) || summary[0]["successRate"] != nil || summary[0]["complete"] != false {
				t.Fatalf("transport timeout changed the unattempted task denominator: %+v", summary)
			}
		})
	}
}

func TestBaselineHTTPNoResponseDoesNotInventDiagnostics(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Error("fixture cannot close the connection before an HTTP response")
					return
				}
				connection, _, err := hijacker.Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				if err = connection.Close(); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			code, events, _ := executeHTTPBaseline(t, baselineHTTPInput(protocol, server.URL+"/close-without-response", true))
			if code == 0 || calls.Load() != 1 || len(baselineEventsOfType(events, "sample")) != 0 {
				t.Fatalf("network failure was retried or ran task samples: code=%d calls=%d events=%+v", code, calls.Load(), events)
			}
			preflight := baselineEventsOfType(events, "preflight")
			if len(preflight) != 1 || preflight[0]["status"] != "failed" || preflight[0]["failureCode"] != "provider_failed" {
				t.Fatalf("non-timeout network failure changed its generic classification: %+v", preflight)
			}
			baselineHTTPAssertUnavailableResponseDebug(t, preflight[0])
			for _, field := range []string{"httpStatus", "responseKind", "schemaCheck", "wireCheck", "usage"} {
				if _, present := preflight[0][field]; present {
					t.Fatalf("network failure fabricated %s evidence: %+v", field, preflight)
				}
			}
		})
	}
}

func TestBaselineHTTPWrongAnswersRemainInFailureDenominator(t *testing.T) {
	fixture := &baselineHTTPFixture{
		t: t, protocol: "openai", path: "/oracle-failure", model: "private/baseline-verified", wrongFinal: true,
	}
	server := httptest.NewServer(fixture)
	defer server.Close()
	code, events, _ := executeHTTPBaseline(t, baselineHTTPInput("openai", server.URL+fixture.path, true))
	samples := baselineEventsOfType(events, "sample")
	if code == 0 || len(samples) != 2 {
		t.Fatalf("failed samples were discarded: code=%d events=%+v", code, events)
	}
	for _, sample := range samples {
		if sample["status"] != "failed" || sample["failureCode"] == "" || sample["failureCode"] == nil {
			t.Fatalf("incorrect structured answer became a pass: %+v", sample)
		}
	}
	summary := baselineEventsOfType(events, "summary")
	if len(summary) != 1 || summary[0]["requested"] != float64(2) || summary[0]["attempted"] != float64(2) ||
		summary[0]["failed"] != float64(2) || summary[0]["passed"] != float64(0) ||
		summary[0]["skipped"] != float64(0) || summary[0]["successRate"] != float64(0) {
		t.Fatalf("failure denominator was not retained: %+v", summary)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.probes != 1 || fixture.streams != 8 {
		t.Fatalf("oracle failures triggered hidden retries: probes=%d streams=%d", fixture.probes, fixture.streams)
	}
}
