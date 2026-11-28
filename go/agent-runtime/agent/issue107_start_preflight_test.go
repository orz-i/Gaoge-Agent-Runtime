package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type issue107ProbeStartInputs struct{ called int }

func (*issue107ProbeStartInputs) BindingID() string { return "preflight-inputs" }
func (source *issue107ProbeStartInputs) ReadRunInputs(context.Context, kernel.Run, uint64, int) ([]agent.RunInput, error) {
	source.called++
	return nil, nil
}
func TestIssue107ParentedStartMustPassPureHostPreflightBeforeCreate(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-preflight", true: "denying-preflight"}[configured], func(t *testing.T) {
			rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
			parent, err := rt.Create(t.Context(), kernel.CreateRequest{
				ID: "root", Kind: "workflow", Actor: actor, Thread: thread, Goal: "original", State: []byte(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			inputs := &issue107ProbeStartInputs{}
			deps := agent.Dependencies{Runtime: rt, Model: &nativeLeaseFixtureModel{},
				Inputs: inputs}
			if configured {
				deps.StartControlPreflight = func(context.Context, agent.StartRequest) error { return agent.ErrInvalidRequest }
			}
			runner, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			_, startErr := runner.StartRun(t.Context(), agent.StartRequest{
				ID: "attempt-child", ParentRunID: parent.Run.ID, RequestID: "effect", Goal: "new model call",
				OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"isolatedContext":true}`)),
				Actor:             actor, Thread: thread, Model: "model",
			})
			if !agent.IsNativeStartNoEffect(startErr) || inputs.called != 0 {
				t.Fatalf("Host Start ran before pure frozen source admission: err=%v inputsCalls=%d", startErr, inputs.called)
			}
			if _, loadErr := rt.Load(t.Context(), "attempt-child"); !errors.Is(loadErr, kernel.ErrNotFound) {
				t.Fatalf("invalid frozen child was created/pinned before Host admission: %v", loadErr)
			}
			if _, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "invalid child must not block"); err != nil {
				t.Fatalf("failed Host preflight stranded parent Grant: %v", err)
			}
		})
	}
}
