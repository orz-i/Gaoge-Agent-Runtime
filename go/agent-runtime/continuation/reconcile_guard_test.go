package continuation_test

import (
	"encoding/json"
	"fmt"
	"testing"

	continuationadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestSchedulerReconcileFirstPartyGuardAvoidsQuadraticChildReplay(t *testing.T) {
	fixture := newSchedulerFixture(t)
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: fixture.store, Queue: fixture.delivery, Relations: fixture.relations,
		Runs: fixture.runtime, Clock: fixedClock{}, ProjectorID: "guarded-recovery",
		ReconcileGuard: continuationadapter.ReconcileGuard,
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := createRun(t, fixture.runtime, "guarded-team", "team")
	type member struct {
		Delegation struct {
			ChildRunID string `json:"childRunID"`
			Status     string `json:"status"`
		} `json:"delegation"`
	}
	members := make([]member, 0, 12)
	for i := 0; i < 12; i++ {
		child := createRun(t, fixture.runtime, fmt.Sprintf("guarded-child-%03d", i), "agent")
		ensureRelation(t, fixture.relations, runrelation.Draft{
			ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
			Kind: runrelation.KindTeamMember, OwnerNodeID: fmt.Sprintf("member-%03d", i),
		})
		completeRun(t, fixture.runtime, child)
		for j := range members {
			members[j].Delegation.Status = "completed"
		}
		current := member{}
		current.Delegation.ChildRunID = child.Run.ID
		current.Delegation.Status = "running"
		members = append(members, current)
		state, marshalErr := json.Marshal(map[string]any{"mode": "parallel", "members": members})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		parent, err = fixture.runtime.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: state,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := scheduler.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		queuedJobs(t, fixture.delivery, i+1)
	}
	for range 2 {
		if err := scheduler.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	queuedJobs(t, fixture.delivery, len(members))
}
