package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

var errRootOutputAuditBusinessUnknown = errors.New("business callback outcome unknown")

type rootOutputAuditBusinessHandler struct{ calls int }

func (h *rootOutputAuditBusinessHandler) HandleInteractionResponse(context.Context, harness.InteractionResponseContext) (harness.InteractionResponseResult, error) {
	h.calls++
	return harness.InteractionResponseResult{}, errRootOutputAuditBusinessUnknown
}

// Child Refresh/Resume are read-only when a resolved Host callback remains
// outcome-unknown. Their Turn Snapshot.Output must always be the TOP-LEVEL
// Run's output, not a child Run result observed while taking the safety fence.
// Check both a running root with no result and a completed physical root
// whose own result must be retained. Every case uses fresh durable fixtures.
func TestPendingChildObservationNeverProjectsChildResultAsTurnOutput(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		completeRoot bool
	}{
		{name: "root_running_no_output"},
		{name: "root_completed_has_own_output", completeRoot: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			turnID, parentItemID, relations, rootRunID, store, runtime := newRootOutputFrozenParentHarness(t)
			handler := &rootOutputAuditBusinessHandler{}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
				Store: store, Clock: featureInvocationClock{},
				Teams: auditRunningTeamFeature{runtime: runtime}, Relations: relations,
				Interactions: handler,
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
				ParentItemID: parentItemID, RequestID: "child-result-pending",
				Goal: "child can finish despite pending host callback",
				Mode: team.ExecutionSequential, Members: []team.Member{{ID: "child", Goal: "work"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			var child harness.Invocation
			for _, value := range started.Invocations {
				if value.ParentItemID != "" && value.ExecutionClass == harness.ExecutionTeam {
					child = value
					break
				}
			}
			if child.ID == "" || child.Status != harness.InvocationRunning {
				t.Fatalf("expected live child: %+v", child)
			}
			waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
				InvocationID: child.ID, ParentItemID: parentItemID,
				Key: "child-host-callback", Kind: harness.InteractionChoice, Schema: json.RawMessage(`{}`),
			})
			if err != nil || len(waiting.Interactions) != 1 {
				t.Fatalf("child did not enter waiting: turn=%+v err=%v", waiting.Turn, err)
			}
			_, resolveErr := runner.ResolveInteraction(t.Context(), turnID, waiting.Interactions[0].ID,
				harness.ResolveInteractionRequest{Response: json.RawMessage(`{}`)})
			if !errors.Is(resolveErr, harness.ErrInteractionOutcomeUnknown) || handler.calls != 1 {
				t.Fatalf("expected one outstanding Host callback: calls=%d err=%v", handler.calls, resolveErr)
			}

			// The physical child finished after the unverified callback claim,
			// but Harness intentionally must not terminalize that Invocation.
			beforeChild, err := runtime.Load(t.Context(), child.ExecutionRefID)
			if err != nil || beforeChild.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("child physical Run not live: %+v err=%v", beforeChild.Run, err)
			}
			finished, err := runtime.Apply(t.Context(), beforeChild.Run.ID, beforeChild.Run.Revision,
				kernel.Mutation{
					Status: kernel.RunStatusCompleted, State: beforeChild.State,
					Result: &kernel.Result{ContentType: "text", Content: json.RawMessage(`"child-only-output"`)},
				})
			if err != nil || finished.Result == nil {
				t.Fatalf("child physical completion failed: %+v err=%v", finished.Run, err)
			}

			rootBefore, err := runtime.Load(t.Context(), rootRunID)
			if err != nil || rootBefore.Result != nil || rootBefore.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("root fixture must start with no output: %+v err=%v", rootBefore, err)
			}
			if scenario.completeRoot {
				rootBefore, err = runtime.Apply(t.Context(), rootRunID, rootBefore.Run.Revision, kernel.Mutation{
					Status: kernel.RunStatusCompleted, State: rootBefore.State,
					Result: &kernel.Result{ContentType: "text", Content: json.RawMessage(`"root-only-output"`)},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			// Baseline is a regular top-level Turn read. Child observations
			// must return precisely the same output projection.
			control, err := runner.Load(t.Context(), turnID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.completeRoot {
				if control.Output == nil || string(control.Output.Content) != `"root-only-output"` {
					t.Fatalf("expected authoritative root answer, got %+v", control.Output)
				}
			} else if control.Output != nil {
				t.Fatalf("running root unexpectedly has output: %+v", control.Output)
			}

			for _, operation := range []struct {
				name string
				call func() (harness.Snapshot, error)
			}{
				{"RefreshInvocation", func() (harness.Snapshot, error) {
					return runner.RefreshInvocation(t.Context(), child.ID)
				}},
				{"ResumeInvocation", func() (harness.Snapshot, error) {
					return runner.ResumeInvocation(t.Context(), child.ID)
				}},
			} {
				t.Run(operation.name, func(t *testing.T) {
					snapshot, observeErr := operation.call()
					if observeErr != nil {
						t.Fatalf("fenced child observation failed: %v", observeErr)
					}
					if !reflect.DeepEqual(snapshot.Output, control.Output) {
						t.Fatalf("WRONG TURN OUTPUT: %s returned %+v, authoritative root %+v",
							operation.name, snapshot.Output, control.Output)
					}
					if snapshot.Turn.ID != turnID || handler.calls != 1 {
						t.Fatalf("observation changed owner or replayed callback: turn=%s calls=%d", snapshot.Turn.ID, handler.calls)
					}
				})
			}

			rootAfter, err := runtime.Load(t.Context(), rootRunID)
			if err != nil || rootAfter.Run.Revision != rootBefore.Run.Revision {
				t.Fatalf("child observation advanced root Run: before=%+v after=%+v err=%v", rootBefore.Run, rootAfter.Run, err)
			}
			childAfter, err := store.GetInvocation(t.Context(), child.ID)
			if err != nil || childAfter.Status != harness.InvocationRunning {
				t.Fatalf("unknown callback must remain unreconciled: %+v err=%v", childAfter, err)
			}
		})
	}
}

// The older shared fixture intentionally omitted frozen root Input to test
// legacy recovery. An output-owning Run requires the real sealed Actor/Thread;
// keep the stricter fixture local rather than changing unrelated test cases.
func newRootOutputFrozenParentHarness(t *testing.T) (string, string, *runrelation.Registry, string, *harness.MemoryStore, *kernel.Runtime) {
	t.Helper()
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "root-output-frozen-thread"}
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "root-output-frozen-turn"}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sessionID, turnID, hostThread, hostTurn, actor, now)
	rootRunID := "root-output-physical-parent"
	if _, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: rootRunID, Kind: agent.RunKind, Actor: actor, Thread: thread,
		RequestID: "root-output-root-start", Goal: "coordinate", State: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(struct {
		Goal   string           `json:"goal"`
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}{Goal: "coordinate", Actor: actor, Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(input)
	parent := harness.Invocation{
		ID: mustRootOutputInvocationID(turnID), TurnID: turnID, CapabilityKey: harness.CapabilityAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionAgent,
		Input: input, InputHash: fmt.Sprintf("%x", hash), ExecutionRefID: rootRunID,
		Status: harness.InvocationRunning, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	parentItemID := "root-output-parent-item"
	if _, _, err := store.AppendItem(t.Context(), harness.Item{
		ID: parentItemID, TurnID: turnID, Kind: harness.ItemInvocation, Status: harness.ItemStarted,
		RunID: rootRunID, InvocationID: parent.ID, Payload: json.RawMessage(`{"executionClass":"agent"}`),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return turnID, parentItemID, relations, rootRunID, store, runtime
}

// Owns the first physical root RequestID of this dedicated fixture.
func mustRootOutputInvocationID(turnID string) string {
	id, err := harness.InvocationID(turnID, "", harness.CapabilityAgent, "root-output-root-start")
	if err != nil {
		panic(err)
	}
	return id
}
