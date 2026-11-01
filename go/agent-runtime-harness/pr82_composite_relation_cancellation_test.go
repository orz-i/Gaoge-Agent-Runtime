package harness_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// Before this correction, both the Running and Failed Team roots returned
// success and closed cancellation despite a live remote member. The child is
// deliberately executed by an independent ChildRunner, without a local Run.
func TestPR82CompositeCancellationNeedsOriginalRelationIndex(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		pending bool
		mode    string
	}{
		{name: "running-no-index", pending: true, mode: "none"},
		{name: "failed-no-index", mode: "none"},
		{name: "running-different-index", pending: true, mode: "different"},
		{name: "failed-different-index", mode: "different"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			rt := newFeatureInvocationRuntime(t)
			store := harness.NewMemoryStore()
			children := &pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}}
			coordinator, err := handoff.New(children)
			if err != nil {
				t.Fatal(err)
			}
			var featureRelations runrelation.Recorder
			var harnessRelations harness.RunRelationSource
			if scenario.mode == "different" {
				featureRelations = newFeatureInvocationRelations(t)
				harnessRelations = newFeatureInvocationRelations(t)
			}
			feature, err := team.NewRunner(team.Dependencies{
				Runtime: rt, Handoffs: coordinator, Relations: featureRelations,
			})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: rt, Agent: loadingFeatureAgent{runtime: rt},
				Store: store, Clock: featureInvocationClock{},
				Teams: feature, Relations: harnessRelations,
			})
			if err != nil {
				t.Fatal(err)
			}
			members := []team.Member{{ID: "survivor", Goal: "continues"}}
			mode := team.ExecutionSequential
			if !scenario.pending {
				members = append(members, team.Member{ID: "fail", Goal: "fail-fast"})
				mode = team.ExecutionParallel
			}
			started, startErr := runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{
				StartRequest: harness.StartRequest{
					HostThread: harness.HostRef{Kind: testThreadKind, ID: "pr82-no-index-" + scenario.name},
					HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "pr82-no-index-" + scenario.name},
					Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
					Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "pr82-no-index-" + scenario.name},
					RequestID:  "pr82-no-index-start", Goal: "composite cancellation coverage",
					Config: harness.ConfigSnapshot{Model: "fixture-model"},
				},
				Mode:    mode,
				Members: members,
				Join:    handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureFailFast},
			})
			if scenario.pending {
				if startErr != nil && !errors.Is(startErr, team.ErrMemberPending) {
					t.Fatalf("running team: %v", startErr)
				}
				if started.Turn.Status != harness.TurnRunning {
					t.Fatalf("running team status: %s", started.Turn.Status)
				}
			} else if !errors.Is(startErr, team.ErrTeamFailed) || started.Turn.Status != harness.TurnFailed {
				t.Fatalf("failed team: status=%s err=%v", started.Turn.Status, startErr)
			}
			root, ok := harness.TopLevelInvocation(started)
			if !ok {
				t.Fatal("missing root Invocation")
			}
			parent, err := rt.Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := team.ViewState(parent)
			if err != nil {
				t.Fatal(err)
			}
			survivor := ""
			for _, m := range view.Members {
				physical, loadErr := children.LoadRun(t.Context(), m.Delegation.ChildRunID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				if physical.Run.Status == kernel.RunStatusRunning {
					survivor = physical.Run.ID
				}
			}
			if survivor == "" {
				t.Fatal("fixture did not create live remote member")
			}
			_, cancelErr := runner.Cancel(t.Context(), started.Turn.ID, "cancel all physical children")
			if !errors.Is(cancelErr, harness.ErrRuntimeCancellationUnverified) {
				t.Fatalf("missing relation coverage returned %v; want unverified", cancelErr)
			}
			still, err := children.LoadRun(t.Context(), survivor)
			if err != nil || still.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("original remote task was incorrectly cancelled: status=%s err=%v", still.Run.Status, err)
			}
			items, err := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if !harness.ActiveCancellationFence(items) {
				t.Fatal("unknown cleanup closed the cancellation intent")
			}
			_, retryErr := runner.Cancel(t.Context(), started.Turn.ID, "retry unresolved cleanup")
			// The first attempt now refuses the unverified physical route
			// BEFORE cancelling the Kernel parent. An unresolved fence cannot
			// be resumed while that parent is still Running, so a repeated
			// request may return ErrConflict instead of ErrRuntimeCancellationUnverified.
			// Both are fail-closed; neither certifies remote cleanup.
			if !errors.Is(retryErr, harness.ErrRuntimeCancellationUnverified) &&
				!errors.Is(retryErr, harness.ErrConflict) {
				t.Fatalf("repeated cancellation inferred success: %v", retryErr)
			}
			items, err = store.ListItems(t.Context(), started.Turn.ID, 0, 500)
			if err != nil || !harness.ActiveCancellationFence(items) {
				t.Fatalf("repeated cancellation released unresolved intent: %v", err)
			}
			current, err := store.GetTurn(t.Context(), started.Turn.ID)
			if err != nil || current.Status == harness.TurnCancelled {
				t.Fatalf("unknown remote cleanup was projected as Cancelled: status=%s err=%v", current.Status, err)
			}
		})
	}
}

// When both Feature and Harness use the original registry, local Team
// cancellation can still verify physical descendants and settle normally.
// A different registry instance is never equivalent merely because it has
// the same Go type or backing data.
func TestPR82CompositeRelationRecorderIdentity(t *testing.T) {
	rt := newFeatureInvocationRuntime(t)
	original := newFeatureInvocationRelations(t)
	other := newFeatureInvocationRelations(t)
	coordinator, err := handoff.New(&pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}})
	if err != nil {
		t.Fatal(err)
	}
	feature, err := team.NewRunner(team.Dependencies{Runtime: rt, Handoffs: coordinator, Relations: original})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := groupchat.NewRunner(groupchat.Dependencies{Runtime: rt, Handoffs: coordinator, Relations: original})
	if err != nil {
		t.Fatal(err)
	}
	flow, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: workflowWaitNoopEffects{}, Relations: original})
	if err != nil {
		t.Fatal(err)
	}
	var typedNil *runrelation.Registry
	if !feature.RecordsRelationsTo(original) || feature.RecordsRelationsTo(other) || feature.RecordsRelationsTo(nil) ||
		!chat.RecordsRelationsTo(original) || chat.RecordsRelationsTo(other) || chat.RecordsRelationsTo(nil) ||
		!flow.RecordsRelationsTo(original) || flow.RecordsRelationsTo(other) || flow.RecordsRelationsTo(nil) ||
		runrelation.SameRecorder(original, other) || runrelation.SameRecorder(typedNil, typedNil) {
		t.Fatal("composite index proof accepted foreign or missing recorder")
	}
}
