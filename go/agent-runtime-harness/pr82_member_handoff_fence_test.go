package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
)

// The MemberID-only path is a real public delegation entry, not a legacy
// exemption from the physical dispatch/cancellation arbitration.
type pr82MemberAdmissionRaceStore struct {
	Store
	parentID string
	intent   bool
	injected bool
}

func (s *pr82MemberAdmissionRaceStore) AppendItem(ctx context.Context, item Item) (Item, bool, error) {
	saved, fresh, err := s.Store.AppendItem(ctx, item)
	if err != nil || !fresh || s.injected || item.Kind != ItemDelegation ||
		item.Status != ItemStarted || item.ParentItemID != "" {
		return saved, fresh, err
	}
	s.injected = true
	if s.intent {
		now := pr82HandoffClock{}.Now()
		_, _, err = s.Store.AppendItem(ctx, Item{
			ID: "pr82-member-cancel-intent", TurnID: item.TurnID, Kind: ItemDiagnostic,
			Status: ItemStarted, ParentItemID: CancellationFenceParentItemID,
			CreatedAt: now, UpdatedAt: now,
		})
		return saved, fresh, err
	}
	root, err := s.GetInvocation(ctx, s.parentID)
	if err != nil {
		return saved, fresh, err
	}
	root.Status = InvocationCancelled
	_, err = s.UpdateInvocation(ctx, root, root.Revision)
	return saved, fresh, err
}

func TestPR82PublicMemberDelegationFencedAfterDurableStartedItem(t *testing.T) {
	for _, scenario := range []string{"parent-terminal", "cancel-intent"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			store := &pr82MemberAdmissionRaceStore{
				Store: f.store, parentID: f.parent.ID, intent: scenario == "cancel-intent",
			}
			f.runner.store = store
			_, err := f.runner.Delegate(t.Context(), f.turn.ID, DelegateRequest{
				MemberID: "analyst", Goal: "independent member execution",
			})
			if !store.injected {
				t.Fatal("public Delegate did not reach the injected Item admission boundary")
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("Member Handoff admission should fail closed after %s: %v", scenario, err)
			}
			if f.handoff.calls != 0 {
				t.Fatalf("Member Handoff dispatched %d external effects after %s", f.handoff.calls, scenario)
			}
		})
	}
}

func TestPR82MemberHandoffFencedAfterChildCreate(t *testing.T) {
	for _, scenario := range []string{"parent-terminal", "cancel-intent"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			store := &pr82HandoffInjectionStore{Store: f.store}
			store.onCreate = func(ctx context.Context, _ Invocation) error {
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
				_, _, err := f.store.AppendItem(ctx, Item{
					ID: "pr82-member-post-create-cancel", TurnID: f.turn.ID,
					Kind: ItemDiagnostic, Status: ItemStarted,
					ParentItemID: CancellationFenceParentItemID,
					CreatedAt:    now, UpdatedAt: now,
				})
				return err
			}
			f.runner.store = store
			delegation := f.delegation()
			delegation.RoleID = ""
			_, err := f.runner.executeDelegation(t.Context(), f.turn, f.parent, f.parentRun, delegation, f.startedItemID)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("Member Handoff did not reject %s after child Create: %v", scenario, err)
			}
			if f.handoff.calls != 0 {
				t.Fatalf("Member Handoff called external child %d times after %s", f.handoff.calls, scenario)
			}
		})
	}
}

func TestPR82MemberHandoffUnknownOutcomeRetainsGrant(t *testing.T) {
	f := newPR82HandoffFixture(t)
	f.handoff.result = func(handoff.Delegation) (handoff.Delegation, error) {
		return handoff.Delegation{}, errors.New("child StartOrLoad reply lost")
	}
	delegation := f.delegation()
	delegation.RoleID = ""
	dispatch := func() error {
		_, err := f.runner.executeDelegation(t.Context(), f.turn, f.parent, f.parentRun, delegation, f.startedItemID)
		return err
	}
	if err := dispatch(); !errors.Is(err, ErrExecutionOutcomeUnknown) {
		t.Fatalf("member unknown outcome not retained: %v", err)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil || !PendingExecutionDispatch(items, "") {
		t.Fatalf("unknown member outcome settled its grant: %v", err)
	}
	if err := dispatch(); !errors.Is(err, ErrConflict) {
		t.Fatalf("member replay did not respect pending grant: %v", err)
	}
	if f.handoff.calls != 1 {
		t.Fatalf("external member StartOrLoad dispatched %d times, expected once", f.handoff.calls)
	}
}
