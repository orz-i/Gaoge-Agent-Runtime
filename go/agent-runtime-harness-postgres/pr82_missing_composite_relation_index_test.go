package harnesspostgres_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

func TestPR82UnindexedCompositeCancellationPersistsUnknownAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		for _, mode := range []string{"absent", "different"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				assertPR82UnindexedCompositeCancellation(t, create(t), mode)
			})
		}
	}
}

func TestRealPostgresPR82UnindexedCompositeCancellationPersistsUnknown(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, _ := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"absent", "different"} {
		t.Run(mode, func(t *testing.T) {
			// The Turn/Run identities are distinct between cases.
			assertPR82UnindexedCompositeCancellation(t, store, mode)
		})
	}
}

func assertPR82UnindexedCompositeCancellation(t *testing.T, store harness.Store, mode string) {
	t.Helper()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	ag, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: pr72RealContextModel{}})
	if err != nil {
		t.Fatal(err)
	}
	var featureRelations runrelation.Recorder
	var harnessRelations harness.RunRelationSource
	if mode == "different" {
		featureRelations, err = runrelation.New(memory.NewRunRelationStore(), nil)
		if err != nil {
			t.Fatal(err)
		}
		harnessRelations, err = runrelation.New(memory.NewRunRelationStore(), nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	children := &pr82PhysicalMemberRunner{}
	coordinator, err := handoff.New(children)
	if err != nil {
		t.Fatal(err)
	}
	feature, err := team.NewRunner(team.Dependencies{Runtime: rt, Handoffs: coordinator, Relations: featureRelations})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: ag, Teams: feature, Store: store,
		Clock: pr72RealContextClock{}, Relations: harnessRelations,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, startErr := runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: "conversation", ID: "pr82-composite-index-" + mode},
			HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "pr82-composite-index-" + mode},
			Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread:     kernel.ThreadRef{Kind: "conversation", ID: "pr82-composite-index-" + mode},
			RequestID:  "pr82-composite-index-start", Goal: "cannot cancel unindexed remote child",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Mode:    team.ExecutionSequential,
		Members: []team.Member{{ID: "remote-member", Goal: "remote work is running"}},
	})
	if startErr != nil && !errors.Is(startErr, team.ErrMemberPending) {
		t.Fatalf("start: %v", startErr)
	}
	if started.Turn.Status != harness.TurnRunning || children.starts.Load() != 1 {
		t.Fatalf("fixture did not dispatch one running remote child: status=%s calls=%d", started.Turn.Status, children.starts.Load())
	}
	_, cancelErr := runner.Cancel(t.Context(), started.Turn.ID, "stop remote member")
	if !errors.Is(cancelErr, harness.ErrRuntimeCancellationUnverified) {
		t.Fatalf("unindexed composite cancellation returned %v", cancelErr)
	}
	items, err := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
	if err != nil || !harness.ActiveCancellationFence(items) {
		t.Fatalf("unverified cleanup lost its persisted fence: err=%v", err)
	}
	current, err := store.GetTurn(t.Context(), started.Turn.ID)
	if err != nil || current.Status == harness.TurnCancelled {
		t.Fatalf("remote child falsely reported cancelled: status=%s err=%v", current.Status, err)
	}
}
