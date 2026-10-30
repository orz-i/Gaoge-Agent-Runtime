package harness_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// A pre-stable persisted root with incomplete frozen actor/thread evidence
// must not reconstruct execution authority from Session.HostThread/Actor.
// The old path fell back to that Session and silently dispatched a new Run
// after RetryInvocation had already rotated the durable execution identity.
func TestRetryRootMissingFrozenIdentityDoesNotRotateOrDispatch(t *testing.T) {
	for _, testcase := range []string{"actor_missing", "thread_missing", "both_missing"} {
		t.Run(testcase, func(t *testing.T) {
			runtime := newFeatureInvocationRuntime(t)
			store := harness.NewMemoryStore()
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: unusedFeatureAgent{}, Store: store,
				Clock: featureInvocationClock{}, Teams: completedTeamFeature{runtime},
			})
			if err != nil {
				t.Fatal(err)
			}
			now := featureInvocationClock{}.Now()
			hostThread := harness.HostRef{Kind: testThreadKind, ID: "retry-frozen-thread"}
			sessionID, err := harness.SessionID(hostThread)
			if err != nil {
				t.Fatal(err)
			}
			turnID, err := harness.TurnID(sessionID, harness.HostRef{Kind: testContextHostKind, ID: "retry-frozen-turn"})
			if err != nil {
				t.Fatal(err)
			}
			seedFeatureInvocationEnvelope(t, store, sessionID, turnID, hostThread,
				harness.HostRef{Kind: testContextHostKind, ID: "retry-frozen-turn"},
				kernel.ActorRef{TenantID: testTenant, ActorID: testActor}, now)
			turn, err := store.GetTurn(t.Context(), turnID)
			if err != nil {
				t.Fatal(err)
			}
			turn.Status = harness.TurnFailed
			if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
				t.Fatal(err)
			}

			input := struct {
				Goal    string             `json:"goal"`
				Actor   kernel.ActorRef    `json:"actor"`
				Thread  kernel.ThreadRef   `json:"thread"`
				Mode    team.ExecutionMode `json:"mode"`
				Members []team.Member      `json:"members"`
			}{
				Goal: "frozen retry", Mode: team.ExecutionSequential,
				Members: []team.Member{{ID: retryTeamMemberID, Goal: retryTeamMemberGoal}},
			}
			if testcase == "thread_missing" {
				input.Actor = kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
			}
			if testcase == "actor_missing" {
				input.Thread = kernel.ThreadRef{Kind: testThreadKind, ID: "frozen-runtime-thread"}
			}
			payload, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(payload)
			invID := "hiv_retry_identity_" + testcase
			original := harness.Invocation{
				ID: invID, TurnID: turnID,
				CapabilityKey: harness.CapabilityTeam, DefinitionVersion: harness.RuntimeCapabilityVersion,
				ExecutionClass: harness.ExecutionTeam, Input: payload, InputHash: hex.EncodeToString(digest[:]),
				ExecutionRefID: harness.CapabilityExecutionRefID(invID),
				Status:         harness.InvocationFailed, Attempt: 1, Revision: 1,
				OutputRefs: []harness.HostRef{}, ErrorCode: "fixture.failed",
				CreatedAt: now, UpdatedAt: now,
			}
			if _, _, err = store.CreateInvocation(t.Context(), original); err != nil {
				t.Fatal(err)
			}
			_, err = runner.RetryInvocation(t.Context(), invID)
			if !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("missing frozen authority silently retried from Session: err=%v", err)
			}
			stored, err := store.GetInvocation(t.Context(), invID)
			if err != nil {
				t.Fatal(err)
			}
			nextTurn, err := store.GetTurn(t.Context(), turnID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Attempt != 1 || stored.ExecutionRefID != original.ExecutionRefID ||
				stored.Status != harness.InvocationFailed || stored.Revision != original.Revision ||
				nextTurn.Status != harness.TurnFailed || nextTurn.Revision != 2 {
				t.Fatalf("invalid frozen root identity rotated Retry CAS: invocation=%+v turn=%+v", stored, nextTurn)
			}
		})
	}
}
