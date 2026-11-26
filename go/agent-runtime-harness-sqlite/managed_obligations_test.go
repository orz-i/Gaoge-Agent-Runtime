package harnesssqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesssqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-sqlite"
	kernelsqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type sqliteManagedCut struct {
	*kernelsqlite.Store
	event  string
	before bool
}
type sqliteManagedInterrupted struct{}

func (s sqliteManagedCut) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	hit := s.event == "" && m.ManagedSafePoint.Present()
	for _, e := range m.Events {
		if s.event != "" && e.Type == s.event {
			hit = true
		}
	}
	if hit && s.before {
		panic(sqliteManagedInterrupted{})
	}
	out, err := s.Store.ApplyExecution(ctx, l, rev, m)
	if err == nil && hit {
		panic(sqliteManagedInterrupted{})
	}
	return out, err
}

type sqliteManagedModel struct {
	requests   []model.Request
	onGenerate func(model.Request) error
}

func (m *sqliteManagedModel) Generate(_ context.Context, r model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(r))
	if m.onGenerate != nil {
		if err := m.onGenerate(r); err != nil {
			return model.Response{}, err
		}
	}
	return model.Response{Content: "answer"}, nil
}

// The actual SQLite Kernel and relational Host stores are reopened separately.
// The shared mixed proof must survive both persistence round trips, including
// closure reconciliation when CANCEL committed before the budget ACK.
func TestSQLiteManagedOriginalObligationsReopen(t *testing.T) {
	for _, name := range []string{"resume", "cancel closure", "public cancel",
		"claimed/before", "claimed/after", "completed/before", "completed/after", "input_applied/before", "input_applied/after"} {
		t.Run(name, func(t *testing.T) {
			cancel := strings.Contains(name, "cancel")
			phase, _, frontier := strings.Cut(name, "/")
			event := ""
			if frontier {
				event = "agent.model_invocation." + phase
				if phase == "input_applied" {
					event = "agent.input_applied"
				}
			}
			wantBefore := 0
			if phase == "completed" || phase == "input_applied" {
				wantBefore = 1
			}
			wantCalls := 2
			if frontier && phase != "input_applied" {
				wantCalls = 1
			}
			ctx := t.Context()
			dir := testprofile.Dir(t)
			hp, kp := filepath.Join(dir, "harness.sqlite"), filepath.Join(dir, "kernel.sqlite")
			host, e := harnesssqlite.CreateNew(ctx, hp)
			if e != nil {
				t.Fatal(e)
			}
			ks, e := kernelsqlite.CreateNew(ctx, kp)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = host.Close(); _ = ks.Close() }()
			inputs, e := agent.NewMemoryRunInputs("sqlite-original-inputs")
			if e != nil {
				t.Fatal(e)
			}
			provider := &sqliteManagedModel{}
			rt, e := kernel.New(kernel.Dependencies{Store: sqliteManagedCut{Store: ks, event: event, before: strings.HasSuffix(name, "/before")}, ManagedRecoveryHost: host})
			if e != nil {
				t.Fatal(e)
			}
			if phase == "input_applied" {
				provider.onGenerate = func(r model.Request) error {
					if len(provider.requests) != 1 {
						return nil
					}
					current, e := ks.Load(ctx, r.RunID)
					if e != nil {
						return e
					}
					_, e = inputs.Submit(ctx, current.Run, "late", "late steering")
					return e
				}
			}
			req := request()
			options := harness.AgentSessionOptions{Runtime: rt, Store: host, Model: provider, Inputs: inputs,
				Session: harness.SessionOptions{HostThread: req.HostThread, Actor: req.Actor, Thread: req.Thread,
					Config: harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "fixture", MaxModelOutputTokens: 128}, SharedBudget: &budget.Limits{MaxLLMCalls: 3}},
					Transcript: harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
						return nil, nil
					})},
				Context: harness.AgentSessionContextOptions{ManagedRecovery: true, ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
					return runtimecontext.ModelWindow{ContextTokens: 4096, ReservedOutputTokens: 128}, nil
				}},
				Execution:    agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1}, Tool: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1}},
				RunExecution: &agent.RunExecution{OwnerID: "sqlite-managed-original", LeaseDuration: 3 * time.Second}}
			session, e := harness.OpenAgentSession(options)
			if e != nil {
				t.Fatal(e)
			}
			func() {
				defer func() {
					if _, ok := recover().(sqliteManagedInterrupted); !ok {
						t.Fatal("missing safe point cut")
					}
				}()
				_, _ = session.Prompt(ctx, "managed-obligations", "original task")
			}()
			pending, e := session.Load(ctx, "managed-obligations")
			if e != nil {
				t.Fatal(e)
			}
			root, ok := harness.TopLevelInvocation(pending)
			if !ok {
				t.Fatal("missing root")
			}
			original, e := ks.Load(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			safe, e := ks.LoadManagedRecovery(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			proof, ok := safe.SafePoint.AgentObligations()
			if !ok || proof.LedgerID != pending.Turn.ID || len(provider.requests) != wantBefore {
				t.Fatalf("wrong cut: %+v", proof)
			}
			if !frontier {
				if _, e = inputs.Submit(ctx, original.Run, "late", "late steering"); e != nil {
					t.Fatal(e)
				}
			}
			if name == "cancel closure" {
				now := time.Now().UTC()
				payload, _ := json.Marshal(map[string]string{"boundary": "cancel_scope_pending", "runID": root.ExecutionRefID, "protocol": "effects_v1", "canceller": "native"})
				_, fresh, e := host.AppendItem(ctx, harness.Item{ID: "sqlite-managed-cancel", TurnID: pending.Turn.ID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: payload, CreatedAt: now, UpdatedAt: now})
				if e != nil || !fresh {
					t.Fatalf("cancel contender: %v", e)
				}
			}
			if e = host.Close(); e != nil {
				t.Fatal(e)
			}
			if e = ks.Close(); e != nil {
				t.Fatal(e)
			}
			host, e = harnesssqlite.OpenExisting(ctx, hp)
			if e != nil {
				t.Fatal(e)
			}
			ks, e = kernelsqlite.OpenExisting(ctx, kp)
			if e != nil {
				t.Fatal(e)
			}
			rt, e = kernel.New(kernel.Dependencies{Store: ks, ManagedRecoveryHost: host})
			if e != nil {
				t.Fatal(e)
			}
			if reopened, e := ks.LoadManagedRecovery(ctx, root.ExecutionRefID); e != nil || kernel.ManagedDigest(reopened) != kernel.ManagedDigest(safe) {
				t.Fatalf("safe point changed after reopen: %v", e)
			}
			options.Runtime, options.Store, options.Session.Transcript = rt, host, nil
			options.RunExecution = &agent.RunExecution{OwnerID: "sqlite-managed-successor", LeaseDuration: 3 * time.Second}
			session, e = harness.OpenAgentSession(options)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = session.Resume(ctx, "managed-obligations"); !errors.Is(e, kernel.ErrExecutionConflict) || len(provider.requests) != wantBefore {
				t.Fatalf("live lease permitted work: %v", e)
			}
			if delay := time.Until(safe.SafePoint.OriginalLease.ExpiresAt) + 100*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			if name == "cancel closure" {
				parked, e := rt.ParkManagedContinuation(ctx, root.ExecutionRefID)
				if e != nil {
					t.Fatal(e)
				}
				selected, e := rt.SelectManagedContinuation(ctx, parked)
				if e != nil {
					t.Fatal(e)
				}
				closed, e := rt.CancelParkedManagedContinuation(ctx, selected)
				if e != nil {
					t.Fatal(e)
				}
				receipt, e := rt.ManagedRecoveryClosure(ctx, closed.SafePoint.ID)
				if e != nil {
					t.Fatal(e)
				}
				if e = host.CloseManagedRecovery(ctx, receipt); e == nil {
					t.Fatal("Host closed before original budget")
				}
			}
			var result harness.Snapshot
			if name == "public cancel" {
				result, e = session.Cancel(ctx, "managed-obligations", "stop original task")
			} else {
				result, e = session.Resume(ctx, "managed-obligations")
			}
			if e != nil {
				t.Fatal(e)
			}
			ledger, e := host.LoadBudget(ctx, pending.Turn.ID)
			if e != nil {
				t.Fatal(e)
			}
			final, e := ks.Load(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			v, e := agent.ViewState(final)
			if e != nil {
				t.Fatal(e)
			}
			if !ledger.FinishedRuns[root.ExecutionRefID] || ledger.View("").Reserved != (budget.Usage{}) {
				t.Fatalf("budget lifetime not closed: %+v", ledger.View(""))
			}
			if cancel {
				if result.Turn.Status != harness.TurnCancelled || v.InputCursor != 0 || len(provider.requests) != 0 || !ledger.RunCancelled(root.ExecutionRefID) || ledger.View("").Usage.LLMCalls != 0 {
					t.Fatalf("cancel entered effects or lost obligations: %+v", ledger.View(""))
				}
			} else {
				wantCursor := uint64(0)
				if wantCalls == 2 {
					wantCursor = 1
				}
				if result.Turn.Status != harness.TurnCompleted || v.InputCursor != wantCursor || len(provider.requests) != wantCalls || ledger.View("").Usage.LLMCalls != wantCalls {
					t.Fatalf("resume accounting mismatch: %+v", ledger.View(""))
				}
				if wantCalls == 2 {
					first, _ := json.Marshal(provider.requests[0].Messages)
					second, _ := json.Marshal(provider.requests[1].Messages)
					if strings.Contains(string(first), "late steering") || strings.Count(string(second), "late steering") != 1 {
						t.Fatal("late input changed frozen invocation")
					}
				}
			}
		})
	}
}
