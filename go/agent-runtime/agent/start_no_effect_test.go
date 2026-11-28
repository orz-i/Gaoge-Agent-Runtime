package agent_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type pr72ForgedStartNoEffectPreparer struct {
	runner **agent.Runner
	calls  atomic.Int32
}

func (m *pr72ForgedStartNoEffectPreparer) Prepare(ctx context.Context, _ model.Request) (model.Request, error) {
	m.calls.Add(1)
	_, err := (*m.runner).StartRun(ctx, agent.StartRequest{ID: "nested-invalid-run", Goal: "nested", RequiredToolKeys: []string{"missing"}})
	return model.Request{}, err
}

func TestPR72NativeStartPreCreateFailureIsDirectAndHasNoRun(t *testing.T) {
	base := memory.NewStore()
	rt, err := kernel.New(kernel.Dependencies{Store: base})
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &nativeLeaseFixtureModel{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: modelClient})
	if err != nil {
		t.Fatal(err)
	}
	request := agent.StartRequest{
		ID: "pr72-invalid-start", Goal: "invalid mandatory tool",
		Actor:            kernel.ActorRef{TenantID: "t", ActorID: "actor"},
		Thread:           kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		RequiredToolKeys: []string{"tool.not.authorized"},
	}
	_, err = runner.StartRun(t.Context(), request)
	if !errors.Is(err, agent.ErrInvalidRequest) || !agent.IsNativeStartNoEffect(err) {
		t.Fatalf("native definite pre-Kernel failure must keep original cause and direct proof: %v", err)
	}
	if _, err = rt.Load(t.Context(), request.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("native preflight unexpectedly created a Kernel Run: %v", err)
	}
	if modelClient.calls != 0 {
		t.Fatalf("native preflight called Model %d times", modelClient.calls)
	}
	if agent.IsNativeStartNoEffect(errors.Join(agent.ErrInvalidRequest, errors.New("forged"))) ||
		agent.IsNativeStartNoEffect(errors.Join(err, errors.New("wrapped native proof"))) {
		t.Fatal("wrapped error obtained direct native pre-create authority")
	}
}

func TestPR72NativeStartPreparerCannotForwardNestedNoEffectAuthority(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	var runner *agent.Runner
	preparer := &pr72ForgedStartNoEffectPreparer{runner: &runner}
	runner, err = agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: &nativeLeaseFixtureModel{}, PrepareModelRequest: preparer.Prepare,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartRun(t.Context(), agent.StartRequest{
		ID: "pr72-preparer-outer", Goal: "outer", Actor: kernel.ActorRef{TenantID: "t", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
	})
	if !errors.Is(err, agent.ErrInvalidRequest) || agent.IsNativeStartNoEffect(err) || preparer.calls.Load() != 1 {
		t.Fatalf("Preparer forwarded nested preflight proof: calls=%d err=%v", preparer.calls.Load(), err)
	}
}

func TestPR72NativeStartCommittedCreateLostReplyIsNotNoEffect(t *testing.T) {
	base := memory.NewStore()
	wrapped := &lostCreateResponseStore{Store: base, ExecutionStore: base}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: &nativeLeaseFixtureModel{}})
	if err != nil {
		t.Fatal(err)
	}
	request := agent.StartRequest{
		ID: "pr72-commit-ack-lost", Goal: "must not fake no effects",
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
	}
	_, err = runner.StartRun(t.Context(), request)
	if !errors.Is(err, errLostCreateCommitResponse) || agent.IsNativeStartNoEffect(err) {
		t.Fatalf("uncertain Kernel Store Create was misclassified as native no-effect: %v", err)
	}
	if snapshot, loadErr := rt.Load(t.Context(), request.ID); loadErr != nil || snapshot.Run.ID != request.ID {
		t.Fatalf("lost ACK concealed committed Kernel Create: %+v err=%v", snapshot.Run, loadErr)
	}
}

// The native proof is minted before the preparer is entered. A nested
// rejection cannot certify that the enclosing, already-created Run had no effect.
func TestPR72NativeStartInvalidInputNeverEntersPreparer(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	var runner *agent.Runner
	preparer := &pr72ForgedStartNoEffectPreparer{runner: &runner}
	runner, err = agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: &nativeLeaseFixtureModel{}, PrepareModelRequest: preparer.Prepare,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartRun(t.Context(), agent.StartRequest{
		ID: "pr72-invalid-before-any-preparer", Goal: "missing required local tool",
		Actor:            kernel.ActorRef{TenantID: "t", ActorID: "actor"},
		Thread:           kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		RequiredToolKeys: []string{"tool.not.authorized"},
	})
	if !errors.Is(err, agent.ErrInvalidRequest) || !agent.IsNativeStartNoEffect(err) || preparer.calls.Load() != 0 {
		t.Fatalf("preflight entered potentially effectful preparer: calls=%d err=%v", preparer.calls.Load(), err)
	}
}
