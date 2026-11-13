package harness

import (
	"context"
	"encoding/json"
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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type rejectedReceiptModel struct {
	calls      int
	code       string
	cancel     context.CancelFunc
	accepted   bool
	onGenerate func()
}

func (client *rejectedReceiptModel) Generate(context.Context, model.Request) (model.Response, error) {
	client.calls++
	if client.onGenerate != nil {
		client.onGenerate()
	}
	if client.cancel != nil {
		client.cancel()
	}
	response := model.Response{Content: "known final answer", ResponseID: "known-completion", Usage: &model.Usage{InputTokens: 20, OutputTokens: 10}}
	if client.accepted {
		return response, nil
	}
	return response, model.NewResponseRejectedError(model.NewClassifiedError(errBudgetResponseRejected, client.code))
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
			store, _, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 1})
			projection := &receiptProjectionFaultStore{MemoryStore: store}
			shared, err := NewBudgetMiddleware(BudgetMiddlewareDependencies{Store: projection, Clock: hostedBudgetClock{}})
			if err != nil {
				t.Fatal(err)
			}
			client := &rejectedReceiptModel{accepted: true, onGenerate: func() { projection.fail = !replay }}
			base := memory.NewStore()
			backing := &receiptFaultStore{Store: base, fired: !replay}
			now := time.Now().UTC()
			runtime, err := kernel.New(kernel.Dependencies{Store: backing, Clock: durabilityTestClock{now: now}})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, ModelOutcomeVerifier: shared, Clock: durabilityTestClock{now: now}, ModelMiddleware: []plugin.ModelMiddleware{shared}, RunMiddleware: []plugin.RunMiddleware{shared}})
			if err != nil {
				t.Fatal(err)
			}
			final, callErr := runner.StartRun(t.Context(), agent.StartRequest{ID: request.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "receipt"}, Goal: "finish", Limits: budget.Limits{MaxLLMCalls: 1}})
			if callErr == nil || client.calls != 1 {
				t.Fatalf("original projection/receipt fault missing: %v calls=%d", callErr, client.calls)
			}
			original, err := runtime.Load(t.Context(), request.RunID)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := runtime.LookupChildExecution(t.Context(), request.RunID)
			if err != nil {
				t.Fatal(err)
			}
			completion, err := runtime.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
			if err != nil || completion.HostCompleted {
				t.Fatalf("unknown whole Host callback incorrectly completed: %+v %v", completion, err)
			}
			ledgerBefore, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			if replay {
				projection.fail = true // Pure receipt recovery must never call this projection.
				runner, err = agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, ModelOutcomeVerifier: shared, Clock: durabilityTestClock{now: now.Add(3 * time.Minute)}, ModelMiddleware: []plugin.ModelMiddleware{shared}, RunMiddleware: []plugin.RunMiddleware{shared}})
				if err != nil {
					t.Fatal(err)
				}
				final, callErr = runner.Resume(t.Context(), original.Run.ID, original.Run.Revision)
				assertRootReceiptEvidenceOnly(t, runtime, original, final, grant, callErr)
				again, againErr := runner.Resume(t.Context(), final.Run.ID, final.Run.Revision)
				if !errors.Is(againErr, agent.ErrRootOutcomeUnknown) || !reflect.DeepEqual(again, final) {
					t.Fatalf("duplicate unknown receipt advanced: %v", againErr)
				}
				view, _ := agent.ViewState(final)
				if view.ModelInvocations[0].ErrorCode != "" || view.ModelInvocations[0].Response.Content != "known final answer" {
					t.Fatal("recovery reran projection or rewrote original accepted response")
				}
			} else {
				// The original callback already consumed its known rejected response
				// before Host return. Do not roll back that real terminal outcome.
				view, viewErr := agent.ViewState(final)
				if viewErr != nil || final.Run.Status != kernel.RunStatusFailed || final.Run.ErrorCode != "model.post_generation_failed" || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Status != agent.ModelInvocationConsumed || view.ModelInvocations[0].ProviderResponseID != "known-completion" || view.Budget.Usage.TotalTokens != 30 {
					t.Fatalf("original known rejection lost: %+v %v", view, viewErr)
				}
			}
			ledgerAfter, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil || !reflect.DeepEqual(ledgerBefore, ledgerAfter) || ledgerAfter.View("").UnknownUsage || ledgerAfter.View("").Usage.LLMCalls != 1 || ledgerAfter.View("").Usage.TotalTokens != 30 || ledgerAfter.View("").Usage.ToolCalls != 0 || ledgerAfter.View("").Reserved.LLMCalls != 0 || client.calls != 1 {
				t.Fatalf("known ledger receipt changed: %+v %v calls=%d", ledgerAfter, err, client.calls)
			}
			actualGrant, err := runtime.LookupChildExecution(t.Context(), request.RunID)
			if err != nil || actualGrant != grant {
				t.Fatalf("unknown Host scope retired: %v", err)
			}

		})
	}
}

type receiptFaultStore struct {
	kernel.Store
	afterCommit, fired bool
}

func (store *receiptFaultStore) Apply(ctx context.Context, id string, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	for _, event := range mutation.Events {
		if !store.fired && event.Type == "agent.model_invocation.completed" {
			store.fired = true
			if store.afterCommit {
				if _, err := store.Store.Apply(ctx, id, revision, mutation); err != nil {
					return kernel.Snapshot{}, err
				}
			}
			return kernel.Snapshot{}, errBudgetResponseRejected
		}
	}
	return store.Store.Apply(ctx, id, revision, mutation)
}

func TestSettledRejectionRecoversSDKReceiptWithoutRegeneration(t *testing.T) {
	for _, code := range []string{"", "model.post_generation_failed"} {
		for _, fault := range []string{"before_receipt_commit", "after_receipt_commit", "caller_cancelled"} {
			t.Run(code+"/"+fault, func(t *testing.T) {
				store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 1})
				base := memory.NewStore()
				faults := &receiptFaultStore{Store: base, afterCommit: fault == "after_receipt_commit", fired: fault == "caller_cancelled"}
				client := &rejectedReceiptModel{code: code}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if fault == "caller_cancelled" {
					client.cancel = cancel
				}
				now := time.Now().UTC()
				newRunner := func(backing kernel.Store, at time.Time) (*kernel.Runtime, *agent.Runner) {
					runtime, err := kernel.New(kernel.Dependencies{Store: backing, Clock: durabilityTestClock{now: at}})
					if err != nil {
						t.Fatal(err)
					}
					runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, ModelOutcomeVerifier: shared, Clock: durabilityTestClock{now: at}, ModelMiddleware: []plugin.ModelMiddleware{shared}, RunMiddleware: []plugin.RunMiddleware{shared}})
					if err != nil {
						t.Fatal(err)
					}
					return runtime, runner
				}
				_, runner := newRunner(faults, now)
				_, err := runner.StartRun(ctx, agent.StartRequest{ID: request.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "receipt"}, Goal: "finish", Limits: budget.Limits{MaxLLMCalls: 1}})
				if err == nil {
					t.Fatal("injected receipt failure was ignored")
				}
				runtime, restarted := newRunner(base, now.Add(3*time.Minute))
				pending, err := runtime.Load(t.Context(), request.RunID)
				if err != nil {
					t.Fatal(err)
				}
				grant, err := runtime.LookupChildExecution(t.Context(), request.RunID)
				if err != nil {
					t.Fatal(err)
				}
				ledgerBefore, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
				if err != nil {
					t.Fatal(err)
				}
				final, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
				assertRootReceiptEvidenceOnly(t, runtime, pending, final, grant, err)
				again, againErr := restarted.Resume(t.Context(), final.Run.ID, final.Run.Revision)
				if !errors.Is(againErr, agent.ErrRootOutcomeUnknown) || !reflect.DeepEqual(again, final) {
					t.Fatalf("duplicate unknown receipt advanced: %v", againErr)
				}
				view, _ := agent.ViewState(final)
				if view.ModelInvocations[0].ErrorCode == "" {
					t.Fatal("original rejected receipt lost rejection")
				}
				ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
				if err != nil || !reflect.DeepEqual(ledgerBefore, ledger) || ledger.View("").UnknownUsage || ledger.View("").Reserved.LLMCalls != 0 || ledger.View("").Usage.TotalTokens != 30 || ledger.View("").Usage.ToolCalls != 0 || client.calls != 1 {
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
	response, err := shared.Model(ctx, request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		cancel() // Known final receipts must survive caller cancellation.
		return model.Response{
			ResponseID:      "provider-complete",
			Usage:           &model.Usage{InputTokens: 20, OutputTokens: 10},
			HostedToolCalls: []model.HostedToolCall{{ID: "completed-search", ToolKey: "native.search", Status: "completed"}},
		}, model.NewResponseRejectedError(model.NewClassifiedError(cause, "model.upstream_request_invalid"))
	})
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
	restarted, err := NewBudgetMiddleware(BudgetMiddlewareDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	response, err = restarted.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		t.Fatal("completed rejected generation was dispatched again")
		return model.Response{}, nil
	})
	if !model.IsResponseRejectedError(err) || model.ClassifiedErrorCode(err) != "model.upstream_request_invalid" || response.ResponseID != "provider-complete" {
		t.Fatalf("replay turned rejection into success: %+v %v", response, err)
	}
}

func TestRejectedResponseCannotTurnPartialUsageIntoKnownCompletion(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial_stream", true: "missing_usage"}[marked], func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1})
			_, err := shared.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
				if marked {
					return model.Response{}, model.NewResponseRejectedError(errors.New("missing final receipt"))
				}
				return model.Response{Usage: &model.Usage{OutputTokens: 7}}, context.DeadlineExceeded
			})
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

// Receipt evidence is not permission to consume it or complete an unknown Host.
func assertRootReceiptEvidenceOnly(t *testing.T, runtime *kernel.Runtime, before, after kernel.Snapshot, grant kernel.ChildExecutionGrant, outcome error) {
	t.Helper()
	if !errors.Is(outcome, agent.ErrRootOutcomeUnknown) || after.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("receipt recovery advanced unknown Host: %+v %v", after.Run, outcome)
	}
	view, err := agent.ViewState(after)
	if err != nil || len(view.ModelInvocations) != 1 {
		t.Fatalf("missing receipt evidence: %+v %v", view, err)
	}
	receipt := view.ModelInvocations[0]
	if receipt.Status != agent.ModelInvocationCompleted || receipt.ProviderResponseID != "known-completion" || receipt.Usage == nil || receipt.Usage.InputTokens != 20 || receipt.Usage.OutputTokens != 10 || receipt.ConsumedAt != nil || view.Budget.Usage.LLMCalls != 0 || view.Budget.Usage.ToolCalls != 0 {
		t.Fatalf("receipt consumed/lost: %+v", view)
	}
	old, err := agent.ViewState(before)
	if err != nil || len(old.ModelInvocations) != 1 {
		t.Fatal("missing original invocation")
	}
	original := old.ModelInvocations[0]
	if receipt.ID != original.ID || receipt.RunID != original.RunID || receipt.SourceRevision != original.SourceRevision || receipt.RequestHash != original.RequestHash || receipt.Provider != original.Provider || receipt.BindingFingerprint != original.BindingFingerprint || receipt.Model != original.Model || receipt.DispatchAttempt != original.DispatchAttempt || receipt.ExecutionAttempt != original.ExecutionAttempt || !reflect.DeepEqual(receipt.Request, original.Request) {
		t.Fatal("receipt rebound original source/attempt")
	}
	var left, right map[string]json.RawMessage
	if json.Unmarshal(before.State, &left) != nil || json.Unmarshal(after.State, &right) != nil {
		t.Fatal("invalid state")
	}
	delete(left, "modelInvocations")
	delete(right, "modelInvocations")
	if !reflect.DeepEqual(left, right) || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) || !reflect.DeepEqual(before.Result, after.Result) {
		t.Fatal("receipt-only changed messages/budget/tool/deferred/control state")
	}
	originalRun := before.Run
	originalRun.Revision = after.Run.Revision
	originalRun.UpdatedAt = after.Run.UpdatedAt
	if !reflect.DeepEqual(originalRun, after.Run) {
		t.Fatal("receipt-only changed Run identity/status")
	}
	actual, err := runtime.LookupChildExecution(t.Context(), before.Run.ID)
	if err != nil || actual != grant {
		t.Fatalf("receipt-only retired original scope: %v", err)
	}
	completion, err := runtime.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
	if err != nil || completion.HostCompleted {
		t.Fatalf("receipt-only upgraded Host completion: %+v %v", completion, err)
	}
}
