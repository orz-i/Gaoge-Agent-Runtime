package harness_test

import (
	"context"
	"errors"
	"math/rand"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// These tests isolate Store return data without changing its actual rows.
// Create and Get are independently untrusted at the Host execution boundary.
type readPoisonStore struct {
	harness.Store
	poisonConfig     bool
	poisonInvocation bool
	invocationCalls  int
}

func (s *readPoisonStore) GetConfigSnapshot(ctx context.Context, id string) (harness.ConfigSnapshot, error) {
	value, err := s.Store.GetConfigSnapshot(ctx, id)
	if err == nil && s.poisonConfig {
		value.Model = "unapproved-provider"
	}
	return value, err
}

func (s *readPoisonStore) GetInvocation(ctx context.Context, id string) (harness.Invocation, error) {
	value, err := s.Store.GetInvocation(ctx, id)
	if err == nil && s.poisonInvocation {
		value.ExecutionRefID = "hr_other-execution"
	}
	return value, err
}

func (s *readPoisonStore) CreateInvocation(ctx context.Context, value harness.Invocation) (harness.Invocation, bool, error) {
	s.invocationCalls++
	return s.Store.CreateInvocation(ctx, value)
}

func newReadPoisonHarness(t *testing.T, store harness.Store, client model.Client) *harness.Runner {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	loop, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loop, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestHardcutCanonicalConfigReadCannotChangeFirstDispatch(t *testing.T) {
	for _, kind := range []string{"persisted-payload", "returned-invocation-index"} {
		t.Run(kind, func(t *testing.T) {
			store := &readPoisonStore{Store: harness.NewMemoryStore()}
			if kind == "persisted-payload" {
				store.poisonConfig = true
			} else {
				store.poisonInvocation = true
			}
			client := &configMutationCaptureModel{}
			runner := newReadPoisonHarness(t, store, client)
			_, err := runner.StartAgentTurn(t.Context(), testStartRequest())
			if !errors.Is(err, harness.ErrConflict) || len(client.requests) != 0 {
				t.Fatalf("%s accepted untrusted read before physical model: err=%v calls=%d", kind, err, len(client.requests))
			}
			if kind == "persisted-payload" && store.invocationCalls != 0 {
				t.Fatalf("poisoned config created Invocation before dispatch: %d", store.invocationCalls)
			}
		})
	}
}

func TestHardcutConfigReadAfterSuccessfulRunCannotAlterViewOrReplay(t *testing.T) {
	store := &readPoisonStore{Store: harness.NewMemoryStore()}
	client := &configMutationCaptureModel{}
	runner := newReadPoisonHarness(t, store, client)
	first, err := runner.StartAgentTurn(t.Context(), testStartRequest())
	if err != nil || first.Turn.Status != harness.TurnCompleted || len(client.requests) != 1 {
		t.Fatalf("first execution err=%v turn=%s calls=%d", err, first.Turn.Status, len(client.requests))
	}
	store.poisonConfig = true
	if _, err = runner.Load(t.Context(), first.Turn.ID); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("Load exposed corrupted frozen Config: %v", err)
	}
	if _, err = runner.StartAgentTurn(t.Context(), testStartRequest()); !errors.Is(err, harness.ErrConflict) || len(client.requests) != 1 {
		t.Fatalf("replay resumed using corrupted frozen Config: err=%v model_calls=%d", err, len(client.requests))
	}
}

// Seeded property-style mutation across independent first admissions. A
// stale claimed ContentHash must never authorize changed Model, ToolKeys or
// AdmissionHash even if the Store receives otherwise valid JSON.
func TestHardcutCanonicalConfigMutationProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20261104))
	for n := 0; n < 120; n++ {
		mutation := rng.Intn(3)
		store := &configMutationStore{Store: harness.NewMemoryStore()}
		store.mutate = func(c *harness.ConfigSnapshot) {
			switch mutation {
			case 0:
				c.Model = "other-model"
			case 1:
				c.ToolKeys = []string{"host.unapproved"}
			case 2:
				c.AdmissionHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}
		}
		client := &configMutationCaptureModel{}
		runner := newReadPoisonHarness(t, store, client)
		request := testStartRequest()
		request.RequestID = "stable-property"
		_, err := runner.StartAgentTurn(t.Context(), request)
		if !errors.Is(err, harness.ErrConflict) || store.invocationCreates != 0 || len(client.requests) != 0 {
			t.Fatalf("mutation sample %d kind %d crossed trust boundary: err=%v invocations=%d models=%d", n, mutation, err, store.invocationCreates, len(client.requests))
		}
	}
}
