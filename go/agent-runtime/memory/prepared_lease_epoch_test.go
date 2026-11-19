package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type settlementLeaseClock struct{ now time.Time }

func (c *settlementLeaseClock) Now() time.Time { return c.now }

type settlementLeaseModel struct{}

func (settlementLeaseModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "done"}, nil
}

func TestPreparedCustodySurvivesExpiredUndispatchedLease(t *testing.T) {
	clock := &settlementLeaseClock{now: time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStoreWithExecutionClock(clock)})
	if err != nil {
		t.Fatal(err)
	}
	r, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: settlementLeaseModel{}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := r.PrepareRun(t.Context(), agent.StartRequest{ID: "prepared", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, RequestID: "start", Goal: "prepare"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: prepared.Run.ID, OwnerID: "crashed-before-physical", LeaseDuration: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err = rt.VerifyPreparedRootSettlement(t.Context(), prepared); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("live lease was ignored: %v", err)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if err = rt.VerifyPreparedRootSettlement(t.Context(), prepared); err != nil {
		t.Fatalf("expired undispatched lease lost physical proof: %v", err)
	}
	if _, err = rt.Cancel(t.Context(), prepared.Run.ID, prepared.Run.Revision, "stop before physical admission"); err != nil {
		t.Fatal(err)
	}
}
