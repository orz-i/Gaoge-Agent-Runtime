package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
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
				cancel := kernel.RunCreationDecision{RunID: retry.ID, Status: kernel.RunStatusCancelled, Reason: "original reason", Budget: *retry.Budget}
				if got, err := rt.DecideBeforeCreate(ctx, cancel); err != nil || got != cancel {
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
				if got, err := peer.DecideBeforeCreate(ctx, replay); err != nil || got != cancel || !reflect.DeepEqual(closed, load("scope")) {
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
				if _, e = peer.DecideBeforeCreate(ctx, cancel); !errors.Is(e, kernel.ErrAlreadyExists) {
					t.Fatalf("created identity admitted missing-Run cancellation: %v", e)
				}
				after := load("scope")
				if after.Revision != before.Revision+1 || !after.RootRetries[retry.ID].Active || after.RunCancelled(retry.ID) {
					t.Fatalf("retry split: %+v", after)
				}
				if _, ok := after.Runs[retry.ID]; !ok {
					t.Fatal("retry registration missing")
				}
				// Both negative outcomes can close a first Attempt even before
				// its ledger exists. The next generation enrolls only itself.
				for _, status := range []kernel.RunStatus{kernel.RunStatusCancelled, kernel.RunStatusFailed} {
					first := newRequest("uncreated-"+string(status), "scope-"+string(status), "")
					q := kernel.RunCreationDecision{RunID: first.ID, Status: status, Reason: "definite", Budget: *first.Budget}
					if got, err := rt.DecideBeforeCreate(ctx, q); err != nil || got != q {
						t.Fatalf("first negative: %+v %v", got, err)
					}
					l := load(first.Budget.ScopeID)
					if l.Revision != 1 || !l.Cancelled || !l.RunCancelled(first.ID) || len(l.Runs) != 0 || len(l.Reservations) != 0 {
						t.Fatal("negative first fabricated membership or failed to close quota")
					}
					if got, err := peer.LoadCreationDecision(ctx, first.ID); err != nil || got != q {
						t.Fatalf("read original negative: %+v %v", got, err)
					}
					other := q
					other.Status = kernel.RunStatusCancelled
					if status == kernel.RunStatusCancelled {
						other.Status = kernel.RunStatusFailed
					}
					if got, err := peer.DecideBeforeCreate(ctx, other); err != nil || got != q || !reflect.DeepEqual(l, load(first.Budget.ScopeID)) {
						t.Fatalf("opposing decision replaced first: %+v %v", got, err)
					}
					if _, err := peer.Create(ctx, first); !errors.Is(err, q.Rejection()) {
						t.Fatalf("late Create won negative: %v", err)
					}
					next := newRequest(first.ID+"-retry", first.Budget.ScopeID, "")
					next.Budget.Retry = budget.RootRetryGrant{InvocationID: "inv-" + string(status), OriginalRunID: first.ID, Attempt: 2, Active: true}
					if _, err := peer.Create(ctx, next); err != nil {
						t.Fatalf("uncreated first Retry: %v", err)
					}
					after := load(first.Budget.ScopeID)
					if after.Revision != 2 || len(after.Runs) != 1 || after.RunCancelled(next.ID) || !after.RootRetries[next.ID].Active {
						t.Fatal("first Retry did not preserve native identity closure")
					}
				}
				// A budget-only marker cannot authorize a nonexistent native predecessor.
				phantom := newRequest("forged-next", "forged-scope", "")
				phantom.Budget.Retry = budget.RootRetryGrant{InvocationID: "forged-inv", OriginalRunID: "missing-predecessor", Attempt: 2, Active: true}
				l, err := c.Ensure(ctx, phantom.Budget.ScopeID, limits)
				if err != nil {
					t.Fatal(err)
				}
				l.Cancelled, l.CancelledRuns = true, map[string]bool{"missing-predecessor": true}
				l.Revision++
				if err = f.Ledger.SaveBudget(ctx, l, l.Revision-1); err != nil {
					t.Fatal(err)
				}
				if _, err = rt.Create(ctx, phantom); !errors.Is(err, budget.ErrLedgerConflict) || !reflect.DeepEqual(l, load(l.ID)) {
					t.Fatalf("ledger marker substituted native predecessor: %v", err)
				}
				if _, err = rt.LoadCreationDecision(ctx, phantom.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("failed Create left identity: %v", err)
				}

				// Three competing commands use the one permanent Run identity.
				race := newRequest("race", "race-scope", "")
				if _, e = c.Ensure(ctx, race.Budget.ScopeID, limits); e != nil {
					t.Fatal(e)
				}
				type result struct {
					decision kernel.RunCreationDecision
					err      error
				}
				start, createErr, negative := make(chan struct{}), make(chan error, 1), make(chan result, 2)
				go func() {
					<-start
					_, err := rt.Create(ctx, race)
					createErr <- err
				}()
				for _, status := range []kernel.RunStatus{kernel.RunStatusCancelled, kernel.RunStatusFailed} {
					go func() {
						<-start
						d, err := peer.DecideBeforeCreate(ctx, kernel.RunCreationDecision{RunID: race.ID, Status: status, Reason: "definite", Budget: *race.Budget})
						negative <- result{d, err}
					}()
				}
				close(start)
				ce, a, b := <-createErr, <-negative, <-negative
				l = load("race-scope")
				_, member := l.Runs[race.ID]
				if l.Revision != 2 || (ce == nil && (!errors.Is(a.err, kernel.ErrAlreadyExists) || !errors.Is(b.err, kernel.ErrAlreadyExists) || !member || l.RunCancelled(race.ID))) ||
					(ce != nil && (a.err != nil || b.err != nil || a.decision != b.decision || !a.decision.Valid() || !errors.Is(ce, a.decision.Rejection()) || member || !l.RunCancelled(race.ID))) {
					t.Fatalf("create/cancel/fail election split: %v %+v %+v %+v", ce, a, b, l)
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
				if _, _, e = f.Store.Commit(ctx, (lease).RunID, root.Run.Revision, kernel.StoreMutation{Record: kernel.Record{Run: run, State: root.State}, Ledger: plan}, kernel.LeaseCommit(lease)); !errors.Is(e, budget.ErrLedgerConflict) {
					t.Fatalf("foreign ledger accepted: %v", e)
				}
				run.BudgetScopeID = "other"
				if _, _, e = f.Store.Commit(ctx, (lease).RunID, root.Run.Revision, kernel.StoreMutation{Record: kernel.Record{Run: run, State: root.State}}, kernel.LeaseCommit(lease)); e == nil {
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
					if _, _, e = f.Store.Commit(ctx, (lease).RunID, root.Run.Revision, kernel.StoreMutation{Record: kernel.Record{Run: run, State: root.State}}, kernel.LeaseCommit(lease)); e != nil {
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

// The same actual Create, owned admission and entry transactions must fence
// logical local children even when no SharedBudget is configured.
func runNativeLogicalOwner(t *testing.T, factory func(*testing.T) ExecutionBudgetStore) {
	for _, cut := range []string{"success", "foreign", "missing", "terminal", "admission", "entry"} {
		t.Run(cut, func(t *testing.T) {
			ctx := t.Context()
			f := factory(t)
			rt := ea1StartRuntime(t, f.Store)
			req := ea1AdmissionRequest()
			parent, err := rt.Create(ctx, kernel.CreateRequest{ID: "parent", Kind: "agent", Actor: req.Actor, Thread: req.Thread, Goal: "parent", State: json.RawMessage("{}")})
			if err != nil {
				t.Fatal(err)
			}
			req.LogicalParentRunID = parent.Run.ID
			req.OriginFingerprint = agent.FrozenOriginFingerprint([]byte("frozen-child"))
			stop := func() error {
				_, e := rt.Cancel(ctx, parent.Run.ID, parent.Run.Revision, "original parent stopped")
				return e
			}
			w := ea1AgentAdmissionWrap(t, f.Store)
			switch cut {
			case "foreign":
				req.Actor.ActorID = "foreign"
			case "missing":
				req.LogicalParentRunID = "missing"
			case "terminal":
				if err := stop(); err != nil {
					t.Fatal(err)
				}
			case "admission":
				w.afterCreate = func(context.Context, kernel.Snapshot) error { return stop() }
			case "entry":
				w.afterOwned = func(context.Context, kernel.ChildExecutionGrant) error { return stop() }
			}
			m := &ea1AdmissionModel{}
			runner := ea1AdmissionConfiguredRunner(t, ea1StartRuntime(t, w), m, nil, false)
			out, err := runner.StartRun(ctx, req)
			if cut == "success" {
				if err != nil || out.Run.Status != kernel.RunStatusCompleted || out.Run.ParentRunID != "" ||
					out.Run.LogicalParentRunID != parent.Run.ID || out.Run.BudgetScopeID != "" ||
					!agent.MatchesFrozenOrigin(out, []byte("frozen-child")) ||
					m.calls.Load() != 1 {
					t.Fatalf("native logical owner not retained: %+v %v", out.Run, err)
				}
				return
			}
			if err == nil || m.calls.Load() != 0 {
				t.Fatalf("rejected owner entered a callback: %v", err)
			}
			if cut == "foreign" || cut == "missing" || cut == "terminal" {
				ea1StartAbsent(t, rt, req.ID)
				return
			}
			s, err := rt.Load(ctx, req.ID)
			if err != nil || s.Run.LogicalParentRunID != parent.Run.ID || s.Run.Revision != 1 {
				t.Fatalf("original child changed on denial: %+v %v", s.Run, err)
			}
			if _, err = rt.LookupChildExecution(ctx, req.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("denied entry retained grant: %v", err)
			}
			if cut == "entry" {
				receipt, err := rt.LookupOwnedRetirementForRun(ctx, req.ID)
				if err != nil || !receipt.Valid() || !receipt.Evidence.Completion.NotEntered() {
					t.Fatalf("exact no-entry retirement not retained: %+v %v", receipt, err)
				}
			}
		})
	}
}
