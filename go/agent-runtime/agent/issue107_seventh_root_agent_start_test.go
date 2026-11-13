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

type auditUnadmittedRootMiddleware struct{ physicalEffects int }

func (m *auditUnadmittedRootMiddleware) Name() string { return "audit.unadmitted_root.start" }
func (m *auditUnadmittedRootMiddleware) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if inv.Operation == plugin.RunStart && inv.ParentRunID == "" {
		// Host Run Middleware is a documented arbitrary callback; simulate its
		// physical write BEFORE the new Root Kernel Create can reject an ID clash.
		m.physicalEffects++
	}
	return next(ctx)
}
func TestAuditIssue107RootAgentStartMiddlewareExecutesBeforeFailedCreate(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	reserved, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "root-duplicate", Kind: agent.RunKind,
		Actor: actor, Thread: thread, Goal: "original Root", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	middleware := &auditUnadmittedRootMiddleware{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: auditAgentModel{},
		RunMiddleware: []plugin.RunMiddleware{middleware},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartRun(t.Context(), agent.StartRequest{
		ID: reserved.Run.ID, Goal: "different Root", Actor: actor, Thread: thread, Model: "test",
	})
	current, loadErr := rt.Load(t.Context(), reserved.Run.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	t.Logf("rootStatus=%s rootRevision=%d CreateRejected=%t HostPhysicalEffects=%d",
		current.Run.Status, current.Run.Revision, errors.Is(startErr, kernel.ErrAlreadyExists), middleware.physicalEffects)
	if middleware.physicalEffects != 0 && errors.Is(startErr, kernel.ErrAlreadyExists) {
		t.Fatal("Root Agent Start executed arbitrary Host RunMiddleware effects before failed Kernel Create; no original Run or physical Grant owns those effects")
	}
}
