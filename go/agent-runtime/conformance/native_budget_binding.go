package conformance

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func runNativeBudgetBinding(t *testing.T, factory func(*testing.T) ExecutionBudgetStore) {
	for _, mode := range []string{"create-retry", "capacity", "foreign", "cancel", "expire", "unknown-expire", "complete"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f := factory(t)
			rt := ea1StartRuntime(t, f.Store)
			limits := budget.Limits{MaxChildRuns: 1, MaxToolCalls: 2}
			newRequest := func(id, scope, parent string) kernel.CreateRequest {
				return kernel.CreateRequest{ID: id, Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "test", State: json.RawMessage("{}"), ExecutionRequired: true, Budget: &budget.RunEnrollment{ScopeID: scope, Limits: limits, Binding: budget.RunBudget{ParentRunID: parent}}}
			}
			load := func(scope string) budget.Ledger {
				t.Helper()
				l, e := f.Ledger.LoadBudget(ctx, scope)
				if e != nil {
					t.Fatal(e)
				}
				return l
			}
			req := newRequest("root", "scope", "")
			if mode == "expire" || mode == "unknown-expire" {
				// Expiry can be exercised immediately, without wall-clock sleeps,
				// using an already-due persisted deadline from an older writer.
				now := time.Now().UTC().Add(-time.Minute)
				_, e := f.Store.Create(ctx, kernel.Record{ExecutionRequired: true, BudgetEnrollment: req.Budget, Run: kernel.Run{ID: req.ID, Kind: req.Kind, Actor: req.Actor, Thread: req.Thread, Goal: req.Goal, Status: kernel.RunStatusRunning, Revision: 1, CreatedAt: now, UpdatedAt: now, DeadlineAt: &now, BudgetScopeID: "scope"}, State: req.State}, nil)
				if e != nil {
					t.Fatal(e)
				}
			} else if _, e := rt.Create(ctx, req); e != nil {
				t.Fatal(e)
			}
			root, e := rt.Load(ctx, "root")
			if e != nil || root.Run.BudgetScopeID != "scope" || load("scope").Revision != 1 {
				t.Fatalf("create binding split: %+v %v", root.Run, e)
			}
			c := budget.Coordinator{Store: f.Ledger}
			switch mode {
			case "create-retry":
				before := load("scope")
				foreign := newRequest("root", "foreign", "")
				if _, e = rt.Create(ctx, foreign); !errors.Is(e, kernel.ErrAlreadyExists) {
					t.Fatalf("duplicate: %v", e)
				}
				if _, e = f.Ledger.LoadBudget(ctx, "foreign"); !errors.Is(e, budget.ErrLedgerNotFound) {
					t.Fatalf("duplicate enrolled foreign ledger: %v", e)
				}
				if !reflect.DeepEqual(before, load("scope")) {
					t.Fatal("duplicate changed original ledger")
				}
				if _, e = rt.Cancel(ctx, "root", root.Run.Revision, "stop"); e != nil {
					t.Fatal(e)
				}
				before = load("scope")
				retry := newRequest("retry", "scope", "")
				retry.Budget.Retry = budget.RootRetryGrant{InvocationID: "inv", OriginalRunID: "root", Attempt: 2, Active: true}
				cancel := kernel.RunCreationCancellation{RunID: retry.ID, Reason: "original reason", Budget: *retry.Budget}
				if got, err := rt.CancelBeforeCreate(ctx, cancel); err != nil || got != cancel {
					t.Fatalf("cancel-before-create: %+v %v", got, err)
				}
				closed := load("scope")
				if closed.Revision != before.Revision+1 || !closed.RunCancelled(retry.ID) {
					t.Fatalf("cancelled selection not committed: %+v", closed)
				}
				if _, ok := closed.Runs[retry.ID]; ok {
					t.Fatal("negative identity fabricated a Run enrollment")
				}
				peer := rt
				if f.Reopen != nil {
					store, _ := f.Reopen(t)
					peer = ea1StartRuntime(t, store)
				}
				if _, err := peer.Load(ctx, retry.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("negative identity fabricated a Run: %v", err)
				}
				if _, err := peer.Create(ctx, retry); !errors.Is(err, kernel.ErrCreationCancelled) {
					t.Fatalf("cancelled identity admitted Create: %v", err)
				}
				replay := cancel
				replay.Reason = "retry reason"
				if got, err := peer.CancelBeforeCreate(ctx, replay); err != nil || got != cancel || !reflect.DeepEqual(closed, load("scope")) {
					t.Fatalf("negative identity replay changed original fact: %+v %v", got, err)
				}
				before = closed
				retry.ID = "retry-next"
				retry.Budget.Retry.OriginalRunID = "retry"
				retry.Budget.Retry.Attempt = 3
				if _, e = rt.Create(ctx, retry); e != nil {
					t.Fatal(e)
				}
				cancel.RunID, cancel.Budget = retry.ID, *retry.Budget
				if _, e = peer.CancelBeforeCreate(ctx, cancel); !errors.Is(e, kernel.ErrAlreadyExists) {
					t.Fatalf("created identity admitted missing-Run cancellation: %v", e)
				}
				after := load("scope")
				if after.Revision != before.Revision+1 || !after.RootRetries[retry.ID].Active || after.RunCancelled(retry.ID) {
					t.Fatalf("retry split: %+v", after)
				}
				if _, ok := after.Runs[retry.ID]; !ok {
					t.Fatal("retry registration missing")
				}
				// Competing connections must decide one identity even before a Run exists.
				race := newRequest("race", "race-scope", "")
				if _, e = c.Ensure(ctx, race.Budget.ScopeID, limits); e != nil {
					t.Fatal(e)
				}
				start, createErr, cancelErr := make(chan struct{}), make(chan error, 1), make(chan error, 1)
				go func() {
					<-start
					_, err := rt.Create(ctx, race)
					createErr <- err
				}()
				go func() {
					<-start
					_, err := peer.CancelBeforeCreate(ctx, kernel.RunCreationCancellation{RunID: race.ID, Budget: *race.Budget})
					cancelErr <- err
				}()
				close(start)
				ce, xe := <-createErr, <-cancelErr
				l := load("race-scope")
				_, member := l.Runs[race.ID]
				if l.Revision != 2 || (ce == nil) == (xe == nil) ||
					(ce == nil && (!errors.Is(xe, kernel.ErrAlreadyExists) || !member || l.RunCancelled(race.ID))) ||
					(xe == nil && (!errors.Is(ce, kernel.ErrCreationCancelled) || member || !l.RunCancelled(race.ID))) {
					t.Fatalf("create/cancel election split: %v %v %+v", ce, xe, l)
				}
			case "capacity":
				unbound := newRequest("unbound", "scope", "root")
				unbound.ParentRunID, unbound.Budget = "root", nil
				if _, e = rt.Create(ctx, unbound); !errors.Is(e, budget.ErrLedgerConflict) {
					t.Fatalf("unbound child escaped: %v", e)
				}
				foreign := newRequest("foreign", "other", "root")
				if _, e = rt.Create(ctx, foreign); !errors.Is(e, budget.ErrLedgerConflict) {
					t.Fatalf("foreign child escaped: %v", e)
				}
				if _, e = rt.Create(ctx, newRequest("child", "scope", "root")); e != nil {
					t.Fatal(e)
				}
				before := load("scope")
				if _, e = rt.Create(ctx, newRequest("denied", "scope", "root")); !errors.Is(e, budget.ErrExhausted) {
					t.Fatalf("capacity: %v", e)
				}
				if _, e = rt.Load(ctx, "denied"); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("denied Run exists: %v", e)
				}
				if !reflect.DeepEqual(before, load("scope")) {
					t.Fatal("denied Run altered ledger")
				}
			case "foreign":
				if _, e = c.Ensure(ctx, "other", limits); e != nil {
					t.Fatal(e)
				}
				if _, e = c.RegisterRun(ctx, "other", "root", budget.RunBudget{}); e != nil {
					t.Fatal(e)
				}
				other := load("other")
				plan, e := budget.PlanLedgerMutation(ctx, other, func(c budget.Coordinator) error {
					_, err := c.Reserve(ctx, "other", budget.Reservation{ID: "foreign", RunID: "root", RequestHash: "x", Requested: budget.Usage{ToolCalls: 1}}, true)
					return err
				})
				if e != nil {
					t.Fatal(e)
				}
				lease, e := f.Store.(kernel.ExecutionStore).AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: "root", OwnerID: "owner", LeaseDuration: time.Minute})
				if e != nil {
					t.Fatal(e)
				}
				run := root.Run
				run.Revision++
				run.UpdatedAt = time.Now().UTC()
				if _, e = f.Store.(kernel.ExecutionStore).ApplyExecution(ctx, lease, root.Run.Revision, kernel.StoreMutation{Record: kernel.Record{Run: run, State: root.State}, Ledger: plan}); !errors.Is(e, budget.ErrLedgerConflict) {
					t.Fatalf("foreign ledger accepted: %v", e)
				}
				run.BudgetScopeID = "other"
				if _, e = f.Store.(kernel.ExecutionStore).ApplyExecution(ctx, lease, root.Run.Revision, kernel.StoreMutation{Record: kernel.Record{Run: run, State: root.State}}); e == nil {
					t.Fatal("mutable scope")
				}
				if !reflect.DeepEqual(other, load("other")) {
					t.Fatal("foreign ledger changed")
				}
			default:
				res := budget.Reservation{ID: "tool:root:call", RunID: "root", RequestHash: "x", Requested: budget.Usage{ToolCalls: 1}}
				if mode != "complete" {
					if _, e = c.Reserve(ctx, "scope", res, true); e != nil {
						t.Fatal(e)
					}
				}
				if mode == "unknown-expire" {
					if _, e = c.DispatchTool(ctx, "scope", res); e != nil {
						t.Fatal(e)
					}
				}
				before := load("scope")
				switch mode {
				case "expire", "unknown-expire":
					_, expired, e := rt.Expire(ctx, "root", root.Run.Revision)
					if mode == "unknown-expire" {
						if expired || !errors.Is(e, kernel.ErrChildExecutionBusy) || !reflect.DeepEqual(before, load("scope")) {
							t.Fatalf("unknown expired: %v %v", expired, e)
						}
						current, e := rt.Load(ctx, "root")
						if e != nil || current.Run.Revision != root.Run.Revision {
							t.Fatal("unknown changed Run")
						}
						return
					}
					if e != nil || !expired {
						t.Fatalf("expire: %v %v", expired, e)
					}
				case "cancel":
					if _, e = rt.Cancel(ctx, "root", root.Run.Revision, "stop"); e != nil {
						t.Fatal(e)
					}
				default:
					lease, e := f.Store.(kernel.ExecutionStore).AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: "root", OwnerID: "owner", LeaseDuration: time.Minute})
					if e != nil {
						t.Fatal(e)
					}
					run := root.Run
					run.Revision++
					run.Status = kernel.RunStatusCompleted
					run.UpdatedAt = time.Now().UTC()
					run.EndedAt = &run.UpdatedAt
					if _, e = f.Store.(kernel.ExecutionStore).ApplyExecution(ctx, lease, root.Run.Revision, kernel.StoreMutation{Record: kernel.Record{Run: run, State: root.State}}); e != nil {
						t.Fatal(e)
					}
				}
				after := load("scope")
				if !after.FinishedRuns["root"] || after.Revision != before.Revision+1 {
					t.Fatalf("terminal split: %+v", after)
				}
				if mode != "complete" && (!after.RunCancelled("root") || after.Reservations[res.ID].Status != budget.ReservationReleased) {
					t.Fatalf("control accounting: %+v", after)
				}
			}
		})
	}
}
