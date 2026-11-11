package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

func TestRootNativeOutputRequiresFrozenKindActorThread(t *testing.T) {
	tests := []struct {
		name          string
		foreignKind   bool
		foreignActor  bool
		foreignThread bool
		wantConflict  bool
	}{
		{name: "matching_root"},
		{name: "different_run_kind", foreignKind: true, wantConflict: true},
		{name: "different_tenant_actor", foreignActor: true, wantConflict: true},
		{name: "different_thread", foreignThread: true, wantConflict: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner, store, turn, root, actor, thread := rootOutputOwnedNativeFixture(t)
			kind := agent.RunKind
			if tc.foreignKind {
				kind = team.RunKind
			}
			if tc.foreignActor {
				actor = kernel.ActorRef{TenantID: "foreign-tenant", ActorID: "foreign-actor"}
			}
			if tc.foreignThread {
				thread = kernel.ThreadRef{Kind: "conversation", ID: "different-thread"}
			}
			created, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{
				ID: root.ExecutionRefID, Kind: kind, Actor: actor, Thread: thread,
				RequestID: "native-owned", Goal: "answer", State: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.runtime.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
				Status: kernel.RunStatusCompleted, State: created.State,
				Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"foreign-or-owned-result"`)},
			})
			if err != nil {
				t.Fatal(err)
			}
			settled := root
			settled.Status = InvocationCompleted
			settled.UpdatedAt = settled.UpdatedAt.Add(time.Second)
			seedCtx := terminalFixtureContext(t.Context(), root, TurnCompleted)
			_, err = store.UpdateInvocation(seedCtx, settled, root.Revision)
			if err != nil {
				t.Fatal(err)
			}
			turn.Status = TurnCompleted
			turn.UpdatedAt = turn.UpdatedAt.Add(time.Second)
			turn, err = store.UpdateTurn(seedCtx, turn, turn.Revision)
			if err != nil {
				t.Fatal(err)
			}
			startedItem := Item{
				ID: "root-output-started", TurnID: turn.ID, Kind: ItemAgentMessage, Status: ItemStarted,
				HostRef:   &HostRef{Kind: "message", ID: "root-output-message"},
				Payload:   json.RawMessage(`{"role":"assistant"}`),
				CreatedAt: turn.CreatedAt, UpdatedAt: turn.UpdatedAt,
			}
			if _, _, err = store.AppendItem(t.Context(), startedItem); err != nil {
				t.Fatal(err)
			}
			for _, method := range []struct {
				name string
				run  func() (Snapshot, error)
			}{
				{"Load", func() (Snapshot, error) { return runner.Load(t.Context(), turn.ID) }},
				{"Refresh", func() (Snapshot, error) { return runner.Refresh(t.Context(), turn.ID) }},
				{"FinalizeHostOutput", func() (Snapshot, error) {
					return runner.FinalizeHostOutput(t.Context(), turn.ID)
				}},
			} {
				t.Run(method.name, func(t *testing.T) {
					result, loadErr := method.run()
					if tc.wantConflict {
						if !errors.Is(loadErr, ErrConflict) || result.Output != nil {
							t.Fatalf("root accepted unowned Run: output=%+v err=%v", result.Output, loadErr)
						}
						items, itemErr := store.ListItems(t.Context(), turn.ID, 0, defaultItemListLimit)
						if itemErr != nil {
							t.Fatal(itemErr)
						}
						for _, item := range items {
							// Refresh can legitimately repair original Invocation Items,
							// but an unowned physical Run must not project its own Run
							// Item or a terminal Host message into the original Turn.
							if item.Kind == ItemAgentRun || (item.Kind == ItemAgentMessage && item.Status == ItemCompleted) {
								t.Fatalf("unowned Run wrote terminal/physical fact: %+v", item)
							}
						}
						storedTurn, readErr := store.GetTurn(t.Context(), turn.ID)
						if readErr != nil || storedTurn.Revision != turn.Revision || storedTurn.Status != turn.Status {
							t.Fatalf("unowned Run changed original Turn: before=%+v after=%+v err=%v", turn, storedTurn, readErr)
						}
					} else if loadErr != nil || result.Output == nil ||
						string(result.Output.Content) != `"foreign-or-owned-result"` {
						t.Fatalf("valid native root result lost: output=%+v err=%v", result.Output, loadErr)
					}
				})
			}
		})
	}
}

func rootOutputOwnedNativeFixture(t *testing.T) (*Runner, *MemoryStore, Turn, Invocation, kernel.ActorRef, kernel.ThreadRef) {
	t.Helper()
	now := time.Date(2026, time.November, 1, 8, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "root-tenant", ActorID: "root-actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "root-native-thread"}
	session := Session{
		ID: "root-owned-session", HostThread: HostRef{Kind: thread.Kind, ID: thread.ID},
		Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err = store.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	turnID := "root-owned-turn"
	config, err := SealConfigSnapshot(turnID, ConfigSnapshot{AdmissionHash: strings.Repeat("a", 64)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	turn := Turn{
		ID: turnID, SessionID: session.ID, HostTurn: HostRef{Kind: "conversation_turn", ID: "host-turn"},
		ConfigSnapshotID: config.ID, Status: TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err = store.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	root, err := newAgentInvocation(turnID, "native-owned", "answer", actor, thread, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateInvocation(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	return &Runner{runtime: runtime, store: store, clock: systemClock{}}, store, turn, root, actor, thread
}

func TestRootOutputMissingFrozenIdentityFailsClosed(t *testing.T) {
	runner := newAcceptedStartupGapRunner(t) // intentionally no frozen Input
	root, err := runner.store.GetInvocation(t.Context(), "hiv_startup_gap")
	if err != nil {
		t.Fatal(err)
	}
	run, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: root.ExecutionRefID, Kind: agent.RunKind,
		Actor:     kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:    kernel.ThreadRef{Kind: "conversation", ID: "thread_startup_gap"},
		RequestID: "unverified-legacy", Goal: "unowned Run",
		State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.runtime.Apply(t.Context(), run.Run.ID, run.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: run.State,
		Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"unverified-result"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := runner.Load(context.Background(), root.TurnID); !errors.Is(err, ErrConflict) || got.Output != nil {
		t.Fatalf("missing original frozen root authority accepted physical result: %+v err=%v", got.Output, err)
	}
}

// Regression: a result-free failed foreign Run with the same
// physical ID must not be allowed to terminalize an Accepted root Turn.
func TestRootResultFreeForeignRunCannotProjectRootFailure(t *testing.T) {
	runner, store, turn, root, _, thread := rootOutputOwnedNativeFixture(t)
	physical, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: root.ExecutionRefID, Kind: team.RunKind,
		Actor:  kernel.ActorRef{TenantID: "other-tenant", ActorID: "other-user"},
		Thread: thread, RequestID: "foreign-work", Goal: "unrelated failed task", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runner.runtime.Apply(t.Context(), physical.Run.ID, physical.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: physical.State,
		ErrorCode: "foreign.task.failure", ErrorDetail: "failure in another tenant",
	})
	if err != nil || failed.Result != nil {
		t.Fatalf("fixture requires result-free foreign failed Run: %+v err=%v", failed, err)
	}
	result, refreshErr := runner.Refresh(t.Context(), turn.ID)
	afterTurn, turnErr := store.GetTurn(t.Context(), turn.ID)
	afterInvocation, invErr := store.GetInvocation(t.Context(), root.ID)
	if turnErr != nil || invErr != nil {
		t.Fatalf("read durable state: turn=%v invocation=%v", turnErr, invErr)
	}
	if afterTurn.Status != turn.Status || afterTurn.Revision != turn.Revision || afterInvocation.Status != root.Status || afterInvocation.Revision != root.Revision {
		t.Fatalf("RESULT-FREE FOREIGN RUN MUTATED ROOT: refreshErr=%v returned=%+v storedTurn=%+v storedInvocation=%+v", refreshErr, result.Turn, afterTurn, afterInvocation)
	}
	if !errors.Is(refreshErr, ErrConflict) {
		t.Fatalf("result-free foreign Run accepted or other failure: %v", refreshErr)
	}
	items, err := store.ListItems(t.Context(), turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind == ItemAgentRun || (item.Kind == ItemInvocation && item.Status == ItemFailed) {
			t.Fatalf("unowned failure projected in Item: %+v", item)
		}
	}
}

// Regression: if all four identity fields match but the original
// frozen Goal differs, is a physical Result still incorrectly attributed?
func TestRootMatchingKindActorThreadButDifferentGoalFailsClosed(t *testing.T) {
	runner, store, turn, root, actor, thread := rootOutputOwnedNativeFixture(t)
	physical, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: root.ExecutionRefID, Kind: agent.RunKind, Actor: actor, Thread: thread,
		RequestID: "native-owned", Goal: "different task from frozen answer", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	done, err := runner.runtime.Apply(t.Context(), physical.Run.ID, physical.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: physical.State,
		Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"OTHER-TASK-RESULT"`)},
	})
	if err != nil || done.Result == nil {
		t.Fatalf("setup=%+v err=%v", done, err)
	}
	refreshed, refreshErr := runner.Refresh(t.Context(), turn.ID)
	afterTurn, readErr := store.GetTurn(t.Context(), turn.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !errors.Is(refreshErr, ErrConflict) || refreshed.Output != nil || afterTurn.Status != turn.Status {
		t.Fatalf("UNFROZEN GOAL ACCEPTED: err=%v output=%+v status=%s originally=%s", refreshErr, refreshed.Output, afterTurn.Status, turn.Status)
	}
}

// Regression: a foreign failed Run has no Result, yet terminal Host
// ACK must not be published solely because the ID matches.
func TestRootResultFreeForeignRunCannotFinalizeHostMessage(t *testing.T) {
	runner, store, turn, root, _, thread := rootOutputOwnedNativeFixture(t)
	invocation := root
	invocation.Status = InvocationFailed
	invocation.UpdatedAt = invocation.UpdatedAt.Add(time.Second)
	seedCtx := terminalFixtureContext(t.Context(), invocation, TurnFailed)
	updated, err := store.UpdateInvocation(seedCtx, invocation, invocation.Revision)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = TurnFailed
	turn.UpdatedAt = turn.UpdatedAt.Add(time.Second)
	turn, err = store.UpdateTurn(seedCtx, turn, turn.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.AppendItem(t.Context(), Item{
		ID: "pending-host-output", TurnID: turn.ID, Kind: ItemAgentMessage, Status: ItemStarted,
		HostRef: &HostRef{Kind: "message", ID: "host-pending"}, Payload: json.RawMessage(`{"role":"assistant"}`),
		CreatedAt: turn.CreatedAt, UpdatedAt: turn.UpdatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: updated.ExecutionRefID, Kind: team.RunKind,
		Actor: kernel.ActorRef{TenantID: "other-tenant", ActorID: "other-actor"}, Thread: thread,
		RequestID: "foreign-failure", Goal: "unrelated failed run", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.runtime.Apply(t.Context(), foreign.Run.ID, foreign.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: foreign.State, ErrorCode: "foreign.failure",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, ackErr := runner.FinalizeHostOutput(t.Context(), turn.ID)
	items, itemErr := store.ListItems(t.Context(), turn.ID, 0, 500)
	if itemErr != nil {
		t.Fatal(itemErr)
	}
	for _, item := range items {
		if item.Kind == ItemAgentMessage && item.Status == ItemFailed {
			t.Fatalf("UNOWNED TERMINAL HOST MESSAGE PERSISTED: ackErr=%v item=%+v", ackErr, item)
		}
	}
	if !errors.Is(ackErr, ErrConflict) {
		t.Fatalf("foreign Run was accepted as ACK authority: err=%v", ackErr)
	}
}

// A genuinely owned physical root failure has no Result but can still commit
// its failed Turn/Invocation and complete the original Host-message ACK.
func TestRootOwnedResultFreeFailureStillProjectsAndFinalizes(t *testing.T) {
	runner, store, turn, root, actor, thread := rootOutputOwnedNativeFixture(t)
	physical, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: root.ExecutionRefID, Kind: agent.RunKind, Actor: actor, Thread: thread,
		RequestID: "native-owned", Goal: "answer", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.runtime.Apply(t.Context(), physical.Run.ID, physical.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: physical.State,
		ErrorCode: "owned.failure", ErrorDetail: "original execution failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.AppendItem(t.Context(), Item{
		ID: "original-host-output-start", TurnID: turn.ID, Kind: ItemAgentMessage, Status: ItemStarted,
		HostRef: &HostRef{Kind: "message", ID: "original-owner-message"},
		Payload: json.RawMessage(`{"role":"assistant"}`), CreatedAt: turn.CreatedAt, UpdatedAt: turn.UpdatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := runner.Refresh(t.Context(), turn.ID)
	if err != nil || observed.Turn.Status != TurnFailed || observed.Output != nil {
		t.Fatalf("real result-free failure could not project: state=%s output=%+v err=%v", observed.Turn.Status, observed.Output, err)
	}
	persisted, err := store.GetInvocation(t.Context(), root.ID)
	if err != nil || persisted.Status != InvocationFailed || persisted.ErrorCode != "owned.failure" {
		t.Fatalf("real failure lost Invocation evidence: %+v err=%v", persisted, err)
	}
	for range 2 {
		ack, ackErr := runner.FinalizeHostOutput(t.Context(), turn.ID)
		if ackErr != nil || ack.Turn.Status != TurnFailed || ack.Output != nil {
			t.Fatalf("original failed Host ACK not idempotent: turn=%s output=%+v err=%v", ack.Turn.Status, ack.Output, ackErr)
		}
	}
	items, err := store.ListItems(t.Context(), turn.ID, 0, defaultItemListLimit)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range items {
		if item.Kind != ItemAgentMessage || item.Status != ItemFailed {
			continue
		}
		count++
		var payload modelTimelinePayload
		if err = json.Unmarshal(item.Payload, &payload); err != nil || payload.ContentHash != "" || payload.ContentBytes != 0 {
			t.Fatalf("result-free owner ACK carried content: payload=%+v err=%v", payload, err)
		}
	}
	if count != 1 {
		t.Fatalf("original Host ACK replay emitted %d terminal messages, want 1", count)
	}
}

// Root ResumeTurn must not dispatch a Model/Tool continuation from a foreign
// physical Run whose ID, Kind and Actor happen to match but its frozen Thread
// or Goal does not.
func TestResumeTurnRejectsWrongFrozenThreadOrGoalBeforeAgentDispatch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wrongThread bool
		wrongGoal   bool
	}{
		{name: "wrong_thread", wrongThread: true},
		{name: "wrong_goal", wrongGoal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, store, turn, root, actor, thread := rootOutputOwnedNativeFixture(t)
			agentFeature := &pr82UnexpectedNativeAgent{runtime: runner.runtime}
			runner.agent = agentFeature
			if tc.wrongThread {
				thread.ID = "other-thread"
			}
			goal := "answer"
			if tc.wrongGoal {
				goal = "different-physical-task"
			}
			_, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{
				ID: root.ExecutionRefID, Kind: agent.RunKind, Actor: actor, Thread: thread,
				RequestID: "native-owned", Goal: goal, State: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, resumeErr := runner.ResumeTurn(t.Context(), turn.ID)
			if !errors.Is(resumeErr, ErrConflict) || agentFeature.resumes != 0 {
				t.Fatalf("foreign root reached Agent.Resume: calls=%d err=%v", agentFeature.resumes, resumeErr)
			}
			current, err := store.GetInvocation(t.Context(), root.ID)
			if err != nil || current.Revision != root.Revision || current.Status != root.Status {
				t.Fatalf("foreign root changed Invocation: %+v err=%v", current, err)
			}
		})
	}
}
