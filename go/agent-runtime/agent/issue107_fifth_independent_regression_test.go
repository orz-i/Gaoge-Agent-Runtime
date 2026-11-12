package agent_test

import (
	"context"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type auditAgentModel struct{}

func (auditAgentModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "ok"}, nil
}

type auditChildStartMiddleware struct {
	runtime         *kernel.Runtime
	parent          kernel.Snapshot
	parentCancelErr error
	physicalEffects int
}

func (m *auditChildStartMiddleware) Name() string { return "audit.host.before_first_child_grant" }
func (m *auditChildStartMiddleware) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if inv.Operation == plugin.RunStart && inv.ParentRunID == m.parent.Run.ID {
		// Host physical callbacks run before Kernel child Create and before Begin.
		_, m.parentCancelErr = m.runtime.Cancel(ctx, m.parent.Run.ID, m.parent.Run.Revision,
			"cancel in parented child Start middleware")
		m.physicalEffects++
	}
	return next(ctx)
}

func TestAuditIssue107ParentedAgentStartHostMiddlewareEffectBeforeGrant(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "parent-run", Kind: "workflow", Actor: actor, Thread: thread,
		Goal: "parent", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &auditChildStartMiddleware{runtime: rt, parent: parent}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: auditAgentModel{},
		RunMiddleware: []plugin.RunMiddleware{wrapper},
		StartControlPreflight: func(_ context.Context, req agent.StartRequest) error {
			if req.ParentRunID != parent.Run.ID {
				return agent.ErrInvalidRequest
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "child-agent", RequestID: "frozen-effect", ParentRunID: parent.Run.ID,
		OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"isolatedContext":true}`)),
		Actor:             actor, Thread: thread, Goal: "perform task", Model: "test",
	})
	after, loadErr := rt.Load(t.Context(), parent.Run.ID)
	t.Logf("parent=%s parentCancel=%v childStart=%v HostExternalEffects=%d",
		after.Run.Status, wrapper.parentCancelErr, startErr, wrapper.physicalEffects)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if wrapper.parentCancelErr == nil && wrapper.physicalEffects != 0 && after.Run.Status == kernel.RunStatusCancelled {
		t.Fatal("parent cancelled before parented Agent Start middleware physically issued external Host effect without a child Grant")
	}
}
