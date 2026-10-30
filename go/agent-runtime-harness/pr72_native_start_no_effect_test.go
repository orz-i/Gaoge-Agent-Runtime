package harness_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

func TestPR72NativeAgentPreCreateInputErrorRetiresGrant(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: direct, Store: harness.NewMemoryStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	req := testStartRequest()
	req.RequiredToolKeys = []string{"missing-authorized-tool"}
	first, err := runner.StartAgentTurn(t.Context(), req)
	if !errors.Is(err, agent.ErrInvalidRequest) || errors.Is(err, harness.ErrExecutionOutcomeUnknown) ||
		first.Turn.Status != harness.TurnFailed {
		t.Fatalf("native pre-Create error permanently stranded start: status=%s err=%v", first.Turn.Status, err)
	}
	items, loadErr := runner.ListTimeline(t.Context(), first.Turn.ID, 0, 500)
	if loadErr != nil || harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("no-effect native start retained a physical grant: error=%v", loadErr)
	}
	if _, loadErr = rt.Load(t.Context(), first.Invocations[0].ExecutionRefID); !errors.Is(loadErr, kernel.ErrNotFound) {
		t.Fatalf("native start created a Kernel Run despite invalid Tool: %v", loadErr)
	}
	again, replayErr := runner.StartAgentTurn(t.Context(), req)
	if replayErr != nil || again.Turn.Status != harness.TurnFailed {
		t.Fatalf("idempotent failed no-effect Start replay changed state: %s err=%v", again.Turn.Status, replayErr)
	}
	cancelled, cancelErr := runner.Cancel(t.Context(), first.Turn.ID, "cleanup invalid admission")
	if cancelErr != nil || cancelled.Turn.Status != harness.TurnFailed {
		t.Fatalf("no-effect grant prevented safe terminal response: %s err=%v", cancelled.Turn.Status, cancelErr)
	}
}

type pr72NativeReceiptLostAckStore struct {
	harness.Store
	failed atomic.Bool
}

func (s *pr72NativeReceiptLostAckStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	created, fresh, err := s.Store.AppendItem(ctx, item)
	if err == nil && fresh && item.Kind == harness.ItemDiagnostic && item.Status == harness.ItemCompleted &&
		s.failed.CompareAndSwap(false, true) {
		return harness.Item{}, false, errors.New("audit native Start completion ACK lost after COMMIT")
	}
	return created, fresh, err
}

func TestPR72NativeAgentNoEffectReceiptLostACKDoesNotInferFailure(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	store := &pr72NativeReceiptLostAckStore{Store: harness.NewMemoryStore()}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: direct, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	req := testStartRequest()
	req.RequiredToolKeys = []string{"unauthorized-native-tool"}
	_, startErr := runner.StartAgentTurn(t.Context(), req)
	if !errors.Is(startErr, harness.ErrExecutionOutcomeUnknown) || !store.failed.Load() {
		t.Fatalf("lost completion ACK silently failed or released authorization: %v", startErr)
	}
	sid, err := harness.SessionID(req.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := harness.TurnID(sid, req.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetTurn(t.Context(), tid)
	if err != nil || current.Status != harness.TurnAccepted {
		t.Fatalf("lost ACK was projected as terminal: %s err=%v", current.Status, err)
	}
	// Completion actually committed; the next identical Start may safely
	// prove no effect again, but never infer a failure from the lost ACK alone.
	items, err := runner.ListTimeline(t.Context(), tid, 0, 500)
	if err != nil || harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("persisted completion receipt was absent: %v", err)
	}
	// The first caller did not infer a terminal failure from its lost ACK.
	// Another identical, authorized request may use the durable receipt and
	// the same native pre-Create validation to fail safely without a Run.
	recovered, replayErr := runner.StartAgentTurn(t.Context(), req)
	if !errors.Is(replayErr, agent.ErrInvalidRequest) || recovered.Turn.Status != harness.TurnFailed {
		t.Fatalf("durable no-effect reconciliation failed: status=%s err=%v", recovered.Turn.Status, replayErr)
	}
}

// Host Run middleware is not a custom Agent implementation: it cannot grant
// the exact native Agent's Start permission to fail after physical effects by
// returning the public FeatureStartNoEffect contract.
type pr72NativeMiddlewarePublicProof struct{ calls atomic.Int32 }

func (*pr72NativeMiddlewarePublicProof) Name() string { return "pr72.native-middleware-public-proof" }
func (m *pr72NativeMiddlewarePublicProof) Run(_ context.Context, _ plugin.RunInvocation, _ plugin.RunNext) (kernel.Snapshot, error) {
	m.calls.Add(1) // Represents an already-issued external side effect.
	return kernel.Snapshot{}, harness.FeatureStartNoEffect(errors.New("host middleware falsely certified zero effects"))
}

func TestPR72NativeAgentMiddlewareCannotForgePublicStartNoEffect(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	middleware := &pr72NativeMiddlewarePublicProof{}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: directModel{}, RunMiddleware: []plugin.RunMiddleware{middleware},
	})
	if err != nil {
		t.Fatal(err)
	}
	backing := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: direct, Store: backing, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	_, startErr := runner.StartAgentTurn(t.Context(), request)
	if !errors.Is(startErr, harness.ErrExecutionOutcomeUnknown) || middleware.calls.Load() != 1 {
		t.Fatalf("middleware public proof discharged native Agent: err=%v calls=%d", startErr, middleware.calls.Load())
	}
	sid, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := harness.TurnID(sid, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := backing.GetTurn(t.Context(), tid)
	if err != nil || turn.Status != harness.TurnAccepted {
		t.Fatalf("unknown native middleware effect was terminalized: status=%s err=%v", turn.Status, err)
	}
	items, err := runner.ListTimeline(t.Context(), tid, 0, 500)
	if err != nil || !harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("middleware discharged unknown dispatch: err=%v", err)
	}
	_, cancelErr := runner.Cancel(t.Context(), tid, "cancel unverified native middleware")
	if !errors.Is(cancelErr, harness.ErrConflict) {
		t.Fatalf("operator cancellation escaped unknown native middleware effect: %v", cancelErr)
	}
}
