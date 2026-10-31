package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type auditNestedUnknownApp struct {
	runner   *harness.Runner
	store    *harness.MemoryStore
	childID  string
	childErr error
}

func (e *auditNestedUnknownApp) ExecuteApplicationCapability(ctx context.Context, req harness.ApplicationCapabilityRequest) (harness.ApplicationCapabilityResult, error) {
	if req.Invocation.ParentItemID != "" {
		e.childID = req.Invocation.ID
		return harness.ApplicationCapabilityResult{}, errors.New("external child effect response lost")
	}
	_, _, err := e.store.AppendItem(ctx, harness.Item{ID: "audit-app-parent", TurnID: req.Invocation.TurnID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted})
	if err != nil {
		return harness.ApplicationCapabilityResult{}, err
	}
	_, e.childErr = e.runner.StartApplicationInvocation(ctx, req.Invocation.TurnID, harness.ApplicationInvocationRequest{ParentItemID: "audit-app-parent", RequestID: "audit-child", Goal: "child effect", CapabilityKey: "app.child", DefinitionVersion: "1"})
	return harness.ApplicationCapabilityResult{}, nil
}
func TestAuditPR82TerminalApplicationRootMustNotHideUnknownChild(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	e := &auditNestedUnknownApp{store: store}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store, Clock: featureInvocationClock{}, Applications: e})
	if err != nil {
		t.Fatal(err)
	}
	e.runner = runner
	root, err := runner.StartApplicationTurn(t.Context(), harness.ApplicationTurnRequest{StartRequest: harness.StartRequest{HostThread: harness.HostRef{Kind: testThreadKind, ID: "audit-app-thread"}, HostTurn: harness.HostRef{Kind: testContextHostKind, ID: "audit-app-turn"}, Actor: kernel.ActorRef{TenantID: testTenant, ActorID: testActor}, Thread: kernel.ThreadRef{Kind: testThreadKind, ID: "audit-app-thread"}, RequestID: "audit-root", Goal: "root application"}, CapabilityKey: "app.root", DefinitionVersion: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(e.childErr, harness.ErrApplicationOutcomeUnknown) {
		t.Fatalf("child admission not reproduced: %v", e.childErr)
	}
	child, err := store.GetInvocation(t.Context(), e.childID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Turn.Status != harness.TurnCompleted || child.Status != harness.InvocationRunning {
		t.Fatalf("wrong setup: root=%s child=%s", root.Turn.Status, child.Status)
	}
	_, cancelErr := runner.Cancel(t.Context(), root.Turn.ID, "stop descendants")
	after, err := store.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(cancelErr, harness.ErrApplicationOutcomeUnknown) {
		t.Fatalf("P1: terminal Application root Cancel reports success with unresolved child: root=%s child=%s cancelErr=%v", root.Turn.Status, after.Status, cancelErr)
	}
}
