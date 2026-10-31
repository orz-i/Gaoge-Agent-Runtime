package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

type pr82HandoffClock struct{}

func (pr82HandoffClock) Now() time.Time { return time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC) }

type pr82HandoffCounter struct {
	calls  int
	result func(handoff.Delegation) (handoff.Delegation, error)
}

func (h *pr82HandoffCounter) StartOrLoad(_ context.Context, _ kernel.Snapshot, d handoff.Delegation) (handoff.Delegation, error) {
	h.calls++
	if h.result != nil {
		return h.result(d)
	}
	d.Status = handoff.StatusRunning
	return d, handoff.ErrChildPending
}

type pr82HandoffInjectionStore struct {
	Store
	onCreate func(context.Context, Invocation) error
	onGrant  func(context.Context, Item) error
}

func (s *pr82HandoffInjectionStore) CreateInvocation(ctx context.Context, inv Invocation) (Invocation, bool, error) {
	saved, fresh, err := s.Store.CreateInvocation(ctx, inv)
	if err == nil && fresh && inv.ParentItemID != "" && s.onCreate != nil {
		if injectErr := s.onCreate(ctx, inv); injectErr != nil {
			return saved, fresh, injectErr
		}
	}
	return saved, fresh, err
}

func (s *pr82HandoffInjectionStore) AppendItem(ctx context.Context, item Item) (Item, bool, error) {
	saved, fresh, err := s.Store.AppendItem(ctx, item)
	if err == nil && fresh && item.ParentItemID == ExecutionDispatchParentItemID && s.onGrant != nil {
		if injectErr := s.onGrant(ctx, item); injectErr != nil {
			return saved, fresh, injectErr
		}
	}
	return saved, fresh, err
}

type pr82HandoffFixture struct {
	runner        *Runner
	store         *MemoryStore
	runtime       *kernel.Runtime
	parent        Invocation
	parentRun     kernel.Snapshot
	turn          Turn
	startedItemID string
	handoff       *pr82HandoffCounter
}

func newPR82HandoffFixture(t *testing.T) *pr82HandoffFixture {
	t.Helper()
	ctx := t.Context()
	now := pr82HandoffClock{}.Now()
	store := NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "pr82-role-thread"}
	run, err := runtime.Create(ctx, kernel.CreateRequest{
		ID: "pr82-root-run", Kind: agent.RunKind, Actor: actor, Thread: thread, RequestID: "pr82-root",
		Goal: "delegate", State: []byte("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	session := Session{ID: "pr82-session", HostThread: HostRef{Kind: "conversation", ID: thread.ID},
		Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	config, err := SealConfigSnapshot("pr82-turn", ConfigSnapshot{AdmissionHash: strings.Repeat("a", 64)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(ctx, config); err != nil {
		t.Fatal(err)
	}
	turn := Turn{ID: "pr82-turn", SessionID: session.ID, HostTurn: HostRef{Kind: "message", ID: "pr82-turn"},
		ConfigSnapshotID: config.ID, Status: TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	root := Invocation{ID: "pr82-root", TurnID: turn.ID, CapabilityKey: CapabilityAgent,
		ExecutionClass: ExecutionAgent, ExecutionRefID: run.Run.ID, Status: InvocationRunning, Attempt: 1,
		Revision: 1, OutputRefs: []HostRef{}, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.CreateInvocation(ctx, root); err != nil {
		t.Fatal(err)
	}
	startedPayload, err := json.Marshal(delegationItemPayload{DelegationID: "pr82-delegation", MemberID: "analyst", RoleID: "analyst",
		ChildRunID: "pr82-delegated-run", ParentRunID: root.ExecutionRefID, Goal: "review", Status: handoff.StatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	started := Item{ID: "pr82-delegation-started", TurnID: turn.ID, InvocationID: root.ID,
		RunID: "pr82-delegated-run", Kind: ItemDelegation, Status: ItemStarted, Payload: startedPayload,
		CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.AppendItem(ctx, started); err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	counter := &pr82HandoffCounter{}
	runner := &Runner{store: store, runtime: runtime, relations: relations, handoffs: counter, clock: pr82HandoffClock{}}
	return &pr82HandoffFixture{runner: runner, store: store, runtime: runtime, parent: root,
		parentRun: run, turn: turn, startedItemID: started.ID, handoff: counter}
}

func (f *pr82HandoffFixture) delegation() handoff.Delegation {
	return handoff.Delegation{ID: "pr82-delegation", MemberID: "analyst", RoleID: "analyst",
		ChildRunID: "pr82-delegated-run", Goal: "review", Status: handoff.StatusQueued}
}

func (f *pr82HandoffFixture) execute(ctx context.Context) (DelegationResult, error) {
	return f.runner.executeDelegation(ctx, f.turn, f.parent, f.parentRun, f.delegation(), f.startedItemID)
}

func TestPR82RoleHandoffRefusesParentTerminalAndCancellationAfterChildCreate(t *testing.T) {
	for _, scenario := range []string{"parent-terminal", "cancel-pending"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			wrap := &pr82HandoffInjectionStore{Store: f.store}
			wrap.onCreate = func(ctx context.Context, _ Invocation) error {
				if scenario == "parent-terminal" {
					root, err := f.store.GetInvocation(ctx, f.parent.ID)
					if err != nil {
						return err
					}
					root.Status = InvocationCancelled
					_, err = f.store.UpdateInvocation(ctx, root, root.Revision)
					return err
				}
				now := pr82HandoffClock{}.Now()
				_, _, err := f.store.AppendItem(ctx, Item{ID: "pr82-cancel-intent", TurnID: f.turn.ID,
					RunID: f.parent.ExecutionRefID, Kind: ItemDiagnostic, Status: ItemStarted,
					ParentItemID: CancellationFenceParentItemID, CreatedAt: now, UpdatedAt: now})
				return err
			}
			f.runner.store = wrap
			_, err := f.execute(t.Context())
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("expected pre-effect rejection: %v", err)
			}
			if f.handoff.calls != 0 {
				t.Fatalf("Handoff called %d times after cancellation", f.handoff.calls)
			}
			items, listErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if scenario == "parent-terminal" && PendingExecutionDispatch(items, "") {
				t.Fatal("definitive no-effect parent rejection left grant unresolved")
			}
		})
	}
}

func TestPR82RoleHandoffRefusesRootKernelTerminationAfterGrant(t *testing.T) {
	f := newPR82HandoffFixture(t)
	wrap := &pr82HandoffInjectionStore{Store: f.store}
	wrap.onGrant = func(ctx context.Context, _ Item) error {
		current, err := f.runtime.Load(ctx, f.parent.ExecutionRefID)
		if err != nil {
			return err
		}
		_, err = f.runtime.Cancel(ctx, current.Run.ID, current.Run.Revision, "cancel after grant")
		return err
	}
	f.runner.store = wrap
	_, err := f.execute(t.Context())
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected final root fence: %v", err)
	}
	if f.handoff.calls != 0 {
		t.Fatalf("Handoff called %d times after root Kernel cancellation", f.handoff.calls)
	}
	items, listErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if PendingExecutionDispatch(items, "") {
		t.Fatal("root termination left no-effect grant pending")
	}
}

func TestPR82RoleHandoffUnknownOutcomeKeepsGrantAndStopsReplay(t *testing.T) {
	f := newPR82HandoffFixture(t)
	f.handoff.result = func(_ handoff.Delegation) (handoff.Delegation, error) {
		return handoff.Delegation{}, errors.New("remote StartRun acknowledgment lost")
	}
	if _, err := f.execute(t.Context()); !errors.Is(err, ErrExecutionOutcomeUnknown) {
		t.Fatalf("ambiguous physical result was not retained: %v", err)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !PendingExecutionDispatch(items, "") {
		t.Fatal("ambiguous Handoff start grant closed")
	}
	if _, err = f.execute(t.Context()); !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed external effect did not block: %v", err)
	}
	if f.handoff.calls != 1 {
		t.Fatalf("remote Handoff started %d times", f.handoff.calls)
	}
}

func TestPR82RoleHandoffSettlesVerifiedOutcome(t *testing.T) {
	f := newPR82HandoffFixture(t)
	f.handoff.result = func(delegation handoff.Delegation) (handoff.Delegation, error) {
		_, createErr := f.runtime.Create(t.Context(), kernel.CreateRequest{ID: delegation.ChildRunID,
			Kind: agent.RunKind, Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread,
			RequestID: delegation.ID, Goal: delegation.Goal, State: []byte("{}")})
		if createErr != nil {
			return handoff.Delegation{}, createErr
		}
		delegation.Status = handoff.StatusRunning
		return delegation, handoff.ErrChildPending
	}
	delegation := f.delegation()
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	result, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, delegation, f.startedItemID)
	if !errors.Is(err, handoff.ErrChildPending) || result.Status != handoff.StatusRunning {
		t.Fatalf("legitimate Handoff failed: result=%+v err=%v", result, err)
	}
	if f.handoff.calls != 1 {
		t.Fatalf("Handoff calls: %d", f.handoff.calls)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if PendingExecutionDispatch(items, "") {
		t.Fatal("verified Handoff outcome left grant open")
	}
}
