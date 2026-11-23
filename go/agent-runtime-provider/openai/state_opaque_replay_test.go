package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type originalReplayApproval struct{}

func (originalReplayApproval) Name() string { return "exact-original-tool-approval" }
func (originalReplayApproval) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	return plugin.ApprovalRequired, nil
}

// Covers the previously missing physical Run-State boundary: a real OpenAI
// Responses decoder supplies a tool call and encrypted reasoning with raw HTML
// characters; a new Agent Runner must resume the original approved Tool and
// send the exact original signed replay in the second HTTP request. This test
// uses only a loopback server and must never contact the paid Provider.
func TestOpenAIOriginalToolReplaySurvivesAgentStateAndApprovedRestart(t *testing.T) {
	const arguments = `{"html":"<>&","n":1}`
	var originalResponse map[string]any
	if err := json.Unmarshal([]byte(encryptedToolResponse), &originalResponse); err != nil {
		t.Fatal(err)
	}
	originalResponse["output"].([]any)[1].(map[string]any)["arguments"] = arguments
	response, err := json.Marshal(originalResponse)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 2 {
			var incoming struct {
				Input []map[string]json.RawMessage `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil || len(incoming.Input) != 4 {
				t.Errorf("original resumed Provider request missing: %+v err=%v", incoming, err)
			} else {
				var original string
				if err := json.Unmarshal(incoming.Input[2]["arguments"], &original); err != nil || original != arguments {
					// Provider wire field is a JSON string, not a structured Tool object.
					t.Errorf("original Tool arguments changed on second upstream request: %s err=%v", incoming.Input[2]["arguments"], err)
				}
				if string(incoming.Input[1]["encrypted_content"]) != `"opaque-fixture"` {
					t.Errorf("original encrypted reasoning was not continued: %+v", incoming.Input)
				}
			}
		}
		body := string(response)
		if count != 1 {
			body = completedAnswer
		}
		_, _ = fmt.Fprintln(w, "event: response.completed")
		_, _ = fmt.Fprintln(w, "data: "+fmt.Sprintf(`{"type":"response.completed","response":%s}`, body))
		_, _ = fmt.Fprintln(w)
	}))
	defer server.Close()

	client, err := openai.New(openai.Config{APIKey: "test", Endpoint: server.URL, DefaultModel: "gpt-reasoning-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: definition,
		Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
			effects++
			if string(r.Call.Arguments) != arguments {
				t.Fatalf("original Tool physical intent rewritten after Agent restart: %s", r.Call.Arguments)
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := interaction.New(runtime)
	if err != nil {
		t.Fatal(err)
	}
	dependencies := agent.Dependencies{
		Runtime: runtime, Model: client, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{originalReplayApproval{}},
	}
	first, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := first.StartRun(t.Context(), agent.StartRequest{
		ID: "openai-original-raw-restart", Actor: kernel.ActorRef{TenantID: "audit", ActorID: "tester"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "raw-tool-replay"},
		Goal:   "use lookup and complete", Model: "gpt-reasoning-fixture", ToolKeys: []string{definition.Key},
	})
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput || waiting.Checkpoint == nil ||
		count != 1 || effects != 0 {
		t.Fatalf("first original Provider call did not wait for approval: status=%s err=%v http=%d tools=%d", waiting.Run.Status, err, count, effects)
	}
	// Independently reconstruct Agent with the same durable Kernel and exact
	// original Host-selected Tool/Policy/Provider identity.
	second, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := second.ResolveApproval(t.Context(), waiting.Run.ID, waiting.Run.Revision,
		plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || count != 2 || effects != 1 {
		t.Fatalf("original Tool/Provider replay failed after restart: status=%s err=%v http=%d tools=%d", completed.Run.Status, err, count, effects)
	}
	// A consumed decision must not authorize a second external Tool or Provider.
	_, repeatErr := second.ResolveApproval(t.Context(), completed.Run.ID, completed.Run.Revision,
		plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if !errors.Is(repeatErr, agent.ErrApprovalRequired) || count != 2 || effects != 1 {
		t.Fatalf("consumed approval caused original external effects to repeat: err=%v http=%d tools=%d", repeatErr, count, effects)
	}
}
