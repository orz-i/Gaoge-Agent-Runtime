package agent_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type initialToolsModel struct {
	t     *testing.T
	keys  []string
	calls int
}

func (stub *initialToolsModel) Generate(_ context.Context, input model.Request) (model.Response, error) {
	stub.calls++
	if len(input.Tools) != len(stub.keys)+1 {
		stub.t.Fatalf("expected %d initially visible tools plus SDK search, received %d", len(stub.keys), len(input.Tools))
	}
	for _, key := range stub.keys {
		if !slices.ContainsFunc(input.Tools, func(item tools.Definition) bool { return item.Key == key }) {
			stub.t.Fatalf("initial tool %q missing from first model request", key)
		}
	}
	if !slices.ContainsFunc(input.Tools, func(item tools.Definition) bool { return item.Key == agent.ToolDiscoveryKey }) {
		stub.t.Fatal("SDK search Tool missing from first model request")
	}
	// The response deliberately calls no tools. Initial does not mean
	// Required; completion should succeed in a single model invocation.
	return model.Response{Content: "no tools needed"}, nil
}

func TestRuntimeToolDiscoveryInitialToolsAreNotMandatory(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	executions := 0
	registry, keys := discoveryTestRegistry(t, 32, &executions)
	initial := []string{keys[0], keys[3]}
	stub := &initialToolsModel{t: t, keys: initial}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: stub, Catalog: registry, Executor: registry,
		ToolDiscovery: agent.LexicalToolDiscovery{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("initial-nonmandatory", "initial-nonmandatory-id", "Answer simply", keys...)
	request.InitialToolKeys = initial
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || stub.calls != 1 || executions != 0 {
		t.Fatalf("initial tools forced execution: run=%+v err=%v modelCalls=%d exec=%d",
			snapshot.Run, err, stub.calls, executions)
	}
	var persistent struct {
		Discovery struct {
			InitialToolKeys []string `json:"initialToolKeys"`
			LoadedKeys      []string `json:"loadedKeys"`
		} `json:"discovery"`
	}
	if err = json.Unmarshal(snapshot.State, &persistent); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(persistent.Discovery.InitialToolKeys, initial) ||
		!slices.Equal(persistent.Discovery.LoadedKeys, initial) {
		t.Fatalf("initial set not frozen and loaded: %+v", persistent.Discovery)
	}
}

func TestRuntimeToolDiscoveryRejectsUnselectedInitialTool(t *testing.T) {
	t.Parallel()
	for _, candidate := range []string{"mcp.outside_grant", agent.ToolDiscoveryKey} {
		runtime, _ := newTestRuntimeAndApprovals(t)
		executions := 0
		registry, keys := discoveryTestRegistry(t, 32, &executions)
		runner, err := agent.NewRunner(agent.Dependencies{
			Runtime: runtime, Model: simpleTextModel{t: t, wantTools: 32},
			Catalog: registry, Executor: registry, ToolDiscovery: agent.LexicalToolDiscovery{},
		})
		if err != nil {
			t.Fatal(err)
		}
		request := startRequest("initial-invalid", "initial-invalid-id", "Answer", keys...)
		request.InitialToolKeys = []string{candidate}
		snapshot, err := runner.StartRun(t.Context(), request)
		if err == nil || snapshot.Run.ID != "" || executions != 0 {
			t.Fatalf("invalid initial key %q was accepted: run=%+v err=%v", candidate, snapshot.Run, err)
		}
	}
}
