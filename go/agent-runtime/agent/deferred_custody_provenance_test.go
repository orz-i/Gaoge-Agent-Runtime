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

func custodyStartRequest(id string) agent.StartRequest {
	return agent.StartRequest{ID: id, Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "thread", ID: "p"}, RequestID: "source", Goal: "g", Model: "model"}
}

func TestDeferredControlCannotBorrowForeignPolicyCallback(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	client := &deferredOwnedPolicyClient{}
	original, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := original.PrepareRun(t.Context(), custodyStartRequest("foreign-policy"))
	if err != nil {
		t.Fatal(err)
	}
	release, finished := make(chan struct{}), make(chan struct{})
	foreign, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client,
		NormalizeModelPolicy: func(ctx context.Context, req model.Request) (model.PolicyBinding, error) {
			go func() { <-release; close(finished) }()
			return client.NormalizeModelPolicy(ctx, req)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done, err := foreign.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(release); <-finished })
	for _, observer := range []*agent.Runner{original, foreign} {
		if observer.VerifiedDeferredRootControl(t.Context(), rt, done) {
			t.Fatal("observer upgraded an opaque executing callback's custody")
		}
	}
}

type preparationObserver struct{}

func (preparationObserver) Name() string                          { return "test.preparation-observer" }
func (preparationObserver) Observe(context.Context, plugin.Event) {}

func TestReconstructedControlCannotUpgradeOpaquePreparationHistory(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	client := &deferredOwnedPolicyClient{}
	opaque, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client,
		Observers: []plugin.Observer{preparationObserver{}}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := opaque.PrepareRun(t.Context(), custodyStartRequest("opaque-prepare"))
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.VerifiedDeferredRootControl(t.Context(), rt, prepared) {
		t.Fatal("rebuilt observer borrowed opaque Prepare custody")
	}
	// Even a genuine later scoped callback cannot settle earlier opaque preparation.
	done, err := rebuilt.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.VerifiedDeferredRootControl(t.Context(), rt, done) {
		t.Fatal("latest scoped callback erased opaque physical history")
	}
}
