package harness_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// Application Invocations have no physical Kernel Run. Even if an unrelated
// Run (including another tenant's) occupies the same deterministic ID, the
// original Host-owned OutputRefs remain the only Application result.
func TestRootApplicationNeverProjectsForeignKernelOutput(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{
		outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "owned-review"}},
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: harness.NewMemoryStore(), Clock: featureInvocationClock{}, Applications: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := testStartRequest()
	start.HostThread.ID = "root-output-application-thread"
	start.HostTurn.ID = "root-output-application-turn"
	start.Thread.ID = start.HostThread.ID
	start.RequestID = "root-output-application"
	start.OutputMessage = &harness.HostRef{Kind: "message", ID: "root-output-application-message"}

	started, err := runner.StartApplicationTurn(t.Context(), harness.ApplicationTurnRequest{
		StartRequest: start, CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	root, ok := harness.TopLevelInvocation(started)
	if !ok || root.ExecutionClass != harness.ExecutionApplication ||
		started.Turn.Status != harness.TurnCompleted || started.Output != nil ||
		len(root.OutputRefs) != 1 || root.OutputRefs[0].ID != "owned-review" {
		t.Fatalf("expected Host-owned Application result without Kernel output: %+v", started)
	}
	if _, err = runtime.Load(t.Context(), root.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("Application unexpectedly has a physical Run: %v", err)
	}
	foreign, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: root.ExecutionRefID, Kind: team.RunKind,
		Actor:     kernel.ActorRef{TenantID: "different-tenant", ActorID: "other-owner"},
		Thread:    kernel.ThreadRef{Kind: "conversation", ID: "different-thread"},
		RequestID: "unrelated-run", Goal: "unrelated result",
		State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Apply(t.Context(), foreign.Run.ID, foreign.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: foreign.State,
		Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"FOREIGN-SECRET"`)},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		run  func() (harness.Snapshot, error)
	}{
		{"Load", func() (harness.Snapshot, error) {
			return runner.Load(t.Context(), started.Turn.ID)
		}},
		{"Refresh", func() (harness.Snapshot, error) {
			return runner.Refresh(t.Context(), started.Turn.ID)
		}},
		{"FinalizeHostOutput", func() (harness.Snapshot, error) {
			return runner.FinalizeHostOutput(t.Context(), started.Turn.ID)
		}},
		{"FinalizeHostOutputReplay", func() (harness.Snapshot, error) {
			return runner.FinalizeHostOutput(t.Context(), started.Turn.ID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, observeErr := tc.run()
			if observeErr != nil || got.Output != nil {
				t.Fatalf("foreign physical Result leaked via %s: output=%+v err=%v", tc.name, got.Output, observeErr)
			}
			owner, found := harness.TopLevelInvocation(got)
			if !found || owner.ExecutionClass != harness.ExecutionApplication ||
				len(owner.OutputRefs) != 1 || owner.OutputRefs[0].ID != "owned-review" {
				t.Fatalf("Host-owned Application result disappeared: %+v", owner)
			}
			for _, item := range got.Items {
				if item.Kind != harness.ItemAgentMessage || item.Status != harness.ItemCompleted {
					continue
				}
				var payload struct {
					ContentHash  string `json:"contentHash"`
					ContentBytes int    `json:"contentBytes"`
				}
				if json.Unmarshal(item.Payload, &payload) != nil ||
					payload.ContentHash != "" || payload.ContentBytes != 0 {
					t.Fatalf("foreign Kernel result persisted in terminal Host Item: %+v", item)
				}
			}
		})
	}
	if executor.calls != 1 {
		t.Fatalf("Application effect unexpectedly re-executed: calls=%d", executor.calls)
	}
}
