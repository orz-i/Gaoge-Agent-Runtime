package openai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/openai"
)

func TestMissingOrIncompleteUsageIsNotZeroUsage(t *testing.T) {
	for _, usage := range []string{"null", "{}", `{"input_tokens":7}`, `{"output_tokens":3}`} {
		t.Run(usage, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"id":"resp-usage","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"response"}]}],"usage":%s}`, usage)
			})
			result, err := client.Generate(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}}})
			if err != nil || result.Usage != nil {
				t.Fatalf("missing usage fabricated a receipt: %+v %v", result, err)
			}
		})
	}
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"ok","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":0,"output_tokens":0}}`)
	})
	result, err := client.Generate(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}}})
	if err != nil || result.Usage == nil || result.Usage.InputTokens != 0 {
		t.Fatalf("explicit zero usage lost: %+v %v", result, err)
	}
}

func TestMissingUsageInSSEAndBudgetedAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, "event: response.completed")
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp-usage","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":null}}`)
		_, _ = fmt.Fprintln(w)
	}))
	defer server.Close()
	client, err := openai.New(openai.Config{APIKey: "test", DefaultModel: "gpt-test", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.GenerateStream(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}}}, nil)
	if err != nil || result.Usage != nil {
		t.Fatalf("stream missing usage fabricated receipt: %+v %v", result, err)
	}

	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Limits: agent.Limits{MaxTotalTokens: 1}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, runErr := runner.StartRun(context.Background(), agent.StartRequest{
		ID: "usage-audit", Actor: kernel.ActorRef{TenantID: "audit", ActorID: "test"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "test"}, Goal: "hello", Model: "gpt-test",
	})
	if snapshot.Run.Status == kernel.RunStatusCompleted || runErr == nil {
		t.Fatalf("Agent accepted unknown token consumption as zero: status=%s err=%v", snapshot.Run.Status, runErr)
	}
	if !strings.Contains(runErr.Error(), "usage") {
		t.Fatalf("unexpected failure reason: %v", runErr)
	}
}

func TestRefusalContentIsPreserved(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"refusal-1","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"I cannot assist."}]}],"usage":{"input_tokens":8,"output_tokens":4}}`)
	})
	response, err := client.Generate(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}}})
	if err != nil || response.Content != "I cannot assist." || response.Usage == nil {
		t.Fatalf("refusal dropped: %+v %v", response, err)
	}
	jsonText, err := json.Marshal(response)
	if err != nil || !strings.Contains(string(jsonText), "I cannot assist.") {
		t.Fatalf("refusal not preserved: %q %v", jsonText, err)
	}
}
