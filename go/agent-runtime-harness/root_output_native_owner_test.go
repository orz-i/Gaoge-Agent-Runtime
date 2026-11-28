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
)

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
	session := Session{ID: "root-owned-session", HostThread: HostRef{Kind: thread.Kind, ID: thread.ID}, Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now}
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
	turn := Turn{ID: turnID, SessionID: session.ID, HostTurn: HostRef{Kind: "conversation_turn", ID: "host-turn"}, ConfigSnapshotID: config.ID, Status: TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	root, err := newAgentInvocation(turnID, "native-owned", "answer", actor, thread, nil, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateInvocation(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	return &Runner{runtime: runtime, store: store, clock: systemClock{}}, store, turn, root, actor, thread
}
func TestRootOutputMissingFrozenIdentityFailsClosed(t *testing.T) {
	runner := newAcceptedStartupGapRunner(t)
	root, err := runner.store.GetInvocation(t.Context(), "hiv_startup_gap")
	if err != nil {
		t.Fatal(err)
	}
	run, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{ID: root.ExecutionRefID, Kind: agent.RunKind, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread_startup_gap"}, RequestID: "unverified-legacy", Goal: "unowned Run", State: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.runtime.Apply(t.Context(), run.Run.ID, run.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: run.State, Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"unverified-result"`)}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := runner.Load(context.Background(), root.TurnID); !errors.Is(err, ErrConflict) || got.Output != nil {
		t.Fatalf("missing original frozen root authority accepted physical result: %+v err=%v", got.Output, err)
	}
}

func TestRootMatchingKindActorThreadButDifferentGoalFailsClosed(t *testing.T) {
	runner, store, turn, root, actor, thread := rootOutputOwnedNativeFixture(t)
	physical, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{ID: root.ExecutionRefID, Kind: agent.RunKind, Actor: actor, Thread: thread, RequestID: "native-owned", Goal: "different task from frozen answer", State: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	done, err := runner.runtime.Apply(t.Context(), physical.Run.ID, physical.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: physical.State, Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"OTHER-TASK-RESULT"`)}})
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

func TestRootOwnedResultFreeFailureStillProjectsAndFinalizes(t *testing.T) {
	runner, store, turn, root, actor, thread := rootOutputOwnedNativeFixture(t)
	physical, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{ID: root.ExecutionRefID, Kind: agent.RunKind, Actor: actor, Thread: thread, RequestID: "native-owned", Goal: "answer", State: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.runtime.Apply(t.Context(), physical.Run.ID, physical.Run.Revision, kernel.Mutation{Status: kernel.RunStatusFailed, State: physical.State, ErrorCode: "owned.failure", ErrorDetail: "original execution failed"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.AppendItem(t.Context(), Item{ID: "original-host-output-start", TurnID: turn.ID, Kind: ItemAgentMessage, Status: ItemStarted, HostRef: &HostRef{Kind: "message", ID: "original-owner-message"}, Payload: json.RawMessage(`{"role":"assistant"}`), CreatedAt: turn.CreatedAt, UpdatedAt: turn.UpdatedAt})
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
	for range // Regression: if all four identity fields match but the original
	// frozen Goal differs, is a physical Result still incorrectly attributed?
	// A genuinely owned physical root failure has no Result but can still commit
	// its failed Turn/Invocation and complete the original Host-message ACK.
	// Root ResumeTurn must not dispatch a Model/Tool continuation from a foreign
	// physical Run whose ID, Kind and Actor happen to match but its frozen Thread
	// or Goal does not.
	2 {
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

func TestResumeTurnRejectsWrongFrozenThreadOrGoalBeforeAgentDispatch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wrongThread bool
		wrongGoal   bool
	}{{name: "wrong_thread", wrongThread: true}, {name: "wrong_goal", wrongGoal: true}} {
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
			_, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{ID: root.ExecutionRefID, Kind: agent.RunKind, Actor: actor, Thread: thread, RequestID: "native-owned", Goal: goal, State: json.RawMessage(`{}`)})
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
