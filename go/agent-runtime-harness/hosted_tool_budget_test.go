package harness

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type hostedBudgetClock struct{}

func (hostedBudgetClock) Now() time.Time { return time.Now() }

func hostedBudgetFixture(t *testing.T, limits budget.Limits) (*MemoryStore, *BudgetAuthority, model.Request) {
	t.Helper()
	store := NewMemoryStore()
	now := time.Now().UTC()
	config, err := SealConfigSnapshot("hosted-budget-turn", ConfigSnapshot{SharedBudget: &limits}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), Turn{ID: config.TurnID, SessionID: "session", HostTurn: HostRef{Kind: "test", ID: "turn"}, ConfigSnapshotID: config.ID, Status: TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateInvocation(t.Context(), Invocation{ID: "hosted-invocation", TurnID: config.TurnID, ExecutionRefID: "hosted-run", Status: InvocationRunning, Revision: 1, Attempt: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	shared, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	return store, shared, model.Request{RunID: "hosted-run", InvocationID: "search-1", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("research")}}, HostedTools: []model.HostedTool{{Key: "native.search"}}, Tools: []tools.Definition{{Key: "local.read", Name: "read"}}}
}

// bindHostedBudgetAuthority keeps the fixture's Host projection store while
// binding native accounting to the exact Kernel Store used by the Agent.
func bindHostedBudgetAuthority(t testing.TB, runtime *kernel.Runtime, authority *BudgetAuthority, meter ...model.TokenAdmissionPlanner) *BudgetAuthority {
	t.Helper()
	dependencies := authority.dependencies
	dependencies.Runtime = runtime
	dependencies.Ledgers = nil
	if len(meter) != 0 {
		dependencies.Meter = meter[0]
	}
	bound, err := NewBudgetAuthority(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

// admitModelLedgerFixture creates only the original persisted dispatch fact.
// It is a ledger-reducer fixture, not a Provider dispatch path.
func admitModelLedgerFixture(ctx context.Context, shared *BudgetAuthority, original model.Request) (model.Request, error) {
	ledger, err := shared.bindRun(ctx, original.RunID)
	if err != nil {
		return model.Request{}, err
	}
	physical, reservation, err := shared.modelReservation(ctx, ledger, original)
	if err != nil {
		return physical, err
	}
	reservation.PhysicalRequestHash, err = model.PhysicalRequestHash(physical)
	if err != nil {
		return physical, err
	}
	mutation, err := budget.PlanDispatch(ctx, ledger, reservation, true)
	if err != nil {
		return physical, err
	}
	return physical, shared.dependencies.Ledgers.SaveBudget(ctx, mutation.Next(), ledger.Revision)
}

func settleModelLedgerFixture(ctx context.Context, shared *BudgetAuthority, physical model.Request, receipt model.Response, callErr error) (model.Response, error) {
	ctx = context.WithoutCancel(ctx)
	ledger, err := shared.bindRun(ctx, physical.RunID)
	if err != nil {
		return model.Response{}, err
	}
	saved, err := shared.coordinator().SettleModel(ctx, ledger.ID, physical, receipt, callErr)
	if err != nil {
		_, suspendErr := shared.coordinator().Suspend(ctx, ledger.ID, "model:"+physical.InvocationID)
		return model.Response{}, errors.Join(callErr, err, suspendErr)
	}
	result, err := budget.DecodeModelReceipt(saved.Reservations["model:"+physical.InvocationID])
	return result, errors.Join(callErr, err)
}

func TestHostedBudgetExhaustionStillAllowsFinalModelResponse(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 3, MaxToolCalls: 3, MaxLLMCalls: 8})
	for index, want := range []int{3, 0, 0} {
		request.InvocationID = fmt.Sprintf("search-%d", index)
		var fixturePhysical1 model.Request
		_, err := func() (model.Response, error) {
			var admissionErr error
			fixturePhysical1, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
			if admissionErr != nil {
				return model.Response{}, admissionErr
			}
			response, callErr := func(bounded model.Request) (model.Response, error) {
				if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != want {
					t.Fatalf("ceiling=%v want=%d", bounded.HostedToolCallLimit, want)
				}
				response := model.Response{Content: "final evidence"}
				for call := range want {
					response.HostedToolCalls = append(response.HostedToolCalls, model.HostedToolCall{ID: fmt.Sprintf("%d-%d", index, call), ToolKey: "native.search", Status: "completed"})
				}
				if want == 0 && (len(bounded.HostedTools) != 1 || len(bounded.Tools) != 0 || bounded.RequireToolCall) {
					t.Fatalf("exhausted tools remain: %+v", bounded)
				}
				return response, nil
			}(fixturePhysical1)
			return settleModelLedgerFixture(t.Context(), shared, fixturePhysical1, response, callErr)
		}()
		if err != nil {
			t.Fatal(err)
		}
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.Usage.HostedToolCalls != 3 || view.Usage.ToolCalls != 3 || view.Usage.LLMCalls != 3 || view.Reserved.HostedToolCalls != 0 {
		t.Fatalf("usage=%+v", view)
	}
}

func TestHostedReservationLeavesNoLocalToolsInLastSharedSlot(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1, MaxLLMCalls: 3})
	request.RequireToolCall = true
	for index, want := range []int{1, 0} {
		request.InvocationID = fmt.Sprintf("last-slot-%d", index)
		var fixturePhysical2 model.Request
		_, err := func() (model.Response, error) {
			var admissionErr error
			fixturePhysical2, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
			if admissionErr != nil {
				return model.Response{}, admissionErr
			}
			response, callErr := func(bounded model.Request) (model.Response, error) {
				if len(bounded.Tools) != 0 || bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != want {
					t.Fatalf("native search and local tool can compete for last slot: %+v", bounded)
				}
				response := model.Response{Content: "completed from existing evidence"}
				if want > 0 {
					response.HostedToolCalls = []model.HostedToolCall{{ID: "last-search", ToolKey: "native.search", Status: "completed"}}
				}
				return response, nil
			}(fixturePhysical2)
			return settleModelLedgerFixture(t.Context(), shared, fixturePhysical2, response, callErr)
		}()
		if err != nil {
			t.Fatal(err)
		}
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.Usage.ToolCalls != 1 || view.Usage.HostedToolCalls != 1 || view.Usage.LLMCalls != 2 || view.Reserved.ToolCalls != 0 {
		t.Fatalf("final model response could not complete within quota: %+v", view)
	}
	if len(request.Messages) != 1 {
		t.Fatal("frozen original request was mutated")
	}
}

func TestHostedBudgetUnknownReceiptHoldsQuotaAcrossRestart(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 2, MaxToolCalls: 4})
	var fixturePhysical3 model.Request
	_, err := func() (model.Response, error) {
		var admissionErr error
		fixturePhysical3, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(q model.Request) (model.Response, error) {
			return model.Response{}, context.DeadlineExceeded
		}(fixturePhysical3)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical3, response, callErr)
	}()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	restarted, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = func() (model.Response, error) {
		_, e := admitModelLedgerFixture(t.Context(), restarted, request)
		return model.Response{}, e
	}()
	if !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("restart=%v", err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if view := ledger.View(""); !view.UnknownUsage || view.Reserved.HostedToolCalls != 2 || view.Reserved.ToolCalls != 2 {
		t.Fatalf("lost reservation=%+v", view)
	}
}

func TestHostedBudgetConcurrentRequestsCannotOverReserve(t *testing.T) {
	store, shared, original := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 3, MaxToolCalls: 3})
	var group sync.WaitGroup
	for index := range 3 {
		group.Go(func() {
			request := model.CloneRequest(original)
			request.InvocationID = fmt.Sprintf("parallel-%d", index)
			var fixturePhysical4 model.Request
			_, err := func() (model.Response, error) {
				var admissionErr error
				fixturePhysical4, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
				if admissionErr != nil {
					return model.Response{}, admissionErr
				}
				response, callErr := func(bounded model.Request) (model.Response, error) {
					response := model.Response{Content: "evidence"}
					for call := range *bounded.HostedToolCallLimit {
						response.HostedToolCalls = append(response.HostedToolCalls, model.HostedToolCall{ID: fmt.Sprintf("%d-%d", index, call), ToolKey: "native.search", Status: "completed"})
					}
					return response, nil
				}(fixturePhysical4)
				return settleModelLedgerFixture(t.Context(), shared, fixturePhysical4, response, callErr)
			}()
			if err != nil && !errors.Is(err, budget.ErrLedgerConflict) && !errors.Is(err, budget.ErrExhausted) {
				t.Errorf("admission=%v", err)
			}
		})
	}
	group.Wait()
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.Usage.HostedToolCalls+view.Reserved.HostedToolCalls > 3 || view.Usage.ToolCalls+view.Reserved.ToolCalls > 3 {
		t.Fatalf("overspent=%+v", view)
	}
}

func TestHostedBudgetIsOptIn(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{})
	var fixturePhysical5 model.Request
	_, err := func() (model.Response, error) {
		var admissionErr error
		fixturePhysical5, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(bounded model.Request) (model.Response, error) {
			if bounded.HostedToolCallLimit != nil || len(bounded.HostedTools) != 1 {
				t.Fatalf("implicit system ceiling: %+v", bounded)
			}
			return model.Response{Content: "unchanged", HostedToolCalls: []model.HostedToolCall{{ID: "unlimited-search", ToolKey: "native.search", Status: "completed"}}}, nil
		}(fixturePhysical5)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical5, response, callErr)
	}()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if usage := ledger.View("").Usage; usage.ToolCalls != 1 || usage.HostedToolCalls != 1 {
		t.Fatalf("unlimited native execution must still be accounted: %+v", usage)
	}
}

// The shared Tool ceiling is sufficient by itself. A hosted-only ceiling is
// optional, not a prerequisite for native execution admission.
func TestHostedSharedToolLimitBoundsNativeCallsWithoutHostedLimit(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxToolCalls: 1, MaxLLMCalls: 3})
	request.RequireToolCall = true
	for step := range 2 {
		request.InvocationID = fmt.Sprintf("total-only-%d", step)
		var fixturePhysical6 model.Request
		_, err := func() (model.Response, error) {
			var admissionErr error
			fixturePhysical6, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
			if admissionErr != nil {
				return model.Response{}, admissionErr
			}
			response, callErr := func(bounded model.Request) (model.Response, error) {
				want := 1 - step
				if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != want {
					t.Fatalf("step %d failed to bound native dispatch under shared tool limit: %v", step, bounded.HostedToolCallLimit)
				}
				if len(bounded.Tools) != 0 || bounded.RequireToolCall != (want > 0) {
					t.Fatalf("step %d changed tool visibility or required native call: %+v", step, bounded)
				}
				answer := model.Response{Content: "done"}
				if step == 0 {
					answer.HostedToolCalls = []model.HostedToolCall{{ID: "one", ToolKey: "native.search", Status: "completed"}}
				}
				return answer, nil
			}(fixturePhysical6)
			return settleModelLedgerFixture(t.Context(), shared, fixturePhysical6, response, callErr)
		}()
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if usage := ledger.View("").Usage; usage.HostedToolCalls != 1 || usage.ToolCalls != 1 {
		t.Fatalf("shared tool budget was overspent: %+v", usage)
	}
}

// A completed provider response that violates the host's limit is accounted
// truthfully but MUST be rejected, never reported as an admitted success.
func TestHostedSharedToolLimitRejectsOverrunAfterAuthoritativeReceipt(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxToolCalls: 1})
	var fixturePhysical7 model.Request
	_, err := func() (model.Response, error) {
		var admissionErr error
		fixturePhysical7, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(bounded model.Request) (model.Response, error) {
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
				t.Fatalf("missing dispatch limit: %v", bounded.HostedToolCallLimit)
			}
			return model.Response{Content: "unexpected extra hosted execution", HostedToolCalls: []model.HostedToolCall{
				{ID: "a", ToolKey: "native.search", Status: "completed"},
				{ID: "b", ToolKey: "native.search", Status: "completed"},
			}}, nil
		}(fixturePhysical7)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical7, response, callErr)
	}()
	if !errors.Is(err, budget.ErrInvalidUsage) {
		t.Fatalf("unreported provider overrun: %v", err)
	}
	ledger, e := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if e != nil {
		t.Fatal(e)
	}
	if got := ledger.View("").Usage.ToolCalls; got != 2 {
		t.Fatalf("authoritative charges were erased: %d", got)
	}
}
