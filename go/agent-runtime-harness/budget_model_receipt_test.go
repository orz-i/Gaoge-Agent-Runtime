package harness

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

var errBudgetResponseRejected = errors.New("completed model response was rejected")

type rejectedReceiptModel struct {
	calls      int
	code       string
	cancel     context.CancelFunc
	accepted   bool
	onGenerate func()
	request    model.Request
	response   model.Response
	receiptErr error
}

func (client *rejectedReceiptModel) Generate(_ context.Context, q model.Request) (model.Response, error) {
	client.calls++
	client.request = model.CloneRequest(q)
	if client.onGenerate != nil {
		client.onGenerate()
	}
	if client.cancel != nil {
		client.cancel()
	}
	response := model.Response{Content: "known final answer", ResponseID: "known-completion", Usage: &model.Usage{InputTokens: 20, OutputTokens: 10}}
	client.response = model.CloneResponse(response)
	if !client.accepted {
		client.receiptErr = model.NewResponseRejectedError(model.NewClassifiedError(errBudgetResponseRejected, client.code))
	}
	return response, client.receiptErr
}

// Provider evidence is available independently of the local Run/ledger CAS.
// Lookups are read-only and qualify the exact frozen physical request.
func (client *rejectedReceiptModel) VerifyModelOutcome(_ context.Context, q model.Request) (model.Response, error) {
	if client.calls != 1 || !reflect.DeepEqual(model.CloneRequest(q), client.request) {
		return model.Response{}, ErrConflict
	}
	return model.CloneResponse(client.response), client.receiptErr
}

type receiptProjectionFaultStore struct {
	*MemoryStore
	fail bool
}

func (store *receiptProjectionFaultStore) AppendItem(ctx context.Context, item Item) (Item, bool, error) {
	if store.fail && item.Kind == ItemBudget {
		return Item{}, false, errBudgetResponseRejected
	}
	return store.MemoryStore.AppendItem(ctx, item)
}

func TestBudgetProjectionFailureCannotEraseSettledModelReceipt(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial_return", true: "receipt_replay"}[replay], func(t *testing.T) {
			store, fixtureAuthority, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 1})
			projection := &receiptProjectionFaultStore{MemoryStore: store}
			fixtureAuthority.dependencies.Store = projection
			client := &rejectedReceiptModel{accepted: true, onGenerate: func() { projection.fail = !replay }}
			base := memory.NewStore()
			backing := &receiptFaultStore{Store: base, fired: !replay}
			now := time.Now().UTC()
			rt, err := kernel.New(kernel.Dependencies{Store: backing, Clock: durabilityTestClock{now: now}})
			if err != nil {
				t.Fatal(err)
			}
			shared := bindHostedBudgetAuthority(t, rt, fixtureAuthority)
			deps := agent.Dependencies{Runtime: rt, Model: client, ModelOutcomeVerifier: client, Clock: durabilityTestClock{now: now}, BoundedBudget: mustBudgetAdapter(t, shared)}
			runner, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			_, callErr := runner.StartRun(t.Context(), agent.StartRequest{ID: request.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "receipt"}, Goal: "finish", Limits: budget.Limits{MaxLLMCalls: 1}})
			if callErr == nil || client.calls != 1 {
				t.Fatalf("original projection/receipt fault missing: %v calls=%d", callErr, client.calls)
			}
			original, err := rt.Load(t.Context(), request.RunID)
			if err != nil {
				t.Fatal(err)
			}
			ledgerBefore, err := rt.BudgetStore().LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			id := "model:" + client.request.InvocationID
			res := ledgerBefore.Reservations[id]
			if replay {
				if !res.Dispatched || res.Status != budget.ReservationHeld || ledgerBefore.View("").Usage.LLMCalls != 0 {
					t.Fatalf("pre-commit fault split original receipt: %+v", res)
				}
				projection.fail = true
				runner, err = agent.NewRunner(deps)
				if err != nil {
					t.Fatal(err)
				}
				original, err = runner.ReconcileModelOutcome(t.Context(), original.Run.ID, original.Run.Revision)
				if err != nil {
					t.Fatal("receipt-only recovery used projection", err)
				}
			} else if res.Status != budget.ReservationSettled || ledgerBefore.View("").Usage.TotalTokens != 30 {
				t.Fatalf("projection erased original committed receipt: %+v", res)
			}
			v, err := agent.ViewState(original)
			if err != nil || len(v.ModelInvocations) != 1 || v.ModelInvocations[0].ErrorCode != "" || v.ModelInvocations[0].Response.Content != "known final answer" {
				t.Fatalf("original successful receipt rewritten: %+v %v", v, err)
			}
			ledger, err := rt.BudgetStore().LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil || ledger.View("").UnknownUsage || ledger.View("").Usage.LLMCalls != 1 || ledger.View("").Usage.TotalTokens != 30 || ledger.View("").Reserved.LLMCalls != 0 || client.calls != 1 {
				t.Fatalf("original receipt accounting: %+v %v", ledger, err)
			}
			if !replay && !reflect.DeepEqual(res, ledger.Reservations[id]) {
				t.Fatal("projection changed settled bytes")
			}
			projection.fail = false
			if original.Run.Status == kernel.RunStatusRunning {
				original, err = runner.Resume(t.Context(), original.Run.ID, original.Run.Revision)
			}
			if err != nil || original.Run.Status != kernel.RunStatusCompleted || client.calls != 1 {
				t.Fatal("committed success did not resume", err, original.Run.Status, client.calls)
			}
			if _, err = rt.LookupChildExecution(t.Context(), request.RunID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("settled grant retained", err)
			}
		})
	}
}

type receiptFaultStore struct {
	kernel.Store
	afterCommit, fired bool
}

func (store *receiptFaultStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Control == nil {
		for _, event := range commitMutation.Events {
			if !store.fired && event.Type == "agent.model_invocation.completed" {
				store.fired = true
				if store.afterCommit {
					if _, _, err := store.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth); err != nil {
						return kernel.Snapshot{}, false, err
					}
				}
				return kernel.Snapshot{}, false, errBudgetResponseRejected
			}
		}
		return store.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
	}
	return store.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

func TestSettledRejectionRecoversSDKReceiptWithoutRegeneration(t *testing.T) {
	for _, code := range []string{"", "model.post_generation_failed"} {
		for _, fault := range []string{"before_receipt_commit", "after_receipt_commit", "caller_cancelled"} {
			t.Run(code+"/"+fault, func(t *testing.T) {
				_, fixtureAuthority, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 1})
				base := memory.NewStore()
				faults := &receiptFaultStore{Store: base, afterCommit: fault == "after_receipt_commit", fired: fault == "caller_cancelled"}
				client := &rejectedReceiptModel{code: code}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if fault == "caller_cancelled" {
					client.cancel = cancel
				}
				now := time.Now().UTC()
				var shared *BudgetAuthority
				newRunner := func(backing kernel.Store, at time.Time) (*kernel.Runtime, *agent.Runner) {
					runtime, err := kernel.New(kernel.Dependencies{Store: backing, Clock: durabilityTestClock{now: at}})
					if err != nil {
						t.Fatal(err)
					}
					shared = bindHostedBudgetAuthority(t, runtime, fixtureAuthority)
					runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, ModelOutcomeVerifier: client, Clock: durabilityTestClock{now: at}, BoundedBudget: mustBudgetAdapter(t, shared)})
					if err != nil {
						t.Fatal(err)
					}
					return runtime, runner
				}
				initialRuntime, runner := newRunner(faults, now)
				_, err := runner.StartRun(ctx, agent.StartRequest{ID: request.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "receipt"}, Goal: "finish", Limits: budget.Limits{MaxLLMCalls: 1}})
				if err == nil {
					t.Fatal("injected receipt failure was ignored")
				}
				runtime, restarted := newRunner(base, now.Add(3*time.Minute))
				pending, err := runtime.Load(t.Context(), request.RunID)
				if err != nil {
					t.Fatal(err)
				}
				ledgerBefore, err := initialRuntime.BudgetStore().LoadBudget(t.Context(), "hosted-budget-turn")
				if err != nil {
					t.Fatal(err)
				}
				id := "model:" + client.request.InvocationID
				if fault == "before_receipt_commit" {
					res := ledgerBefore.Reservations[id]
					if res.Status != budget.ReservationHeld || !res.Dispatched || ledgerBefore.View("").Usage.LLMCalls != 0 {
						t.Fatal("receipt committed outside Run", res)
					}
				}
				final := pending
				if pending.Run.Status == kernel.RunStatusRunning {
					final, err = restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
				}
				if final.Run.Status != kernel.RunStatusFailed {
					t.Fatalf("rejection did not reach failed native outcome: %+v %v", final.Run, err)
				}
				assertNativeReceiptConsumed(t, runtime, pending, final)
				again, againErr := restarted.Resume(t.Context(), final.Run.ID, final.Run.Revision)
				if !errors.Is(againErr, agent.ErrRunTerminal) || !reflect.DeepEqual(again, final) {
					t.Fatalf("terminal rejection replayed: %v", againErr)
				}
				view, _ := agent.ViewState(final)
				if view.ModelInvocations[0].ErrorCode == "" {
					t.Fatal("original rejected receipt lost rejection")
				}
				ledger, err := runtime.BudgetStore().LoadBudget(t.Context(), "hosted-budget-turn")
				if err != nil || (fault != "before_receipt_commit" && !reflect.DeepEqual(ledgerBefore.Reservations, ledger.Reservations)) || ledger.Reservations[id].Status != budget.ReservationSettled || ledger.View("").UnknownUsage || ledger.View("").Reserved.LLMCalls != 0 || ledger.View("").Usage.TotalTokens != 30 || ledger.View("").Usage.ToolCalls != 0 || client.calls != 1 {
					t.Fatalf("receipt lookup changed ledger/generation: %+v %v calls=%d", ledger, err, client.calls)
				}

			})
		}
	}
}

func TestRejectedCompletedResponseSettlesUsageAndReplaysFailureAfterRestart(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 3, MaxToolCalls: 2, MaxHostedToolCalls: 2})
	cause := errors.New("private invalid output detail")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var fixturePhysical1 model.Request
	response, err := func() (model.Response, error) {
		var admissionErr error
		fixturePhysical1, admissionErr = admitModelLedgerFixture(ctx, shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(q model.Request) (model.Response, error) {
			cancel() // Known final receipts must survive caller cancellation.
			return model.Response{
				ResponseID:      "provider-complete",
				Usage:           &model.Usage{InputTokens: 20, OutputTokens: 10},
				HostedToolCalls: []model.HostedToolCall{{ID: "completed-search", ToolKey: "native.search", Status: "completed"}},
			}, model.NewResponseRejectedError(model.NewClassifiedError(cause, "model.upstream_request_invalid"))
		}(fixturePhysical1)
		return settleModelLedgerFixture(ctx, shared, fixturePhysical1, response, callErr)
	}()
	if !errors.Is(err, cause) || !model.IsResponseRejectedError(err) || response.ResponseID != "provider-complete" {
		t.Fatalf("rejection lost its receipt/cause: %+v %v", response, err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.UnknownUsage || view.Reserved.LLMCalls != 0 || view.Usage.LLMCalls != 1 || view.Usage.TotalTokens != 30 || view.Usage.ToolCalls != 1 || view.Usage.HostedToolCalls != 1 {
		t.Fatalf("known rejection left uncertain or incorrect charges: %+v", view)
	}
	if len(response.HostedToolCalls) != 0 || strings.Contains(string(ledger.Reservations["model:"+request.InvocationID].Receipt), "completed-search") {
		t.Fatal("rejected hosted payload persisted in shared receipt")
	}
	if strings.Contains(string(ledger.Reservations["model:"+request.InvocationID].Receipt), cause.Error()) {
		t.Fatal("private rejection detail leaked into accounting receipt")
	}
	restarted, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	response, err = restarted.VerifyModelOutcome(t.Context(), fixturePhysical1)
	if !model.IsResponseRejectedError(err) || model.ClassifiedErrorCode(err) != "model.upstream_request_invalid" || response.ResponseID != "provider-complete" {
		t.Fatalf("replay turned rejection into success: %+v %v", response, err)
	}
}

func TestRejectedResponseCannotTurnPartialUsageIntoKnownCompletion(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial_stream", true: "missing_usage"}[marked], func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1})
			var fixturePhysical2 model.Request
			_, err := func() (model.Response, error) {
				var admissionErr error
				fixturePhysical2, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
				if admissionErr != nil {
					return model.Response{}, admissionErr
				}
				response, callErr := func(q model.Request) (model.Response, error) {
					if marked {
						return model.Response{}, model.NewResponseRejectedError(errors.New("missing final receipt"))
					}
					return model.Response{Usage: &model.Usage{OutputTokens: 7}}, context.DeadlineExceeded
				}(fixturePhysical2)
				return settleModelLedgerFixture(t.Context(), shared, fixturePhysical2, response, callErr)
			}()
			if err == nil {
				t.Fatal("uncertain outcome became successful")
			}
			ledger, loadErr := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if view := ledger.View(""); !view.UnknownUsage || view.Usage.LLMCalls != 0 || view.Reserved.LLMCalls != 1 {
				t.Fatalf("uncertain charge was released or settled: %+v", view)
			}
		})
	}
}

// Returned fixed stages allow the native successor to consume the exact receipt once.
func assertNativeReceiptConsumed(t *testing.T, runtime *kernel.Runtime, before, after kernel.Snapshot) {
	t.Helper()
	old, err := agent.ViewState(before)
	if err != nil || len(old.ModelInvocations) != 1 {
		t.Fatalf("missing original invocation: %v", err)
	}
	view, err := agent.ViewState(after)
	if err != nil || len(view.ModelInvocations) != 1 {
		t.Fatalf("missing recovered invocation: %v", err)
	}
	original, receipt := old.ModelInvocations[0], view.ModelInvocations[0]
	if receipt.ID != original.ID || receipt.RunID != original.RunID || receipt.SourceRevision != original.SourceRevision || receipt.RequestHash != original.RequestHash || receipt.Provider != original.Provider || receipt.BindingFingerprint != original.BindingFingerprint || receipt.Model != original.Model || receipt.DispatchAttempt != original.DispatchAttempt || receipt.ExecutionAttempt != original.ExecutionAttempt {
		t.Fatal("recovery rebound original source or attempt")
	}
	if receipt.Request.RunID != "" || receipt.Request.InvocationID != "" || len(receipt.Request.Messages) != 0 || len(receipt.Request.Tools) != 0 || receipt.SamplingPlan != nil {
		t.Fatal("consumed invocation retained executable request instead of original hash")
	}
	if receipt.Status != agent.ModelInvocationConsumed || receipt.ProviderResponseID != "known-completion" || receipt.Response.Usage == nil || receipt.Response.Usage.InputTokens != 20 || receipt.Response.Usage.OutputTokens != 10 || receipt.ConsumedAt == nil || view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.ToolCalls != 0 {
		t.Fatalf("original receipt not consumed exactly once: %+v", view)
	}
	if _, err = runtime.LookupChildExecution(t.Context(), before.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("settled native grant retained: %v", err)
	}
}
