package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type issue107ProbeStartMiddleware struct{ called int }

func (m *issue107ProbeStartMiddleware) Name() string { return "issue107.probe_untrusted_start" }
func (m *issue107ProbeStartMiddleware) Run(ctx context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	m.called++
	return next(ctx)
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
			middleware := &issue107ProbeStartMiddleware{}
			deps := agent.Dependencies{Runtime: rt, Model: auditAgentModel{},
				RunMiddleware: []plugin.RunMiddleware{middleware}}
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
			if !agent.IsNativeStartNoEffect(startErr) || middleware.called != 0 {
				t.Fatalf("Host Start ran before pure frozen source admission: err=%v middlewareCalls=%d", startErr, middleware.called)
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
