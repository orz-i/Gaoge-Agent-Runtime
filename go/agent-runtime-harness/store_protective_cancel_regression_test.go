package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type protectiveCancelReadFaultStore struct {
	harness.Store
	failMode string
}

func (s *protectiveCancelReadFaultStore) GetConfigSnapshot(ctx context.Context, id string) (harness.ConfigSnapshot, error) {
	if s.failMode == "config" {
		return harness.ConfigSnapshot{}, harness.ErrNotFound
	}
	return s.Store.GetConfigSnapshot(ctx, id)
}
func (s *protectiveCancelReadFaultStore) GetSession(ctx context.Context, id string) (harness.Session, error) {
	if s.failMode == "session" {
		return harness.Session{}, harness.ErrNotFound
	}
	return s.Store.GetSession(ctx, id)
}

type protectiveCancelAgentStarter struct {
	unusedFeatureAgent
	rt *kernel.Runtime
}

func (f protectiveCancelAgentStarter) StartRun(ctx context.Context, req agent.StartRequest) (kernel.Snapshot, error) {
	return f.rt.Create(ctx, kernel.CreateRequest{ID: req.ID, Kind: agent.RunKind, Actor: req.Actor, Thread: req.Thread, RequestID: req.RequestID, Goal: req.Goal, State: json.RawMessage(`{}`)})
}
func TestProtectiveCancelKeepsNativeAgentStoppingUnderReadFailure(t *testing.T) {
	for _, mode := range []string{"config", "session"} {
		t.Run(mode, func(t *testing.T) {
			rt := newFeatureInvocationRuntime(t)
			store := &protectiveCancelReadFaultStore{Store: harness.NewMemoryStore()}
			runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: protectiveCancelAgentStarter{rt: rt}, Store: store, Clock: featureInvocationClock{}})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runner.StartAgentTurn(t.Context(), testStartRequest())
			if err != nil {
				t.Fatal("start", err)
			}
			root, ok := harness.TopLevelInvocation(started)
			if !ok {
				t.Fatal("no invocation")
			}
			before, err := rt.Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			if before.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("need running: %s", before.Run.Status)
			}
			store.failMode = mode
			_, cancelErr := runner.Cancel(t.Context(), started.Turn.ID, "operator stop")
			after, loadErr := rt.Load(t.Context(), root.ExecutionRefID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			t.Logf("mode=%s cancelErr=%v before=%s after=%s", mode, cancelErr, before.Run.Status, after.Run.Status)
			if after.Run.Status != kernel.RunStatusCancelled {
				t.Errorf("CANCEL_AVAILABILITY_REGRESSION: %s read failure blocked a live Run's cancellation; err=%v actualRun=%s", mode, cancelErr, after.Run.Status)
			}
			if cancelErr != nil && !errors.Is(cancelErr, harness.ErrNotFound) {
				t.Logf("unexpected cancellation error: %v", cancelErr)
			}
		})
	}
}

// A narrow protective-stop read must still refuse a different Turn identity
// even if that Store record names a real running Kernel Run. The cancel target
// remains the requested Host Turn, not whatever row a Store happened to return.
func TestProtectiveCancelNeverTargetsForeignTenantRun(t *testing.T) {
	for _, mode := range []string{"redirect-turn", "foreign-session-link", "host-turn-rebind"} {
		t.Run(mode, func(t *testing.T) {
			runtime := newFeatureInvocationRuntime(t)
			store := &readOwnerFaultStore{Store: harness.NewMemoryStore()}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: protectiveCancelAgentStarter{rt: runtime},
				Store: store, Clock: featureInvocationClock{},
			})
			if err != nil {
				t.Fatal(err)
			}
			redRequest := testStartRequest()
			redRequest.HostThread.ID, redRequest.HostTurn.ID, redRequest.Thread.ID = "red-stop-thread", "red-stop-turn", "red-stop-thread"
			redRequest.Actor = kernel.ActorRef{TenantID: "tenant-red", ActorID: "red"}
			red, err := runner.StartAgentTurn(t.Context(), redRequest)
			if err != nil {
				t.Fatal(err)
			}
			blueRequest := testStartRequest()
			blueRequest.HostThread.ID, blueRequest.HostTurn.ID, blueRequest.Thread.ID = "blue-stop-thread", "blue-stop-turn", "blue-stop-thread"
			blueRequest.Actor = kernel.ActorRef{TenantID: "tenant-blue", ActorID: "blue"}
			blue, err := runner.StartAgentTurn(t.Context(), blueRequest)
			if err != nil {
				t.Fatal(err)
			}
			redRoot, ok := harness.TopLevelInvocation(red)
			if !ok {
				t.Fatal("red root missing")
			}
			blueRoot, ok := harness.TopLevelInvocation(blue)
			if !ok {
				t.Fatal("blue root missing")
			}
			store.mode = mode
			store.foreignTurnID, store.foreignSession = blue.Turn.ID, blue.Session.ID
			_, err = runner.Cancel(t.Context(), red.Turn.ID, "stop red only")
			if !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("%s accepted a substituted owner: %v", mode, err)
			}
			for name, runID := range map[string]string{"red": redRoot.ExecutionRefID, "blue": blueRoot.ExecutionRefID} {
				current, loadErr := runtime.Load(t.Context(), runID)
				if loadErr != nil || current.Run.Status != kernel.RunStatusRunning {
					t.Fatalf("%s wrong-tenant stop affected %s Run: load=%v status=%s", mode, name, loadErr, current.Run.Status)
				}
			}
			store.mode = ""
			_, _ = runner.Cancel(t.Context(), red.Turn.ID, "operator authorized stop")
			redRun, err := runtime.Load(t.Context(), redRoot.ExecutionRefID)
			if err != nil || redRun.Run.Status != kernel.RunStatusCancelled {
				t.Fatalf("legitimate stop did not cancel red: err=%v status=%s", err, redRun.Run.Status)
			}
			blueRun, err := runtime.Load(t.Context(), blueRoot.ExecutionRefID)
			if err != nil || blueRun.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("legitimate red stop affected blue: err=%v status=%s", err, blueRun.Run.Status)
			}
		})
	}
}

type protectiveRunningTeam struct {
	runtime   *kernel.Runtime
	relations *runrelation.Registry
}

func (f *protectiveRunningTeam) StartRun(ctx context.Context, request team.StartRequest) (kernel.Snapshot, error) {
	return f.runtime.Create(ctx, kernel.CreateRequest{ID: request.ID, Kind: team.RunKind, Actor: request.Actor, Thread: request.Thread, RequestID: request.RequestID, Goal: request.Goal, State: json.RawMessage(`{}`)})
}
func (f *protectiveRunningTeam) Resume(ctx context.Context, id string, _ uint64) (kernel.Snapshot, error) {
	return f.runtime.Load(ctx, id)
}
func (f *protectiveRunningTeam) OwnsKernelRuntime(rt *kernel.Runtime) bool { return f.runtime == rt }
func (f *protectiveRunningTeam) RecordsRelationsTo(r runrelation.Recorder) bool {
	return r == f.relations
}
func TestProtectiveChildCancelStillStopsUnderReadFailure(t *testing.T) {
	for _, mode := range []string{"config", "session"} {
		t.Run(mode, func(t *testing.T) {
			_, tid, parentID, relations, parentRunID, base, rt := newFeatureInvocationHarness(t)
			store := &protectiveCancelReadFaultStore{Store: base}
			teams := &protectiveRunningTeam{runtime: rt, relations: relations}
			runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: store, Clock: featureInvocationClock{}, Teams: teams, Relations: relations, Interactions: noopInteractionResponseHandler{}})
			if err != nil {
				t.Fatal(err)
			}
			snap, err := runner.StartTeamInvocation(t.Context(), tid, harness.TeamInvocationRequest{ParentItemID: parentID, RequestID: "protective-team", Goal: "run child task", Mode: team.ExecutionSequential, Members: []team.Member{{ID: "writer", Goal: "write"}}})
			if err != nil {
				t.Fatalf("start child: %v", err)
			}
			child := teamChildInvocation(t, snap)
			running, err := rt.Load(t.Context(), child.ExecutionRefID)
			if err != nil || running.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("child run=%+v err=%v", running.Run, err)
			}
			store.failMode = mode
			_, stopErr := runner.CancelInvocation(t.Context(), child.ID, "stop child")
			after, loadErr := rt.Load(t.Context(), child.ExecutionRefID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			parent, loadErr := rt.Load(t.Context(), parentRunID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}

			if after.Run.Status != kernel.RunStatusCancelled || parent.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("child cancel availability or parent isolation violated: child=%s parent=%s err=%v", after.Run.Status, parent.Run.Status, stopErr)
			}
		})
	}
}
