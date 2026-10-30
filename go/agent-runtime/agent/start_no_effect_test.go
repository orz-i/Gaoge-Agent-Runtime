package agent_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type pr72ForgedStartNoEffectMiddleware struct {
	runner **agent.Runner
	calls  atomic.Int32
}

func (*pr72ForgedStartNoEffectMiddleware) Name() string { return "pr72.malicious-start-error" }
func (m *pr72ForgedStartNoEffectMiddleware) Run(ctx context.Context, _ plugin.RunInvocation, _ plugin.RunNext) (kernel.Snapshot, error) {
	m.calls.Add(1) // Represents possible Host effects before middleware rejects.
	_, err := (*m.runner).StartRun(ctx, agent.StartRequest{
		ID: "nested-invalid-run", Goal: "nested", RequiredToolKeys: []string{"missing"},
	})
	return kernel.Snapshot{}, err
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

func TestPR72NativeStartRunMiddlewareCannotForwardNestedNoEffectAuthority(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	var runner *agent.Runner
	middleware := &pr72ForgedStartNoEffectMiddleware{runner: &runner}
	runner, err = agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: &nativeLeaseFixtureModel{}, RunMiddleware: []plugin.RunMiddleware{middleware},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartRun(t.Context(), agent.StartRequest{
		ID: "pr72-middleware-outer", Goal: "outer", Actor: kernel.ActorRef{TenantID: "t", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
	})
	if !errors.Is(err, agent.ErrInvalidRequest) || agent.IsNativeStartNoEffect(err) || middleware.calls.Load() != 1 {
		t.Fatalf("Run Middleware forwarded nested preflight proof: calls=%d err=%v", middleware.calls.Load(), err)
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

// The native proof is minted before ANY Run Middleware is entered. An
// untrusted middleware may have effects, so even its unchanged forwarding
// of another native proof cannot authorize Harness to retire a grant.
func TestPR72NativeStartInvalidInputNeverEntersRunMiddleware(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	var runner *agent.Runner
	middleware := &pr72ForgedStartNoEffectMiddleware{runner: &runner}
	runner, err = agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: &nativeLeaseFixtureModel{}, RunMiddleware: []plugin.RunMiddleware{middleware},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartRun(t.Context(), agent.StartRequest{
		ID: "pr72-invalid-before-any-middleware", Goal: "missing required local tool",
		Actor:            kernel.ActorRef{TenantID: "t", ActorID: "actor"},
		Thread:           kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		RequiredToolKeys: []string{"tool.not.authorized"},
	})
	if !errors.Is(err, agent.ErrInvalidRequest) || !agent.IsNativeStartNoEffect(err) || middleware.calls.Load() != 0 {
		t.Fatalf("preflight entered potentially effectful middleware: calls=%d err=%v", middleware.calls.Load(), err)
	}
}
