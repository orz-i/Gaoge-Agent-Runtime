package harness

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type contextGrantAuditModel struct{}

func (contextGrantAuditModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "done"}, nil
}

func TestDelegatedAgentDoesNotInheritSupervisorContextArtifactTool(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "nested"}[nested], func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			config, err := SealConfigSnapshot(f.turn.ID, ConfigSnapshot{
				AdmissionHash: strings.Repeat("a", 64), AgentSpec: AgentSpec{Model: "supervisor", ToolKeys: []string{ContextArtifactToolKey}},
			}, pr82HandoffClock{}.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = f.store.PutConfigSnapshot(t.Context(), config); err != nil {
				t.Fatal(err)
			}
			turn := f.turn
			turn.ConfigSnapshotID = config.ID
			turn.ContextCheckpointID = "supervisor-private-checkpoint"
			owner := f.parent
			if nested {
				direct, err := agent.NewRunner(agent.Dependencies{Runtime: f.runtime, Model: contextGrantAuditModel{}})
				if err != nil {
					t.Fatal(err)
				}
				parent, err := direct.PrepareRun(t.Context(), agent.StartRequest{
					ID: "nested-specialist", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
					Thread: kernel.ThreadRef{Kind: "conversation", ID: "pr82-role-thread"},
					Goal:   "specialist", Model: "specialist", ToolKeys: nil,
				})
				if err != nil {
					t.Fatal(err)
				}
				owner = Invocation{ID: "nested-invocation", TurnID: turn.ID, ParentItemID: f.startedItemID,
					ExecutionRefID: parent.Run.ID}
			}
			scope, _, err := f.runner.prepareDelegation(t.Context(), turn, owner, DelegateRequest{MemberID: "child", Goal: "inspect"})
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(scope.ToolKeys, ContextArtifactToolKey) || slices.Contains(scope.InitialToolKeys, ContextArtifactToolKey) {
				t.Fatalf("child obtained implicit supervisor Context Tool: tools=%v eager=%v", scope.ToolKeys, scope.InitialToolKeys)
			}
			if len(scope.ToolKeys) > 0 || len(scope.InitialToolKeys) > 0 {
				t.Fatalf("unselected tools propagated to child: %+v", scope)
			}
		})
	}
}
