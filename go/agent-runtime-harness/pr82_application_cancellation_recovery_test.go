package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type auditAppAckStore struct {
	harness.Store
	fail bool
}

func (s *auditAppAckStore) AppendItem(ctx context.Context, v harness.Item) (harness.Item, bool, error) {
	if s.fail && v.Kind == harness.ItemDiagnostic && v.Status == harness.ItemCompleted && string(v.Payload) == `{"boundary":"cancel_scope_settled"}` {
		s.fail = false
		return harness.Item{}, false, errors.New("lost cancellation settlement before commit")
	}
	return s.Store.AppendItem(ctx, v)
}
func auditCompletedApp(t *testing.T, s harness.Store) (*harness.Runner, harness.Snapshot) {
	t.Helper()
	r := newFeatureInvocationRuntime(t)
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: r, Agent: loadingFeatureAgent{runtime: r}, Store: s, Clock: featureInvocationClock{}, Applications: &applicationCapabilityExecutorFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := runner.StartApplicationTurn(t.Context(), harness.ApplicationTurnRequest{StartRequest: harness.StartRequest{HostThread: harness.HostRef{Kind: testThreadKind, ID: "audit-extra-thread"}, HostTurn: harness.HostRef{Kind: testContextHostKind, ID: "audit-extra-turn"}, Actor: kernel.ActorRef{TenantID: testTenant, ActorID: testActor}, Thread: kernel.ThreadRef{Kind: testThreadKind, ID: "audit-extra-thread"}, RequestID: "audit-extra-root", Goal: "root application"}, CapabilityKey: "app.root", DefinitionVersion: "1"})
	if err != nil {
		t.Fatal(err)
	}
	return runner, snap
}
func TestAuditPR82ApplicationCancellationACKRecovery(t *testing.T) {
	s := &auditAppAckStore{Store: harness.NewMemoryStore(), fail: true}
	r, root := auditCompletedApp(t, s)
	_, err := r.Cancel(t.Context(), root.Turn.ID, "cancel")
	if err == nil {
		t.Fatal("fault injection not reached")
	}
	_, err = r.Cancel(t.Context(), root.Turn.ID, "cancel")
	if err != nil {
		t.Fatalf("Application cancellation cannot resume persisted intent without Kernel Run: %v", err)
	}
}
func TestAuditPR82TerminalApplicationAcceptedChildCancel(t *testing.T) {
	s := harness.NewMemoryStore()
	r, root := auditCompletedApp(t, s)
	p, _ := harness.TopLevelInvocation(root)
	child := p
	child.ID = "accepted-child"
	child.ParentItemID = "host-parent"
	child.ExecutionRefID = "accepted-child-effect"
	child.Status = harness.InvocationAccepted
	child.Revision = 1
	_, _, err := s.CreateInvocation(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Cancel(t.Context(), root.Turn.ID, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.GetInvocation(t.Context(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != harness.InvocationCancelled {
		t.Fatalf("accepted child survived cancel: %s", after.Status)
	}
}

type auditWaitChildExecutor struct {
	runner   *harness.Runner
	store    *harness.MemoryStore
	childID  string
	childErr error
}

func (e *auditWaitChildExecutor) ExecuteApplicationCapability(ctx context.Context, req harness.ApplicationCapabilityRequest) (harness.ApplicationCapabilityResult, error) {
	if req.Invocation.ParentItemID != "" {
		e.childID = req.Invocation.ID
		return harness.ApplicationCapabilityResult{Interaction: &harness.ApplicationInteractionRequest{Key: "audit-choice", Kind: harness.InteractionChoice, Schema: json.RawMessage(`{"type":"object"}`)}}, nil
	}
	_, _, err := e.store.AppendItem(ctx, harness.Item{ID: "wait-host-parent", TurnID: req.Invocation.TurnID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted})
	if err != nil {
		return harness.ApplicationCapabilityResult{}, err
	}
	_, e.childErr = e.runner.StartApplicationInvocation(ctx, req.Invocation.TurnID, harness.ApplicationInvocationRequest{ParentItemID: "wait-host-parent", RequestID: "wait-child", Goal: "wait child", CapabilityKey: "app.child", DefinitionVersion: "1"})
	return harness.ApplicationCapabilityResult{}, nil
}
func TestAuditPR82TerminalApplicationWaitingChildCancel(t *testing.T) {
	s := harness.NewMemoryStore()
	rt := newFeatureInvocationRuntime(t)
	e := &auditWaitChildExecutor{store: s}
	r, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: s, Clock: featureInvocationClock{}, Applications: e})
	if err != nil {
		t.Fatal(err)
	}
	e.runner = r
	root, err := r.StartApplicationTurn(t.Context(), harness.ApplicationTurnRequest{StartRequest: harness.StartRequest{HostThread: harness.HostRef{Kind: testThreadKind, ID: "audit-wait-thread"}, HostTurn: harness.HostRef{Kind: testContextHostKind, ID: "audit-wait-turn"}, Actor: kernel.ActorRef{TenantID: testTenant, ActorID: testActor}, Thread: kernel.ThreadRef{Kind: testThreadKind, ID: "audit-wait-thread"}, RequestID: "audit-wait-root", Goal: "root application"}, CapabilityKey: "app.root", DefinitionVersion: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if e.childErr != nil {
		t.Fatal(e.childErr)
	}
	child, _ := s.GetInvocation(t.Context(), e.childID)
	if child.Status != harness.InvocationWaitingInput {
		t.Fatalf("setup: %s", child.Status)
	}
	_, err = r.Cancel(t.Context(), root.Turn.ID, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetInvocation(t.Context(), e.childID)
	if after.Status != harness.InvocationCancelled {
		t.Fatalf("waiting child survived: %s", after.Status)
	}
}

type auditLateAppClaimStore struct {
	harness.Store
	childID   string
	after     bool
	claimErr  error
	attempted bool
}

func (s *auditLateAppClaimStore) claim(ctx context.Context) {
	s.attempted = true
	child, e := s.GetInvocation(ctx, s.childID)
	if e != nil {
		s.claimErr = e
		return
	}
	child.Status = harness.InvocationRunning
	_, s.claimErr = s.UpdateInvocation(ctx, child, child.Revision)
}
func (s *auditLateAppClaimStore) AppendItem(ctx context.Context, v harness.Item) (harness.Item, bool, error) {
	inject := s.childID != "" && !s.attempted && v.Kind == harness.ItemDiagnostic && v.Status == harness.ItemStarted && v.ParentItemID == harness.CancellationFenceParentItemID
	if inject && !s.after {
		s.claim(ctx)
	}
	a, b, e := s.Store.AppendItem(ctx, v)
	if inject && s.after && e == nil {
		s.claim(ctx)
	}
	return a, b, e
}
func TestAuditPR82ApplicationCancelSerializesLateChildClaim(t *testing.T) {
	for _, after := range []bool{false, true} {
		name := "before"
		if after {
			name = "after"
		}
		t.Run(name, func(t *testing.T) {
			s := &auditLateAppClaimStore{Store: harness.NewMemoryStore(), after: after}
			r, root := auditCompletedApp(t, s)
			p, _ := harness.TopLevelInvocation(root)
			child := p
			child.ID = "late-child"
			child.ParentItemID = "host-parent"
			child.ExecutionRefID = "late-child-effect"
			child.Status = harness.InvocationAccepted
			child.Revision = 1
			_, _, err := s.CreateInvocation(t.Context(), child)
			if err != nil {
				t.Fatal(err)
			}
			s.childID = child.ID
			_, err = r.Cancel(t.Context(), root.Turn.ID, "cancel")
			if !s.attempted {
				t.Fatal("claim injection missing")
			}
			if after {
				if !errors.Is(s.claimErr, harness.ErrConflict) || err != nil {
					t.Fatalf("late claim after intent: claim=%v cancel=%v", s.claimErr, err)
				}
			} else if s.claimErr != nil || !errors.Is(err, harness.ErrApplicationOutcomeUnknown) {
				t.Fatalf("early claim before intent: claim=%v cancel=%v", s.claimErr, err)
			}
		})
	}
}
