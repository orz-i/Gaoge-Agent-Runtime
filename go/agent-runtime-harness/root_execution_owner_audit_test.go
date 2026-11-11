package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Regression: root cancellation provenance: a foreign Run with
// colliding ID and differing frozen Goal must not be physically controlled
// before the owner has been proved. Physical control requires the frozen root owner.
func TestRootCancelMustNotControlForeignPhysicalRun(t *testing.T) {
	runner, store, turn, root, actor, thread := rootOutputOwnedNativeFixture(t)
	runner.cancellation = runner.runtime
	foreign, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: root.ExecutionRefID, Kind: agent.RunKind, Actor: actor, Thread: thread,
		RequestID: "foreign-work", Goal: "foreign user's task instead of answer", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, cancelErr := runner.Cancel(t.Context(), turn.ID, "cancel original task")
	observed, err := runner.runtime.Load(t.Context(), foreign.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.GetTurn(t.Context(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("FOREIGN PHYSICAL RUN CANCELLED: status=%s cancelErr=%v rootTurn=%s", observed.Run.Status, cancelErr, persisted.Status)
	}
	if cancelErr == nil || persisted.Status == TurnCancelled {
		t.Fatalf("UNVERIFIED ROOT CANCEL ACCEPTED: err=%v turn=%s", cancelErr, persisted.Status)
	}
}

// Regression: the physical request identity differs
// even though Goal/Kind/Actor/Thread and Run ID are identical. Only the
// already admitted original request may project a physical result.
func TestRootRejectsWrongOriginalRequestID(t *testing.T) {
	runner, store, turn, root, actor, thread := rootOutputOwnedNativeFixture(t)
	unrelated, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{
		ID: root.ExecutionRefID, Kind: agent.RunKind, Actor: actor, Thread: thread,
		RequestID: "unrelated-request-not-native-owned", Goal: "answer", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.runtime.Apply(t.Context(), unrelated.Run.ID, unrelated.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: unrelated.State,
		Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"UNRELATED-REQUEST-RESULT"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, refreshErr := runner.Refresh(t.Context(), turn.ID)
	persisted, err := store.GetTurn(t.Context(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != turn.Status || persisted.Revision != turn.Revision || result.Output != nil || !errors.Is(refreshErr, ErrConflict) {
		t.Fatalf("WRONG ORIGINAL REQUEST PROJECTED: result=%+v err=%v persisted=%+v original=%+v", result.Output, refreshErr, persisted, turn)
	}
}

// The original RequestID is bound by InvocationID only for attempt 1.
// Explicit RetryInvocation rotates Run ID and deterministically derives a
// fresh RequestID; that real positive path must remain authorized.
func TestRootRetryRequestIDUsesFrozenAttemptDerivation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		wrong bool
	}{
		{name: "matching_retry_request"},
		{name: "foreign_retry_request", wrong: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, store, turn, first, actor, thread := rootOutputOwnedNativeFixture(t)
			first.Status = InvocationFailed
			seedCtx := terminalFixtureContext(t.Context(), first, TurnFailed)
			first, err := store.UpdateInvocation(seedCtx, first, first.Revision)
			if err != nil {
				t.Fatal(err)
			}
			turn.Status = TurnFailed
			turn, err = store.UpdateTurn(seedCtx, turn, turn.Revision)
			if err != nil {
				t.Fatal(err)
			}
			secondRef := invocationExecutionRefID(first.ID, first.ExecutionClass, first.Attempt+1)
			second, err := store.RetryInvocation(t.Context(), first.ID, first.Revision, secondRef, time.Now().UTC())
			if err != nil || second.Attempt != 2 {
				t.Fatalf("retry rotation failed: attempt=%d err=%v", second.Attempt, err)
			}
			requestID := stableID("hretry", second.ID, attemptString(second.Attempt))
			if tc.wrong {
				requestID = "foreign-retry-request"
			}
			physical, err := runner.runtime.Create(t.Context(), kernel.CreateRequest{
				ID: second.ExecutionRefID, Kind: agent.RunKind, Actor: actor, Thread: thread,
				RequestID: requestID, Goal: "answer", State: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.runtime.Apply(t.Context(), physical.Run.ID, physical.Run.Revision, kernel.Mutation{
				Status: kernel.RunStatusCompleted, State: physical.State,
				Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"retried-answer"`)},
			})
			if err != nil {
				t.Fatal(err)
			}
			observed, refreshErr := runner.Refresh(t.Context(), turn.ID)
			current, err := store.GetTurn(t.Context(), turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wrong {
				if !errors.Is(refreshErr, ErrConflict) || current.Status != TurnRunning || observed.Output != nil {
					t.Fatalf("foreign retry request accepted: turn=%s output=%+v err=%v", current.Status, observed.Output, refreshErr)
				}
				return
			}
			if refreshErr != nil || current.Status != TurnCompleted || observed.Output == nil || string(observed.Output.Content) != `"retried-answer"` {
				t.Fatalf("valid retry lost: turn=%s output=%+v err=%v", current.Status, observed.Output, refreshErr)
			}
		})
	}
}
