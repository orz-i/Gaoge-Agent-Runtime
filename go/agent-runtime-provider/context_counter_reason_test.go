package provider_test

import (
	"context"
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

func TestInputTokenCountUnavailableClassifiesWithoutExposingProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   provider.InputCountFailureClass
	}{
		{"unsupported-bad-request", 400, `{"error":{"message":"private-api-key-in-response","code":"unsupported_payload"}}`, provider.InputCountHTTP4xx},
		{"not-found", 404, `{"error":{"message":"private-key"}}`, provider.InputCountHTTP4xx},
		{"rate-limited", 429, `{"error":{"message":"private-key"}}`, provider.InputCountHTTP4xx},
		{"server-outage", 503, `{"error":{"message":"private-key"}}`, provider.InputCountHTTP5xx},
		{"redirect", 302, "", provider.InputCountOtherHTTP},
		{"malformed-success", 200, `{"object":"response.input_tokens","input_tokens":"bad"}`, provider.InputCountTransportOrProtocol},
		{"zero-success", 200, `{"object":"response.input_tokens","input_tokens":0}`, provider.InputCountTransportOrProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses/input_tokens" {
					t.Errorf("wrong path: %s", r.URL.Path)
				}
				calls.Add(1)
				if tc.status == 302 {
					w.Header().Set("Location", "https://other.invalid/steal")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client, err := openai.New(openai.Config{Endpoint: srv.URL + "/v1/responses",
				APIKey: "fixture-secret", HTTPClient: srv.Client(), BindingScope: "count-failure-bin-test"})
			if err != nil {
				t.Fatal(err)
			}
			counter := provider.ContextTokenCounter{Client: client}
			request := model.Request{Model: "native-test", Messages: []model.Message{
				{Role: model.RoleUser, Content: model.TextParts("short test")},
			}}
			payload, err := model.MarshalRequestForInputCount(request)
			if err != nil {
				t.Fatal(err)
			}
			count, err := counter.Count(t.Context(), runtimecontext.TokenCountRequest{
				Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-test"}, Payload: payload,
			})
			if count != 0 || !errors.Is(err, provider.ErrTokenCountUnavailable) ||
				provider.ClassifyInputCountFailure(err) != tc.want || calls.Load() != 1 {
				t.Fatalf("count failure not correctly classified count=%d class=%s calls=%d err=%v",
					count, provider.ClassifyInputCountFailure(err), calls.Load(), err)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "unsupported_payload") {
				t.Fatal("upstream response text leaked through count failure")
			}
		})
	}
}

func TestInputTokenCountUnavailableLocallyRejectsBeforeHTTP(t *testing.T) {
	var httpCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpCalls.Add(1)
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":20}`))
	}))
	defer srv.Close()
	client, err := openai.New(openai.Config{Endpoint: srv.URL + "/v1/responses",
		APIKey: "fixture-secret", HTTPClient: srv.Client(), BindingScope: "count-local-bin-test"})
	if err != nil {
		t.Fatal(err)
	}
	counter := provider.ContextTokenCounter{Client: client}
	req := model.Request{Model: "native-test", Messages: []model.Message{
		{Role: model.RoleUser, Content: model.TextParts("probe")},
	}, ModelOptions: []byte(`{"reasoning":{"effort":"high"}}`)}
	payload, err := model.MarshalRequestForInputCount(req)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]runtimecontext.TokenCountRequest{
		"unverified-effort": {Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-test"}, Payload: payload},
		"foreign-model":     {Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "other"}, Payload: payload},
		"other-protocol":    {Context: runtimecontext.TokenCountContext{Protocol: "anthropic", Model: "native-test"}, Payload: payload},
	} {
		t.Run(name, func(t *testing.T) {
			count, err := counter.Count(t.Context(), input)
			if count != 0 || !errors.Is(err, provider.ErrTokenCountUnavailable) ||
				provider.ClassifyInputCountFailure(err) != provider.InputCountLocalRejected || httpCalls.Load() != 0 {
				t.Fatalf("local rejection not classified: count=%d class=%s http=%d err=%v", count,
					provider.ClassifyInputCountFailure(err), httpCalls.Load(), err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := counter.Count(ctx, runtimecontext.TokenCountRequest{
		Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-test"}, Payload: payload,
	}); !errors.Is(err, context.Canceled) || provider.ClassifyInputCountFailure(err) != provider.InputCountOther {
		t.Fatalf("cancellation should remain cancellation and not a claimed Provider rejection: %v", err)
	}
}
