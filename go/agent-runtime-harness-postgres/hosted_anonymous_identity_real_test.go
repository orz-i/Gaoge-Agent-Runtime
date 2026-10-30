package harnesspostgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type hostedAnonymousRealClock struct{}

func (hostedAnonymousRealClock) Now() time.Time { return time.Now().UTC() }

// Two actual PostgreSQL connections must not observe an anonymous terminal
// as two independently settled native calls, nor turn a held model dispatch
// into fresh permission to call the same Provider after process restart.
func TestRealPostgresAnonymousHostedTerminalStaysHeldAfterRestart(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	second, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}

	const turnID = "hosted-anonymous-real-turn"
	const runID = "hosted-anonymous-real-run"
	now := time.Now().UTC()
	session := harness.Session{
		ID:         "hosted-anonymous-real-session",
		HostThread: harness.HostRef{Kind: "conversation", ID: "hosted-anonymous-real-thread"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Revision:   1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err = first.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	config, err := harness.SealConfigSnapshot(turnID, harness.ConfigSnapshot{
		SharedBudget: &budget.Limits{MaxHostedToolCalls: 2, MaxToolCalls: 2, MaxLLMCalls: 3},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = first.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = first.CreateTurn(t.Context(), harness.Turn{
		ID: turnID, SessionID: "hosted-anonymous-real-session",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "native-id-replay"},
		ConfigSnapshotID: config.ID, Status: harness.TurnRunning, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = first.CreateInvocation(t.Context(), harness.Invocation{
		ID: "hosted-anonymous-real-invocation", TurnID: turnID,
		ExecutionClass: harness.ExecutionAgent, CapabilityKey: harness.CapabilityAgent,
		ExecutionRefID: runID, Status: harness.InvocationRunning, Attempt: 1,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	shared, err := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{
		Store: first, Clock: hostedAnonymousRealClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{
		RunID: runID, InvocationID: "original-model-request",
		Messages:    []model.Message{{Role: model.RoleUser, Content: "Verify only identified native calls"}},
		HostedTools: []model.HostedTool{{Key: "native.search"}},
	}
	providerCalls := 0
	_, err = shared.Model(t.Context(), request, nil, func(_ context.Context, actual model.Request, _ model.StreamSink) (model.Response, error) {
		providerCalls++
		if actual.HostedToolCallLimit == nil || *actual.HostedToolCallLimit != 2 {
			t.Fatalf("physical provider was not bounded: %+v", actual)
		}
		return model.Response{Content: "repeated anonymous tool result", HostedToolCalls: []model.HostedToolCall{
			{ToolKey: "native.search", Status: "completed"},
			{ToolKey: "native.search", Status: "completed"},
		}}, nil
	})
	if !errors.Is(err, budget.ErrInvalidUsage) || providerCalls != 1 {
		t.Fatalf("anonymous terminal was settled as verifiable charge: err=%v providerCalls=%d", err, providerCalls)
	}

	prior, err := second.LoadBudget(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	held := prior.Reservations["model:"+request.InvocationID]
	view := prior.View("")
	if held.Status != budget.ReservationHeld || !held.Dispatched ||
		view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 2 ||
		view.Usage.LLMCalls != 0 || !view.UnknownUsage {
		t.Fatalf("cross-connection ledger fabricated settled hosted calls: held=%+v view=%+v", held, view)
	}
	for id := range prior.Reservations {
		if strings.HasPrefix(id, "hosted:") {
			t.Fatalf("anonymous physical results manufactured an individual hosted receipt: %s", id)
		}
	}
	restarted, err := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{
		Store: second, Clock: hostedAnonymousRealClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		providerCalls++
		return model.Response{Content: "unsafe repeat"}, nil
	})
	if !errors.Is(err, budget.ErrWaiting) || providerCalls != 1 {
		t.Fatalf("restart reissued unknown provider effects: err=%v providerCalls=%d", err, providerCalls)
	}
	after, err := first.LoadBudget(t.Context(), turnID)
	if err != nil || after.View("").Usage.HostedToolCalls != 0 ||
		after.View("").Reserved.HostedToolCalls != 2 || !after.View("").UnknownUsage {
		t.Fatalf("restart did not preserve unknown native budget: view=%+v err=%v", after.View(""), err)
	}
}
