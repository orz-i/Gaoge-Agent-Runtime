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

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const baselineHTTPFakeKey = "fake_baseline_key_123"

var baselineToolNames = map[string]string{
	"lookup_release":        lookupKey,
	"read_context_artifact": harness.ContextArtifactToolKey,
	"save_draft":            draftKey,
}

// The server decodes the real provider request and derives every task action
// from those messages. No source fixture or scripted cursor bypasses HTTP.
type baselineHTTPFixture struct {
	t          *testing.T
	protocol   string
	path       string
	model      string
	wrongFinal bool
	mu         sync.Mutex
	probes     int
	streams    int
	answers    []taskAnswer
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
	Type      string     `json:"type"`
	Role      model.Role `json:"role"`
	Content   string     `json:"content"`
	CallID    string     `json:"call_id"`
	Name      string     `json:"name"`
	Arguments string     `json:"arguments"`
	Output    string     `json:"output"`
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
		for _, item := range items {
			switch item.Type {
			case "":
				request.Messages = append(request.Messages, model.Message{Role: item.Role, Content: item.Content})
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

func (fixture *baselineHTTPFixture) writeResponse(w http.ResponseWriter, response model.Response, stream bool) {
	usage := map[string]int{"input_tokens": 31, "output_tokens": 13}
	if fixture.protocol == "openai" {
		output := make([]any, 0, len(response.ToolCalls)+1)
		for _, call := range response.ToolCalls {
			output = append(output, map[string]any{
				"type": "function_call", "call_id": baselineHTTPFakeKey + "_" + call.ID, "name": baselineProviderToolName(call), "arguments": string(call.Arguments),
			})
		}
		if response.Content != "" {
			output = append(output, map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": response.Content}}})
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
		"model": "private/baseline-verified", "contextTokens": 8192, "maxOutputTokens": 512,
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
	code := runBaseline(t.Context(), bytes.NewReader(data), &output)
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
			if len(baselineEventsOfType(events, "start")) != 1 || len(baselineEventsOfType(events, "preflight")) != 1 {
				t.Fatalf("start/preflight envelope missing: %+v", events)
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
			first, second := fixture.answers[0], fixture.answers[1]
			if first.DraftID != "draft-1" || second.DraftID != "draft-1" ||
				first.DeploymentCode == second.DeploymentCode || first.Window == second.Window ||
				first.ArtifactID == second.ArtifactID || first.ArtifactID == "" || second.ArtifactID == "" {
				t.Fatalf("cases did not independently recover distinct persisted evidence: %+v", fixture.answers)
			}
		})
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
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]string{
						"code": baselineHTTPFakeKey, "type": baselineHTTPFakeKey,
						"message": "private-upstream-payload-" + baselineHTTPFakeKey,
					},
				})
			}))
			defer server.Close()
			code, events, output := executeHTTPBaseline(t, baselineHTTPInput(protocol, server.URL+"/private-api", true))
			if code == 0 || calls.Load() != 1 || strings.Contains(output, "private-upstream-payload") ||
				len(baselineEventsOfType(events, "sample")) != 0 {
				t.Fatalf("HTTP failure leaked or became a sample: code=%d calls=%d events=%+v", code, calls.Load(), events)
			}
			preflight := baselineEventsOfType(events, "preflight")
			if len(preflight) != 1 || preflight[0]["status"] != "failed" {
				t.Fatalf("provider refusal was not reported: %+v", preflight)
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
		http.Redirect(w, r, targetURL+"/redirected", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	code, events, _ := executeHTTPBaseline(t, baselineHTTPInput("openai", source.URL+"/original", true))
	if code == 0 || sourceCalls.Load() != 1 || targetCalls.Load() != 0 || len(baselineEventsOfType(events, "sample")) != 0 {
		t.Fatalf("redirect escaped the selected endpoint: code=%d source=%d target=%d events=%+v", code, sourceCalls.Load(), targetCalls.Load(), events)
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
