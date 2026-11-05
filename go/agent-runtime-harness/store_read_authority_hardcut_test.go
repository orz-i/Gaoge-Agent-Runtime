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
)

// A Store is allowed to expose a previously committed foreign record only
// under that record's own key. The Host's requested key cannot be silently
// redirected on the read side, even when the returned record is otherwise
// structurally valid and backed by a real completed Kernel Run.
type readOwnerFaultStore struct {
	harness.Store
	mode           string
	foreignTurnID  string
	foreignSession string
	foreignConfig  string
}

func (s *readOwnerFaultStore) GetTurn(ctx context.Context, id string) (harness.Turn, error) {
	if s.mode == "redirect-turn" {
		return s.Store.GetTurn(ctx, s.foreignTurnID)
	}
	value, err := s.Store.GetTurn(ctx, id)
	if err != nil {
		return value, err
	}
	switch s.mode {
	case "foreign-session-link":
		value.SessionID = s.foreignSession
	case "foreign-config-link":
		value.ConfigSnapshotID = s.foreignConfig
	case "host-turn-rebind":
		value.HostTurn.ID = "silently-rebound-host-turn"
	}
	return value, nil
}

func (s *readOwnerFaultStore) GetSession(ctx context.Context, id string) (harness.Session, error) {
	if s.mode == "redirect-session" {
		return s.Store.GetSession(ctx, s.foreignSession)
	}
	value, err := s.Store.GetSession(ctx, id)
	if err == nil && s.mode == "session-host-rebind" {
		value.HostThread.ID = "silently-rebound-host-thread"
	}
	return value, err
}

type readOwnerFixture struct {
	runner *harness.Runner
	store  *readOwnerFaultStore
	red    harness.Snapshot
	blue   harness.Snapshot
	model  *physicalStartCaptureModel
}

func newReadOwnerFixture(t *testing.T) readOwnerFixture {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &physicalStartCaptureModel{}
	loop, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: modelClient})
	if err != nil {
		t.Fatal(err)
	}
	store := &readOwnerFaultStore{Store: harness.NewMemoryStore()}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loop, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	redReq := testStartRequest()
	redReq.HostThread.ID, redReq.HostTurn.ID, redReq.Thread.ID = "red-thread", "red-turn", "red-thread"
	redReq.Actor = kernel.ActorRef{TenantID: "tenant-red", ActorID: "red"}
	redReq.Goal = "red confidential goal"
	red, err := runner.StartAgentTurn(t.Context(), redReq)
	if err != nil || red.Turn.Status != harness.TurnCompleted {
		t.Fatalf("red start: %v / %+v", err, red.Turn)
	}
	blueReq := testStartRequest()
	blueReq.HostThread.ID, blueReq.HostTurn.ID, blueReq.Thread.ID = "blue-thread", "blue-turn", "blue-thread"
	blueReq.Actor = kernel.ActorRef{TenantID: "tenant-blue", ActorID: "blue"}
	blueReq.Goal = "blue confidential goal"
	blue, err := runner.StartAgentTurn(t.Context(), blueReq)
	if err != nil || blue.Turn.Status != harness.TurnCompleted {
		t.Fatalf("blue start: %v / %+v", err, blue.Turn)
	}
	if len(modelClient.requests) != 2 {
		t.Fatalf("fixture failed to create 2 physical model requests: %d", len(modelClient.requests))
	}
	store.foreignTurnID = blue.Turn.ID
	store.foreignSession = blue.Session.ID
	store.foreignConfig = blue.Config.ID
	return readOwnerFixture{runner: runner, store: store, red: red, blue: blue, model: modelClient}
}

func TestHardcutReadTurnAndSessionCannotBorrowForeignTenant(t *testing.T) {
	for _, mode := range []string{
		"redirect-turn", "redirect-session", "foreign-session-link", "foreign-config-link",
		"host-turn-rebind", "session-host-rebind",
	} {
		t.Run(mode, func(t *testing.T) {
			f := newReadOwnerFixture(t)
			f.store.mode = mode
			for _, operation := range []string{"Load", "Refresh", "LoadState", "Resume", "Cancel", "Timeline"} {
				var err error
				switch operation {
				case "Load":
					_, err = f.runner.Load(t.Context(), f.red.Turn.ID)
				case "Refresh":
					_, err = f.runner.Refresh(t.Context(), f.red.Turn.ID)
				case "LoadState":
					_, err = f.runner.LoadState(t.Context(), f.red.Turn.ID)
				case "Resume":
					_, err = f.runner.ResumeTurn(t.Context(), f.red.Turn.ID)
				case "Cancel":
					_, err = f.runner.Cancel(t.Context(), f.red.Turn.ID, "audit-fault")
				case "Timeline":
					_, err = f.runner.ListTimeline(t.Context(), f.red.Turn.ID, 0, 10)
				}
				if !errors.Is(err, harness.ErrConflict) {
					t.Errorf("%s accepted %s under red Turn: err=%v", operation, mode, err)
				}
				if len(f.model.requests) != 2 {
					t.Fatalf("%s dispatched new model on corrupt Store return: %d", operation, len(f.model.requests))
				}
			}
			f.store.mode = ""
			read, err := f.runner.Load(t.Context(), f.red.Turn.ID)
			if err != nil || read.Turn.Status != harness.TurnCompleted || read.Session.ID != f.red.Session.ID {
				t.Fatalf("bad read modified authoritative Turn: err=%v status=%s session=%s", err, read.Turn.Status, read.Session.ID)
			}
		})
	}
}

// Seeded single-transition matrix: no operation may substitute a foreign
// Session/Turn as the Host's exact requested ID. Different operation orders
// must not turn a failed read into physical execution permission.
func TestHardcutReadAuthorityStateMatrix(t *testing.T) {
	random := rand.New(rand.NewSource(20261104))
	for sample := 0; sample < 60; sample++ {
		f := newReadOwnerFixture(t)
		modes := []string{
			"redirect-turn", "redirect-session", "foreign-session-link", "foreign-config-link",
			"host-turn-rebind", "session-host-rebind",
		}
		for step := 0; step < 8; step++ {
			f.store.mode = modes[random.Intn(len(modes))]
			var err error
			switch random.Intn(4) {
			case 0:
				_, err = f.runner.Load(t.Context(), f.red.Turn.ID)
			case 1:
				_, err = f.runner.Refresh(t.Context(), f.red.Turn.ID)
			case 2:
				_, err = f.runner.LoadState(t.Context(), f.red.Turn.ID)
			case 3:
				_, err = f.runner.ResumeTurn(t.Context(), f.red.Turn.ID)
			}
			if !errors.Is(err, harness.ErrConflict) || len(f.model.requests) != 2 {
				t.Fatalf("sample=%d step=%d mode=%s: err=%v modelCalls=%d", sample, step, f.store.mode, err, len(f.model.requests))
			}
		}
		f.store.mode = ""
		if snap, err := f.runner.Load(t.Context(), f.red.Turn.ID); err != nil || snap.Session.Actor.TenantID != "tenant-red" {
			t.Fatalf("sample %d leaked after fault stopped: err=%v tenant=%s", sample, err, snap.Session.Actor.TenantID)
		}
	}
}
