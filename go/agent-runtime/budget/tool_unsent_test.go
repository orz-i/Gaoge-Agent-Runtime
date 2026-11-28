package budget_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

func TestPlanUnsentToolDispatchPreservesOriginalReservationAndRevision(t *testing.T) {
	for _, release := range []bool{false, true} {
		t.Run(map[bool]string{false: "park", true: "release"}[release], func(t *testing.T) {
			ctx := t.Context()
			c := budget.Coordinator{Store: budget.NewMemoryLedgerStore()}
			if _, err := c.Ensure(ctx, "scope", budget.Limits{MaxToolCalls: 1}); err != nil {
				t.Fatal(err)
			}
			source, err := c.RegisterRun(ctx, "scope", "run", budget.RunBudget{})
			if err != nil {
				t.Fatal(err)
			}
			q := budget.Reservation{ID: "tool:run:call", RunID: "run", RequestHash: "original-call-digest", Requested: budget.Usage{ToolCalls: 1}}
			plan, err := budget.PlanDispatch(ctx, source, q, true)
			if err != nil {
				t.Fatal(err)
			}
			if err = c.Store.SaveBudget(ctx, plan.Next(), plan.ExpectedRevision()); err != nil {
				t.Fatal(err)
			}
			held := plan.Next()
			if release {
				held, err = c.CancelRoot(ctx, "scope", "run")
				if err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(q)
			proof := featurecontrol.Seal(q.RunID, held.Revision, "agent_tool_not_entered", held.ID, raw, nil)
			foreign := q
			foreign.RequestHash = "other-call"
			if _, err = budget.PlanUnsentToolDispatch(ctx, held, foreign, proof); err == nil {
				t.Fatal("foreign original request accepted")
			}
			if _, err = budget.PlanUnsentToolDispatch(ctx, held, q, nil); err == nil {
				t.Fatal("unproven dispatched reservation reclaimed")
			}
			closedPlan, err := budget.PlanUnsentToolDispatch(ctx, held, q, proof)
			if err != nil {
				t.Fatal(err)
			}
			closed := closedPlan.Next()
			status := budget.ReservationWaiting
			if release {
				status = budget.ReservationReleased
			}
			r := closed.Reservations[q.ID]
			if r.Status != status || r.Active || r.Dispatched || closed.View("").Reserved.ToolCalls != 0 {
				t.Fatal("invalid no-send closure", r)
			}
			if err = c.Store.SaveBudget(ctx, closed, closedPlan.ExpectedRevision()); err != nil {
				t.Fatal(err)
			}
			if _, err = budget.PlanUnsentToolDispatch(ctx, closed, q, proof); err == nil {
				t.Fatal("stale no-entry proof was accepted")
			}
			next, err := budget.PlanDispatch(ctx, closed, q, true)
			if release {
				if err == nil {
					t.Fatal("released call re-admitted")
				}
				return
			}
			if err != nil || !next.Next().Reservations[q.ID].Dispatched {
				t.Fatal("successor reservation not held", err)
			}
			if _, err = budget.PlanUnsentToolDispatch(ctx, next.Next(), q, proof); err == nil {
				t.Fatal("stale no-send observer parked successor")
			}
			if err = c.Store.SaveBudget(ctx, next.Next(), next.ExpectedRevision()); err != nil {
				t.Fatal(err)
			}
			if err = c.Store.SaveBudget(ctx, closed, closedPlan.ExpectedRevision()); !errors.Is(err, budget.ErrLedgerConflict) {
				t.Fatal("stale closure rewrote successor", err)
			}
		})
	}
}

// The actual CAS, not an earlier preview, owns physical admission. Native
// cancellation denies only the exact original undispatched reservation.
func TestPlanDispatchToolExactIdentityAndElectedCancellation(t *testing.T) {
	for _, mode := range []string{"cancel-before-dispatch", "dispatch-before-cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			c := budget.Coordinator{Store: budget.NewMemoryLedgerStore()}
			if _, err := c.Ensure(ctx, "scope", budget.Limits{MaxToolCalls: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := c.RegisterRun(ctx, "scope", "run", budget.RunBudget{}); err != nil {
				t.Fatal(err)
			}
			q := budget.Reservation{ID: "tool:run:call", RunID: "run", RequestHash: "original", Requested: budget.Usage{ToolCalls: 1}}
			held, err := c.Reserve(ctx, "scope", q, true)
			if err != nil {
				t.Fatal(err)
			}
			foreign := q
			foreign.RequestHash = "foreign"
			if rejected, err := budget.PlanDispatch(ctx, held, foreign, true); !errors.Is(err, budget.ErrLedgerConflict) || rejected != nil {
				t.Fatal("foreign dispatch acquired authority", err)
			}
			if mode == "dispatch-before-cancel" {
				dispatched, err := budget.PlanDispatch(ctx, held, q, true)
				if err != nil || !dispatched.Next().Reservations[q.ID].Dispatched {
					t.Fatal("original dispatch failed", err)
				}
				if err = c.Store.SaveBudget(ctx, dispatched.Next(), dispatched.ExpectedRevision()); err != nil {
					t.Fatal(err)
				}
			}
			cancelled, err := c.CancelRoot(ctx, "scope", "run")
			if err != nil {
				t.Fatal(err)
			}
			rejected, err := budget.PlanDispatch(ctx, cancelled, q, true)
			if rejected != nil || !errors.Is(err, budget.ErrLedgerConflict) {
				t.Fatal("denied dispatch created a proposal", err)
			}
			actual, err := c.Store.LoadBudget(ctx, "scope")
			if err != nil || !reflect.DeepEqual(actual, cancelled) {
				t.Fatal("denied dispatch mutated ledger", err)
			}
			res := actual.Reservations[q.ID]
			if mode == "cancel-before-dispatch" {
				if res.Status != budget.ReservationReleased || res.Active || res.Dispatched || res.Usage != (budget.Usage{}) {
					t.Fatal("original unsent denial not retained", res)
				}
			} else if res.Status != budget.ReservationHeld || !res.Dispatched {
				t.Fatal("previous physical dispatch incorrectly certified unsent", res)
			}
		})
	}
}
