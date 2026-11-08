package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestInputTokenCountUsesSameNativeInputsAndSiblingPath(t *testing.T) {
	var countCalls, generateCalls atomic.Int32
	var counted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/gateway/v1/responses/input_tokens":
			countCalls.Add(1)
			if req.Method != http.MethodPost || req.Header.Get("Authorization") != "Bearer fixture-key" {
				t.Errorf("invalid count transport")
			}
			if err := json.NewDecoder(req.Body).Decode(&counted); err != nil {
				t.Error(err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":243}`))
		case "/gateway/v1/responses":
			generateCalls.Add(1)
			t.Error("counter must not dispatch a generation request")
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := New(Config{APIKey: "fixture-key", Endpoint: server.URL + "/gateway/v1/responses",
		BindingScope: "input-count-test", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Request{Model: "native-test", Messages: []model.Message{
		{Role: model.RoleSystem, Content: model.TextParts("system")},
		{Role: model.RoleUser, Content: model.TextParts(strings.Repeat("unicode证据 ", 120))},
	}, Tools: []tools.Definition{{Key: "evidence", Name: "get_evidence", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}}}
	before, err := client.prepare(req, false)
	if err != nil {
		t.Fatal(err)
	}
	count, err := client.CountInputTokens(context.Background(), req)
	if err != nil || count != 243 || countCalls.Load() != 1 || generateCalls.Load() != 0 {
		t.Fatalf("invalid native count count=%d err=%v calls=%d/%d", count, err, countCalls.Load(), generateCalls.Load())
	}
	for _, key := range []string{"model", "input", "tools"} {
		if !reflect.DeepEqual(counted[key], normalizeJSON(t, before.body[key])) {
			t.Fatalf("count request changed provider-native %s", key)
		}
	}
	for _, forbidden := range []string{"stream", "store", "include", "max_output_tokens", "temperature", "top_p"} {
		if _, exists := counted[forbidden]; exists {
			t.Fatalf("count request sent output-only field %s", forbidden)
		}
	}
}

func normalizeJSON(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestInputTokenCountPreservesCertifiedStatelessReasoningReplay(t *testing.T) {
	var counted map[string]json.RawMessage
	var generationCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/input_tokens" {
			generationCalls.Add(1)
			t.Errorf("counting attempted generation: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&counted); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":450}`))
	}))
	defer srv.Close()
	client, err := New(Config{APIKey: "fixture", Endpoint: srv.URL + "/v1/responses",
		HTTPClient: srv.Client(), BindingScope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := encodeReplay([]json.RawMessage{
		json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-frozen-cipher"}`),
		json.RawMessage(`{"type":"function_call","id":"fc_1","status":"completed","name":"lookup","call_id":"call_1","arguments":"{\"x\":1}"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{
		Model: "native-replay",
		Messages: []model.Message{
			{Role: model.RoleUser, Content: model.TextParts("lookup x")},
			{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call_1", ToolKey: "lookup", Name: "lookup",
				Arguments: json.RawMessage(`{"x":1}`)}}, ProviderReplay: replay},
			{Role: model.RoleTool, ToolCallID: "call_1", Content: model.TextParts(`{"result":"ok"}`)},
		},
		Tools: []tools.Definition{{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`)}},
	}
	before, err := client.prepare(request, false)
	if err != nil {
		t.Fatal(err)
	}
	count, err := client.CountInputTokens(t.Context(), request)
	if err != nil || count != 450 || generationCalls.Load() != 0 {
		t.Fatalf("replay count incorrectly treated as generation: count=%d err=%v generation=%d",
			count, err, generationCalls.Load())
	}
	var sentItems []map[string]any
	if err := json.Unmarshal(counted["input"], &sentItems); err != nil {
		t.Fatal(err)
	}
	if len(sentItems) != 4 || sentItems[1]["type"] != "reasoning" ||
		sentItems[1]["encrypted_content"] != "opaque-frozen-cipher" ||
		sentItems[2]["type"] != "function_call" ||
		sentItems[2]["call_id"] != "call_1" ||
		sentItems[3]["type"] != "function_call_output" ||
		!reflect.DeepEqual(normalizeJSON(t, sentItems), normalizeJSON(t, before.body["input"])) {
		t.Fatal("token counting lost original signed reasoning/Tool replay projection")
	}
	// A changed transcript tool CallID cannot authorize either counting or generation.
	bad := model.CloneRequest(request)
	bad.Messages[1].ToolCalls[0].ID = "foreign"
	if count, err := client.CountInputTokens(t.Context(), bad); err == nil || count != 0 {
		t.Fatalf("tampered replay counted: count=%d err=%v", count, err)
	}
}

func TestInputTokenCountRejectsUntrustedReceiptsAndNoFollowRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":3}`))
	}))
	defer target.Close()
	for _, tc := range []struct {
		name, body string
		status     int
		redirect   bool
	}{
		{"missing", `{"object":"response.input_tokens"}`, 200, false},
		{"zero", `{"object":"response.input_tokens","input_tokens":0}`, 200, false},
		{"negative", `{"object":"response.input_tokens","input_tokens":-2}`, 200, false},
		{"huge", `{"object":"response.input_tokens","input_tokens":9223372036854775807}`, 200, false},
		{"fraction", `{"object":"response.input_tokens","input_tokens":1.5}`, 200, false},
		{"wrong-object", `{"object":"response","input_tokens":42}`, 200, false},
		{"duplicate", `{"object":"response.input_tokens","input_tokens":4,"input_tokens":1}`, 200, false},
		{"non-json", "<html/>", 200, false},
		{"remote-fail", `{"error":{"message":"secret"}}`, 429, false},
		{"redirect", "", 302, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/v1/responses/input_tokens" {
					t.Errorf("wrong path %s", req.URL.Path)
				}
				if tc.redirect {
					w.Header().Set("Location", target.URL+"/steal")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client, err := New(Config{APIKey: "test-key", Endpoint: srv.URL + "/v1/responses",
				HTTPClient: srv.Client(), BindingScope: "test"})
			if err != nil {
				t.Fatal(err)
			}
			count, err := client.CountInputTokens(t.Context(), model.Request{
				Model: "private", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hello")}},
			})
			if count != 0 || err == nil {
				t.Fatalf("untrusted count accepted count=%d err=%v", count, err)
			}
			if errors.Is(err, context.Canceled) {
				t.Fatal("unexpected cancellation")
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatalf("credentials followed redirect (%d)", redirected.Load())
	}
}

func TestInputTokenCounterRejectsRequestBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	client, err := New(Config{APIKey: "fixture", Endpoint: srv.URL + "/v1/responses",
		HTTPClient: srv.Client(), BindingScope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{Model: "unverified", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hello")}},
		ModelOptions: json.RawMessage(`{"reasoning":{"effort":"high"}}`)}
	if _, err := client.CountInputTokens(t.Context(), request); err == nil || calls.Load() != 0 {
		t.Fatalf("unverified effort crossed count HTTP: err=%v calls=%d", err, calls.Load())
	}
}
