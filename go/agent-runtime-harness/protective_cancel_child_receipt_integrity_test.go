package harness_test

import (
	"context"
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type protectiveChildReadFaultStore struct {
	harness.Store
	fail     bool
	mutation string
}

func (s *protectiveChildReadFaultStore) GetInvocation(ctx context.Context, id string) (harness.Invocation, error) {
	value, err := s.Store.GetInvocation(ctx, id)
	if err == nil && s.fail && value.ParentItemID != "" {
		switch s.mutation {
		case "", "bad-output-ref":
			value.OutputRefs = []harness.HostRef{{Kind: "", ID: ""}}
		case "bad-input-hash":
			value.InputHash = "wrong-frozen-hash"
		case "bad-input":
			value.Input = json.RawMessage(`{"goal":"other task"}`)
		case "bad-turn":
			value.TurnID = "ht_foreign"
		case "bad-attempt":
			value.Attempt++
		case "bad-run":
			value.ExecutionRefID = "hcr_foreign"
		case "bad-id":
			value.ID = "hiv_foreign"
		case "bad-parent":
			value.ParentItemID = ""
		case "bad-class":
			value.ExecutionClass = harness.ExecutionApplication
		case "bad-lifecycle":
			value.Revision = 0
		}
	}
	return value, err
}

type protectivePendingTeam struct {
	runtime   *kernel.Runtime
	relations *runrelation.Registry
}

func (f *protectivePendingTeam) StartRun(ctx context.Context, r team.StartRequest) (kernel.Snapshot, error) {
	return f.runtime.Create(ctx, kernel.CreateRequest{ID: r.ID, Kind: team.RunKind, Actor: r.Actor, Thread: r.Thread, RequestID: r.RequestID, Goal: r.Goal, State: json.RawMessage(`{}`)})
}
func (f *protectivePendingTeam) Resume(ctx context.Context, id string, _ uint64) (kernel.Snapshot, error) {
	return f.runtime.Load(ctx, id)
}
func (f *protectivePendingTeam) OwnsKernelRuntime(rt *kernel.Runtime) bool { return f.runtime == rt }
func (f *protectivePendingTeam) RecordsRelationsTo(recorder runrelation.Recorder) bool {
	return recorder == f.relations
}
func TestProtectiveChildStopUsesFrozenIdentityNotOutputMetadata(t *testing.T) {
	_, tid, parentID, relations, parentRunID, backing, runtime := newFeatureInvocationHarness(t)
	store := &protectiveChildReadFaultStore{Store: backing}
	teams := &protectivePendingTeam{runtime: runtime, relations: relations}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store, Clock: featureInvocationClock{}, Teams: teams, Relations: relations, Interactions: noopInteractionResponseHandler{}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := runner.StartTeamInvocation(t.Context(), tid, harness.TeamInvocationRequest{ParentItemID: parentID, RequestID: "child-output-refs", Goal: "run child task", Mode: team.ExecutionSequential, Members: []team.Member{{ID: "writer", Goal: "write"}}})
	if err != nil {
		t.Fatalf("start child: %v", err)
	}
	child := teamChildInvocation(t, snap)
	running, err := runtime.Load(t.Context(), child.ExecutionRefID)
	if err != nil || running.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("child run=%s err=%v", running.Run.Status, err)
	}
	store.fail = true
	_, stopErr := runner.CancelInvocation(t.Context(), child.ID, "operator child stop")
	childAfter, err := runtime.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	parentAfter, err := runtime.Load(t.Context(), parentRunID)
	if err != nil {
		t.Fatal(err)
	}

	if childAfter.Run.Status != kernel.RunStatusCancelled || parentAfter.Run.Status != kernel.RunStatusRunning {
		t.Errorf("CHILD_STOP_BLOCKED_BY_NONOWNER_METADATA: child=%s parent=%s err=%v", childAfter.Run.Status, parentAfter.Run.Status, stopErr)
	}
}

// A bad frozen child payload or Run origin cannot become a cancellation
// permission. The original child and its parent must both remain running.
func TestProtectiveChildStopRejectsBrokenFrozenExecutionIdentity(t *testing.T) {
	for _, mutation := range []string{
		"bad-input-hash", "bad-input", "bad-turn", "bad-attempt",
		"bad-run", "bad-id", "bad-parent", "bad-class", "bad-lifecycle",
	} {
		t.Run(mutation, func(t *testing.T) {
			_, turnID, parentItemID, relations, parentRunID, backing, runtime := newFeatureInvocationHarness(t)
			store := &protectiveChildReadFaultStore{Store: backing, mutation: mutation}
			teams := &protectivePendingTeam{runtime: runtime, relations: relations}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
				Clock: featureInvocationClock{}, Teams: teams, Relations: relations,
				Interactions: noopInteractionResponseHandler{},
			})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
				ParentItemID: parentItemID, RequestID: "identity-guard-team", Goal: "execute original child",
				Mode: team.ExecutionSequential, Members: []team.Member{{ID: "writer", Goal: "write"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			child := teamChildInvocation(t, snapshot)
			store.fail = true
			_, stopErr := runner.CancelInvocation(t.Context(), child.ID, "deny unproven child")
			original, loadErr := runtime.Load(t.Context(), child.ExecutionRefID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			parent, loadErr := runtime.Load(t.Context(), parentRunID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if stopErr == nil || original.Run.Status != kernel.RunStatusRunning || parent.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("%s crossed frozen child/parent stop fence: err=%v child=%s parent=%s",
					mutation, stopErr, original.Run.Status, parent.Run.Status)
			}
		})
	}
}
