package agent_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type deferredOwnedPolicyClient struct {
	policies atomic.Int32
	models   atomic.Int32
}

func (client *deferredOwnedPolicyClient) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.models.Add(1)
	if request.BindingRef != "verified-route" {
		return model.Response{}, agent.ErrInvalidRequest
	}
	return model.Response{Content: "done"}, nil
}
func (client *deferredOwnedPolicyClient) NormalizeModelPolicy(_ context.Context, _ model.Request) (model.PolicyBinding, error) {
	client.policies.Add(1)
	return model.PolicyBinding{BindingRef: "verified-route"}, nil
}

func TestDeferredCancellationProofRejectsExternalPolicyCallbackLifetime(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &deferredOwnedPolicyClient{}
	release := make(chan struct{})
	returned := make(chan struct{})
	var external atomic.Int32
	r, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: modelClient,
		NormalizeModelPolicy: func(ctx context.Context, req model.Request) (model.PolicyBinding, error) {
			go func() { <-release; external.Add(1); close(returned) }()
			return modelClient.NormalizeModelPolicy(ctx, req)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := r.PrepareRun(t.Context(), agent.StartRequest{ID: "policy-external", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "thread", ID: "p"}, RequestID: "source", Goal: "g", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := r.Resume(t.Context(), initial.Run.ID, initial.Run.Revision)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("completed callback: %+v %v", done.Run, err)
	}
	if r.VerifiedDeferredRootControl(t.Context(), rt, done) {
		t.Fatal("external Host policy function obtained a settled physical-callback cancellation receipt")
	}
	close(release)
	<-returned
	if external.Load() != 1 {
		t.Fatal("detached effect did not execute, test invalid")
	}
}

func TestDeferredCancellationProofAcceptsSelectedModelOwnedPolicy(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	client := &deferredOwnedPolicyClient{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := runner.PrepareRun(t.Context(), agent.StartRequest{ID: "policy-owned", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "thread", ID: "p"}, RequestID: "source2", Goal: "g", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := runner.Resume(t.Context(), initial.Run.ID, initial.Run.Revision)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted || client.models.Load() != 1 || client.policies.Load() != 1 {
		t.Fatalf("provider-owned policy did not execute as selected callback: %+v %v policies=%d models=%d", done.Run, err, client.policies.Load(), client.models.Load())
	}
	if !runner.VerifiedDeferredRootControl(t.Context(), rt, done) {
		t.Fatal("first-party selected Model callback ownership was lost")
	}
}
