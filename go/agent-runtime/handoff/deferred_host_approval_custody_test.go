package handoff_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type auditApprovalModel struct{}

func (auditApprovalModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{ToolCalls: []tools.Call{{ID: "call-1", ToolKey: "product.read", Arguments: json.RawMessage("{}")}}}, nil
}

type auditDetachedApprovalPolicy struct {
	release  <-chan struct{}
	finished chan<- struct{}
	effects  *atomic.Int32
}

func (p auditDetachedApprovalPolicy) Name() string { return "audit-approval" }
func (p auditDetachedApprovalPolicy) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	go func() { <-p.release; p.effects.Add(1); close(p.finished) }()
	return plugin.ApprovalRequired, nil
}
func TestDeferredHostApprovalCallbackNeverGrantsNativeCustody(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := interaction.New(rt)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "product.read", Name: "read_data", InputSchema: json.RawMessage("{\"type\":\"object\"}")},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			t.Fatal("unapproved tool executed")
			return tools.ExecutionResult{}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	finished := make(chan struct{})
	var effects atomic.Int32
	r, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: auditApprovalModel{}, Catalog: catalog, Executor: catalog,
		Approvals: interactionadapter.New(approvals),
		ApprovalPolicies: []plugin.ApprovalPolicy{
			auditDetachedApprovalPolicy{release: release, finished: finished, effects: &effects},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := handoff.NewDeferredAgent(r)
	if err != nil {
		t.Fatal(err)
	}
	parent := verifiedHandoffParent()
	delegation := handoff.Delegation{ID: "approval-callback", MemberID: "local", ChildRunID: "approval-child", Goal: "check", Model: "model", ToolKeys: []string{"product.read"}, Status: handoff.StatusQueued}
	_, err = c.StartOrLoad(t.Context(), parent, delegation)
	if !errors.Is(err, handoff.ErrChildPending) {
		t.Fatalf("Prepare: %v", err)
	}
	child, err := rt.Load(t.Context(), delegation.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := r.Resume(t.Context(), child.Run.ID, child.Run.Revision)
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("expected Tool approval waiting: status=%s err=%v", waiting.Run.Status, err)
	}
	claim := c.VerifiedLocalDelegationControl(t.Context(), rt, delegation, waiting)
	if err := rt.VerifyExecutionCustody(t.Context(), waiting, kernel.CustodyRetired); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("entered Host approval policy obtained scoped execution evidence: %v", err)
	}
	strict := r.VerifiedDeferredRootControl(t.Context(), rt, waiting)
	t.Logf("Host ApprovalPolicy launched detached effect; NativeStartCancellationFencedByKernel=%t; VerifiedLocalDelegationControl=%t; effects before release=%d",
		r.NativeStartCancellationFencedByKernel(rt), claim, effects.Load())
	t.Logf("Verification through newer owned-root settlement branch=%t, despite separate host approval side effect still pending", strict)
	if claim || strict || r.NativeStartCancellationFencedByKernel(rt) {
		t.Fatal("untracked Host approval callback borrowed native/deferred custody")
	}
	close(release)
	<-finished
	t.Logf("detached effect later=%d", effects.Load())
}
