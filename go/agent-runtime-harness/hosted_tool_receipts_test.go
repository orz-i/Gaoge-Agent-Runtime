package harness

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestHostedEmptyStatusWithIdentityKeepsQuotaUntilExplicitTerminal(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{
		MaxHostedToolCalls: 1, MaxToolCalls: 1, MaxLLMCalls: 3,
	})
	var fixturePhysical1 model.Request
	_, err := func() (model.Response, error) {
		var admissionErr error
		fixturePhysical1, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(bounded model.Request) (model.Response, error) {
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
				t.Fatalf("initial native allowance was not fenced: %+v", bounded)
			}
			return model.Response{Content: "pending native result", HostedToolCalls: []model.HostedToolCall{{
				ID: "pending-native", ToolKey: "native.search", Status: "",
			}}}, nil
		}(fixturePhysical1)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical1, response, callErr)
	}()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 1 ||
		!view.UnknownUsage || !toolUsageFromLedger(ledger).Incomplete {
		t.Fatalf("empty status was incorrectly paid or released: %+v", view)
	}
	restarted, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	request.InvocationID = "reconcile-native"
	var fixturePhysical2 model.Request
	_, err = func() (model.Response, error) {
		var admissionErr error
		fixturePhysical2, admissionErr = admitModelLedgerFixture(t.Context(), restarted, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(bounded model.Request) (model.Response, error) {
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 0 {
				t.Fatalf("restart turned unknown native effect into new authorization: %+v", bounded)
			}
			return model.Response{Content: "confirmed", HostedToolCalls: []model.HostedToolCall{{
				ID: "pending-native", ToolKey: "native.search", Status: "completed",
			}}}, nil
		}(fixturePhysical2)
		return settleModelLedgerFixture(t.Context(), restarted, fixturePhysical2, response, callErr)
	}()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view = ledger.View("")
	if view.Usage.HostedToolCalls != 1 || view.Reserved.HostedToolCalls != 0 ||
		view.UnknownUsage || toolUsageFromLedger(ledger).Incomplete {
		t.Fatalf("exact terminal receipt failed to reconcile pending native effect: %+v", view)
	}
}

func TestHostedAnonymousUnfinishedStatusKeepsDispatchedModelReservation(t *testing.T) {
	for _, status := range []string{"", "in_progress", "unknown-provider-state"} {
		t.Run(status, func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{
				MaxHostedToolCalls: 1, MaxToolCalls: 1, MaxLLMCalls: 2,
			})
			calls := 0
			var fixturePhysical3 model.Request
			_, err := func() (model.Response, error) {
				var admissionErr error
				fixturePhysical3, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
				if admissionErr != nil {
					return model.Response{}, admissionErr
				}
				response, callErr := func(bounded model.Request) (model.Response, error) {
					calls++
					if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
						t.Fatalf("initial native allowance missing: %+v", bounded)
					}
					return model.Response{Content: "incomplete", HostedToolCalls: []model.HostedToolCall{{
						ToolKey: "native.search", Status: status, // Missing provider-owned Call ID.
					}}}, nil
				}(fixturePhysical3)
				return settleModelLedgerFixture(t.Context(), shared, fixturePhysical3, response, callErr)
			}()
			if !errors.Is(err, budget.ErrInvalidUsage) || calls != 1 {
				t.Fatalf("anonymous unverified native effect was not fenced: err=%v calls=%d", err, calls)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			view := ledger.View("")
			if view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 1 ||
				!view.UnknownUsage || !toolUsageFromLedger(ledger).Incomplete {
				t.Fatalf("unidentified effect silently refunded native quota: %+v", view)
			}
			restarted, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = func() (model.Response, error) {
				_, e := admitModelLedgerFixture(t.Context(), restarted, request)
				return model.Response{}, e
			}()
			if !errors.Is(err, budget.ErrLedgerConflict) || calls != 1 {
				t.Fatalf("unidentified native effect was automatically retried: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestHostedPauseReceiptsHoldAndReconcileAcrossRestart(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 2, MaxLLMCalls: 6})
	call := model.HostedToolCall{ID: "native-pending", ToolKey: "native.search", Status: "in_progress"}
	for step := range 4 {
		request.InvocationID = string(rune('a' + step))
		if step == 1 {
			var err error
			shared, err = NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}})
			if err != nil {
				t.Fatal(err)
			}
		}
		var fixturePhysical4 model.Request
		_, err := func() (model.Response, error) {
			var admissionErr error
			fixturePhysical4, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
			if admissionErr != nil {
				return model.Response{}, admissionErr
			}
			response, callErr := func(bounded model.Request) (model.Response, error) {
				// The in-progress receipt holds the only slot until settled.
				// Continuation of that old execution is not a new allowance.
				want := 0
				if step == 0 {
					want = 1
				}
				if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != want {
					t.Fatalf("step %d allowance=%v", step, bounded.HostedToolCallLimit)
				}
				if step == 1 && len(bounded.Tools) != 1 {
					t.Fatal("a pending native receipt consumed the same slot twice, hiding the authorized local tool")
				}
				if step >= 2 {
					call.Status = "completed"
				}
				return model.Response{Content: "evidence", Continue: step < 2, Usage: &model.Usage{InputTokens: 17, OutputTokens: 3}, HostedToolCalls: []model.HostedToolCall{call}}, nil
			}(fixturePhysical4)
			return settleModelLedgerFixture(t.Context(), shared, fixturePhysical4, response, callErr)
		}()
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
		if err != nil {
			t.Fatal(err)
		}
		view := ledger.View("")
		usage := toolUsageFromLedger(ledger)
		if step < 2 {
			if view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 1 || !view.UnknownUsage || !usage.Incomplete {
				t.Fatalf("pending receipt was lost: %+v %+v", view, usage)
			}
		} else if view.Usage.HostedToolCalls != 1 || view.Reserved.HostedToolCalls != 0 || view.UnknownUsage || usage.Incomplete || usage.HostedCallsByKey["native.search"] != 1 {
			t.Fatalf("completion counted twice or not reconciled: %+v %+v", view, usage)
		}
		if view.Usage.InputTokens != int64((step+1)*17) {
			t.Fatalf("known model tokens were not settled: %+v", view)
		}
		// Reusing a logical invocation returns its durable receipt without dispatch.
		_, err = shared.VerifyModelOutcome(t.Context(), fixturePhysical4)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestHostedUnfinishedReceiptSurvivesTerminalRun(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1})
	var fixturePhysical5 model.Request
	_, err := func() (model.Response, error) {
		var admissionErr error
		fixturePhysical5, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(q model.Request) (model.Response, error) {
			return model.Response{Content: "partial answer", HostedToolCalls: []model.HostedToolCall{{ID: "unresolved", ToolKey: "native.search", Status: "in_progress"}}}, nil
		}(fixturePhysical5)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical5, response, callErr)
	}()
	if err != nil {
		t.Fatal(err)
	}
	request.InvocationID = "final-only"
	zero := 0
	request.HostedToolCallLimit = &zero
	var fixturePhysical6 model.Request
	_, err = func() (model.Response, error) {
		var admissionErr error
		fixturePhysical6, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(bounded model.Request) (model.Response, error) {
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 0 {
				t.Fatal("final-only sampling resumed uncertain native work")
			}
			return model.Response{Content: "final answer with an explicitly unknown native outcome"}, nil
		}(fixturePhysical6)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical6, response, callErr)
	}()
	if err != nil {
		t.Fatal(err)
	}
	coordinator := budget.Coordinator{Store: store}
	ledger, err := coordinator.FinishRuns(t.Context(), "hosted-budget-turn", request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = coordinator.Cancel(t.Context(), ledger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view := ledger.View(""); view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 1 || !view.UnknownUsage || !toolUsageFromLedger(ledger).Incomplete {
		t.Fatalf("terminal/cancel fabricated a completion: %+v", view)
	}
}

// Held receipts authorize completion of the existing effect only. They are
// not another allowance for new provider executions on the next model step.
func TestHostedContinuationHeldReceiptCannotGrantNewNativeCall(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1, MaxLLMCalls: 3})
	var fixturePhysical7 model.Request
	_, err := func() (model.Response, error) {
		var admissionErr error
		fixturePhysical7, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(bounded model.Request) (model.Response, error) {
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
				t.Fatalf("initial new-call allowance: %v", bounded.HostedToolCallLimit)
			}
			return model.Response{Continue: true, HostedToolCalls: []model.HostedToolCall{{ID: "old", ToolKey: "native.search", Status: "in_progress"}}}, nil
		}(fixturePhysical7)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical7, response, callErr)
	}()
	if err != nil {
		t.Fatal(err)
	}
	request.InvocationID = "finish-old"
	var fixturePhysical8 model.Request
	_, err = func() (model.Response, error) {
		var admissionErr error
		fixturePhysical8, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(bounded model.Request) (model.Response, error) {
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 0 {
				t.Fatalf("held receipt became a fresh call permit: %v", bounded.HostedToolCallLimit)
			}
			if len(bounded.HostedTools) != 1 || len(bounded.Tools) != 0 {
				t.Fatalf("native replay was removed or a local call was admitted: %+v", bounded)
			}
			return model.Response{Content: "old complete", HostedToolCalls: []model.HostedToolCall{{ID: "old", ToolKey: "native.search", Status: "completed"}}}, nil
		}(fixturePhysical8)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical8, response, callErr)
	}()
	if err != nil {
		t.Fatal(err)
	}
	ledger, e := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if e != nil {
		t.Fatal(e)
	}
	view := ledger.View("")
	if view.Usage.HostedToolCalls != 1 || view.Reserved.HostedToolCalls != 0 || view.UnknownUsage {
		t.Fatalf("old receipt was not settled exactly once: %+v", view)
	}
}

// Existing held work and newly available quota are independent. Only unused
// quota may be delegated for NEW native executions.
func TestHostedContinuationReservesOnlyFreshCalls(t *testing.T) {
	_, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 3, MaxToolCalls: 3})
	var fixturePhysical9 model.Request
	_, err := func() (model.Response, error) {
		var admissionErr error
		fixturePhysical9, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(bounded model.Request) (model.Response, error) {
			return model.Response{Continue: true, HostedToolCalls: []model.HostedToolCall{
				{ID: "old-1", ToolKey: "native.search", Status: "in_progress"},
				{ID: "old-2", ToolKey: "native.search", Status: "in_progress"},
			}}, nil
		}(fixturePhysical9)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical9, response, callErr)
	}()
	if err != nil {
		t.Fatal(err)
	}
	request.InvocationID = "fresh-only"
	var fixturePhysical10 model.Request
	_, err = func() (model.Response, error) {
		var admissionErr error
		fixturePhysical10, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(bounded model.Request) (model.Response, error) {
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
				t.Fatalf("held calls inflated the new-execution limit: %v", bounded.HostedToolCallLimit)
			}
			return model.Response{Content: "done", HostedToolCalls: []model.HostedToolCall{
				{ID: "old-1", ToolKey: "native.search", Status: "completed"},
				{ID: "old-2", ToolKey: "native.search", Status: "completed"},
				{ID: "new-3", ToolKey: "native.search", Status: "completed"},
			}}, nil
		}(fixturePhysical10)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical10, response, callErr)
	}()
	if err != nil {
		t.Fatal(err)
	}
}

// Provider final frames can contain both an interim and final observation for
// one named Tool. Their relative order must not strand a paid native receipt.
func TestHostedSameResponseTerminalWinsAndReplaysExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name, first, last string
	}{
		{"pending_then_completed", "in_progress", "completed"},
		{"completed_then_pending", "completed", "in_progress"},
		{"running_then_failed", "running", "failed"},
		{"queued_then_cancelled", "queued", "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1, MaxLLMCalls: 3})
			var fixturePhysical11 model.Request
			_, err := func() (model.Response, error) {
				var admissionErr error
				fixturePhysical11, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
				if admissionErr != nil {
					return model.Response{}, admissionErr
				}
				response, callErr := func(bounded model.Request) (model.Response, error) {
					if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
						t.Fatalf("missing bounded initial dispatch: %v", bounded.HostedToolCallLimit)
					}
					return model.Response{Content: "final result", HostedToolCalls: []model.HostedToolCall{
						{ID: "call", ToolKey: "native.search", Status: tc.first},
						{ID: "call", ToolKey: "native.search", Status: tc.last},
					}}, nil
				}(fixturePhysical11)
				return settleModelLedgerFixture(t.Context(), shared, fixturePhysical11, response, callErr)
			}()
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			view := ledger.View("")
			if view.Usage.HostedToolCalls != 1 || view.Reserved.HostedToolCalls != 0 || view.UnknownUsage {
				t.Fatalf("terminal lost / duplicate billed: %+v", view)
			}
			usage := toolUsageFromLedger(ledger)
			if usage.Incomplete || usage.HostedCalls != 1 || usage.HostedCallsByKey["native.search"] != 1 {
				t.Fatalf("host projection disagrees with settled receipt: %+v", usage)
			}
			_, err = shared.VerifyModelOutcome(t.Context(), fixturePhysical11)
			if err != nil {
				t.Fatal(err)
			}
			request.InvocationID = "after-terminal"
			var fixturePhysical12 model.Request
			_, err = func() (model.Response, error) {
				var admissionErr error
				fixturePhysical12, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
				if admissionErr != nil {
					return model.Response{}, admissionErr
				}
				response, callErr := func(bounded model.Request) (model.Response, error) {
					if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 0 {
						t.Fatalf("already-charged tool has gained new authorization: %v", bounded.HostedToolCallLimit)
					}
					return model.Response{Content: "done"}, nil
				}(fixturePhysical12)
				return settleModelLedgerFixture(t.Context(), shared, fixturePhysical12, response, callErr)
			}()
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// In-progress synonyms and unfamiliar nonempty statuses do not prove a
// completion. All consumers must classify them identically.
func TestHostedNonterminalStatusesPreserveUncertainCharges(t *testing.T) {
	for _, status := range []string{"requested", "pending", "in_progress", "running", "queued", "unknown_vendor_state"} {
		t.Run(status, func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1})
			var fixturePhysical13 model.Request
			_, err := func() (model.Response, error) {
				var admissionErr error
				fixturePhysical13, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
				if admissionErr != nil {
					return model.Response{}, admissionErr
				}
				response, callErr := func(q model.Request) (model.Response, error) {
					return model.Response{Content: "still pending", HostedToolCalls: []model.HostedToolCall{
						{ID: "unresolved", ToolKey: "native.search", Status: status},
					}}, nil
				}(fixturePhysical13)
				return settleModelLedgerFixture(t.Context(), shared, fixturePhysical13, response, callErr)
			}()
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			view := ledger.View("")
			usage := toolUsageFromLedger(ledger)
			if view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 1 ||
				!view.UnknownUsage || !usage.Incomplete || usage.HostedCalls != 0 ||
				usage.HostedCallsByKey["native.search"] != 0 || hostedToolItemStatus(status) != ItemStarted {
				t.Fatalf("inconsistent pending status %q: ledger=%+v presentation=%+v timeline=%s",
					status, view, usage, hostedToolItemStatus(status))
			}
		})
	}
}
