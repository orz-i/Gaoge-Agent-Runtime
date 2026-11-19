package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type preparedCallbackFixtureModel struct{}

func (preparedCallbackFixtureModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "completed"}, nil
}

func TestSQLPreparedCustodySurvivesExpiredUndispatchedLease(t *testing.T) {
	db := openKernelStoreTestDB(t)
	rt, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	r, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: preparedCallbackFixtureModel{}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := r.PrepareRun(t.Context(), agent.StartRequest{ID: "expired-prepared", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, RequestID: "start", Goal: "prepare"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: prepared.Run.ID, OwnerID: "crashed-before-physical", LeaseDuration: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err = rt.VerifyPreparedRootSettlement(t.Context(), prepared); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("live owner was ignored: %v", err)
	}
	if err = db.Table("agent_kernel_runs").Where("run_id = ?", prepared.Run.ID).UpdateColumn("execution_expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err = rt.VerifyPreparedRootSettlement(t.Context(), prepared); err != nil {
		t.Fatalf("expired undispatched owner lost physical proof: %v", err)
	}
	if _, err = rt.Cancel(t.Context(), prepared.Run.ID, prepared.Run.Revision, "stop"); err != nil {
		t.Fatal(err)
	}
}

// SQL and Memory must share the same physical Root authority rule. Only the
// first-party PREPARE operation is safe with future Host Approval policies:
// after Resume executes Model/Tool callbacks its acknowledged retirement is
// NOT an original no-effect Prepare proof.
func TestSQLPreparedRootControlDoesNotBorrowLaterPhysicalRetirement(t *testing.T) {
	db := openKernelStoreTestDB(t)
	rt, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	r, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: preparedCallbackFixtureModel{}})
	if err != nil {
		t.Fatal(err)
	}
	start := func(id string) kernel.Snapshot {
		t.Helper()
		s, e := r.PrepareRun(t.Context(), agent.StartRequest{
			ID: id, RequestID: "source-" + id,
			Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "original", Model: "model",
		})
		if e != nil || s.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("Prepare Root: %+v %v", s.Run, e)
		}
		if e = rt.VerifyPreparedRootSettlement(t.Context(), s); e != nil {
			t.Fatalf("original prepare receipt missing: %v", e)
		}
		return s
	}
	original := start("original-prepared")
	cancelled, err := rt.Cancel(t.Context(), original.Run.ID, original.Run.Revision, "before Model")
	if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("native pre-dispatch Cancel: %+v %v", cancelled.Run, err)
	}
	if err = rt.VerifyPreparedRootSettlement(t.Context(), cancelled); err != nil {
		t.Fatalf("original native Cancel successor lost Prep proof: %v", err)
	}
	next := start("next-prepared")
	terminal, err := r.Resume(t.Context(), next.Run.ID, next.Run.Revision)
	if err != nil || terminal.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("Resume Model callback: %+v %v", terminal.Run, err)
	}
	if err = rt.VerifyPreparedRootSettlement(t.Context(), terminal); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("post-Model terminal borrowed earlier Prepare: %v", err)
	}
	if err = rt.VerifyOwnedRootSettlement(t.Context(), terminal); err != nil {
		t.Fatalf("completed physical callback lacked ordinary receipt: %v", err)
	}
}
