package transportconsumer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	transport "github.com/orz-i/Gaoge-Agent-Runtime/go/provider-transport"
)

// Keep the independent consumer explicit: an SDK alias would defeat this gate.
type transportPort interface {
	Generate(context.Context, transport.RouteConfig, transport.GenerateInput) (*transport.GenerateOutput, error)
	GenerateStream(context.Context, transport.RouteConfig, transport.GenerateInput, func(transport.GenerateStreamEvent) error) (*transport.GenerateOutput, error)
	CountInputTokens(context.Context, transport.RouteConfig, transport.GenerateInput) (int64, error)
	ListModels(context.Context, transport.RouteConfig) ([]transport.ModelItem, error)
}

var _ transportPort = (*transport.Client)(nil)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestIndependentTransportConsumer(t *testing.T) {
	calls := 0
	client := transport.NewClient(func(time.Duration) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			if request.Method != http.MethodPost || request.URL.Host != "fixture.invalid" || request.URL.Path != "/v1/chat/completions" {
				t.Fatalf("unexpected transport request: %s %s", request.Method, request.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
				Body:    io.NopCloser(strings.NewReader(`{"id":"fixture-completion","choices":[{"index":0,"message":{"role":"assistant","content":"fixture accepted"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`)),
				Request: request,
			}, nil
		})}
	})
	output, err := client.Generate(context.Background(), transport.RouteConfig{
		Protocol: transport.AdapterOpenAIChatCompletions, BaseURL: "https://fixture.invalid/v1",
		UpstreamModel: "fixture-model", SingleProviderAttempt: true,
	}, transport.GenerateInput{Messages: []transport.Message{{Role: "user", Content: "fixture input"}}})
	if err != nil {
		t.Fatal(err)
	}
	if output == nil || output.Text != "fixture accepted" || output.Usage.InputTokens != 7 || output.Usage.OutputTokens != 2 || calls != 1 {
		t.Fatalf("public output or single-attempt identity changed: %#v calls=%d", output, calls)
	}
}

func TestIndependentConsumerRetainsHostNetworkBoundary(t *testing.T) {
	_, err := transport.NewClient(nil).Generate(context.Background(), transport.RouteConfig{
		Protocol: transport.AdapterOpenAIResponses, BaseURL: "https://fixture.invalid", UpstreamModel: "fixture-model",
	}, transport.GenerateInput{Messages: []transport.Message{{Role: "user", Content: "fixture input"}}})
	if !errors.Is(err, transport.ErrHTTPClientRequired) {
		t.Fatalf("missing host network policy did not fail closed: %v", err)
	}
}
