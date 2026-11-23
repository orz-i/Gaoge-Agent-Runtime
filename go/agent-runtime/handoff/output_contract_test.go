package handoff_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type deferredStructuredModel struct{ calls int }

func (*deferredStructuredModel) Capabilities(_ context.Context, modelID string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: modelID == "typed-child"}, nil
}
func (m *deferredStructuredModel) Generate(_ context.Context, r model.Request) (model.Response, error) {
	m.calls++
	if r.OutputContract == nil || r.OutputContract.Name != "named_answer" {
		return model.Response{}, model.ErrCapabilityUnavailable
	}
	return model.Response{Content: `{"answer":"verified"}`}, nil
}

func TestHandoffFreezesAndVerifiesIndependentChildOutputContract(t *testing.T) {
	const schema = `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`
	store := memory.NewStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &deferredStructuredModel{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: modelClient})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := handoff.NewDeferredAgent(runner)
	if err != nil {
		t.Fatal(err)
	}
	parent := verifiedHandoffParent()
	original := handoff.Delegation{
		ID: "typed-delegation", MemberID: "named", MemberRevision: "r1",
		ChildRunID: "typed-child-run", Goal: "produce typed result", Model: "typed-child",
		Status:         handoff.StatusQueued,
		OutputContract: &model.OutputContract{Name: "named_answer", Schema: json.RawMessage(schema)},
	}
	prepared, err := coordinator.StartOrLoad(t.Context(), parent, original)
	if !errors.Is(err, handoff.ErrChildPending) || modelClient.calls != 0 {
		t.Fatalf("preparation dispatched model or lost pending child: %+v err=%v", prepared, err)
	}
	child, err := runtime.Load(t.Context(), original.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(child)
	if err != nil || !reflect.DeepEqual(view.OutputContract, original.OutputContract) || !handoff.VerifiedChildRun(prepared, parent.Run, child) {
		t.Fatalf("physical child lost original output schema: %+v err=%v", view, err)
	}
	modified := original
	modified.OutputContract = model.CloneOutputContract(original.OutputContract)
	modified.OutputContract.Name = "foreign_answer"
	if _, err := coordinator.StartOrLoad(t.Context(), parent, modified); !errors.Is(err, handoff.ErrChildRunUnverified) {
		t.Fatalf("same-ID forged output contract borrowed original run: %v", err)
	}
	if handoff.VerifiedChildRun(modified, parent.Run, child) {
		t.Fatal("same-ID child with another output schema was verified")
	}
	completed, err := runner.Resume(t.Context(), child.Run.ID, child.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || modelClient.calls != 1 {
		t.Fatalf("original typed child could not complete: status=%s err=%v calls=%d", completed.Run.Status, err, modelClient.calls)
	}
	final, err := coordinator.Refresh(t.Context(), parent, original)
	if err != nil || final.Status != handoff.StatusCompleted || modelClient.calls != 1 {
		t.Fatalf("original Handoff lost its immutable structured result: %+v err=%v", final, err)
	}
	if _, err := coordinator.Refresh(t.Context(), parent, modified); !errors.Is(err, handoff.ErrChildRunUnverified) {
		t.Fatalf("forged child schema refreshed another physical run: %v", err)
	}
	if _, err := coordinator.StartOrLoad(t.Context(), parent, original); err != nil || modelClient.calls != 1 {
		t.Fatalf("terminal typed child replayed the Model: err=%v calls=%d", err, modelClient.calls)
	}
}

// A non-native child may have performed remote work even when its returned
// Run snapshot cannot later prove an enforced strict output contract. Prevent
// dispatch at the existing Handoff child-resolution boundary, not after Send.
func TestHandoffRejectsUnattestableStrictOutputBeforeRemoteEffects(t *testing.T) {
	const schema = `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`
	delegation := handoff.Delegation{
		ID: "strict", MemberID: "remote", MemberRevision: "r1",
		ChildRunID: "remote-child", Goal: "return typed output", Status: handoff.StatusQueued,
		OutputContract: &model.OutputContract{Name: "answer", Schema: json.RawMessage(schema)},
	}
	parent := verifiedHandoffParent()
	for _, routed := range []bool{false, true} {
		t.Run(map[bool]string{true: "routed", false: "static"}[routed], func(t *testing.T) {
			remote := &unverifiedChildRunner{loadErr: kernel.ErrNotFound}
			var coordinator *handoff.Coordinator
			var err error
			if routed {
				coordinator, err = handoff.NewRouted(handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) {
					return remote, nil
				}))
			} else {
				coordinator, err = handoff.New(remote)
			}
			if err != nil {
				t.Fatal(err)
			}
			if result, startErr := coordinator.StartOrLoad(t.Context(), parent, delegation); !errors.Is(startErr, handoff.ErrInvalidDelegation) ||
				result.ID != "" || remote.starts != 0 {
				t.Fatalf("unsupported strict child reached external StartRun: result=%+v err=%v starts=%d", result, startErr, remote.starts)
			}
			if result, refreshErr := coordinator.Refresh(t.Context(), parent, delegation); !errors.Is(refreshErr, handoff.ErrInvalidDelegation) ||
				result.ID != "" || remote.starts != 0 {
				t.Fatalf("unsupported strict child could observe foreign Run: result=%+v err=%v starts=%d", result, refreshErr, remote.starts)
			}
		})
	}
}

func TestHandoffRoutedNativeAgentRetainsStrictOutput(t *testing.T) {
	store := memory.NewStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	provider := &deferredStructuredModel{}
	native, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: provider})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := handoff.NewDeferredAgentWithRouting(native, func(handoff.Delegation) (handoff.DelegationRoute, error) {
		return handoff.DelegationRouteLocal, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	delegation := handoff.Delegation{
		ID: "strict-routed", MemberID: "local", ChildRunID: "strict-routed-run", Goal: "typed output",
		Model: "typed-child", Status: handoff.StatusQueued,
		OutputContract: &model.OutputContract{Name: "named_answer", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)},
	}
	got, startErr := coordinator.StartOrLoad(t.Context(), verifiedHandoffParent(), delegation)
	if !errors.Is(startErr, handoff.ErrChildPending) || got.Status != handoff.StatusRunning || provider.calls != 0 {
		t.Fatalf("routed native strict child was denied: status=%s err=%v provider_calls=%d", got.Status, startErr, provider.calls)
	}
	persisted, loadErr := runtime.Load(t.Context(), delegation.ChildRunID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	view, viewErr := agent.ViewState(persisted)
	if viewErr != nil || !reflect.DeepEqual(view.OutputContract, delegation.OutputContract) {
		t.Fatalf("strict output not frozen: %+v err=%v", view.OutputContract, viewErr)
	}
}
