package provider_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	provider "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestContextCounterExactRoutedRequestAndConservativeFallback(t *testing.T) {
	var countCalls, generateCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/responses/input_tokens":
			countCalls.Add(1)
			var payload map[string]json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			var routed string
			_ = json.Unmarshal(payload["model"], &routed)
			if routed != "native-exact" {
				t.Errorf("wrong model %q", routed)
			}
			_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":128}`))
		default:
			generateCalls.Add(1)
			t.Error("count dispatched a generation call")
		}
	}))
	defer server.Close()
	native, err := openai.New(openai.Config{Endpoint: server.URL + "/v1/responses",
		APIKey: "fake", HTTPClient: server.Client(), BindingScope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "host-binding", Client: native})
	if err != nil {
		t.Fatal(err)
	}
	counter := provider.ContextTokenCounter{Client: router}
	request := model.Request{RunID: "run", Model: "openai/native-exact",
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts(strings.Repeat("ambiguous unicode 演示 ", 100))}}}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	// The Context Manager now preserves the original typed model.Request
	// bytes when counting; nested RawMessage Tool arguments may be signed
	// by the Provider replay and cannot be key-reordered.
	base := runtimecontext.TokenCountRequest{
		Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-exact"}, Payload: payload,
	}
	got, err := counter.Count(t.Context(), base)
	if err != nil || got != 128 || countCalls.Load() != 1 || generateCalls.Load() != 0 {
		t.Fatalf("exact routed count=%d err=%v calls=%d generation=%d", got, err, countCalls.Load(), generateCalls.Load())
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{Counter: counter})
	window := runtimecontext.ModelWindow{ContextTokens: 2048,
		TokenCountContext: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-exact"}}
	policy := runtimecontext.Policy{MaxInputTokens: 512}
	assessment, err := manager.AssessModelRequest(t.Context(), request, window, policy)
	if err != nil || assessment.TokenCountSource != runtimecontext.CountExact ||
		assessment.HardTokenEstimate != 128 || !runtimecontext.WithinHardBudget(assessment, policy) {
		t.Fatalf("exact context budget not granted: %#v err=%v", assessment, err)
	}
	if generateCalls.Load() != 0 {
		t.Fatal("count triggered model generation")
	}

	for _, changed := range []runtimecontext.TokenCountRequest{
		{Context: runtimecontext.TokenCountContext{Protocol: "anthropic", Model: "native-exact"}, Payload: payload},
		{Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-other"}, Payload: payload},
		{Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "openai/native-exact"}, Payload: payload},
		{Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-exact"}, Payload: []byte(`{"runID":"x","runID":"y"}`)},
		{Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-exact"}, Payload: []byte(`{"model":"openai/native-exact","foreign":1}`)},
		{Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-exact"}, Payload: []byte(`{"Model":"openai/native-exact","messages":[]}`)},
		{Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-exact"}, Payload: []byte(`{"model":"openai/native-exact"}{"model":"openai/native-exact"}`)},
		{Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-exact"}, Payload: []byte(`{ "model":"openai/native-exact" }`)},
	} {
		before := countCalls.Load()
		if value, err := counter.Count(t.Context(), changed); !errors.Is(err, provider.ErrTokenCountUnavailable) || value != 0 || countCalls.Load() != before {
			t.Fatalf("untrusted count succeeded: count=%d err=%v", value, err)
		}
	}
}

func TestOpenAIOptionalBudgetAdmissionUsesExactInputAndPositiveOutputCeiling(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/responses/input_tokens" {
			t.Errorf("generation boundary crossed: %s", req.URL.Path)
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":256}`))
	}))
	defer server.Close()
	client, err := openai.New(openai.Config{APIKey: "fixture", Endpoint: server.URL + "/v1/responses",
		BindingScope: "test", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "host", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{Model: "openai/budget-native", MaxOutputTokens: 1024,
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("evidence")}}}
	counted, err := router.PlanTokenAdmission(t.Context(), request)
	if err != nil || counted.InputUpperBound != 256 || counted.MaxOutputTokens != 1024 || calls.Load() != 1 {
		t.Fatalf("invalid budget admission: %+v err=%v calls=%d", counted, err, calls.Load())
	}
	request.MaxOutputTokens = 0
	before := calls.Load()
	if _, err := router.PlanTokenAdmission(t.Context(), request); err == nil || calls.Load() != before {
		t.Fatalf("unbounded budget was wrongly admitted: err=%v calls=%d", err, calls.Load())
	}
}

func TestContextCounterMissingNativeCounterNeverMintsExactProof(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client, err := openai.New(openai.Config{APIKey: "fixture", Endpoint: server.URL + "/v1/responses",
		BindingScope: "test", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	counter := provider.ContextTokenCounter{Client: client}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{Counter: counter})
	request := model.Request{Model: "native", Messages: []model.Message{{Role: model.RoleUser,
		Content: model.TextParts(strings.Repeat("dataset ", 150))}}}
	window := runtimecontext.ModelWindow{ContextTokens: 1024,
		TokenCountContext: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native"}}
	policy := runtimecontext.Policy{MaxInputTokens: 300}
	got, err := manager.AssessModelRequest(t.Context(), request, window, policy)
	if err != nil || got.TokenCountSource != runtimecontext.CountEstimated ||
		runtimecontext.WithinHardBudget(got, policy) || calls.Load() != 1 {
		t.Fatalf("missing count bypassed conservative hard budget: %+v err=%v calls=%d", got, err, calls.Load())
	}
}
