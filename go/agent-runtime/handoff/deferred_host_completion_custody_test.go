package handoff_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type audit131DetachedCompletion struct {
	release  <-chan struct{}
	finished chan<- struct{}
	effects  *atomic.Int32
}

func (p audit131DetachedCompletion) ValidateCompletion(context.Context, kernel.Run, model.Response) (agent.CompletionCorrection, error) {
	go func() { <-p.release; p.effects.Add(1); close(p.finished) }()
	return agent.CompletionCorrection{}, nil
}
func TestDeferredHostCompletionCallbackNeverGrantsNativeCustody(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	finished := make(chan struct{})
	var effects atomic.Int32
	r, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: &deferredModel{},
		CompletionPolicies: []agent.CompletionPolicy{
			audit131DetachedCompletion{release: release, finished: finished, effects: &effects},
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
	delegation := handoff.Delegation{ID: "owner-completion", MemberID: "local", ChildRunID: "completion-child", Goal: "finish", Model: "test", Status: handoff.StatusQueued}
	_, err = c.StartOrLoad(t.Context(), parent, delegation)
	if !errors.Is(err, handoff.ErrChildPending) {
		t.Fatalf("prepare: %v", err)
	}
	child, err := rt.Load(t.Context(), delegation.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := r.Resume(t.Context(), child.Run.ID, child.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("callback: %+v %v", completed.Run, err)
	}
	claim := c.VerifiedLocalDelegationControl(t.Context(), rt, delegation, completed)
	if err := rt.VerifyOwnedRootSettlement(t.Context(), completed); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("completed result obtained scoped execution evidence after a Host completion callback: %v", err)
	}
	t.Logf("Host standalone CompletionPolicy has untracked asynchronous effect, NativeStartCancellationFencedByKernel=%t, VerifiedLocalDelegationControl=%t, pendingEffects=%d", r.NativeStartCancellationFencedByKernel(rt), claim, effects.Load())
	if claim || r.NativeStartCancellationFencedByKernel(rt) {
		t.Fatal("untracked Host completion callback borrowed native/deferred custody")
	}
	close(release)
	<-finished
	t.Logf("callback side effect completed after Runtime recognized cancellation custody; effects=%d", effects.Load())
}
