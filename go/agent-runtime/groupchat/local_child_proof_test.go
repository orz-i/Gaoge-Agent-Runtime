package groupchat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

type proofChildRunner struct{ runtime *kernel.Runtime }

func (r proofChildRunner) OwnsKernelRuntime(runtime *kernel.Runtime) bool {
	return r.runtime == runtime
}
func (proofChildRunner) LoadRun(context.Context, string) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrNotFound
}
func (proofChildRunner) StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrNotFound
}

func TestVerifyLocalChildRequiresRouteAndFrozenIdentity(t *testing.T) {
	runtime := &kernel.Runtime{}
	fixed, err := handoff.New(proofChildRunner{runtime})
	if err != nil {
		t.Fatal(err)
	}
	d := handoff.Delegation{ID: "delegation", MemberID: "member", ChildRunID: "child", Goal: "child goal", Status: handoff.StatusRunning}
	state, err := json.Marshal(executionState{Speakers: []speakerExecution{{Delegation: d}}})
	if err != nil {
		t.Fatal(err)
	}
	parent := kernel.Snapshot{Run: kernel.Run{ID: "parent", Kind: RunKind, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}}, State: state}
	child := kernel.Snapshot{Run: kernel.Run{ID: "child", Kind: agent.RunKind, Actor: parent.Run.Actor, Thread: parent.Run.Thread, RequestID: "parent:delegation", Goal: "child goal", Status: kernel.RunStatusRunning}}
	relation := runrelation.Relation{ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.KindGroupChatSpeaker, OwnerNodeID: "delegation"}
	runner := &Runner{runtime: runtime, handoffs: fixed}
	for _, control := range []bool{false, true} {
		if !runner.VerifyLocalChild(runtime, parent, relation, child, control) {
			t.Fatalf("valid local proof rejected: control=%v", control)
		}
		foreign := child
		foreign.Run.Actor.TenantID = "foreign"
		if runner.VerifyLocalChild(runtime, parent, relation, foreign, control) {
			t.Fatal("foreign task accepted")
		}
		wrongOwner := relation
		wrongOwner.OwnerNodeID = "other"
		if runner.VerifyLocalChild(runtime, parent, wrongOwner, child, control) {
			t.Fatal("wrong relation owner accepted")
		}
		if runner.VerifyLocalChild(&kernel.Runtime{}, parent, relation, child, control) {
			t.Fatal("different runtime accepted")
		}
	}
	routed, err := handoff.NewRouted(handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) {
		return proofChildRunner{runtime}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	runner.handoffs = routed
	for _, control := range []bool{false, true} {
		if runner.VerifyLocalChild(runtime, parent, relation, child, control) {
			t.Fatal("dynamic route accepted identical local twin")
		}
	}
	remote, err := handoff.New(proofChildRunner{&kernel.Runtime{}})
	if err != nil {
		t.Fatal(err)
	}
	runner.handoffs = remote
	if runner.VerifyLocalChild(runtime, parent, relation, child, false) || runner.VerifyLocalChild(runtime, parent, relation, child, true) {
		t.Fatal("remote route accepted identical local twin")
	}
}
