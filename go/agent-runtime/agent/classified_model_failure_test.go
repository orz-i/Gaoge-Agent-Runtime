package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type classifiedFailureModel struct {
	cause error
	calls int
}

func (provider *classifiedFailureModel) Generate(context.Context, model.Request) (model.Response, error) {
	provider.calls++
	return model.Response{}, provider.cause
}

func TestAgentDurableTerminalModelCodeKeepsTypedCauseWithoutNewRetry(t *testing.T) {
	t.Parallel()
	secret := errors.New("Provider response Bearer sk-private")
	cases := []struct {
		name      string
		code      string
		retryable bool
		want      string
		attempts  int
	}{
		{"auth permanent", "model.upstream_auth_failed", false, "model.upstream_auth_failed", 1},
		{"quota permanent", "model.upstream_quota_exhausted", false, "model.upstream_quota_exhausted", 1},
		{"rate limited retry exhausted", "model.upstream_rate_limited", true, "model.upstream_rate_limited", 2},
		{"unknown untyped", "", false, "agent.model_failed", 1},
		{"untyped retry exhausted", "", true, "agent.model_retry_exhausted", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			cause := model.NewClassifiedError(secret, tc.code)
			if tc.retryable {
				cause = model.NewRetryableError(cause)
			}
			provider := &classifiedFailureModel{cause: cause}
			runner, err := NewRunner(Dependencies{
				Runtime: runtime, Model: provider,
				Execution: ExecutionPolicy{Model: CallPolicy{
					Timeout: time.Second, MaxAttempts: 2,
					InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond, MaxConcurrency: 1,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := runner.StartRun(t.Context(), StartRequest{
				Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: tc.name},
				Thread: kernel.ThreadRef{Kind: "test", ID: tc.name},
				Goal:   "invoke model",
			})
			if err == nil || !errors.Is(err, secret) {
				t.Fatalf("original cause lost: %v", err)
			}
			if tc.retryable {
				if snapshot.Run.Status != kernel.RunStatusRunning || provider.calls != 1 {
					t.Fatalf("retryable invocation should remain durable after attempt 1: %#v calls=%d", snapshot.Run, provider.calls)
				}
				snapshot, err = runner.Resume(t.Context(), snapshot.Run.ID, snapshot.Run.Revision)
			}
			if snapshot.Run.Status != kernel.RunStatusFailed || snapshot.Run.ErrorCode != tc.want ||
				provider.calls != tc.attempts || !strings.Contains(snapshot.Run.ErrorDetail, "sk-private") {
				t.Fatalf("status=%s code=%s calls=%d err=%v", snapshot.Run.Status, snapshot.Run.ErrorCode, provider.calls, err)
			}
		})
	}
}
