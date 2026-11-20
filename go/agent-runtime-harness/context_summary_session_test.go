package harness_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

func summaryTestPolicy() *agent.WorkingStateSummaryPolicy {
	return &agent.WorkingStateSummaryPolicy{MaxInputTokens: 4096, MaxSerializedBytes: 16384}
}

type summarySessionModel struct {
	agentSessionModel
}

type missingSummaryOriginStore struct {
	harness.Store
	runID string
}

func (store missingSummaryOriginStore) GetInvocationByExecutionRefID(ctx context.Context, runID string) (harness.Invocation, error) {
	if runID == store.runID {
		return harness.Invocation{}, harness.ErrNotFound
	}
	return store.Store.GetInvocationByExecutionRefID(ctx, runID)
}

func (*summarySessionModel) Capabilities(context.Context, string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: true}, nil
}

func TestAgentSessionSummaryProducesNativeStateAndVerifiesPriorOrigin(t *testing.T) {
	for _, name := range []string{"success", "invalid output fallback", "missing prior origin", "native recovery complete source"} {
		t.Run(name, func(t *testing.T) {
			invalid := name == "invalid output fallback"
			nativeRecovery := name == "native recovery complete source"
			store, runs := harness.NewMemoryStore(), memory.NewStore()
			client := &summarySessionModel{}
			summaryCalls := 0
			client.generate = func(request model.Request) (model.Response, error) {
				response := model.Response{Content: "answer", Usage: &model.Usage{InputTokens: 10, OutputTokens: 5}}
				if request.OutputContract != nil && request.OutputContract.Name == "working_state_summary" {
					summaryCalls++
					if len(request.Tools) != 0 || len(request.HostedTools) != 0 || request.RequireToolCall || request.MaxOutputTokens != 128 {
						t.Fatal("summary changed frozen output cap or retained Tool authority")
					}
					response.Content = `{"goal":"retain verified evidence","constraints":["never invent facts"],"completed":[],"pending":["verify original evidence"],"decisions":[]}`
					if invalid {
						response.Content = `{"goal":"malformed output"}`
					}
				}
				return response, nil
			}
			options := agentSessionOptions(t, runs, store, client)
			options.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
				return runtimecontext.ModelWindow{ContextTokens: 65536, ReservedOutputTokens: 128}, nil
			}
			options.Session.Config.ToolKeys = []string{harness.ContextArtifactToolKey}
			options.Session.Config.ToolPolicies = []harness.ToolPolicySnapshot{harness.ContextArtifactToolPolicySnapshot()}
			options.Session.Config.ContextPolicy = runtimecontext.Policy{SoftLimitPercent: 10, MaxSerializedBytes: 100000, PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 128}
			firstText := "first prompt " + strings.Repeat("ORIGINAL_EVIDENCE_", 700)
			secondText := "second prompt " + strings.Repeat("CURRENT_EVIDENCE_", 700)
			texts := map[string]string{"first": firstText, "second": secondText, "third": "third prompt"}
			seedFor := func(names ...string) *harness.ContextSeed {
				seed := workingStateHistory(names...)
				for index := range seed.Entries {
					seed.Entries[index].Required = false
					if seed.Entries[index].Message.Role == model.RoleUser {
						seed.Entries[index].Message.Content = model.TextParts(texts[seed.Entries[index].TurnID])
					}
				}
				return seed
			}
			options.Session.Transcript = harness.TranscriptSourceFunc(func(_ context.Context, _ harness.HostRef, turn harness.HostRef, _ string) (*harness.ContextSeed, error) {
				switch turn.ID {
				case "first":
					return seedFor("first"), nil
				case "second":
					return seedFor("first", "second"), nil
				default:
					return seedFor("first", "second", "third"), nil
				}
			})
			warmup, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := warmup.Prompt(t.Context(), "first", firstText); err != nil {
				t.Fatal(err)
			}
			options.Session.Config.WorkingStateSummary = &agent.WorkingStateSummaryPolicy{MaxInputTokens: 60000, MaxSerializedBytes: 100000}
			if nativeRecovery {
				options.Runtime, err = kernel.New(kernel.Dependencies{Store: runs, ManagedRecoveryHost: store})
				if err != nil {
					t.Fatal(err)
				}
				options.Context.SummaryNativeRecovery = true
			}
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			second, err := session.Prompt(t.Context(), "second", secondText)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := requireStoredContextCheckpoint(t, store, second.Turn.ContextCheckpointID)
			if summaryCalls != 1 || len(client.requests) != 3 {
				root, _ := harness.TopLevelInvocation(second)
				observed, _ := options.Runtime.Load(t.Context(), root.ExecutionRefID)
				view, _ := agent.ViewState(observed)
				assessment, _ := runtimecontext.NewManager(runtimecontext.Dependencies{}).AssessModelRequest(t.Context(), client.requests[len(client.requests)-1], runtimecontext.ModelWindow{ContextTokens: 65536, ReservedOutputTokens: 128}, options.Session.Config.ContextPolicy)
				candidate, candidateErr := runtimecontext.NewManager(runtimecontext.Dependencies{}).CompactPortable(runtimecontext.PortableCompactionRequest{Previous: checkpoint, RunID: root.ExecutionRefID, Messages: runtimecontext.Materialize(checkpoint.Window), Policy: options.Session.Config.ContextPolicy, OmitExtract: true})
				t.Fatalf("expected one extra summary before business: summaries=%d requests=%d policy=%+v tools=%d aligned=%v entries=%d budget=%+v allowance=%+v assessment=%+v candidateRemoved=%d err=%v trace=%+v", summaryCalls, len(client.requests), view.WorkingStateSummary, len(client.requests[len(client.requests)-1].Tools), runtimecontext.SourceAlignedCheckpoint(checkpoint), len(checkpoint.Window.Entries), view.Budget, view.CallAllowance, assessment, candidate.RemovedMessages, candidateErr, checkpoint.Trace)
			}
			root, ok := harness.TopLevelInvocation(second)
			if !ok {
				t.Fatal("missing original root")
			}
			runtimeSnapshot, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(runtimeSnapshot)
			if err != nil || len(view.ModelInvocations) != 2 || view.ModelInvocations[0].Purpose != agent.ModelInvocationContextSummary || view.ModelInvocations[0].Status != agent.ModelInvocationConsumed || view.Budget.Usage.LLMCalls != 2 {
				t.Fatalf("summary did not use and consume the native ledger exactly once: %+v err=%v", view, err)
			}
			if invalid {
				if checkpoint.Window.WorkingState != nil {
					t.Fatal("invalid summary installed WorkingState")
				}
				return
			}
			state := runtimecontext.CloneWorkingState(checkpoint.Window.WorkingState)
			if state == nil || state.Version != 1 || state.Derivation == nil || !agent.VerifyWorkingStateSummaryOrigin(runtimeSnapshot, state, second.Turn.SessionID, options.Session.Actor, options.Session.Thread) {
				t.Fatalf("installed state has no matching native origin: %+v", state)
			}
			if name == "missing prior origin" {
				options.Store = missingSummaryOriginStore{Store: store, runID: root.ExecutionRefID}
				session, err = harness.OpenAgentSession(options)
				if err != nil {
					t.Fatal(err)
				}
			}
			third, err := session.Prompt(t.Context(), "third", texts["third"])
			if err != nil {
				t.Fatal(err)
			}
			updated := requireStoredContextCheckpoint(t, store, third.Turn.ContextCheckpointID)
			if name == "missing prior origin" {
				if summaryCalls != 1 || !reflect.DeepEqual(updated.Window.WorkingState, state) {
					t.Fatal("missing indexed origin was treated as permission to replace automatic state")
				}
				return
			}
			if summaryCalls != 2 || updated.Window.WorkingState == nil || updated.Window.WorkingState.Version != 2 || updated.Window.WorkingState.Derivation.InvocationID == state.Derivation.InvocationID {
				t.Fatalf("valid prior origin did not permit exactly one higher version: calls=%d state=%+v", summaryCalls, updated.Window.WorkingState)
			}
			before := len(client.requests)
			options.Session.Transcript = nil
			options.Session.Config.WorkingStateSummary = nil
			reopened, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := reopened.Resume(t.Context(), "second")
			if err != nil || len(client.requests) != before || replayed.Config.WorkingStateSummary == nil || !reflect.DeepEqual(requireStoredContextCheckpoint(t, store, replayed.Turn.ContextCheckpointID).Window.WorkingState, state) {
				t.Fatalf("new nil-policy handle changed the original frozen state: %v", err)
			}
			originalNative, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			options.Runtime, err = kernel.New(kernel.Dependencies{Store: runs, ManagedRecoveryHost: store})
			if err != nil {
				t.Fatal(err)
			}
			options.Context.SummaryNativeRecovery = !nativeRecovery
			retrospective, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := retrospective.Resume(t.Context(), "second"); !errors.Is(err, harness.ErrInvalidRequest) || len(client.requests) != before {
				t.Fatalf("new handle retroactively selected native summary recovery: %v", err)
			}
			retainedNative, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil || retainedNative.Run.Revision != originalNative.Run.Revision {
				t.Fatal("rejected recovery selector changed the original Run")
			}
		})
	}
}

func TestAgentSessionSummaryRejectsUnsupportedComposition(t *testing.T) {
	for _, mode := range []string{"managed", "both recovery modes", "missing summary host binding", "shared budget", "zero input", "zero bytes"} {
		t.Run(mode, func(t *testing.T) {
			client := &agentSessionModel{}
			options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
			options.Session.Config.WorkingStateSummary = summaryTestPolicy()
			switch mode {
			case "managed":
				options.Context.ManagedRecovery = true
			case "both recovery modes":
				options.Context.ManagedRecovery, options.Context.SummaryNativeRecovery = true, true
			case "missing summary host binding":
				options.Context.SummaryNativeRecovery = true
			case "shared budget":
				options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 3}
			case "zero input":
				options.Session.Config.WorkingStateSummary.MaxInputTokens = 0
			case "zero bytes":
				options.Session.Config.WorkingStateSummary.MaxSerializedBytes = 0
			}
			if _, err := harness.OpenAgentSession(options); !errors.Is(err, harness.ErrInvalidRequest) || len(client.requests) != 0 {
				t.Fatalf("unsupported summary composition was admitted: %v calls=%d", err, len(client.requests))
			}
		})
	}
}

func TestAgentSessionSummaryNativeRecoveryRequiresExactHostStore(t *testing.T) {
	store, runs := harness.NewMemoryStore(), memory.NewStore()
	client := &summarySessionModel{}
	options := agentSessionOptions(t, runs, store, client)
	var err error
	options.Runtime, err = kernel.New(kernel.Dependencies{Store: runs, ManagedRecoveryHost: store})
	if err != nil {
		t.Fatal(err)
	}
	options.Context.SummaryNativeRecovery = true
	options.Session.Config.WorkingStateSummary = summaryTestPolicy()
	if _, err := harness.OpenAgentSession(options); err != nil {
		t.Fatalf("matching explicit native summary recovery composition rejected: %v", err)
	}
	options.Store = harness.NewMemoryStore()
	if _, err := harness.OpenAgentSession(options); !errors.Is(err, harness.ErrInvalidRequest) || len(client.requests) != 0 {
		t.Fatalf("foreign Host Store borrowed native summary recovery: %v calls=%d", err, len(client.requests))
	}
}

func TestNativeSummaryValidatesBothConcreteBridgesActualStore(t *testing.T) {
	store, foreign := harness.NewMemoryStore(), harness.NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), ManagedRecoveryHost: store})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		return runtimecontext.ModelWindow{ContextTokens: 65536, ReservedOutputTokens: 128}, nil
	}
	for _, mode := range []string{"foreign bounded Store", "foreign summary Store"} {
		t.Run(mode, func(t *testing.T) {
			boundedStore, summaryStore := store, foreign
			if mode == "foreign bounded Store" {
				boundedStore, summaryStore = foreign, store
			}
			bounded, err := harness.NewBoundedContextAdapter(boundedStore, runtimecontext.NewManager(runtimecontext.Dependencies{}), resolve, nil)
			if err != nil {
				t.Fatal(err)
			}
			summary, err := harness.NewContextSummaryAdapter(summaryStore, runtime, bounded, resolve)
			if err != nil {
				t.Fatal(err)
			}
			dependencies := agent.Dependencies{Runtime: runtime, Model: &summarySessionModel{}, ContextSummary: summary, RunMiddleware: []plugin.RunMiddleware{bounded}}
			if _, err := agent.NewRunner(dependencies); err != nil {
				t.Fatalf("ordinary adapter construction was unnecessarily restricted: %v", err)
			}
			dependencies.SummaryNativeRecovery = true
			if _, err := agent.NewRunner(dependencies); !errors.Is(err, agent.ErrInvalidRequest) {
				t.Fatalf("native summary accepted a foreign actual bridge Store: %v", err)
			}
		})
	}
}

func TestAgentSessionRejectsExplicitHostSummaryDerivationBeforeAdmission(t *testing.T) {
	store, runs := harness.NewMemoryStore(), memory.NewStore()
	client := &agentSessionModel{}
	options := agentSessionOptions(t, runs, store, client)
	options.Session.Transcript = harness.TranscriptSourceFunc(func(_ context.Context, _ harness.HostRef, _ harness.HostRef, _ string) (*harness.ContextSeed, error) {
		seed := workingStateHistory("forged")
		seed.WorkingState = &runtimecontext.WorkingState{Version: 1, Source: runtimecontext.WorkingStateSource{
			CheckpointID: "ctxc_" + strings.Repeat("a", 32), ContentHash: strings.Repeat("b", 64), CoveredThroughSourceID: "forged", CoveredPathHash: strings.Repeat("c", 64)},
			Derivation: &runtimecontext.WorkingStateDerivation{Kind: runtimecontext.WorkingStateDerivationSummary, RunID: "original-run", InvocationID: "modelinv_" + strings.Repeat("d", 32), RequestHash: strings.Repeat("e", 64), PolicyVersion: agent.WorkingStateSummaryVersion}}
		return seed, nil
	})
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Prompt(t.Context(), "forged", "forged prompt"); !errors.Is(err, harness.ErrInvalidRequest) || len(client.requests) != 0 {
		t.Fatalf("Host forged runtime summary provenance: %v calls=%d", err, len(client.requests))
	}
	assertWorkingStateTurnAbsent(t, store, options.Session.HostThread, "forged")
}

type rootSummaryDifferentStarter struct {
	*agent.Runner
	actual *agent.WorkingStateSummaryPolicy
}

func (s rootSummaryDifferentStarter) StartRun(ctx context.Context, request agent.StartRequest) (kernel.Snapshot, error) {
	request.WorkingStateSummary = agent.CloneWorkingStateSummaryPolicy(s.actual)
	return s.PrepareRun(ctx, request)
}

func TestRootSummaryPolicyMatchesOriginalConfigBeforeProjectionAndResume(t *testing.T) {
	for _, mode := range []string{"dropped", "added", "widened"} {
		t.Run(mode, func(t *testing.T) {
			runs, store, client := memory.NewStore(), harness.NewMemoryStore(), &summarySessionModel{}
			options := agentSessionOptions(t, runs, store, client)
			manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
			bounded, err := harness.NewBoundedContextAdapter(store, manager, options.Context.ResolveWindow, nil)
			if err != nil {
				t.Fatal(err)
			}
			summary, err := harness.NewContextSummaryAdapter(store, options.Runtime, bounded, options.Context.ResolveWindow)
			if err != nil {
				t.Fatal(err)
			}
			loop, err := agent.NewRunner(agent.Dependencies{Runtime: options.Runtime, Model: client, ContextSummary: summary, RunMiddleware: []plugin.RunMiddleware{bounded}})
			if err != nil {
				t.Fatal(err)
			}
			original, actual := summaryTestPolicy(), summaryTestPolicy()
			switch mode {
			case "dropped":
				actual = nil
			case "added":
				original = nil
			case "widened":
				actual.MaxInputTokens++
			}
			host, err := harness.NewRunner(harness.Dependencies{Runtime: options.Runtime, Agent: rootSummaryDifferentStarter{Runner: loop, actual: actual}, Store: store, Context: manager})
			if err != nil {
				t.Fatal(err)
			}
			options.Session.Config.WorkingStateSummary = original
			session, err := host.OpenSession(options.Session)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.Prompt(t.Context(), "original", "original task"); !errors.Is(err, harness.ErrConflict) || len(client.requests) != 0 {
				t.Fatalf("different native summary policy projected: %v calls=%d", err, len(client.requests))
			}
			state, err := session.LoadState(t.Context(), "original")
			if err != nil || state.Turn.Status != harness.TurnAccepted {
				t.Fatalf("mismatched original admission changed: %+v err=%v", state.Turn, err)
			}
			rootID, err := harness.AgentInvocationID(state.Turn.ID, "original")
			if err != nil {
				t.Fatal(err)
			}
			root, err := store.GetInvocation(t.Context(), rootID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			options.Session.Transcript = nil
			options.Session.Config.WorkingStateSummary = actual // A new handle cannot replace the original policy.
			reopened, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.Resume(t.Context(), "original"); !errors.Is(err, harness.ErrConflict) || len(client.requests) != 0 {
				t.Fatalf("different original/native summary policy resumed: %v calls=%d", err, len(client.requests))
			}
			after, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil || before.Run.Revision != after.Run.Revision {
				t.Fatalf("policy rejection performed native work: before=%d after=%d err=%v", before.Run.Revision, after.Run.Revision, err)
			}
		})
	}
}
