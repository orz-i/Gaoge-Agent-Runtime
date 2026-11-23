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
