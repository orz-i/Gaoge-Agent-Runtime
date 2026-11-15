package memory_test

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestRemovedCompositeKindCannotReceivePhysicalExecutionGrant(t *testing.T) {
	for _, name := range []string{"team", "group_chat", "workflow"} {
		t.Run(name, func(t *testing.T) {
			rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			snap, err := rt.Create(t.Context(), kernel.CreateRequest{
				ID: "obsolete-run", Kind: kernel.RunKind(name),
				Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
				Goal:   "unsupported", State: []byte(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rt.BeginChildExecution(t.Context(), snap.Run.ID, snap.Run.Revision); !errors.Is(err, kernel.ErrExecutionRequired) {
				t.Fatalf("removed kind %s gained physical execution authority: %v", name, err)
			}
		})
	}
}

func TestRemovedCompositeRelationKindsAreRejected(t *testing.T) {
	for _, name := range []runrelation.Kind{"team_member", "group_chat_speaker", "workflow_effect"} {
		_, err := runrelation.Prepare(runrelation.Relation{
			ParentRunID: "parent", ChildRunID: "child", Kind: name, OwnerNodeID: "owner",
			CreatedAt: time.Now().UTC(),
		})
		if !errors.Is(err, runrelation.ErrInvalidInput) {
			t.Fatalf("removed relation %s accepted: %v", name, err)
		}
	}
}
