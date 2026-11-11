package transport

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestTransportRequiresHostNetworkPolicy(t *testing.T) {
	for _, factory := range []HTTPClientFactory{nil, func(time.Duration) *http.Client { return nil }, func(time.Duration) *http.Client { return &http.Client{} }} {
		client := NewClient(factory)
		_, err := client.Generate(context.Background(), RouteConfig{Protocol: AdapterOpenAIResponses, BaseURL: "https://example.invalid", UpstreamModel: "fixture"}, GenerateInput{Messages: []Message{{Role: "user", Content: "test"}}})
		if !errors.Is(err, ErrHTTPClientRequired) {
			t.Fatalf("must fail before network without host policy: %v", err)
		}
	}
}

func TestTransportFactoryKeepsHostTimeoutAndIsolation(t *testing.T) {
	var observed time.Duration
	client := NewClient(func(timeout time.Duration) *http.Client {
		observed = timeout
		return &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	})
	first := client.httpClientForRoute(RouteConfig{ConnectTimeoutMS: 321})
	if observed != 321*time.Millisecond {
		t.Fatal(observed)
	}
	bound := client.httpClientForRoute(RouteConfig{ConnectTimeoutMS: 321, SingleProviderAttempt: true})
	if bound == first || first.CheckRedirect != nil || bound.CheckRedirect == nil {
		t.Fatal("single attempt mutated shared host policy")
	}
}
