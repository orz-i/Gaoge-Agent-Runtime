package harnesspostgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	nativesqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"gorm.io/gorm"
)

type generationModel struct {
	calls             int
	failFirst         bool
	delegateEveryRoot bool
}

var generationBackends = []string{"memory", "sql", "native-sqlite"}

func generationNativeSQLite(t *testing.T) *nativesqlite.Store {
	t.Helper()
	dir := testprofile.Dir(t)
	s, err := nativesqlite.CreateNew(t.Context(), filepath.Join(dir, "kernel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func (*generationModel) PlanTokenAdmission(context.Context, model.Request) (model.TokenAdmission, error) {
	return model.TokenAdmission{InputUpperBound: 128, MaxOutputTokens: 64}, nil
}
func (m *generationModel) Generate(_ context.Context, req model.Request) (model.Response, error) {
	m.calls++
	response := model.Response{Usage: &model.Usage{InputTokens: 20, OutputTokens: 10}}
	if m.failFirst && m.calls == 1 {
		return response, model.NewResponseRejectedError(model.NewClassifiedError(errors.New("known refusal"), "model.upstream_request_invalid"))
	}
	if (m.calls == 1 || m.delegateEveryRoot) && req.Model != "specialist-model" {
		response.ToolCalls = []tools.Call{{ID: "delegate", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"researcher","goal":"Find evidence"}`)}}
		return response, nil
	}
	response.Content = "completed retry"
	return response, nil
}

type generationStore interface {
	harness.Store
	budget.LedgerStore
}
type generationClock struct{}

func (generationClock) Now() time.Time { return time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC) }

type generationPolicy struct{}

func (generationPolicy) PrepareDelegation(_ context.Context, _ tools.ExecutionRequest, req harness.DelegateRequest) (harness.DelegateRequest, error) {
	if req.MemberID != "researcher" {
		return harness.DelegateRequest{}, harness.ErrInvalidRequest
	}
	req.MemberRevision = "v3"
	req.Agent = agent.StartRequest{Model: "specialist-model", Limits: budget.Limits{MaxLLMCalls: 2}}
	return req, nil
}

func newGenerationHarness(t *testing.T, sqlBackend bool, m *generationModel) (*harness.Runner, generationStore, *kernel.Runtime, *agent.Runner, harness.StartRequest) {
	backend := "memory"
	if sqlBackend {
		backend = "sql"
	}
	return newGenerationHarnessBackend(t, backend, m)
}

func newGenerationHarnessBackend(t *testing.T, backend string, m *generationModel, wrappers ...func(generationStore) generationStore) (*harness.Runner, generationStore, *kernel.Runtime, *agent.Runner, harness.StartRequest) {
	t.Helper()
	var hs generationStore = harness.NewMemoryStore()
	var ks kernel.Store = memory.NewStore()
	var relationStore runrelation.Store = memory.NewRunRelationStore()
	if backend == "sql" {
		store, db := newGenerationSQLStore(t)
		if err := postgres.Migrate(db); err != nil {
			t.Fatal(err)
		}
		hs, ks = store, postgres.NewKernelStore(db)
		relationStore = postgres.NewRunRelationStore(db)
	}
	if backend == "native-sqlite" {
		s := generationNativeSQLite(t)
		ks, relationStore = s, nativesqlite.NewRunRelationStore(s)
	}
	for _, wrap := range wrappers {
		hs = wrap(hs)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: ks})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(relationStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	if capture, ok := hs.(interface{ setGenerationRelations(*runrelation.Registry) }); ok {
		capture.setGenerationRelations(relations)
	}
	deps := harness.BudgetAuthorityDependencies{Runtime: rt, Store: hs, Relations: relations, Meter: m, Clock: generationClock{}}
	shared, err := harness.NewBudgetAuthority(deps)
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := shared.Adapter()
	if err != nil {
		t.Fatal(err)
	}
	d := harness.NewDelegationToolHandler()
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(d)})
	if err != nil {
		t.Fatal(err)
	}
	original, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: registry, Executor: registry,
		ToolOutcomeVerifier: d,
		BoundedBudget:       bounded})
	if err != nil {
		t.Fatal(err)
	}
	c, err := handoff.NewDeferredAgent(original)
	if err != nil {
		t.Fatal(err)
	}
	r, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: original, Store: hs, Clock: generationClock{}, Handoffs: c, Relations: relations, Budget: shared, DelegationPolicy: generationPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Bind(r); err != nil {
		t.Fatal(err)
	}
	req := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "generation-thread"}, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "generation-turn"},
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "generation-thread"}, Goal: "answer",
		Config: harness.ConfigSnapshot{Environment: harness.VersionRef{ID: "test", Revision: 1},
			SharedBudget:     &budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 16, MaxTotalTokens: 5000, MaxChildRuns: 3, MaxConcurrentRuns: 1},
			DelegationPolicy: harness.DelegationPolicySnapshot{MaxDepth: 1},
			ToolPolicies:     []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()}, AgentSpec: harness.AgentSpec{Model: "model",

				ToolKeys: []string{harness.DelegationToolKey}},
		},
	}
	return r, hs, rt, original, req
}

// CI's PostgreSQL/Redis integration job sets TEST_POSTGRES_DSN. The exact same
// lifecycle matrix then exercises isolated real PostgreSQL transactions and
// durable RunRelations, rather than a separate weaker positive-only scenario.
func newGenerationSQLStore(t *testing.T) (*harnesspostgres.Store, *gorm.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		return newStoreWithDB(t)
	}
	db, _ := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

// scripts/run-integration.mjs deliberately selects TestRealPostgres*, so DSN
// support in the fixture alone is insufficient to exercise this matrix in CI.
func TestRealPostgresRootGenerationLifecycle(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN")) == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"callback-custody", TestGenerationControlRejectsForeignCallbackCustody},
		{"retry-cancellation", TestRetriedRootCancellationCleansItsDeferredChildren},
		{"historical-terminal", TestHistoricalHandoffDoesNotBlockReplacementTerminalLifecycle},
		{"repeated-generations", TestRepeatedRetryCancellationPreservesPriorChildReceipts},
		{"undispatched-leases", TestPreparedChildCancellationSurvivesLeaseOnlyEpochs},
		{"cancel-attempt-CAS", TestCancellationAdmissionCompetesWithExactRetryAttempt},
		{"historical-controls", TestHistoricalChildControlsRejectBeforeIntentAdmission},
		{"waiting-history-retry", TestWaitingOnlyHistorySurvivesRetryAndNoRunCancellation},
		{"retirement-frontier", TestRetiredCallbackClosurePrecedesEveryNewGeneration},
		{"all-cancellation-phases", TestNativeRootCancellationReplaysEveryDurablePhase},
		{"terminal-independent-child-retry", TestFailedRootRetryPreservesIndependentTerminalChildProjection},
		{"cancel-effect-exclusivity", TestCancellationProtocolUnknownEffectsNeverReenterOrBorrowNativeProof},
		{"cancel-effect-terminal-domain", TestCancellationProtocolKernelTerminalDoesNotSignHostReceipt},
		{"cancel-effect-descendant-domain", TestCancellationProtocolRootBusyCannotRedispatchUnknownDescendant},
		{"cancel-effect-owner-reconciliation", TestCancellationProtocolOriginalOwnerReconcilesWithoutRedispatch},
		{"cancel-effect-durable-cuts", TestCancellationProtocolHostDurableCutRecovery},
		{"cancel-waiting-durable-cuts", TestCancellationProtocolWaitingNoDispatchAllDurableCuts},
		{"cancel-initial-root-durable-cuts", TestCancellationProtocolInitialRootNoDispatchAllDurableCuts},
		{"cancel-prepared-child-controls", TestCancellationProtocolPreparedHandoffNoDispatchRecoversBothControls},
		{"cancel-frozen-before-invocation", TestCancellationProtocolFrozenStartedBeforeInvocationIsNoDispatch},
		{"cancel-effect-public-append-denied", TestCancellationProtocolPublicAppendCannotMintClaimsOrReceipts},
		{"cancel-legacy-quarantine", TestCancellationProtocolLegacyIntentCannotInheritCurrentNativeCustody},
		{"cancel-terminal-projection-race", TestCancellationProtocolTerminalProjectionCompetesWithLateEffectClaim},
		{"cancel-waiting-started-admission-history", TestCancellationProtocolWaitingToStartedNoInvocationRetainsHistory},
		{"cancel-nested-waiting-attempt", TestCancellationProtocolHistoricalNestedWaitingRejectsBeforeElection},
	} {
		t.Run(test.name, test.run)
	}
}

type detachedGenerationCompletion struct {
	release  <-chan struct{}
	finished chan<- struct{}
}

func (p detachedGenerationCompletion) ValidateCompletion(context.Context, kernel.Run, model.Response) (agent.CompletionCorrection, error) {
	go func() { <-p.release; close(p.finished) }()
	return agent.CompletionCorrection{}, nil
}

func TestGenerationControlRejectsForeignCallbackCustody(t *testing.T) {
	for _, backend := range generationBackends {
		t.Run(backend, func(t *testing.T) {
			m := &generationModel{}
			r, hs, rt, original, req := newGenerationHarnessBackend(t, backend, m)
			first, err := r.StartAgentTurn(t.Context(), req)
			if err != nil || len(first.Subtasks) != 1 {
				t.Fatalf("delegation fixture: %+v %v", first.Turn, err)
			}
			release, finished := make(chan struct{}), make(chan struct{})
			// Accounting is the same native Run binding; only the foreign
			// completion policy differs from the original scoped composition.
			shared, e := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Runtime: rt, Store: hs, Meter: m, Clock: generationClock{}})
			if e != nil {
				t.Fatal(e)
			}
			bounded, e := shared.Adapter()
			if e != nil {
				t.Fatal(e)
			}
			foreign, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, BoundedBudget: bounded, CompletionPolicies: []agent.CompletionPolicy{detachedGenerationCompletion{release, finished}}})
			if err != nil {
				t.Fatal(err)
			}
			child, err := rt.Load(t.Context(), first.Subtasks[0].RunID)
			if err != nil {
				t.Fatal(err)
			}
			done, err := foreign.Resume(t.Context(), child.Run.ID, child.Run.Revision)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { close(release); <-finished })
			if original.VerifiedDeferredRootControl(t.Context(), rt, done) || foreign.VerifiedDeferredRootControl(t.Context(), rt, done) {
				t.Fatal("a same-Kernel observer upgraded the executing opaque callback")
			}
			if _, err = r.CancelSubtask(t.Context(), first.Turn.ID, first.Subtasks[0].ID, "stop foreign callback"); !errors.Is(err, harness.ErrHandoffCancellationUnverified) {
				t.Fatalf("untracked effect settled cancellation: %v", err)
			}
			items, err := hs.ListItems(t.Context(), first.Turn.ID, 0, 500)
			if err != nil || !harness.ActiveCancellationFence(items) {
				t.Fatalf("unknown effects lost their original fence: %v", err)
			}
		})
	}
}

func TestRetriedRootCancellationCleansItsDeferredChildren(t *testing.T) {
	for _, backend := range generationBackends {
		for _, closeOriginal := range []bool{false, true} {
			name := backend + "/failed"
			if closeOriginal {
				name += "/original-budget-closed"
			}
			t.Run(name, func(t *testing.T) {
				r, hs, rt, _, req := newGenerationHarnessBackend(t, backend, &generationModel{failFirst: true, delegateEveryRoot: true})
				first, err := r.StartAgentTurn(t.Context(), req)
				if first.Turn.Status != harness.TurnFailed {
					t.Fatalf("known original rejection: %s %v", first.Turn.Status, err)
				}
				root, ok := harness.TopLevelInvocation(first)
				if !ok {
					t.Fatal("missing original root")
				}
				if closeOriginal {
					if _, err = r.Cancel(t.Context(), first.Turn.ID, "close original budget"); err != nil {
						t.Fatal(err)
					}
				}
				next, err := r.RetryInvocation(t.Context(), root.ID)
				if err != nil || len(next.Subtasks) != 1 || next.Turn.Status != harness.TurnRunning {
					t.Fatalf("replacement descendant admission: %+v %v", next.Turn, err)
				}
				cancelled, err := r.Cancel(t.Context(), next.Turn.ID, "stop replacement root")
				if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
					t.Fatalf("replacement cleanup: %+v %v", cancelled.Turn, err)
				}
				child, err := rt.Load(t.Context(), next.Subtasks[0].RunID)
				if err != nil || child.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("live child after cancellation: %+v %v", child.Run, err)
				}
				ledger, err := hs.LoadBudget(t.Context(), next.Turn.ID)
				if err != nil || ledger.Cancelled != closeOriginal || !ledger.View("").Cancelled || ledger.View("").Usage.LLMCalls != 2 {
					t.Fatalf("changed history or divergent current cancellation: %+v %v", ledger, err)
				}
				items, err := hs.ListItems(t.Context(), first.Turn.ID, 0, 500)
				if err != nil || harness.ActiveCancellationFence(items) {
					t.Fatalf("stranded original cancellation fence: %v", err)
				}
			})
		}
	}
}

func TestHistoricalHandoffDoesNotBlockReplacementTerminalLifecycle(t *testing.T) {
	for _, backend := range generationBackends {
		for _, sharedBudget := range []bool{false, true} {
			name := backend
			if sharedBudget {
				name += "/budgeted"
			} else {
				name += "/unbudgeted"
			}
			t.Run(name, func(t *testing.T) {
				r, hs, rt, _, req := newGenerationHarnessBackend(t, backend, &generationModel{})
				if !sharedBudget {
					req.Config.SharedBudget = nil
				}
				first, err := r.StartAgentTurn(t.Context(), req)
				if err != nil || len(first.Subtasks) != 1 {
					t.Fatalf("original delegation: %+v %v", first.Turn, err)
				}
				stopped, err := r.Cancel(t.Context(), first.Turn.ID, "cancel original root and child")
				if err != nil || stopped.Turn.Status != harness.TurnCancelled {
					t.Fatalf("original Cancel: %+v %v", stopped.Turn, err)
				}
				root, _ := harness.TopLevelInvocation(stopped)
				next, err := r.RetryInvocation(t.Context(), root.ID)
				if err != nil || next.Turn.Status != harness.TurnCompleted {
					t.Fatalf("historical child blocked replacement: %+v %v", next.Turn, err)
				}
				current, err := hs.GetInvocation(t.Context(), root.ID)
				if err != nil || current.Attempt != 2 || current.Status != harness.InvocationCompleted {
					t.Fatalf("replacement identity: %+v %v", current, err)
				}
				physical, err := rt.Load(t.Context(), current.ExecutionRefID)
				if err != nil || physical.Run.Status != kernel.RunStatusCompleted {
					t.Fatalf("physical replacement: %+v %v", physical.Run, err)
				}
				for _, observe := range []func() (harness.Snapshot, error){
					func() (harness.Snapshot, error) { return r.Refresh(t.Context(), first.Turn.ID) },
					func() (harness.Snapshot, error) {
						return r.Cancel(t.Context(), first.Turn.ID, "observe completed replacement")
					},
				} {
					got, err := observe()
					if err != nil || got.Turn.Status != harness.TurnCompleted || len(got.Subtasks) != 1 || got.Subtasks[0].RunID != first.Subtasks[0].RunID {
						t.Fatalf("historical observation/cleanup: %+v %v", got.Turn, err)
					}
				}
			})
		}
	}
}

func TestRepeatedRetryCancellationPreservesPriorChildReceipts(t *testing.T) {
	for _, backend := range generationBackends {
		t.Run(backend, func(t *testing.T) {
			r, hs, _, _, req := newGenerationHarnessBackend(t, backend, &generationModel{delegateEveryRoot: true})
			current, err := r.StartAgentTurn(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			root, _ := harness.TopLevelInvocation(current)
			var originalFacts []harness.Item
			for attempt := 1; attempt <= 3; attempt++ {
				current, err = r.Cancel(t.Context(), current.Turn.ID, "close current generation")
				if err != nil || current.Turn.Status != harness.TurnCancelled {
					t.Fatalf("Attempt %d cancellation: %+v %v", attempt, current.Turn, err)
				}
				if attempt == 1 {
					originalFacts, err = hs.ListItems(t.Context(), current.Turn.ID, 0, 500)
					if err != nil {
						t.Fatal(err)
					}
				}
				if attempt < 3 {
					current, err = r.RetryInvocation(t.Context(), root.ID)
					if err != nil || current.Turn.Status != harness.TurnRunning || len(current.Subtasks) != attempt+1 {
						t.Fatalf("Attempt %d retry: %+v %v", attempt+1, current.Turn, err)
					}
				}
			}
			for _, fact := range originalFacts {
				retained, err := hs.GetItem(t.Context(), fact.ID)
				if err != nil || retained.Seq != fact.Seq || string(retained.Payload) != string(fact.Payload) {
					t.Fatalf("original receipt changed: %s %v", fact.ID, err)
				}
			}
		})
	}
}

func TestPublicItemAppendCannotForgeAttemptTransition(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			store := create(t)
			previous := rootInvocationFixture("transition-root", "transition-turn", "")
			previous.Status = harness.InvocationCancelled
			now := generationClock{}.Now()
			if _, _, err := store.CreateTurn(t.Context(), harness.Turn{ID: previous.TurnID, SessionID: "session", HostTurn: harness.HostRef{Kind: "test", ID: "source"}, ConfigSnapshotID: "legacy-config", Status: harness.TurnCancelled, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			fact, err := harness.RootRetryTransitionFact(previous, "new-root-run", generationClock{}.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = store.AppendItem(t.Context(), fact); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("public Item minted Store-owned retry authority: %v", err)
			}
		})
	}
}

func TestPreparedChildCancellationSurvivesLeaseOnlyEpochs(t *testing.T) {
	for _, backend := range generationBackends {
		for _, cancelWhileHeld := range []bool{false, true} {
			name := backend
			if cancelWhileHeld {
				name += "/held-then-released"
			} else {
				name += "/lease-only-abort"
			}
			t.Run(name, func(t *testing.T) {
				r, hs, rt, _, req := newGenerationHarnessBackend(t, backend, &generationModel{})
				first, err := r.StartAgentTurn(t.Context(), req)
				if err != nil || len(first.Subtasks) != 1 {
					t.Fatalf("delegation fixture: %+v %v", first.Turn, err)
				}
				child, err := rt.Load(t.Context(), first.Subtasks[0].RunID)
				if err != nil {
					t.Fatal(err)
				}
				lease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: child.Run.ID, OwnerID: "worker-before-Begin", LeaseDuration: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				if cancelWhileHeld {
					if _, err = r.CancelSubtask(t.Context(), first.Turn.ID, first.Subtasks[0].ID, "compete with undispatched lease"); err == nil {
						t.Fatal("live execution lease incorrectly claimed settled")
					}
				}
				if err = rt.ReleaseExecution(t.Context(), lease); err != nil {
					t.Fatal(err)
				}
				if err = rt.VerifyExecutionCustody(t.Context(), child, kernel.CustodyPrepared); err != nil {
					t.Fatalf("lease-only epoch invalidated physical history: %v", err)
				}
				cancelled, err := r.CancelSubtask(t.Context(), first.Turn.ID, first.Subtasks[0].ID, "stop undispatched child")
				if err != nil || len(cancelled.Subtasks) != 1 || cancelled.Subtasks[0].Status != "cancelled" {
					t.Fatalf("original prepared cancellation/recovery: %+v %v", cancelled.Subtasks, err)
				}
				items, err := hs.ListItems(t.Context(), first.Turn.ID, 0, 500)
				if err != nil || harness.ActiveCancellationFence(items) {
					t.Fatalf("lease-only abort stranded the Turn intent: %v", err)
				}
			})
		}
	}
}
