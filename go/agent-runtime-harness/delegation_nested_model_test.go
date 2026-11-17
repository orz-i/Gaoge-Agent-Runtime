package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type nestedModelTestClient struct{}

func (nestedModelTestClient) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "test"}, nil
}

func TestNestedMemberInheritsImmediateParentsFrozenModel(t *testing.T) {
	f := newPR82HandoffFixture(t)
	config, err := SealConfigSnapshot(f.turn.ID, ConfigSnapshot{
		AdmissionHash: strings.Repeat("a", 64), Model: "root-model", ModelOptions: json.RawMessage(`{"temperature":0.8}`),
	}, pr82HandoffClock{}.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	turn := f.turn
	turn.ConfigSnapshotID = config.ID
	local, err := agent.NewRunner(agent.Dependencies{Runtime: f.runtime, Model: nestedModelTestClient{}})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := local.PrepareRun(t.Context(), agent.StartRequest{
		ID: "nested-parent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "pr82-role-thread"},
		Goal:   "specialist parent", Model: "parent-specialist", ModelOptions: json.RawMessage(`{"temperature":0.1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation := Invocation{ID: "nested-invocation", TurnID: turn.ID, ParentItemID: f.startedItemID,
		ExecutionClass: ExecutionAgent, ExecutionRefID: parent.Run.ID}
	for _, tc := range []struct{ name, requested, wantModel, wantOptions string }{
		{"inherit", "", "parent-specialist", `{"temperature":0.1}`},
		{"override", "third-model", "third-model", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frozen, _, err := f.runner.prepareDelegation(t.Context(), turn, invocation, DelegateRequest{
				MemberID: "nested", Goal: "analyze", Agent: agent.StartRequest{Model: tc.requested},
			})
			if err != nil || frozen.Model != tc.wantModel || string(frozen.ModelOptions) != tc.wantOptions {
				t.Fatalf("nested Model selection=%q options=%s err=%v", frozen.Model, frozen.ModelOptions, err)
			}
		})
	}
}
