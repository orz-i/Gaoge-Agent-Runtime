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
	for _, item := range items {
		if item.ParentItemID == harness.ExecutionDispatchParentItemID {
			t.Fatal("native Start still wrote Host execution grant")
		}
	}
	decision, loadErr := rt.LoadCreationDecision(t.Context(), first.Invocations[0].ExecutionRefID)
	if loadErr != nil || decision.Status != kernel.RunStatusFailed {
		t.Fatalf("missing native decision: %+v %v", decision, loadErr)
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

type pr72NativeDecisionLostACKStore struct {
	*memory.Store
	failed atomic.Bool
}

func (s *pr72NativeDecisionLostACKStore) DecideBeforeCreate(ctx context.Context, q kernel.RunCreationDecision) (kernel.RunCreationDecision, error) {
	d, err := s.Store.DecideBeforeCreate(ctx, q)
	if err == nil && s.failed.CompareAndSwap(false, true) {
		return kernel.RunCreationDecision{}, errors.New("native creation decision ACK lost after COMMIT")
	}
	return d, err
}

func TestPR72NativeAgentNoEffectReceiptLostACKDoesNotInferFailure(t *testing.T) {
	native := &pr72NativeDecisionLostACKStore{Store: memory.NewStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: native, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: direct, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	req := testStartRequest()
	req.RequiredToolKeys = []string{"unauthorized-native-tool"}
	_, startErr := runner.StartAgentTurn(t.Context(), req)
	if !errors.Is(startErr, harness.ErrExecutionOutcomeUnknown) || !native.failed.Load() {
		t.Fatalf("lost decision ACK silently projected: %v", startErr)
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
	owners, err := store.ListInvocations(t.Context(), tid)
	if err != nil || len(owners) != 1 {
		t.Fatalf("original selection lost: %v", err)
	}
	d, err := rt.LoadCreationDecision(t.Context(), owners[0].ExecutionRefID)
	if err != nil || d.Status != kernel.RunStatusFailed {
		t.Fatalf("original decision not persisted: %+v %v", d, err)
	}
	recovered, replayErr := runner.Refresh(t.Context(), tid)
	if replayErr != nil || recovered.Turn.Status != harness.TurnFailed {
		t.Fatalf("read-only decision reconciliation failed: %s %v", recovered.Turn.Status, replayErr)
	}
	for _, item := range recovered.Items {
		if item.ParentItemID == harness.ExecutionDispatchParentItemID {
			t.Fatal("decision replay created a Host dispatch")
		}
	}
}

// Host Run middleware is not a custom Agent implementation: it cannot grant
// the exact native Agent's Start permission to fail after physical effects by
// returning a generic unverified no-effect claim.
type pr72NativeMiddlewarePublicProof struct{ calls atomic.Int32 }

func (*pr72NativeMiddlewarePublicProof) Name() string { return "pr72.native-middleware-public-proof" }
func (m *pr72NativeMiddlewarePublicProof) Run(_ context.Context, _ plugin.RunInvocation, _ plugin.RunNext) (kernel.Snapshot, error) {
	m.calls.Add(1) // Represents an already-issued external side effect.
	return kernel.Snapshot{}, errors.New("host middleware falsely certified zero effects")
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
