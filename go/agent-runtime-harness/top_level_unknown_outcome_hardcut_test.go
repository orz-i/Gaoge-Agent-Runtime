package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

type unknownOutcomeFeature struct {
	unusedFeatureAgent
	runtime       *kernel.Runtime
	starts        int
	firstNoEffect bool
	outcome       string
}

func (f *unknownOutcomeFeature) start(ctx context.Context, id string, kind kernel.RunKind, actor kernel.ActorRef, thread kernel.ThreadRef, requestID, goal string) (kernel.Snapshot, error) {
	f.starts++
	if f.firstNoEffect && f.starts == 1 {
		return kernel.Snapshot{}, harness.FeatureStartNoEffect(errors.New("first attempt rejected before dispatch"))
	}
	if f.firstNoEffect {
		created, err := f.runtime.Create(ctx, kernel.CreateRequest{
			ID: id, Kind: kind, Actor: actor, Thread: thread, RequestID: requestID, Goal: goal, State: json.RawMessage(`{}`),
		})
		if err != nil {
			return kernel.Snapshot{}, err
		}
		if _, err = f.runtime.Cancel(ctx, id, created.Run.Revision, "second attempt committed cancellation"); err != nil {
			return kernel.Snapshot{}, err
		}
	}
	// A Host feature may have performed a remote effect but returned no local
	// physical receipt. Never treat the already-cancelled same-ID Kernel Run
	// as evidence that the separate dispatch grant settled.
	switch f.outcome {
	case "plain-error":
		return kernel.Snapshot{}, errors.New("remote completion unverified")
	case "empty-success":
		return kernel.Snapshot{}, nil
	default:
		return kernel.Snapshot{}, fmt.Errorf("remote outcome unknown: %w", kernel.ErrConflict)
	}
}
func (f *unknownOutcomeFeature) StartRun(ctx context.Context, r agent.StartRequest) (kernel.Snapshot, error) {
	return f.start(ctx, r.ID, agent.RunKind, r.Actor, r.Thread, r.RequestID, r.Goal)
}

type unknownOutcomeTeam struct{ *unknownOutcomeFeature }

func (f unknownOutcomeTeam) StartRun(ctx context.Context, r team.StartRequest) (kernel.Snapshot, error) {
	return f.start(ctx, r.ID, team.RunKind, r.Actor, r.Thread, r.RequestID, r.Goal)
}
func (unknownOutcomeTeam) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrConflict
}

type unknownOutcomeWorkflow struct{ *unknownOutcomeFeature }

func (f unknownOutcomeWorkflow) StartRun(ctx context.Context, r workflow.StartRequest) (kernel.Snapshot, error) {
	return f.start(ctx, r.ID, workflow.RunKind, r.Actor, r.Thread, r.RequestID, r.Goal)
}
func (unknownOutcomeWorkflow) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrConflict
}
func (unknownOutcomeWorkflow) ResolveWait(context.Context, string, uint64, json.RawMessage) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrConflict
}

type unknownOutcomeGroupChat struct{ *unknownOutcomeFeature }

func (f unknownOutcomeGroupChat) StartRun(ctx context.Context, r groupchat.StartRequest) (kernel.Snapshot, error) {
	return f.start(ctx, r.ID, groupchat.RunKind, r.Actor, r.Thread, r.RequestID, r.Goal)
}
func (unknownOutcomeGroupChat) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrConflict
}

func startUnknownOutcomeRoot(ctx context.Context, runner *harness.Runner, kind string, request harness.StartRequest) (harness.Snapshot, error) {
	switch kind {
	case "agent":
		return runner.StartAgentTurn(ctx, request)
	case "team":
		return runner.StartTeamTurn(ctx, harness.TeamTurnRequest{StartRequest: request, Mode: team.ExecutionSequential, Members: []team.Member{{ID: "writer", Goal: "write"}}})
	case "workflow":
		return runner.StartWorkflowTurn(ctx, harness.WorkflowTurnRequest{StartRequest: request, Input: json.RawMessage(`{}`)})
	case "groupchat":
		request.Config.Roles = []harness.RoleSnapshot{{ID: "author-role", Revision: 1, Name: "Author", Model: "model"}}
		return runner.StartGroupChatTurn(ctx, harness.GroupChatTurnRequest{StartRequest: request,
			SpeakerPolicy: groupchat.SpeakerDirected, Participants: []groupchat.Participant{{ID: "author"}},
			Speakers: []harness.GroupChatSpeakerSelection{{ParticipantID: "author", RoleID: "author-role", AuthorKind: "agent", AuthorID: "author", AuthorRevision: "1", AuthorName: "Author"}}, MaxUtterances: 1})
	}
	return harness.Snapshot{}, errors.New("invalid test class")
}
func rootKindAndCapability(kind string) (kernel.RunKind, string) {
	switch kind {
	case "agent":
		return agent.RunKind, harness.CapabilityAgent
	case "team":
		return team.RunKind, harness.CapabilityTeam
	case "workflow":
		return workflow.RunKind, harness.CapabilityWorkflow
	case "groupchat":
		return groupchat.RunKind, harness.CapabilityGroupChat
	}
	panic("invalid test class")
}
func newUnknownOutcomeRunner(t *testing.T, rt *kernel.Runtime, f *unknownOutcomeFeature, store harness.Store) *harness.Runner {
	t.Helper()
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: f, Store: store, Clock: featureInvocationClock{},
		Teams: unknownOutcomeTeam{f}, Workflows: unknownOutcomeWorkflow{f}, GroupChats: unknownOutcomeGroupChat{f}})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
func TestHardcutUnknownTopLevelStartCannotClaimCancelledSuccess(t *testing.T) {
	for _, kind := range []string{"agent", "team", "workflow", "groupchat"} {
		for _, outcome := range []string{"wrapped-conflict", "plain-error", "empty-success"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				rt := newFeatureInvocationRuntime(t)
				store := harness.NewMemoryStore()
				feature := &unknownOutcomeFeature{runtime: rt, outcome: outcome}
				runner := newUnknownOutcomeRunner(t, rt, feature, store)
				request := testStartRequest()
				request.RequestID = "unknown-" + kind
				request.HostThread.ID = "unknown-thread-" + kind
				request.Thread.ID = request.HostThread.ID
				request.HostTurn.ID = "unknown-turn-" + kind
				sid, err := harness.SessionID(request.HostThread)
				if err != nil {
					t.Fatal(err)
				}
				tid, err := harness.TurnID(sid, request.HostTurn)
				if err != nil {
					t.Fatal(err)
				}
				runKind, capability := rootKindAndCapability(kind)
				invID, err := harness.InvocationID(tid, "", capability, request.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				runID := harness.CapabilityExecutionRefID(invID)
				if kind == "agent" {
					runID = harness.AgentExecutionRefID(invID)
				}
				created, err := rt.Create(t.Context(), kernel.CreateRequest{ID: runID, Kind: runKind, Actor: request.Actor, Thread: request.Thread, RequestID: request.RequestID, Goal: request.Goal, State: json.RawMessage(`{}`)})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = rt.Cancel(t.Context(), runID, created.Run.Revision, "external cancellation"); err != nil {
					t.Fatal(err)
				}
				_, err = startUnknownOutcomeRoot(t.Context(), runner, kind, request)
				items, readErr := store.ListItems(t.Context(), tid, 0, 500)
				if readErr != nil {
					t.Fatal(readErr)
				}
				pending := harness.PendingExecutionDispatch(items, invID)
				if !pending {
					t.Fatalf("%s fixture missing unknown-effect dispatch grant", kind)
				}
				if !errors.Is(err, harness.ErrExecutionOutcomeUnknown) {
					t.Fatalf("%s suppressed unresolved physical Start despite live dispatch grant: %v", kind, err)
				}
				refreshed, refreshErr := runner.Refresh(t.Context(), tid)
				if !errors.Is(refreshErr, harness.ErrExecutionOutcomeUnknown) || refreshed.Turn.Status == harness.TurnCancelled {
					t.Errorf("%s/%s Refresh misreported unresolved physical Start: status=%s err=%v", kind, outcome, refreshed.Turn.Status, refreshErr)
				}
				persisted, readErr := store.GetTurn(t.Context(), tid)
				if readErr != nil || persisted.Status == harness.TurnCancelled {
					t.Errorf("%s/%s Refresh committed terminal state despite unresolved physical dispatch: status=%s err=%v", kind, outcome, persisted.Status, readErr)
				}
			})
		}
	}
}
func TestHardcutUnknownTopLevelRetryCannotClaimCancelledSuccess(t *testing.T) {
	for _, kind := range []string{"agent", "team", "workflow", "groupchat"} {
		for _, outcome := range []string{"wrapped-conflict", "plain-error", "empty-success"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				rt := newFeatureInvocationRuntime(t)
				store := harness.NewMemoryStore()
				feature := &unknownOutcomeFeature{runtime: rt, firstNoEffect: true, outcome: outcome}
				runner := newUnknownOutcomeRunner(t, rt, feature, store)
				req := testStartRequest()
				req.HostThread.ID = "retry-thread-" + kind
				req.Thread.ID = req.HostThread.ID
				req.HostTurn.ID = "retry-turn-" + kind
				req.RequestID = "retry-" + kind
				first, firstErr := startUnknownOutcomeRoot(t.Context(), runner, kind, req)
				if !errors.Is(firstErr, harness.ErrExecutionNoEffect) || first.Turn.Status != harness.TurnFailed {
					t.Fatalf("%s initial no-effect fixture invalid: status=%s err=%v", kind, first.Turn.Status, firstErr)
				}
				root, ok := harness.TopLevelInvocation(first)
				if !ok {
					t.Fatal("missing root")
				}
				if feature.starts != 1 {
					t.Fatalf("first effect calls=%d", feature.starts)
				}
				_, retryErr := runner.RetryInvocation(t.Context(), root.ID)
				if feature.starts != 2 {
					t.Fatalf("retry did not reach physical adapter, calls=%d err=%v", feature.starts, retryErr)
				}
				if !errors.Is(retryErr, harness.ErrExecutionOutcomeUnknown) {
					t.Fatalf("%s retry swallowed unknown effect under cancelled physical Run: %v", kind, retryErr)
				}
				items, listErr := store.ListItems(t.Context(), first.Turn.ID, 0, 500)
				if listErr != nil || !harness.PendingExecutionDispatch(items, root.ID) {
					t.Fatalf("%s retry lost unknown-effect dispatch receipt: err=%v", kind, listErr)
				}
				refreshed, refreshErr := runner.Refresh(t.Context(), first.Turn.ID)
				if !errors.Is(refreshErr, harness.ErrExecutionOutcomeUnknown) || refreshed.Turn.Status == harness.TurnCancelled {
					t.Errorf("%s retry Refresh misreported unknown physical effect: status=%s err=%v", kind, refreshed.Turn.Status, refreshErr)
				}
			})
		}
	}
}

// A Start whose physical grant was rejected BEFORE invoking any executor is
// different from a dispatched Feature returning no receipt. Only an original
// Agent first Attempt may reconcile its already-cancelled, identity-verified
// Kernel Run as an idempotent cancellation success. Composite starts cannot
// reinterpret the same pre-dispatch conflict as an Agent result.
type rejectTopLevelPhysicalGrantStore struct{ harness.Store }

func (s *rejectTopLevelPhysicalGrantStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if item.ParentItemID == harness.ExecutionDispatchParentItemID {
		// Inject a synthetic Kernel conflict at a pre-dispatch boundary so
		// no Feature adapter was invoked and no dispatch grant was persisted.
		return harness.Item{}, false, kernel.ErrConflict
	}
	return s.Store.AppendItem(ctx, item)
}

func TestHardcutCancelledNoGrantOnlyInitialAgentMayReconcile(t *testing.T) {
	for _, kind := range []string{"agent", "team", "workflow", "groupchat"} {
		t.Run(kind, func(t *testing.T) {
			rt := newFeatureInvocationRuntime(t)
			store := &rejectTopLevelPhysicalGrantStore{Store: harness.NewMemoryStore()}
			feature := &unknownOutcomeFeature{runtime: rt}
			nativeAgent, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: directModel{}})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: nativeAgent, Store: store, Clock: featureInvocationClock{},
				Teams: unknownOutcomeTeam{feature}, Workflows: unknownOutcomeWorkflow{feature}, GroupChats: unknownOutcomeGroupChat{feature}})
			if err != nil {
				t.Fatal(err)
			}
			req := testStartRequest()
			req.RequestID = "cancel-before-grant-" + kind
			req.HostThread.ID = "cancel-before-grant-thread-" + kind
			req.Thread.ID = req.HostThread.ID
			req.HostTurn.ID = "cancel-before-grant-turn-" + kind
			sid, err := harness.SessionID(req.HostThread)
			if err != nil {
				t.Fatal(err)
			}
			tid, err := harness.TurnID(sid, req.HostTurn)
			if err != nil {
				t.Fatal(err)
			}
			runKind, capability := rootKindAndCapability(kind)
			invID, err := harness.InvocationID(tid, "", capability, req.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			runID := harness.CapabilityExecutionRefID(invID)
			if kind == "agent" {
				runID = harness.AgentExecutionRefID(invID)
			}
			created, err := rt.Create(t.Context(), kernel.CreateRequest{ID: runID, Kind: runKind, Actor: req.Actor, Thread: req.Thread,
				RequestID: req.RequestID, Goal: req.Goal, State: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = rt.Cancel(t.Context(), runID, created.Run.Revision, "original physical cancellation"); err != nil {
				t.Fatal(err)
			}
			observed, startErr := startUnknownOutcomeRoot(t.Context(), runner, kind, req)
			if kind == "agent" {
				// The first projection can itself encounter a CAS conflict after
				// committing terminal state. It must remain recoverable and must
				// never be classified as an unknown physical dispatch.
				if startErr != nil && !errors.Is(startErr, kernel.ErrConflict) {
					t.Fatalf("original Agent pre-grant conflict reclassified as unknown: %v", startErr)
				}
				recovered, recoverErr := runner.Refresh(t.Context(), tid)
				if recoverErr != nil || recovered.Turn.Status != harness.TurnCancelled {
					t.Fatalf("original first Agent did not converge to verified cancellation: status=%s err=%v", recovered.Turn.Status, recoverErr)
				}
				if startErr == nil && observed.Turn.Status != harness.TurnCancelled {
					t.Fatalf("successful Agent return did not include cancelled state: %s", observed.Turn.Status)
				}
			} else if !errors.Is(startErr, kernel.ErrConflict) {
				t.Fatalf("%s borrowed first Agent cancelled success exception, err=%v", kind, startErr)
			}
			if feature.starts != 0 {
				t.Fatalf("%s dispatched custom physical Feature without grant: calls=%d", kind, feature.starts)
			}
			items, readErr := store.ListItems(t.Context(), tid, 0, 500)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if harness.PendingExecutionDispatch(items, invID) {
				t.Fatal("a rejected grant was committed as pending")
			}
		})
	}
}
