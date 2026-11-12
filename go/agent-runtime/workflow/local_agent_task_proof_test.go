package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

type localAgentProofModel struct{}

func (localAgentProofModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "ok"}, nil
}

type localAgentProofExecutor struct {
	original *agent.Runner
	recorder runrelation.Recorder
}

func (e *localAgentProofExecutor) Execute(context.Context, EffectRequest) (EffectResult, error) {
	return EffectResult{}, nil
}
func (e *localAgentProofExecutor) LocalAgentTaskRunner() *agent.Runner { return e.original }
func (e *localAgentProofExecutor) LocalAgentTaskRecorder() runrelation.Recorder {
	return e.recorder
}
func (e *localAgentProofExecutor) VerifyAgentTaskOrigin(effect Effect, child kernel.Snapshot) bool {
	if effect.Kind != "host.agent" || effect.Revision != "v1" {
		return false
	}
	var input struct {
		Goal string `json:"goal"`
	}
	return json.Unmarshal(effect.Input, &input) == nil &&
		strings.TrimSpace(input.Goal) != "" && strings.TrimSpace(input.Goal) == child.Run.Goal
}

type localAgentProofFixture struct {
	runtime  *kernel.Runtime
	recorder *runrelation.Registry
	original *agent.Runner
	executor *localAgentProofExecutor
	runner   *Runner
	parent   kernel.Snapshot
	child    kernel.Snapshot
	relation runrelation.Relation
}

func newLocalAgentProofFixture(t *testing.T) localAgentProofFixture {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	original, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: localAgentProofModel{}})
	if err != nil {
		t.Fatal(err)
	}
	executor := &localAgentProofExecutor{original: original, recorder: recorder}
	router, err := NewStaticEffectRouter(nil, EffectRoute{Class: EffectClassAgent, Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	parent := kernel.Snapshot{Run: kernel.Run{
		ID: "parent", Kind: RunKind, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
	}}
	child := kernel.Snapshot{Run: kernel.Run{
		ID: "effect", Kind: agent.RunKind, ParentRunID: parent.Run.ID, Actor: parent.Run.Actor, Thread: parent.Run.Thread,
		RequestID: "effect", Goal: "approved search", Status: kernel.RunStatusWaitingInput,
	}}
	effect := Effect{
		ID: "effect", ActivationID: "activation", NodeID: "node", Class: EffectClassAgent, Kind: "host.agent", Revision: "v1",
		Attempt: 1, Status: EffectPending, ChildRunID: "effect",
		Input: json.RawMessage(`{"goal":"approved search"}`),
	}
	state := executionState{
		Definition: Definition{Nodes: []Node{{
			ID: "node", Type: NodeAgentTask, AgentTask: &AgentTaskNode{AgentKey: "host.agent", Revision: "v1"},
		}}},
		Activations: []Activation{{ID: "activation", NodeID: "node", EffectID: "effect", EffectIDs: []string{"effect"}, Status: ActivationRunning}},
		Effects:     []Effect{effect},
	}
	parent.State, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return localAgentProofFixture{
		runtime: runtime, recorder: recorder, original: original, executor: executor,
		runner: &Runner{runtime: runtime, relations: recorder, effects: router, registry: &DefinitionRegistry{}},
		parent: parent, child: child,
		relation: runrelation.Relation{
			ParentRunID: "parent", ChildRunID: "effect", Kind: runrelation.KindWorkflowEffect,
			OwnerNodeID: "node",
		},
	}
}

func mutateLocalAgentProofState(t *testing.T, f *localAgentProofFixture, mutate func(*executionState)) {
	t.Helper()
	var state executionState
	if err := json.Unmarshal(f.parent.State, &state); err != nil {
		t.Fatal(err)
	}
	mutate(&state)
	var err error
	f.parent.State, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
}

func TestLocalAgentTaskChildProofRequiresExactOriginalRoute(t *testing.T) {
	base := newLocalAgentProofFixture(t)
	for _, control := range []bool{false, true} {
		if !base.runner.VerifyLocalChild(base.runtime, base.parent, base.relation, base.child, control) {
			t.Fatalf("valid AgentTask denied for control=%v", control)
		}
	}
	if !base.runner.UsesLocalAgentRunner(base.runtime, base.original) {
		t.Fatal("exact original Agent must be eligible for checkpoint approval")
	}
	other, err := agent.NewRunner(agent.Dependencies{Runtime: base.runtime, Model: localAgentProofModel{}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	if base.runner.UsesLocalAgentRunner(base.runtime, other) ||
		base.runner.UsesLocalAgentRunner(foreign, base.original) ||
		base.runner.UsesLocalAgentRunner(base.runtime, nil) {
		t.Fatal("approval runner/kernel identity was substituted")
	}

	for name, change := range map[string]func(*localAgentProofFixture){
		"foreign kernel": func(f *localAgentProofFixture) { f.runner.runtime = foreign },
		"foreign agent runner": func(f *localAgentProofFixture) {
			candidate, err := agent.NewRunner(agent.Dependencies{Runtime: f.runtime, Model: localAgentProofModel{}})
			if err != nil {
				t.Fatal(err)
			}
			f.executor.original = candidate
		},
		"no agent route": func(f *localAgentProofFixture) {
			f.runner.effects = &localAgentProofExecutor{original: f.original, recorder: f.recorder}
		},
		"wrong recorder": func(f *localAgentProofFixture) {
			otherRecorder, err := runrelation.New(memory.NewRunRelationStore(), nil)
			if err != nil {
				t.Fatal(err)
			}
			f.executor.recorder = otherRecorder
		},
		"missing recorder":      func(f *localAgentProofFixture) { f.executor.recorder = nil },
		"missing registry":      func(f *localAgentProofFixture) { f.runner.registry = nil },
		"foreign actor":         func(f *localAgentProofFixture) { f.child.Run.Actor.ActorID = "foreign" },
		"foreign tenant":        func(f *localAgentProofFixture) { f.child.Run.Actor.TenantID = "foreign" },
		"foreign thread":        func(f *localAgentProofFixture) { f.child.Run.Thread.ID = "different" },
		"wrong child kind":      func(f *localAgentProofFixture) { f.child.Run.Kind = "other" },
		"wrong parent kind":     func(f *localAgentProofFixture) { f.parent.Run.Kind = "agent" },
		"wrong relation kind":   func(f *localAgentProofFixture) { f.relation.Kind = runrelation.KindTeamMember },
		"wrong relation parent": func(f *localAgentProofFixture) { f.relation.ParentRunID = "sibling" },
		"wrong relation owner":  func(f *localAgentProofFixture) { f.relation.OwnerNodeID = "other" },
		"wrong child id":        func(f *localAgentProofFixture) { f.child.Run.ID = "foreign" },
		"wrong request id":      func(f *localAgentProofFixture) { f.child.Run.RequestID = "foreign" },
		"missing child goal":    func(f *localAgentProofFixture) { f.child.Run.Goal = "" },
		"forged child goal":     func(f *localAgentProofFixture) { f.child.Run.Goal = "unrelated task" },
		"stale retry attempt": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects[0].Attempt = 2 })
		},
		"stale effect child": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects[0].ChildRunID = "previous" })
		},
		"failed effect": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects[0].Status = EffectFailed })
		},
		"untrusted class": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects[0].Class = EffectClassGeneric })
		},
		"wrong effect kind": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects[0].Kind = "other" })
		},
		"wrong effect revision": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects[0].Revision = "other" })
		},
		"wrong effect node": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects[0].NodeID = "other" })
		},
		"wrong node type": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Definition.Nodes[0].Type = NodeEffect })
		},
		"duplicated effect": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects = append(s.Effects, s.Effects[0]) })
		},
		"duplicated node": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Definition.Nodes = append(s.Definition.Nodes, s.Definition.Nodes[0]) })
		},
		"duplicated activation": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Activations = append(s.Activations, s.Activations[0]) })
		},
		"wrong activation id": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects[0].ActivationID = "foreign" })
		},
		"wrong activation node": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Activations[0].NodeID = "foreign" })
		},
		"wrong activation edge": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Activations[0].EffectIDs[0] = "foreign" })
		},
		"mapped effect": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects[0].Mapped = true })
		},
		"compensation effect": func(f *localAgentProofFixture) {
			mutateLocalAgentProofState(t, f, func(s *executionState) { s.Effects[0].Compensation = true })
		},
		"corrupt parent state": func(f *localAgentProofFixture) { f.parent.State = json.RawMessage(`{`) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newLocalAgentProofFixture(t)
			change(&f)
			if f.runner.VerifyLocalChild(base.runtime, f.parent, f.relation, f.child, false) {
				t.Fatal("unverified ownership accepted for read/approval projection")
			}
		})
	}
}

func TestLocalAgentTaskRetryProofAndUnfencedCancellation(t *testing.T) {
	f := newLocalAgentProofFixture(t)
	mutateLocalAgentProofState(t, &f, func(state *executionState) {
		state.Effects[0].Attempt = 2
		state.Effects[0].ChildRunID = "effect_attempt_2"
	})
	f.child.Run.ID = "effect_attempt_2"
	f.relation.ChildRunID = f.child.Run.ID
	f.relation.OwnerNodeID = f.child.Run.ID
	if !f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, false) {
		t.Fatal("current retry with exact effect and owner identity was denied")
	}
	// A Host callback may outlive Kernel cancellation. It can be observed
	// for approval only when the original physical Agent remains unchanged.
	hosted, err := agent.NewRunner(agent.Dependencies{
		Runtime: f.runtime, Model: localAgentProofModel{},
		NormalizeModelPolicy: func(context.Context, model.Request) (model.PolicyBinding, error) {
			return model.PolicyBinding{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.executor.original = hosted
	static, err := NewStaticEffectRouter(nil, EffectRoute{Class: EffectClassAgent, Executor: f.executor})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.effects = static
	if !f.runner.UsesLocalAgentRunner(f.runtime, hosted) ||
		!f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, false) {
		t.Fatal("original Host-prepared Agent observation was denied")
	}
	if f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, true) {
		t.Fatal("Host callbacks without Kernel cancellation fence granted physical control")
	}
	f.relation.OwnerNodeID = "node"
	if f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, false) {
		t.Fatal("later attempt accepted an earlier owner node")
	}
}

func FuzzLocalAgentTaskChildProofRejectsUntrustedIDs(f *testing.F) {
	for _, s := range []string{"", "effect", "effect_attempt_2", "foreign", strings.Repeat("x", 500), "effect\x00foreign"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, childID string) {
		f := newLocalAgentProofFixture(t)
		if childID == "effect" {
			return
		}
		f.child.Run.ID = childID
		f.relation.ChildRunID = childID
		if f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, false) {
			t.Fatalf("forged child id %q accepted", childID)
		}
	})
}
