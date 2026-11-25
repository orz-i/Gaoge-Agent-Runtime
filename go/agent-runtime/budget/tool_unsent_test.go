package budget_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestResolveUnsentToolPreservesOriginalReservationAndRevision(t *testing.T) {
	for _, release := range []bool{false, true} {
		t.Run(map[bool]string{false: "park", true: "release"}[release], func(t *testing.T) {
			ctx := t.Context()
			c := budget.Coordinator{Store: budget.NewMemoryLedgerStore()}
			if _, err := c.Ensure(ctx, "scope", budget.Limits{MaxToolCalls: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := c.RegisterRun(ctx, "scope", "run", budget.RunBudget{}); err != nil {
				t.Fatal(err)
			}
			q := budget.Reservation{ID: "tool:run:call", RunID: "run", RequestHash: "original-call-digest", Requested: budget.Usage{ToolCalls: 1}}
			held, err := c.Reserve(ctx, "scope", q, true)
			if err != nil {
				t.Fatal(err)
			}
			foreign := q
			foreign.RequestHash = "other-call"
			if _, err = c.ResolveUnsentTool(ctx, "scope", foreign, release, held.Revision); !errors.Is(err, budget.ErrLedgerConflict) {
				t.Fatal("foreign original request accepted", err)
			}
			closed, err := c.ResolveUnsentTool(ctx, "scope", q, release, held.Revision)
			if err != nil {
				t.Fatal(err)
			}
			status := budget.ReservationWaiting
			if release {
				status = budget.ReservationReleased
			}
			r := closed.Reservations[q.ID]
			if r.Status != status || r.Active || r.Dispatched || closed.View("").Reserved.ToolCalls != 0 {
				t.Fatal("invalid no-send closure", r)
			}
			repeat, err := c.ResolveUnsentTool(ctx, "scope", q, release, closed.Revision)
			if err != nil || repeat.Revision != closed.Revision {
				t.Fatal("closure replay rewrote original fact", err, repeat.Revision)
			}
			if release {
				if _, err = c.Reserve(ctx, "scope", q, true); !errors.Is(err, budget.ErrLedgerConflict) {
					t.Fatal("released call re-admitted", err)
				}
				return
			}
			next, err := c.Reserve(ctx, "scope", q, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.ResolveUnsentTool(ctx, "scope", q, false, closed.Revision); !errors.Is(err, budget.ErrLedgerConflict) {
				t.Fatal("stale no-send observer parked successor", err)
			}
			if next.Reservations[q.ID].Status != budget.ReservationHeld || !next.Reservations[q.ID].Active {
				t.Fatal("successor reservation not held", next.Reservations[q.ID])
			}
			dispatched, err := c.Dispatch(ctx, "scope", q.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, release := range []bool{false, true} {
				if _, err = c.ResolveUnsentTool(ctx, "scope", q, release, dispatched.Revision); !errors.Is(err, budget.ErrLedgerConflict) {
					t.Fatal("dispatched reservation reclaimed", err)
				}
			}
		})
	}
}
