package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

var strictAnswerSchema = json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)

type structuredFixtureModel struct {
	requests  []model.Request
	responses []model.Response
	supports  bool
}

func (m *structuredFixtureModel) Capabilities(_ context.Context, name string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: m.supports && name == "structured-model"}, nil
}
func (m *structuredFixtureModel) Generate(_ context.Context, req model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(req))
	if len(m.requests) > len(m.responses) {
		return model.Response{Content: `{"answer":"default"}`}, nil
	}
	return m.responses[len(m.requests)-1], nil
}
func structuredRequest(id string) agent.StartRequest {
	return agent.StartRequest{
		ID: id, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:    kernel.ThreadRef{Kind: "conversation", ID: "structured"},
		RequestID: id + "-req", Goal: "Extract the answer", Model: "structured-model",
		OutputContract: &model.OutputContract{Name: "answer", Schema: append(json.RawMessage(nil), strictAnswerSchema...)},
		Limits:         agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 4},
	}
}
func structuredRunner(t *testing.T, client *structuredFixtureModel) (*agent.Runner, *kernel.Runtime) {
	t.Helper()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client})
	if err != nil {
		t.Fatal(err)
	}
	return runner, rt
}

func TestStructuredOutputIsValidatedAndStoredAsNativeJSON(t *testing.T) {
	m := &structuredFixtureModel{supports: true, responses: []model.Response{{Content: `{"answer":"42"}`, Usage: &model.Usage{InputTokens: 5, OutputTokens: 6}}}}
	runner, _ := structuredRunner(t, m)
	request := structuredRequest("structured-valid")
	complete, err := runner.StartRun(t.Context(), request)
	if err != nil || complete.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("complete=%+v err=%v", complete.Run, err)
	}
	if complete.Result == nil || complete.Result.ContentType != "application/json" || string(complete.Result.Content) != `{"answer":"42"}` {
		t.Fatalf("terminal result not raw JSON: %+v", complete.Result)
	}
	view, err := agent.ViewState(complete)
	if err != nil || len(view.ModelInvocations) != 1 || len(m.requests) != 1 {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	if string(m.requests[0].OutputContract.Schema) != string(strictAnswerSchema) || view.OutputContract.Name != "answer" {
		t.Fatalf("frozen output contract lost: sent=%+v view=%+v", m.requests[0], view)
	}
	request.OutputContract.Schema[0] = '['
	if string(view.OutputContract.Schema) != string(strictAnswerSchema) {
		t.Fatal("mutable external schema affected persisted state")
	}
}
func TestStructuredOutputGetsOneBoundedRepair(t *testing.T) {
	m := &structuredFixtureModel{supports: true, responses: []model.Response{
		{Content: `{"answer":123}`, Usage: &model.Usage{InputTokens: 3, OutputTokens: 3}},
		{Content: `{"answer":"fixed"}`, Usage: &model.Usage{InputTokens: 4, OutputTokens: 4}},
	}}
	runner, _ := structuredRunner(t, m)
	completed, err := runner.StartRun(t.Context(), structuredRequest("structured-repair"))
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(m.requests) != 2 {
		t.Fatalf("repair status=%s err=%v requests=%d", completed.Run.Status, err, len(m.requests))
	}
	if completed.Result.ContentType != "application/json" || string(completed.Result.Content) != `{"answer":"fixed"}` {
		t.Fatalf("repair result=%+v", completed.Result)
	}
	view, err := agent.ViewState(completed)
	if err != nil || view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.TotalTokens != 14 || len(view.ModelInvocations) != 2 {
		t.Fatalf("usage/receipt duplicated: %+v err=%v", view, err)
	}
	if m.requests[1].OutputContract == nil || m.requests[1].OutputContract.Name != "answer" || len(m.requests[1].Tools) != 0 {
		t.Fatalf("repair lost frozen schema or tool-free boundary: %+v", m.requests[1])
	}
}
func TestStructuredOutputRepeatedInvalidFailsWithoutSuccess(t *testing.T) {
	m := &structuredFixtureModel{supports: true, responses: []model.Response{
		{Content: `{"answer":1}`}, {Content: `{"answer":false}`},
	}}
	runner, _ := structuredRunner(t, m)
	failed, err := runner.StartRun(t.Context(), structuredRequest("structured-invalid"))
	if !errors.Is(err, agent.ErrInvalidModelResponse) || failed.Run.Status != kernel.RunStatusFailed || len(m.requests) != 2 || failed.Result != nil {
		t.Fatalf("invalid JSON accepted/looped: err=%v status=%s calls=%d result=%+v", err, failed.Run.Status, len(m.requests), failed.Result)
	}
}
func TestStructuredOutputRefusalDoesNotTryToRepair(t *testing.T) {
	m := &structuredFixtureModel{supports: true, responses: []model.Response{
		{Content: "I cannot comply.", Refused: true, Usage: &model.Usage{InputTokens: 3, OutputTokens: 2}},
	}}
	runner, _ := structuredRunner(t, m)
	failed, err := runner.StartRun(t.Context(), structuredRequest("structured-refusal"))
	if !errors.Is(err, agent.ErrOutputRefused) || failed.Run.Status != kernel.RunStatusFailed || len(m.requests) != 1 || failed.Result != nil {
		t.Fatalf("refusal repaired or completed: err=%v status=%s calls=%d", err, failed.Run.Status, len(m.requests))
	}
	view, _ := agent.ViewState(failed)
	if view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.TotalTokens != 5 {
		t.Fatalf("refusal lost receipt: %+v", view.Budget)
	}
}
func TestStructuredOutputRequiresProvenModelCapabilityBeforeRunCreate(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture *structuredFixtureModel
	}{
		{"unsupported", &structuredFixtureModel{supports: false}},
		{"unresolved", &structuredFixtureModel{supports: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, rt := structuredRunner(t, test.fixture)
			req := structuredRequest("capability-" + test.name)
			if test.name == "unresolved" {
				req.Model = "some-other-model"
			}
			_, err := runner.StartRun(t.Context(), req)
			if !errors.Is(err, model.ErrCapabilityUnavailable) || len(test.fixture.requests) > 0 {
				t.Fatalf("unsupported dispatched: %v", err)
			}
			if _, err = rt.Load(t.Context(), req.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("Run created before capability admission: %v", err)
			}
		})
	}
}
func TestStructuredOutputInvalidSchemaRejectedBeforeRunCreate(t *testing.T) {
	m := &structuredFixtureModel{supports: true}
	runner, rt := structuredRunner(t, m)
	req := structuredRequest("invalid-schema")
	req.OutputContract.Schema = json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"additionalProperties":false}`)
	_, err := runner.StartRun(t.Context(), req)
	if !errors.Is(err, model.ErrOutputContract) || len(m.requests) != 0 {
		t.Fatalf("invalid schema admitted: %v", err)
	}
	if _, err = rt.Load(t.Context(), req.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("persisted invalid contract: %v", err)
	}
}
func TestStructuredOutputFrozenDuringPendingIntentRecovery(t *testing.T) {
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{Store: base, eventType: "agent.model_invocation.pending", afterCommit: true}
	m := &structuredFixtureModel{supports: true, responses: []model.Response{{Content: `{"answer":"resumed"}`}}}
	first, err := agent.NewRunner(agent.Dependencies{Runtime: newModelInvocationRuntime(t, faults), Model: m})
	if err != nil {
		t.Fatal(err)
	}
	req := structuredRequest("structured-pending")
	_, err = first.StartRun(t.Context(), req)
	if !errors.Is(err, errInjectedModelInvocationCrash) || len(m.requests) != 0 {
		t.Fatalf("wrong injected boundary: err=%v requests=%d", err, len(m.requests))
	}
	rt := newModelInvocationRuntime(t, base)
	persisted, err := rt.Load(t.Context(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(persisted)
	if err != nil || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Request.OutputContract == nil {
		t.Fatalf("missing frozen contract: %+v err=%v", view, err)
	}
	// Future configuration or capability checks cannot change the pending
	// immutable model invocation. Recovery uses the stored request unchanged.
	req.OutputContract.Schema[0] = '['
	second, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m})
	if err != nil {
		t.Fatal(err)
	}
	complete, err := second.Resume(t.Context(), persisted.Run.ID, persisted.Run.Revision)
	if err != nil || complete.Run.Status != kernel.RunStatusCompleted || len(m.requests) != 1 {
		t.Fatalf("resume: err=%v status=%s calls=%d", err, complete.Run.Status, len(m.requests))
	}
	if !strings.Contains(string(m.requests[0].OutputContract.Schema), `"answer"`) || complete.Result.ContentType != "application/json" {
		t.Fatalf("recovered request/response drifted: %+v result=%+v", m.requests[0], complete.Result)
	}
}
