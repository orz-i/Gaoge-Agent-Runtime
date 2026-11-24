package harness_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type manualTerminalReplayModel struct {
	parentStarted chan struct{}
	parentRelease chan struct{}
	childCalls    atomic.Int32
}

func (m *manualTerminalReplayModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	if strings.HasPrefix(request.RunID, "hchild_") {
		m.childCalls.Add(1)
		return model.Response{Content: "original specialist terminal result"}, nil
	}
	select {
	case m.parentStarted <- struct{}{}:
	default:
	}
	select {
	case <-m.parentRelease:
		return model.Response{Content: "root completed"}, nil
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
}

// A second manual read of the SAME completed Delegation is allowed only
// because its original immutable child, grant-return and terminal Item are
// already proven. This must not reopen the original physical Start.
func TestPR113VerifiedTerminalManualDelegationIsIdempotentReadOnly(t *testing.T) {
	m := &manualTerminalReplayModel{parentStarted: make(chan struct{}, 1), parentRelease: make(chan struct{})}
	runner, _ := newDelegationHarnessWithModel(t, m, 4)
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "terminal-read-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "terminal-read-turn"}
	outcome := make(chan error, 1)
	go func() {
		_, err := runner.StartAgentTurn(t.Context(), harness.StartRequest{
			HostThread: hostThread, HostTurn: hostTurn,
			Actor:  kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread: kernel.ThreadRef{Kind: testThreadKind, ID: "terminal-read-thread"},
			Goal:   "keep root active while manual child completes",
			Config: harness.ConfigSnapshot{
				ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()}, AgentSpec: harness.AgentSpec{Model: delegationTestModelName, ToolKeys: []string{harness.DelegationToolKey}},
			},
		})
		outcome <- err
	}()
	release := sync.OnceFunc(func() { close(m.parentRelease) })
	defer release()
	select {
	case <-m.parentStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("parent model did not start")
	}
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	request := harness.DelegateRequest{MemberID: "analyst", Goal: "review evidence"}
	first, err := runner.Delegate(t.Context(), turnID, request)
	if err != nil || first.Delegation.Status != "completed" {
		t.Fatalf("initial delegation did not complete: %+v err=%v", first.Delegation, err)
	}
	for i := 0; i < 2; i++ {
		second, err := runner.Delegate(t.Context(), turnID, request)
		if err != nil || second.Delegation.ChildRunID != first.Delegation.ChildRunID || second.Delegation.Status != "completed" ||
			string(second.Delegation.Result) != string(first.Delegation.Result) {
			t.Fatalf("read-only original terminal result was rejected: %+v %v", second.Delegation, err)
		}
	}
	if calls := m.childCalls.Load(); calls != 1 {
		t.Fatalf("original specialist reexecuted %d times", calls)
	}
	release()
	select {
	case err := <-outcome:
		if err != nil {
			t.Fatalf("original root failed to finish after child: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("original root did not finish")
	}
}
