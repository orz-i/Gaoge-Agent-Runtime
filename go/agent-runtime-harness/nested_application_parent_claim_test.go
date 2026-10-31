package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// A non-root Application in Accepted has not committed its Host-effect
// intent. It cannot lend execution authority to a nested Application.
type pr82NestedEffectCounter struct{ calls int }

func (e *pr82NestedEffectCounter) ExecuteApplicationCapability(
	_ context.Context, _ ApplicationCapabilityRequest,
) (ApplicationCapabilityResult, error) {
	e.calls++
	return ApplicationCapabilityResult{}, nil
}

func newPR82NestedApplicationParent(
	t *testing.T, status InvocationStatus,
) (*pr82HandoffFixture, Invocation, string, *pr82NestedEffectCounter) {
	t.Helper()
	fixture := newPR82HandoffFixture(t)
	raw, hash, err := marshalInvocationValue(applicationInvocationInput{
		Goal:  "unclaimed intermediate Application",
		Input: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	input, hash, err := sealChildRootInput(raw, hash, fixture.parent)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	parent := Invocation{
		ID: "intermediate-application", TurnID: fixture.turn.ID,
		ParentItemID: fixture.startedItemID, CapabilityKey: "app.intermediate",
		DefinitionVersion: "1", ExecutionClass: ExecutionApplication,
		Input: input, InputHash: hash, ExecutionRefID: "app-intermediate-attempt-1",
		Status: status, Attempt: 1, Revision: 1, OutputRefs: []HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := fixture.store.CreateInvocation(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	if err := fixture.runner.recordInvocationItem(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	parentItemID := invocationLifecycleItemID(parent, parent.Status, parent.Revision)
	counter := &pr82NestedEffectCounter{}
	fixture.runner.applications = counter
	return fixture, parent, parentItemID, counter
}

func TestPR82UnclaimedIntermediateApplicationCannotAdmitChild(t *testing.T) {
	f, parent, parentItemID, effects := newPR82NestedApplicationParent(t, InvocationAccepted)
	_, err := f.runner.StartApplicationInvocation(t.Context(), f.turn.ID, ApplicationInvocationRequest{
		ParentItemID: parentItemID, RequestID: "unclaimed-grandchild",
		Goal: "must not execute", CapabilityKey: "app.grandchild",
		DefinitionVersion: "1", Input: json.RawMessage(`{}`),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Accepted parent must reject nested admission: %v", err)
	}
	if effects.calls != 0 {
		t.Fatalf("Accepted parent dispatched %d nested Host effects", effects.calls)
	}
	values, err := f.store.ListInvocations(t.Context(), parent.TurnID)
	if err != nil || len(values) != 2 {
		t.Fatalf("rejected admission created a nested child: invocations=%d err=%v", len(values), err)
	}
}

func TestPR82ClaimedIntermediateApplicationAllowsChildAndAcceptedFailsFinalFence(t *testing.T) {
	f, parent, parentItemID, _ := newPR82NestedApplicationParent(t, InvocationRunning)
	// The same guard used at admission must authorize a claimed live parent.
	if _, err := f.runner.childInvocationContext(t.Context(), f.turn.ID, parentItemID, ExecutionApplication); err != nil {
		t.Fatalf("Running intermediate parent rejected: %v", err)
	}
	raw, hash, err := marshalInvocationValue(applicationInvocationInput{
		Goal: "nested", Input: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, hash, err := sealChildRootInput(raw, hash, f.parent, invocationAttempt(parent))
	if err != nil {
		t.Fatal(err)
	}
	child := Invocation{
		ID: "grandchild", TurnID: parent.TurnID, ParentItemID: parentItemID,
		ExecutionClass: ExecutionApplication, Input: sealed, InputHash: hash,
	}
	if err := f.runner.requireLiveInvocationParents(t.Context(), child); err != nil {
		t.Fatalf("Running intermediate parent rejected by final effect fence: %v", err)
	}
	parent.Status = InvocationAccepted
	if _, err := f.store.UpdateInvocation(t.Context(), parent, parent.Revision); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.requireLiveInvocationParents(t.Context(), child); !errors.Is(err, ErrConflict) {
		t.Fatalf("parent without claimed effect authorized nested effect: %v", err)
	}
}

type pr82UndoIntermediateClaimStore struct {
	Store
	parentID string
	flipped  bool
}

func (s *pr82UndoIntermediateClaimStore) UpdateInvocation(
	ctx context.Context, value Invocation, expectedRevision uint64,
) (Invocation, error) {
	updated, err := s.Store.UpdateInvocation(ctx, value, expectedRevision)
	if err != nil || s.flipped || value.CapabilityKey != "app.grandchild" ||
		value.Status != InvocationRunning {
		return updated, err
	}
	s.flipped = true
	parent, err := s.GetInvocation(ctx, s.parentID)
	if err != nil {
		return updated, err
	}
	parent.Status = InvocationAccepted
	_, err = s.Store.UpdateInvocation(ctx, parent, parent.Revision)
	return updated, err
}

func TestPR82AcceptedIntermediateAfterChildClaimCannotRunHostEffect(t *testing.T) {
	f, parent, parentItemID, effects := newPR82NestedApplicationParent(t, InvocationRunning)
	store := &pr82UndoIntermediateClaimStore{Store: f.store, parentID: parent.ID}
	f.runner.store = store
	_, err := f.runner.StartApplicationInvocation(t.Context(), f.turn.ID, ApplicationInvocationRequest{
		ParentItemID: parentItemID, RequestID: "post-claim-grandchild",
		Goal: "guard after claim", CapabilityKey: "app.grandchild",
		DefinitionVersion: "1", Input: json.RawMessage(`{}`),
	})
	if !store.flipped {
		t.Fatal("did not inject parent transition after child effect claim")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("post-claim final guard failed to reject unclaimed parent: %v", err)
	}
	if effects.calls != 0 {
		t.Fatalf("post-claim final guard allowed %d external effects", effects.calls)
	}
}
