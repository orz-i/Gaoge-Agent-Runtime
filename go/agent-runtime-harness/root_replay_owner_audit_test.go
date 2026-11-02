package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type auditUnownedTeamReplay struct {
	runtime         *kernel.Runtime
	starts, resumes int
}

func (x *auditUnownedTeamReplay) StartRun(ctx context.Context, req team.StartRequest) (kernel.Snapshot, error) {
	x.starts++
	return x.runtime.Create(ctx, kernel.CreateRequest{
		ID: req.ID, Kind: team.RunKind, Actor: req.Actor, Thread: req.Thread,
		RequestID: req.RequestID, Goal: "foreign-goal-not-frozen", State: json.RawMessage(`{}`),
	})
}
func (x *auditUnownedTeamReplay) Resume(ctx context.Context, runID string, rev uint64) (kernel.Snapshot, error) {
	x.resumes++
	return x.runtime.Load(ctx, runID)
}
func TestRootTeamStartReplayRejectsForeignRunBeforeResume(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	feature := &auditUnownedTeamReplay{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: harness.NewMemoryStore(),
		Clock: featureInvocationClock{}, Teams: feature,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := testStartRequest()
	start.HostThread.ID = "audit-top-level-replay-thread"
	start.HostTurn.ID = "audit-top-level-replay-turn"
	start.Thread.ID = start.HostThread.ID
	start.RequestID = "audit-top-level-replay"
	start.Goal = "legitimate-authorized-team-goal"
	request := harness.TeamTurnRequest{
		StartRequest: start, Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "worker", Goal: "work"}},
	}
	_, firstErr := runner.StartTeamTurn(t.Context(), request)
	if !errors.Is(firstErr, harness.ErrConflict) {
		t.Fatalf("first start must reject wrong goal before projection: %v", firstErr)
	}
	if feature.starts != 1 || feature.resumes != 0 {
		t.Fatalf("unexpected initial physical work: start=%d resume=%d", feature.starts, feature.resumes)
	}
	_, replayErr := runner.StartTeamTurn(t.Context(), request)
	if feature.resumes != 0 {
		t.Fatalf("FOREIGN TEAM RESUMED ON REPLAY BEFORE FROZEN OWNER PROOF: resumes=%d firstErr=%v replayErr=%v", feature.resumes, firstErr, replayErr)
	}
	if !errors.Is(replayErr, harness.ErrConflict) {
		t.Fatalf("replay accepted unowned Run: %v", replayErr)
	}
}

// Independent audit-only: a locally observed child Team Run has the same
// deterministic Run ID and RequestID but a different original frozen Goal.
// Child Resume must not enter its physical Feature before verifying owner.
func TestIndependentChildTeamResumeRejectsForeignGoalBeforeEffect(t *testing.T) {
	_, turnID, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
	feature := &auditUnownedTeamReplay{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Teams: feature, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "child-foreign-goal", Goal: "legitimate-child-task",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "work"}},
	})
	all, err := store.ListInvocations(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	var child harness.Invocation
	for _, value := range all {
		if value.ExecutionClass == harness.ExecutionTeam && value.ParentItemID != "" {
			child = value
		}
	}
	if child.ID == "" {
		t.Fatalf("missing original child start: %v", startErr)
	}
	_, resumeErr := runner.ResumeInvocation(t.Context(), child.ID)
	if feature.resumes != 0 {
		t.Fatalf("FOREIGN CHILD PHYSICAL RESUME: child=%+v startErr=%v resumeErr=%v calls=%d", child, startErr, resumeErr, feature.resumes)
	}
	if !errors.Is(resumeErr, harness.ErrConflict) {
		t.Fatalf("Unowned child Resume not rejected: %v", resumeErr)
	}
}

type independentWrongRootAgent struct {
	unusedFeatureAgent
	runtime *kernel.Runtime
}

func (x independentWrongRootAgent) StartRun(ctx context.Context, req agent.StartRequest) (kernel.Snapshot, error) {
	return x.runtime.Create(ctx, kernel.CreateRequest{
		ID: req.ID, Kind: agent.RunKind, Actor: req.Actor, Thread: req.Thread,
		RequestID: req.RequestID, Goal: "different-root-goal", State: json.RawMessage(`{}`),
	})
}

func TestIndependentChildStartRejectsWrongRootBeforePhysicalFeature(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	teamFeature := &auditUnownedTeamReplay{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: independentWrongRootAgent{runtime: runtime}, Store: store,
		Teams: teamFeature, Clock: featureInvocationClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.HostThread.ID = "wrong-root-parent-admission"
	request.HostTurn.ID = "wrong-root-parent-turn"
	request.Thread.ID = request.HostThread.ID
	request.RequestID = "wrong-root-request"
	request.Goal = "legitimate-root-goal"
	_, firstErr := runner.StartAgentTurn(t.Context(), request)
	if !errors.Is(firstErr, harness.ErrConflict) {
		t.Fatalf("root should fail owner projection: %v", firstErr)
	}
	sid, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := harness.TurnID(sid, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	invID, err := harness.AgentInvocationID(tid, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(t.Context(), tid, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	parentItemID := ""
	for _, item := range items {
		if item.Kind == harness.ItemInvocation && item.InvocationID == invID {
			parentItemID = item.ID
		}
	}
	if parentItemID == "" {
		t.Fatalf("no parent Invocation Item: %+v", items)
	}
	_, childErr := runner.StartTeamInvocation(t.Context(), tid, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "child-under-wrong-root", Goal: "child-work",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "worker", Goal: "work"}},
	})
	if teamFeature.starts != 0 {
		t.Fatalf("UNOWNED ROOT AUTHORIZED CHILD PHYSICAL START: starts=%d rootErr=%v childErr=%v", teamFeature.starts, firstErr, childErr)
	}
	if !errors.Is(childErr, harness.ErrConflict) {
		t.Fatalf("unowned root should be rejected: %v", childErr)
	}
}

// A child Runtime Result with a colliding execution ID but unrelated Goal
// cannot become a completed child Invocation or durable Artifact for the
// admitted child. Root output checks do not authorize child projection.
func TestIndependentChildResultRequiresOriginalOwnerBeforeProjection(t *testing.T) {
	_, tid, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
	feature := &auditUnownedTeamReplay{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Teams: feature, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartTeamInvocation(t.Context(), tid, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "child-result-foreign", Goal: "legitimate-task",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "worker", Goal: "work"}},
	})
	all, err := store.ListInvocations(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	var child harness.Invocation
	for _, inv := range all {
		if inv.ParentItemID != "" && inv.ExecutionClass == harness.ExecutionTeam {
			child = inv
		}
	}
	if child.ID == "" {
		t.Fatalf("missing child start: %v", startErr)
	}
	foreign, err := runtime.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	if foreign.Run.Goal == "legitimate-task" {
		t.Fatalf("test fixture missing wrong Goal: %+v", foreign.Run)
	}
	_, err = runtime.Apply(t.Context(), foreign.Run.ID, foreign.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: foreign.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`"FOREIGN CHILD RESULT"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, refreshErr := runner.RefreshInvocation(t.Context(), child.ID)
	latest, err := store.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(t.Context(), tid, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind == harness.ItemArtifact && item.InvocationID == child.ID && item.Status == harness.ItemCompleted {
			t.Fatalf("UNOWNED CHILD RESULT PERSISTED: item=%+v startErr=%v refreshErr=%v", item, startErr, refreshErr)
		}
	}
	if latest.Status == harness.InvocationCompleted || !errors.Is(refreshErr, harness.ErrConflict) {
		t.Fatalf("UNOWNED CHILD STATUS ACCEPTED: status=%s err=%v", latest.Status, refreshErr)
	}
}

type independentForeignTenantRootAgent struct {
	unusedFeatureAgent
	runtime *kernel.Runtime
}

func (x independentForeignTenantRootAgent) StartRun(ctx context.Context, req agent.StartRequest) (kernel.Snapshot, error) {
	return x.runtime.Create(ctx, kernel.CreateRequest{
		ID: req.ID, Kind: agent.RunKind, Actor: kernel.ActorRef{TenantID: "FOREIGN-TENANT", ActorID: "foreign-actor"},
		Thread: req.Thread, RequestID: req.RequestID, Goal: req.Goal, State: json.RawMessage(`{}`),
	})
}
func TestIndependentForeignRootActorCannotAuthorizeChildStart(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	feature := &auditUnownedTeamReplay{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: independentForeignTenantRootAgent{runtime: runtime}, Store: store,
		Teams: feature, Clock: featureInvocationClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := testStartRequest()
	req.HostThread.ID = "foreign-tenant-parent-thread"
	req.HostTurn.ID = "foreign-tenant-parent-turn"
	req.Thread.ID = req.HostThread.ID
	req.RequestID = "foreign-tenant-parent-request"
	req.Goal = "authorized-parent-goal"
	_, rootErr := runner.StartAgentTurn(t.Context(), req)
	if !errors.Is(rootErr, harness.ErrConflict) {
		t.Fatalf("root actor check did not reject: %v", rootErr)
	}
	sid, err := harness.SessionID(req.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := harness.TurnID(sid, req.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := harness.AgentInvocationID(tid, req.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(t.Context(), tid, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	parentItemID := ""
	for _, item := range items {
		if item.Kind == harness.ItemInvocation && item.InvocationID == rootID {
			parentItemID = item.ID
		}
	}
	if parentItemID == "" {
		t.Fatalf("root Item missing: %+v", items)
	}
	_, childErr := runner.StartTeamInvocation(t.Context(), tid, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "child-cross-tenant", Goal: "authorized-team-child",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "worker", Goal: "work"}},
	})
	if feature.starts != 0 {
		t.Fatalf("CROSS-TENANT ROOT AUTHORIZED TEAM PHYSICAL START: calls=%d rootErr=%v childErr=%v", feature.starts, rootErr, childErr)
	}
	if !errors.Is(childErr, harness.ErrConflict) {
		t.Fatalf("unowned parent accepted: %v", childErr)
	}
}

// CancelInvocation is a physical effect as well: a wrong-Goal same-ID child
// must not be cancelled first and rejected only during later projection.
func TestIndependentChildCancelRejectsForeignRunBeforePhysicalControl(t *testing.T) {
	_, tid, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
	feature := &auditUnownedTeamReplay{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Teams: feature,
		Relations: relations, Cancellation: runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "child-cancel-foreign",
		Goal: "original-child-goal", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "member", Goal: "work"}},
	}
	_, startErr := runner.StartTeamInvocation(t.Context(), tid, request)
	if !errors.Is(startErr, harness.ErrConflict) {
		t.Fatalf("wrong child Goal should be rejected: %v", startErr)
	}
	childID, err := harness.InvocationID(tid, parentItemID, harness.CapabilityTeam, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.GetInvocation(t.Context(), childID)
	if err != nil {
		t.Fatal(err)
	}
	_, cancelErr := runner.CancelInvocation(t.Context(), child.ID, "cancel original child")
	foreign, err := runtime.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	if foreign.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("FOREIGN CHILD RUN PHYSICALLY CANCELLED: status=%s err=%v", foreign.Run.Status, cancelErr)
	}
	// A custom Team Feature without a verified local ChildRunner route is
	// already barred by the original composite cancellation proof. Both
	// that route-specific uncertainty and a direct frozen-owner rejection
	// are valid fail-closed outcomes; physical effects must stay untouched.
	if !errors.Is(cancelErr, harness.ErrConflict) &&
		!errors.Is(cancelErr, harness.ErrRuntimeCancellationUnverified) {
		t.Fatalf("foreign child not rejected: %v", cancelErr)
	}
}
