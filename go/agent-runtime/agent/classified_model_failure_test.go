package agent

import (
	"context"
	"errors"
	"reflect"
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
		{"rate limited remains unknown", "model.upstream_rate_limited", true, "model.upstream_rate_limited", 1},
		{"unknown untyped", "", false, "agent.model_failed", 1},
		{"untyped retryable remains unknown", "", true, "agent.model_failed", 1},
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
			clock := &modelRetryTestClock{now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
			runner, err := NewRunner(Dependencies{
				Runtime: runtime, Model: provider, Clock: clock,
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
			if !errors.Is(err, ErrModelOutcomeUnknown) || snapshot.Run.Status != kernel.RunStatusRunning || snapshot.Run.ErrorCode != tc.want || provider.calls != tc.attempts || !strings.Contains(snapshot.Run.ErrorDetail, "sk-private") {
				t.Fatalf("unknown response lost typed/durable diagnostic: status=%s code=%s calls=%d err=%v", snapshot.Run.Status, snapshot.Run.ErrorCode, provider.calls, err)
			}
			view, viewErr := ViewState(snapshot)
			if viewErr != nil || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Status != ModelInvocationPending || view.ModelInvocations[0].DispatchAttempt != 1 || view.ModelInvocations[0].OutcomeErrorCode != tc.want || !strings.Contains(view.ModelInvocations[0].OutcomeErrorDetail, "sk-private") || view.Budget.Usage.LLMCalls != 0 {
				t.Fatalf("unproved response was settled or lost evidence: %+v %v", view, viewErr)
			}
			clock.Advance(3 * time.Minute)
			_, resumeErr := runner.Resume(t.Context(), snapshot.Run.ID, snapshot.Run.Revision)
			after, loadErr := runtime.Load(t.Context(), snapshot.Run.ID)
			if !errors.Is(resumeErr, ErrRootOutcomeUnknown) || provider.calls != 1 || loadErr != nil || !reflect.DeepEqual(after, snapshot) {
				t.Fatalf("unknown classified result replayed or mutated: %v calls=%d", resumeErr, provider.calls)
			}
		})
	}
}
