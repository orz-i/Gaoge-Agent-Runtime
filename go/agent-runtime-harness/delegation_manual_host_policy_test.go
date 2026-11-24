package harness_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A model Tool policy must also protect the HTTP/manual entry; no separately
// optional manual policy may turn an unapproved MemberID into a physical Run.
func TestManualDelegationUsesExactHostNamedAgentPolicy(t *testing.T) {
	model := &manualTerminalReplayModel{parentStarted: make(chan struct{}, 1), parentRelease: make(chan struct{})}
	runner, _ := newDelegationHarnessWithModel(t, model, 4, hostSelectedAgentPolicy{MemberID: "approved", MemberRevision: "v7"})
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "host-auth-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "host-auth-turn"}
	rootDone := make(chan error, 1)
	go func() {
		_, err := runner.StartAgentTurn(t.Context(), harness.StartRequest{
			HostThread: hostThread, HostTurn: hostTurn,
			Actor:  kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread: kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID},
			Goal:   "keep parent active for Host manual delegation",
			Config: harness.ConfigSnapshot{ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()}, AgentSpec: harness.AgentSpec{Model: delegationTestModelName, ToolKeys: []string{harness.DelegationToolKey}}},
		})
		rootDone <- err
	}()
	release := sync.OnceFunc(func() { close(model.parentRelease) })
	defer release()
	select {
	case <-model.parentStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("root not running")
	}
	sid, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sid, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Delegate(t.Context(), turnID, harness.DelegateRequest{MemberID: "forbidden", Goal: "unapproved"}); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("manual entry bypassed Host member selection: %v", err)
	}
	if model.childCalls.Load() != 0 {
		t.Fatalf("unapproved child executed %d Model calls", model.childCalls.Load())
	}
	before, err := runner.Load(t.Context(), turnID)
	if err != nil || len(before.Subtasks) != 0 {
		t.Fatalf("unapproved child left topology: %+v err=%v", before.Subtasks, err)
	}
	approved, err := runner.Delegate(t.Context(), turnID, harness.DelegateRequest{MemberID: "approved", Goal: "authorized work"})
	if err != nil || approved.Delegation.Status != "completed" || approved.Delegation.MemberRevision != "v7" || model.childCalls.Load() != 1 {
		t.Fatalf("Host-approved member not executed: %+v err=%v calls=%d", approved.Delegation, err, model.childCalls.Load())
	}
	release()
	select {
	case err = <-rootDone:
		if err != nil {
			t.Fatalf("root failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("root did not finish")
	}
}
