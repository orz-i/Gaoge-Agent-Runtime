package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type pr82InteractionWaitExecutor struct{}

func (pr82InteractionWaitExecutor) ExecuteApplicationCapability(context.Context, ApplicationCapabilityRequest) (ApplicationCapabilityResult, error) {
	return ApplicationCapabilityResult{Interaction: &ApplicationInteractionRequest{
		Key: "wait", Kind: InteractionInput, Schema: json.RawMessage(`{"type":"string"}`),
	}}, nil
}

type pr82ParentResponseCounter struct {
	calls     int
	resultErr error
}

func (h *pr82ParentResponseCounter) HandleInteractionResponse(context.Context, InteractionResponseContext) (InteractionResponseResult, error) {
	h.calls++
	return InteractionResponseResult{}, h.resultErr
}

type pr82TerminateInteractionParentStore struct {
	Store
	childID    string
	parentID   string
	terminated bool
}

func (s *pr82TerminateInteractionParentStore) UpdateInvocation(ctx context.Context, value Invocation, revision uint64) (Invocation, error) {
	updated, err := s.Store.UpdateInvocation(ctx, value, revision)
	if err != nil || s.terminated || value.ID != s.childID || value.Status != InvocationRunning {
		return updated, err
	}
	parent, err := s.GetInvocation(ctx, s.parentID)
	if err != nil {
		return updated, err
	}
	parent.Status = InvocationCompleted
	_, err = s.Store.UpdateInvocation(ctx, parent, parent.Revision)
	s.terminated = err == nil
	return updated, err
}

func TestPR82InteractionRequiresLiveParentsBeforeAndAfterClaim(t *testing.T) {
	for _, phase := range []string{"before-claim", "after-claim", "live-parent", "unknown-callback"} {
		t.Run(phase, func(t *testing.T) {
			f, parent, parentItem, _ := newPR82NestedApplicationParent(t, InvocationRunning)
			f.runner.applications = pr82InteractionWaitExecutor{}
			handler := &pr82ParentResponseCounter{resultErr: ErrInteractionNoEffect}
			if phase == "unknown-callback" {
				handler.resultErr = errors.New("lost callback response")
			}
			f.runner.interactions = handler
			waiting, err := f.runner.StartApplicationInvocation(t.Context(), f.turn.ID, ApplicationInvocationRequest{
				ParentItemID: parentItem, RequestID: "waiting-grandchild", Goal: "wait",
				CapabilityKey: "app.grandchild", DefinitionVersion: "1",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(waiting.Interactions) != 1 {
				t.Fatalf("interactions = %d", len(waiting.Interactions))
			}
			interaction := waiting.Interactions[0]
			if phase == "before-claim" {
				if _, err = f.runner.completeApplicationInvocation(t.Context(), f.turn, parent, nil); err != nil {
					t.Fatal(err)
				}
			}
			wrapped := &pr82TerminateInteractionParentStore{Store: f.store, childID: interaction.InvocationID, parentID: parent.ID}
			if phase == "after-claim" {
				f.runner.store = wrapped
			}
			response := ResolveInteractionRequest{Response: json.RawMessage(`"yes"`)}
			_, err = f.runner.ResolveInteraction(t.Context(), f.turn.ID, interaction.ID, response)
			child, loadErr := f.store.GetInvocation(t.Context(), interaction.InvocationID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			switch phase {
			case "before-claim", "after-claim":
				if !errors.Is(err, ErrConflict) || handler.calls != 0 || child.Status != InvocationWaitingInput {
					t.Fatalf("dead parent dispatched or stranded claim: err=%v calls=%d status=%s", err, handler.calls, child.Status)
				}
				if phase == "after-claim" && (!wrapped.terminated || !errors.Is(err, ErrInteractionNoEffect)) {
					t.Fatalf("post-claim rejection not proven no-effect: terminated=%v err=%v", wrapped.terminated, err)
				}
			case "live-parent":
				if !errors.Is(err, ErrInteractionNoEffect) || handler.calls != 1 || child.Status != InvocationWaitingInput {
					t.Fatalf("live parent's callback rejected: err=%v calls=%d status=%s", err, handler.calls, child.Status)
				}
			case "unknown-callback":
				if !errors.Is(err, ErrInteractionOutcomeUnknown) || handler.calls != 1 || child.Status != InvocationRunning {
					t.Fatalf("unknown callback claim lost: err=%v calls=%d status=%s", err, handler.calls, child.Status)
				}
				if _, err = f.runner.completeApplicationInvocation(t.Context(), f.turn, parent, nil); err != nil {
					t.Fatal(err)
				}
				_, err = f.runner.ResolveInteraction(t.Context(), f.turn.ID, interaction.ID, response)
				child, loadErr = f.store.GetInvocation(t.Context(), interaction.InvocationID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				if !errors.Is(err, ErrInteractionOutcomeUnknown) || handler.calls != 1 || child.Status != InvocationRunning {
					t.Fatalf("dead parent erased/replayed unknown callback: err=%v calls=%d status=%s", err, handler.calls, child.Status)
				}
			}
		})
	}
}
