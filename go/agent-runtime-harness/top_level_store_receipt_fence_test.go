package harness_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// A custom Store may violate its Create/Replay return contract even if it
// durably wrote the caller's original record. No returned authority may
// substitute another Session, Config or Turn before Invocation admission.
type topLevelReceiptTamperStore struct {
	harness.Store
	kind              string
	invocationCreates int
}

func (s *topLevelReceiptTamperStore) CreateSession(ctx context.Context, requested harness.Session) (harness.Session, bool, error) {
	got, fresh, err := s.Store.CreateSession(ctx, requested)
	if err != nil {
		return got, fresh, err
	}
	switch s.kind {
	case "session.id":
		got.ID = "hs_foreign"
	case "session.actor":
		got.Actor.ActorID = "foreign-actor"
	case "session.host":
		got.HostThread.ID = "foreign-thread"
	}
	return got, fresh, nil
}

func (s *topLevelReceiptTamperStore) PutConfigSnapshot(ctx context.Context, requested harness.ConfigSnapshot) (harness.ConfigSnapshot, bool, error) {
	got, fresh, err := s.Store.PutConfigSnapshot(ctx, requested)
	if err != nil {
		return got, fresh, err
	}
	switch s.kind {
	case "config.id":
		got.ID = "hc_foreign"
	case "config.turn":
		got.TurnID = "ht_foreign"
	case "config.hash":
		got.ContentHash = strings.Repeat("a", 64)
	}
	return got, fresh, nil
}

func (s *topLevelReceiptTamperStore) CreateTurn(ctx context.Context, requested harness.Turn) (harness.Turn, bool, error) {
	got, fresh, err := s.Store.CreateTurn(ctx, requested)
	if err != nil {
		return got, fresh, err
	}
	switch s.kind {
	case "turn.id":
		// Reproduce the independently audited P1: another valid-looking,
		// already persisted Turn is substituted as the successful return.
		foreign := got
		foreign.ID = "ht_other_committed_turn"
		foreign.HostTurn.ID = "foreign-turn"
		if _, _, err = s.Store.CreateTurn(ctx, foreign); err != nil {
			return harness.Turn{}, false, err
		}
		got = foreign
	case "turn.session":
		got.SessionID = "hs_foreign"
	case "turn.host":
		got.HostTurn.ID = "foreign-turn"
	case "turn.config":
		got.ConfigSnapshotID = "hc_foreign"
	case "turn.fresh-status":
		if fresh {
			got.Status = harness.TurnCompleted
		}
	}
	return got, fresh, nil
}

func (s *topLevelReceiptTamperStore) CreateInvocation(ctx context.Context, candidate harness.Invocation) (harness.Invocation, bool, error) {
	s.invocationCreates++
	return s.Store.CreateInvocation(ctx, candidate)
}

func newTopLevelReceiptRunner(t *testing.T, store harness.Store) (*harness.Runner, *rootEnvelopeCountedModel, *applicationCapabilityExecutorFixture) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &rootEnvelopeCountedModel{}
	loop, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: modelClient})
	if err != nil {
		t.Fatal(err)
	}
	application := &applicationCapabilityExecutorFixture{}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loop, Teams: completedTeamFeature{runtime: runtime},
		Workflows:    completedWorkflowFeature{runtime: runtime},
		Applications: application, Store: store, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, modelClient, application
}

func TestHardcutTopLevelStoreReceiptCannotRebindAdmission(t *testing.T) {
	for _, kind := range []string{
		"session.id", "session.actor", "session.host",
		"config.id", "config.turn", "config.hash",
		"turn.id", "turn.session", "turn.host", "turn.config", "turn.fresh-status",
	} {
		t.Run(kind, func(t *testing.T) {
			store := &topLevelReceiptTamperStore{Store: harness.NewMemoryStore(), kind: kind}
			runner, modelClient, application := newTopLevelReceiptRunner(t, store)
			snapshot, err := runner.StartAgentTurn(t.Context(), testStartRequest())
			if !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("untrusted %s result admitted Agent: status=%s err=%v", kind, snapshot.Turn.Status, err)
			}
			if store.invocationCreates != 0 || modelClient.calls.Load() != 0 || application.calls != 0 {
				t.Fatalf("untrusted %s result crossed effect boundary: invocations=%d model=%d application=%d",
					kind, store.invocationCreates, modelClient.calls.Load(), application.calls)
			}
		})
	}
}

func TestHardcutForeignCommittedTurnCannotStartOtherCapabilities(t *testing.T) {
	tests := []struct {
		name  string
		start func(context.Context, *harness.Runner) (harness.Snapshot, error)
	}{
		{name: "Agent", start: func(ctx context.Context, runner *harness.Runner) (harness.Snapshot, error) {
			return runner.StartAgentTurn(ctx, testStartRequest())
		}},
		{name: "Team", start: func(ctx context.Context, runner *harness.Runner) (harness.Snapshot, error) {
			return runner.StartTeamTurn(ctx, harness.TeamTurnRequest{StartRequest: testStartRequest(),
				Mode: team.ExecutionSequential, Members: []team.Member{{ID: "writer", Goal: "write"}}})
		}},
		{name: "Workflow", start: func(ctx context.Context, runner *harness.Runner) (harness.Snapshot, error) {
			return runner.StartWorkflowTurn(ctx, harness.WorkflowTurnRequest{StartRequest: testStartRequest()})
		}},
		{name: "Application", start: func(ctx context.Context, runner *harness.Runner) (harness.Snapshot, error) {
			return runner.StartApplicationTurn(ctx, harness.ApplicationTurnRequest{
				StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
				DefinitionVersion: testApplicationVersion,
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &topLevelReceiptTamperStore{Store: harness.NewMemoryStore(), kind: "turn.id"}
			runner, modelClient, application := newTopLevelReceiptRunner(t, store)
			snapshot, err := test.start(t.Context(), runner)
			if !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("foreign persisted Turn accepted: status=%s err=%v", snapshot.Turn.Status, err)
			}
			if store.invocationCreates != 0 || modelClient.calls.Load() != 0 || application.calls != 0 {
				t.Fatalf("foreign persisted Turn started effect: invocations=%d model=%d application=%d",
					store.invocationCreates, modelClient.calls.Load(), application.calls)
			}
		})
	}
}

func TestHardcutTopLevelReplayRejectsChangedStoreReceipt(t *testing.T) {
	store := &topLevelReceiptTamperStore{Store: harness.NewMemoryStore()}
	runner, modelClient, _ := newTopLevelReceiptRunner(t, store)
	first, err := runner.StartAgentTurn(t.Context(), testStartRequest())
	if err != nil || first.Turn.Status != harness.TurnCompleted || modelClient.calls.Load() != 1 {
		t.Fatalf("initial start: status=%s err=%v calls=%d", first.Turn.Status, err, modelClient.calls.Load())
	}
	store.kind = "turn.id"
	_, err = runner.StartAgentTurn(t.Context(), testStartRequest())
	if !errors.Is(err, harness.ErrConflict) || modelClient.calls.Load() != 1 || store.invocationCreates != 1 {
		t.Fatalf("invalid replay receipt admitted: err=%v calls=%d creates=%d", err, modelClient.calls.Load(), store.invocationCreates)
	}
}
