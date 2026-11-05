package harness_test

import (
	"context"
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type protectiveRootReadFaultStore struct {
	harness.Store
	fail     bool
	mutation string
}

func (s *protectiveRootReadFaultStore) ListInvocations(ctx context.Context, turnID string) ([]harness.Invocation, error) {
	items, err := s.Store.ListInvocations(ctx, turnID)
	if err != nil || !s.fail {
		return items, err
	}
	for i := range items {
		if items[i].ParentItemID == "" {
			switch s.mutation {
			case "output-ref-invalid":
				items[i].OutputRefs = []harness.HostRef{{Kind: "", ID: ""}}
			case "output-ref-duplicate":
				items[i].OutputRefs = []harness.HostRef{{Kind: "message", ID: "x"}, {Kind: "message", ID: "x"}}
			case "error-code-on-running":
				items[i].ErrorCode = "mutated"
			case "bad-input-hash":
				items[i].InputHash = "bad-frozen-input-hash"
			case "bad-input":
				items[i].Input = json.RawMessage(`{"goal":"foreign goal"}`)
			case "bad-turn":
				items[i].TurnID = "ht_foreign"
			case "bad-attempt":
				items[i].Attempt++
			case "bad-run":
				items[i].ExecutionRefID = "hr_foreign"
			case "bad-id":
				items[i].ID = "hiv_foreign"
			case "bad-parent":
				items[i].ParentItemID = "foreign-parent"
			case "bad-class":
				items[i].ExecutionClass = harness.ExecutionWorkflow
			case "bad-lifecycle":
				items[i].Revision = 0
			}
		}
	}
	return items, nil
}

type protectiveRootPendingAgent struct {
	unusedFeatureAgent
	runtime *kernel.Runtime
}

func (a protectiveRootPendingAgent) StartRun(ctx context.Context, r agent.StartRequest) (kernel.Snapshot, error) {
	return a.runtime.Create(ctx, kernel.CreateRequest{ID: r.ID, Kind: agent.RunKind, Actor: r.Actor, Thread: r.Thread, RequestID: r.RequestID, Goal: r.Goal, State: json.RawMessage(`{}`)})
}
func TestProtectiveRootStopUsesFrozenIdentityNotOutputMetadata(t *testing.T) {
	for _, mutation := range []string{"output-ref-invalid", "output-ref-duplicate", "error-code-on-running"} {
		t.Run(mutation, func(t *testing.T) {
			runtime := newFeatureInvocationRuntime(t)
			store := &protectiveRootReadFaultStore{Store: harness.NewMemoryStore(), mutation: mutation}
			runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: protectiveRootPendingAgent{runtime: runtime}, Store: store, Clock: featureInvocationClock{}})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runner.StartAgentTurn(t.Context(), testStartRequest())
			if err != nil {
				t.Fatal(err)
			}
			root, ok := harness.TopLevelInvocation(started)
			if !ok {
				t.Fatal("missing root")
			}
			before, err := runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			if before.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("before=%s", before.Run.Status)
			}
			store.fail = true
			_, cancelErr := runner.Cancel(t.Context(), started.Turn.ID, "operator protective stop")
			after, err := runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}

			if after.Run.Status != kernel.RunStatusCancelled {
				t.Errorf("STOP_BLOCKED_BY_UNRELATED_METADATA: mutation %s err=%v status=%s", mutation, cancelErr, after.Run.Status)
			}
		})
	}
}

// Refusing an unrelated OutputRef must not expand physical cancellation
// authority. Every immutable origin mutation must leave the real Run alive.
func TestProtectiveRootStopRejectsBrokenFrozenExecutionIdentity(t *testing.T) {
	for _, mutation := range []string{
		"bad-input-hash", "bad-input", "bad-turn", "bad-attempt",
		"bad-run", "bad-id", "bad-parent", "bad-class", "bad-lifecycle",
	} {
		t.Run(mutation, func(t *testing.T) {
			runtime := newFeatureInvocationRuntime(t)
			store := &protectiveRootReadFaultStore{Store: harness.NewMemoryStore(), mutation: mutation}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: protectiveRootPendingAgent{runtime: runtime}, Store: store, Clock: featureInvocationClock{},
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runner.StartAgentTurn(t.Context(), testStartRequest())
			if err != nil {
				t.Fatal(err)
			}
			root, ok := harness.TopLevelInvocation(started)
			if !ok {
				t.Fatal("missing frozen root")
			}
			store.fail = true
			_, stopErr := runner.Cancel(t.Context(), started.Turn.ID, "deny foreign root")
			current, loadErr := runtime.Load(t.Context(), root.ExecutionRefID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if stopErr == nil || current.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("%s crossed the root identity stop fence: err=%v physical=%s", mutation, stopErr, current.Run.Status)
			}
		})
	}
}
