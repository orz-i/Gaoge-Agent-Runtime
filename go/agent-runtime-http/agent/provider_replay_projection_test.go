package agenthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const privateAnthropicThought = "HTTP_AGENT_PRIVATE_THINKING"
const privateAnthropicSignature = "HTTP_AGENT_PRIVATE_SIGNATURE"
const privateOpenAIReasoning = "HTTP_AGENT_PRIVATE_ENCRYPTED_REASONING"

type replaySecurityModel struct {
	provider string
	calls    int
}

func (f *replaySecurityModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	f.calls++
	if f.calls == 1 {
		var replay *model.ProviderReplay
		if f.provider == "anthropic" {
			replay = &model.ProviderReplay{Provider: f.provider, Format: "messages.content.v1", Data: json.RawMessage(fmt.Sprintf(
				`[{"type":"thinking","thinking":%q,"signature":%q},{"type":"tool_use","id":"call-1","name":"echo","input":{"text":"hello"}}]`,
				privateAnthropicThought, privateAnthropicSignature))}
		} else {
			call, err := json.Marshal(map[string]any{
				"type": "function_call", "call_id": "call-1", "name": "echo", "arguments": `{"text":"hello"}`,
			})
			if err != nil {
				return model.Response{}, err
			}
			replay = &model.ProviderReplay{Provider: f.provider, Format: "responses.output.v1", Data: json.RawMessage(fmt.Sprintf(
				`[{"type":"reasoning","encrypted_content":%q},%s]`, privateOpenAIReasoning, call))}
		}
		return model.Response{
			ResponseID: "model-first", ToolCalls: []tools.Call{{ID: "call-1", ToolKey: "echo:v1", Name: "echo", Arguments: json.RawMessage(`{"text":"hello"}`)}},
			ProviderReplay: replay, Usage: &model.Usage{InputTokens: 11, OutputTokens: 5},
		}, nil
	}
	return model.Response{ResponseID: "model-final", Content: "echo completed: hello", Usage: &model.Usage{InputTokens: 13, OutputTokens: 3}}, nil
}

type replayHTTPPrincipal struct{ actor kernel.ActorRef }

func (p replayHTTPPrincipal) ResolvePrincipal(*gin.Context) (kernel.ActorRef, error) {
	return p.actor, nil
}

func TestAgentHTTPCreateAndAuthorizedReadRedactProviderEvidence(t *testing.T) {
	for _, vendor := range []string{"anthropic", "openai"} {
		t.Run(vendor, func(t *testing.T) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			definition := tools.Definition{
				Key: "echo:v1", Name: "echo",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
			}
			executions := 0
			registry, err := tools.NewRegistry([]tools.Registration{{
				Definition: definition,
				Handler: tools.HandlerFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
					executions++
					return tools.ExecutionResult{
						Content: json.RawMessage(`{"echo":"hello"}`),
						Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"},
					}, nil
				}),
			}})
			if err != nil {
				t.Fatal(err)
			}
			modelClient := &replaySecurityModel{provider: vendor}
			runner, err := agent.NewRunner(agent.Dependencies{
				Runtime: runtime, Model: modelClient, Catalog: registry, Executor: registry,
			})
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
			shared := runtimehttp.NewShared(replayHTTPPrincipal{actor: actor}, nil, runtime, nil)
			router := gin.New()
			routes := router.Group("/v1")
			runtimehttp.NewModule(runtimehttp.NewHandler(runtimehttp.Dependencies{
				Runtime: runtime, Shared: shared, PendingToolProjector: PendingTool,
			})).RegisterRoutes(routes)
			NewModule(NewHandler(Dependencies{Runner: runner, Shared: shared})).RegisterRoutes(routes)

			runID := "replay-" + vendor
			body := fmt.Sprintf(`{"clientRunID":%q,"thread":{"kind":"conversation","id":"test"},"input":{"content":"Echo hello with the authorized Tool."},"model":"test-model","toolKeys":["echo:v1"]}`, runID)
			post := httptest.NewRecorder()
			router.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/v1/agent-runs", bytes.NewBufferString(body)))
			if post.Code != http.StatusAccepted {
				t.Fatalf("Agent HTTP create failed: %d %s", post.Code, post.Body.String())
			}
			if modelClient.calls != 2 || executions != 1 {
				t.Fatalf("real Agent run did not execute once: model_calls=%d tools=%d", modelClient.calls, executions)
			}
			internal, err := runtime.Load(t.Context(), runID)
			if err != nil {
				t.Fatal(err)
			}
			if internal.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("unexpected Run status: %s", internal.Run.Status)
			}
			expectedSecrets := []string{privateOpenAIReasoning}
			if vendor == "anthropic" {
				expectedSecrets = []string{privateAnthropicThought, privateAnthropicSignature}
			}
			for _, secret := range expectedSecrets {
				if !bytes.Contains(internal.State, []byte(secret)) {
					t.Errorf("private Kernel state lost signed/encrypted provider replay")
				}
			}
			publicView, err := agent.ViewState(internal)
			if err != nil {
				t.Fatal(err)
			}
			viewJSON, err := json.Marshal(publicView)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range expectedSecrets {
				if bytes.Contains(viewJSON, []byte(secret)) {
					t.Errorf("typed public ViewState exposed private provider evidence")
				}
			}
			get := httptest.NewRecorder()
			router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/runs/"+runID, nil))
			if get.Code != http.StatusOK {
				t.Fatalf("authorized Run GET failed: %d", get.Code)
			}
			for name, public := range map[string][]byte{"agent_create": post.Body.Bytes(), "authorized_get": get.Body.Bytes()} {
				for _, secret := range expectedSecrets {
					if bytes.Contains(public, []byte(secret)) {
						t.Errorf("%s exposed private provider payload", name)
					}
				}
				if bytes.Contains(public, []byte("providerReplay")) || bytes.Contains(public, []byte("encrypted_content")) {
					t.Errorf("%s exposed provider replay identity", name)
				}
				if !bytes.Contains(public, []byte("echo completed: hello")) || !bytes.Contains(public, []byte(runID)) {
					t.Errorf("%s lost author-visible Run content or identity", name)
				}
			}
			// Export redaction must not modify persisted Kernel evidence needed to
			// resume/recover a Tool continuation. The Store remains authoritative.
			persisted, err := runtime.Load(t.Context(), runID)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(persisted.State, internal.State) {
				t.Error("HTTP projection mutated the authoritative Kernel snapshot")
			}
		})
	}
}
