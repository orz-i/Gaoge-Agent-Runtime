package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	valueMessage070E80E8 = "message"
	valueUser94A9F04C    = "user"
)

const (
	valueV1Models31925F3B               = "/v1/models"
	valueExampleApp2690697F             = "Example App"
	valueData9B657B3C                   = "data"
	valueError0A1432E7                  = "error"
	valueHello29F339F3                  = "hello"
	valueHttpsAppExampleCom59DFFC2D     = "https://app.example.com"
	valueHttpsOpenrouterAiApiV1049B421D = "https://openrouter.ai/api/v1"
	valueModelDC18B212                  = "model"
	valueObject0FF1FCDC                 = "object"
)

func TestHTTPClientForRouteReusesClientByConnectTimeout(t *testing.T) {
	client := newTestProviderClient()

	defaultClient := client.httpClientForRoute(RouteConfig{})
	explicitDefaultClient := client.httpClientForRoute(RouteConfig{ConnectTimeoutMS: defaultConnectTimeoutMS})
	customClient := client.httpClientForRoute(RouteConfig{ConnectTimeoutMS: 2500})
	customClientAgain := client.httpClientForRoute(RouteConfig{ConnectTimeoutMS: 2500})

	if defaultClient == nil {
		t.Fatal("expected default route client")
	}
	if defaultClient != explicitDefaultClient {
		t.Fatal("expected default and explicit default connect timeout to reuse the same client")
	}
	if customClient == nil {
		t.Fatal("expected custom route client")
	}
	if customClient != customClientAgain {
		t.Fatal("expected identical custom connect timeout to reuse the same client")
	}
	if defaultClient == customClient {
		t.Fatal("expected different connect timeouts to use separate clients")
	}
}

func TestReadUpstreamBodyRejectsOversizedBody(t *testing.T) {
	_, err := readUpstreamBody(io.MultiReader(
		&repeatingReader{remaining: maxUpstreamBodyBytes},
		strings.NewReader("x"),
	))
	if err == nil {
		t.Fatal("expected oversized upstream body to fail")
	}
}

func TestListModelsFallsBackToOpenAICompatibleModels(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Header.Get("Authorization")+"|"+r.Header.Get("x-api-key"))
		if r.URL.Path != valueV1Models31925F3B {
			t.Fatalf("expected /v1/models, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				valueError0A1432E7: map[string]interface{}{valueMessage070E80E8: "bearer required"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			valueObject0FF1FCDC: "list",
			valueData9B657B3C: []map[string]interface{}{
				{"id": "claude-3-7-sonnet-20250219", valueObject0FF1FCDC: valueModelDC18B212, "owned_by": "clewdr"},
			},
		})
	}))
	defer server.Close()

	items, err := newTestProviderClient().ListModels(context.Background(), RouteConfig{
		Protocol: AdapterAnthropicMessages,
		BaseURL:  server.URL,
		APIKey:   "test-key",
	})
	if err != nil {
		t.Fatalf("expected fallback to succeed, got %v", err)
	}
	if len(items) != 1 || items[0].ID != "claude-3-7-sonnet-20250219" || items[0].OwnedBy != "clewdr" {
		t.Fatalf("unexpected fallback models: %#v", items)
	}
	if len(calls) != 2 {
		t.Fatalf("expected primary and fallback calls, got %d: %#v", len(calls), calls)
	}
	if calls[0] != "|test-key" {
		t.Fatalf("expected primary anthropic auth header, got %q", calls[0])
	}
	if calls[1] != "Bearer test-key|" {
		t.Fatalf("expected fallback bearer auth header, got %q", calls[1])
	}
}

type repeatingReader struct {
	remaining int
}

func (r *repeatingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = 'a'
	}
	r.remaining -= len(p)
	return len(p), nil
}

func TestListModelsFallsBackToOpenAICompatibleModelsForGemini(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1beta/models" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				valueError0A1432E7: map[string]interface{}{valueMessage070E80E8: "no gemini models endpoint"},
			})
			return
		}
		if r.URL.Path != valueV1Models31925F3B {
			t.Fatalf("unexpected fallback path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("expected fallback bearer auth header, got %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			valueObject0FF1FCDC: "list",
			valueData9B657B3C: []map[string]interface{}{
				{"id": "gemini-openai-compatible", valueObject0FF1FCDC: valueModelDC18B212, "owned_by": "proxy"},
			},
		})
	}))
	defer server.Close()

	items, err := newTestProviderClient().ListModels(context.Background(), RouteConfig{
		Protocol: AdapterGoogleInteractions,
		BaseURL:  server.URL,
		APIKey:   "test-key",
	})
	if err != nil {
		t.Fatalf("expected gemini fallback to succeed, got %v", err)
	}
	if len(items) != 1 || items[0].ID != "gemini-openai-compatible" {
		t.Fatalf("unexpected fallback models: %#v", items)
	}
	if len(paths) != 2 || paths[0] != "/v1beta/models" || paths[1] != valueV1Models31925F3B {
		t.Fatalf("expected primary gemini list then openai-compatible fallback, got %#v", paths)
	}
}

func TestListModelsDoesNotFallbackForOpenRouterBaseURL(t *testing.T) {
	if shouldFallbackToOpenAICompatibleModels(RouteConfig{
		Protocol: AdapterGoogleInteractions,
		BaseURL:  valueHttpsOpenrouterAiApiV1049B421D,
	}) {
		t.Fatal("expected openrouter base URL to keep its own models directory")
	}
}

func TestSetOpenRouterAttributionHeaders(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", nil)
	setOpenRouterAttributionHeaders(req, RouteConfig{
		BaseURL:            valueHttpsOpenrouterAiApiV1049B421D,
		AttributionReferer: "https://app.example.com/",
		AttributionTitle:   valueExampleApp2690697F,
	})

	if got := req.Header.Get("HTTP-Referer"); got != valueHttpsAppExampleCom59DFFC2D {
		t.Fatalf("expected referer header, got %q", got)
	}
	if got := req.Header.Get("X-Title"); got != valueExampleApp2690697F {
		t.Fatalf("expected x-title header, got %q", got)
	}
	if got := req.Header.Get("X-OpenRouter-Title"); got != valueExampleApp2690697F {
		t.Fatalf("expected x-openrouter-title header, got %q", got)
	}
	if got := req.Header.Get("X-OpenRouter-Categories"); got != "general-chat" {
		t.Fatalf("expected x-openrouter-categories header, got %q", got)
	}
}

func TestSetOpenRouterAttributionHeadersSkipsNonOpenRouterBaseURL(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.example.com/v1/chat/completions", nil)
	setOpenRouterAttributionHeaders(req, RouteConfig{
		BaseURL:            "https://api.example.com/v1",
		AttributionReferer: valueHttpsAppExampleCom59DFFC2D,
		AttributionTitle:   valueExampleApp2690697F,
	})

	if req.Header.Get("HTTP-Referer") != "" ||
		req.Header.Get("X-Title") != "" ||
		req.Header.Get("X-OpenRouter-Title") != "" ||
		req.Header.Get("X-OpenRouter-Categories") != "" {
		t.Fatalf("expected no openrouter attribution headers, got %#v", req.Header)
	}
}

func TestSetOpenRouterAttributionHeadersRespectsConfiguredHeaders(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", nil)
	setOpenRouterAttributionHeaders(req, RouteConfig{
		BaseURL:            valueHttpsOpenrouterAiApiV1049B421D,
		HeadersJSON:        `{"HTTP-Referer":"https://custom.example.com","X-Title":"Custom App"}`,
		AttributionReferer: valueHttpsAppExampleCom59DFFC2D,
		AttributionTitle:   valueExampleApp2690697F,
	})
	setAdditionalHeaders(req, `{"HTTP-Referer":"https://custom.example.com","X-Title":"Custom App"}`)

	if got := req.Header.Get("HTTP-Referer"); got != "https://custom.example.com" {
		t.Fatalf("expected configured referer header, got %q", got)
	}
	if got := req.Header.Get("X-Title"); got != "Custom App" {
		t.Fatalf("expected configured x-title header, got %q", got)
	}
	if got := req.Header.Get("X-OpenRouter-Title"); got != "" {
		t.Fatalf("expected no default x-openrouter-title when title is configured, got %q", got)
	}
	if got := req.Header.Get("X-OpenRouter-Categories"); got != "general-chat" {
		t.Fatalf("expected default x-openrouter-categories header, got %q", got)
	}
}

func TestOpenAIChatCompletionsStreamRetriesWhenAutoUsageOptionIsRejected(t *testing.T) {
	var includeUsageValues []interface{}
	server := newAutoUsageRetryTestServer(t, &includeUsageValues)
	defer server.Close()

	output, err := newTestProviderClient().GenerateStream(context.Background(), RouteConfig{
		Protocol:      AdapterOpenAIChatCompletions,
		BaseURL:       server.URL,
		UpstreamModel: "gpt-compatible",
	}, GenerateInput{
		Messages: []Message{{Role: valueUser94A9F04C, Content: valueHello29F339F3}},
	}, nil)
	requireNoError(t, err, "generate stream: %v")
	requireCondition(t, output.Text == "ok" && output.Usage.InputTokens == 3 && output.Usage.OutputTokens == 2, "expected retried stream output and usage, got output=%#v usage=%#v", output.Text, output.Usage)
	requireCondition(t, len(includeUsageValues) == 2 && includeUsageValues[0] == true && includeUsageValues[1] == false, "expected retry to disable only auto stream usage, got %#v", includeUsageValues)
}

func newAutoUsageRetryTestServer(t *testing.T, includeUsageValues *[]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireEqual(t, r.URL.Path, "/v1/chat/completions", "expected chat completions path, got %s", r.URL.Path)
		var payload map[string]interface{}
		requireNoError(t, json.NewDecoder(r.Body).Decode(&payload), "decode request: %v")
		streamOptions := asMap(payload["stream_options"])
		*includeUsageValues = append(*includeUsageValues, streamOptions["include_usage"])
		if len(*includeUsageValues) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				valueError0A1432E7: map[string]interface{}{valueMessage070E80E8: "unknown field stream_options.include_usage"},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
}
