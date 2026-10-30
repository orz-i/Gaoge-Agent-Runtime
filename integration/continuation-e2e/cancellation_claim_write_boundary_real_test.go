package continuatione2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var errPGCancellationClaimLostACK = errors.New("real SQL Host Invocation cancellation CAS committed but reply lost")

type pgCancellationClaimLostReplyStore struct {
	harness.Store
	armed        atomic.Bool
	persistedCAS atomic.Int32
}

func (s *pgCancellationClaimLostReplyStore) UpdateInvocation(
	ctx context.Context, value harness.Invocation, expected uint64,
) (harness.Invocation, error) {
	updated, err := s.Store.UpdateInvocation(ctx, value, expected)
	if err == nil && value.Status == harness.InvocationCancelled && s.armed.CompareAndSwap(true, false) {
		s.persistedCAS.Add(1)
		return harness.Invocation{}, errPGCancellationClaimLostACK
	}
	return updated, err
}
func TestRealPostgresCancellationClaimCommittedLostReplyStaysFenced(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN required")
	}
	db, isolated := managedRecoveryDatabase(t, dsn)
	if err := postgresadapter.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, hostStore, runtime := newRealControlHost(t, db, nil)
	req := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "partial-cancel-claim"},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "partial-cancel-claim"},
		Actor:      kernel.ActorRef{TenantID: "test", ActorID: "agent"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "partial-cancel-claim"},
		Goal:       "pause then wait for Host interaction", Config: harness.ConfigSnapshot{Model: "fixture-control"},
		CallAllowance: &agent.CallAllowance{LLMCalls: 1},
	}
	paused, err := first.StartAgentTurn(t.Context(), req)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("fixture did not pause: status=%s err=%v", paused.Turn.Status, err)
	}
	resumed, err := first.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	if err != nil || resumed.Turn.Status != harness.TurnRunning {
		t.Fatalf("fixture did not resume to a valid Running Host Turn: status=%s error=%v", resumed.Turn.Status, err)
	}
	owner, ok := harness.TopLevelInvocation(resumed)
	if !ok {
		t.Fatal("missing root invocation")
	}
	parentItemID := ""
	for _, item := range resumed.Items {
		if item.InvocationID == owner.ID && item.Kind == harness.ItemInvocation {
			parentItemID = item.ID
			break
		}
	}
	if parentItemID == "" {
		t.Fatalf("missing owner Item parent in initial Snapshot: itemCount=%d", len(resumed.Items))
	}
	wrapped := &pgCancellationClaimLostReplyStore{Store: hostStore}
	native, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: realControlResponseModel{},
		CompletionPolicies: []agent.CompletionPolicy{&realControlPauseOnce{}},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: native, Store: wrapped, Clock: managedRecoveryClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := runner.RequestInteraction(t.Context(), paused.Turn.ID, harness.RequestInteraction{
		InvocationID: owner.ID, ParentItemID: parentItemID, Key: "partial-interaction-fence",
		Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil || waiting.Turn.Status != harness.TurnWaitingInput {
		t.Fatalf("couldn't seed real Host Interaction: status=%s err=%v", waiting.Turn.Status, err)
	}
	wrapped.armed.Store(true)
	_, firstErr := runner.Cancel(t.Context(), paused.Turn.ID, "cancel with lost Host CAS ACK")
	if !errors.Is(firstErr, errPGCancellationClaimLostACK) || wrapped.persistedCAS.Load() != 1 {
		t.Fatalf("fault did not commit Host Invocation cancellation CAS before losing ACK: err=%v CAS=%d", firstErr, wrapped.persistedCAS.Load())
	}
	db2 := openManagedRecoveryDB(t, isolated)
	_, store2, rt2 := newRealControlHost(t, db2, nil)
	changed, err := store2.GetInvocation(t.Context(), owner.ID)
	if err != nil || changed.Status != harness.InvocationCancelled {
		t.Fatalf("real SQL Host Invocation not changed: status=%s err=%v", changed.Status, err)
	}
	run, err := rt2.Load(t.Context(), owner.ExecutionRefID)
	if err != nil || run.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("Kernel was modified unexpectedly: run=%s err=%v", run.Run.Status, err)
	}
	items, err := store2.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.ActiveCancellationFence(items) {
		t.Fatalf("UNSAFE: Host InvocationCancelled CAS committed, lost ACK, yet Harness released cancellation fence before Kernel cancellation; Host=%s Kernel=%s", changed.Status, run.Run.Status)
	}
}
