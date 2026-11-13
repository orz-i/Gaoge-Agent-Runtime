package provider_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type providerRetryClock struct{ now time.Time }

func (clock *providerRetryClock) Now() time.Time { return clock.now }

func TestAgentPersistsAndResumesTransientStreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name, kind, event, frame string
		retryable                bool
	}{
		{"openai-failed", "openai", "response.failed", `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"PRIVATE_STREAM_DETAIL"}}}`, true},
		{"openai-error", "openai", "error", `{"type":"error","code":"rate_limit_exceeded","message":"PRIVATE_STREAM_DETAIL"}`, true},
		{"anthropic-error", "anthropic", "error", `{"type":"error","error":{"type":"overloaded_error","message":"PRIVATE_STREAM_DETAIL"}}`, true},
		{"openai-permanent", "openai", "error", `{"type":"error","code":"insufficient_quota","message":"PRIVATE_STREAM_DETAIL"}`, false},
		{"anthropic-permanent", "anthropic", "error", `{"type":"error","error":{"type":"invalid_request_error","message":"PRIVATE_STREAM_DETAIL"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(event, data string) { _, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data) }
				if requests == 1 {
					emit(tc.event, tc.frame)
					return
				}
				if tc.kind == "openai" {
					emit("response.completed", `{"type":"response.completed","response":{"object":"response","id":"resp-retried","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Done"}]}],"usage":{"input_tokens":2,"output_tokens":1}}}`)
					return
				}
				emit("message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"msg-retried","usage":{"input_tokens":2}}}`)
				emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Done"}}`)
				emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
				emit("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)
				emit("message_stop", `{"type":"message_stop"}`)
			}))
			defer server.Close()
			var client model.Client
			var err error
			if tc.kind == "openai" {
				client, err = newAuditedOpenAIClient(openai.Config{APIKey: "fixture-key", DefaultModel: "fixture-model", Endpoint: server.URL})
			} else {
				client, err = newAuditedAnthropicClient(anthropic.Config{APIKey: "fixture-key", DefaultModel: "fixture-model", Endpoint: server.URL})
			}
			if err != nil {
				t.Fatal(err)
			}
			clock := &providerRetryClock{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
			store := memory.NewStoreWithExecutionClock(clock)
			newRunner := func() (*kernel.Runtime, *agent.Runner) {
				runtime, createErr := kernel.New(kernel.Dependencies{Store: store, Clock: clock})
				if createErr != nil {
					t.Fatal(createErr)
				}
				runner, createErr := agent.NewRunner(agent.Dependencies{Runtime: runtime, Clock: clock, Model: client})
				if createErr != nil {
					t.Fatal(createErr)
				}
				return runtime, runner
			}
			runtime, runner := newRunner()
			first, err := runner.StartRun(t.Context(), agent.StartRequest{
				ID: tc.name, Actor: kernel.ActorRef{TenantID: "fixture", ActorID: "owner"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "answer",
			})
			if !errors.Is(err, agent.ErrModelOutcomeUnknown) || model.IsRetryableError(err) != tc.retryable || requests != 1 {
				t.Fatalf("first request: run=%+v calls=%d err=%v", first.Run, requests, err)
			}
			physicalErr := err
			persisted, err := runtime.Load(t.Context(), first.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(persisted.State)+persisted.Run.ErrorDetail, "PRIVATE_STREAM_DETAIL") {
				t.Fatal("upstream stream detail entered durable state")
			}
			assertProviderUnknownNoReplay(t, store, clock, agent.Dependencies{Model: client}, first, physicalErr, func() int64 { return int64(requests) })

		})
	}
}
