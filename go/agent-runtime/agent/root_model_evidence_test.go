package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

func TestRootUnknownHostRetainsExactModelEvidenceWithoutConsumption(t *testing.T) {
	base := memory.NewStore()
	fault := &modelInvocationFaultStore{Store: base, eventType: "agent.model_invocation.completed"}
	rt := newModelInvocationRuntime(t, fault)
	provider := &invocationRecordingModel{}
	host := &rootCountingRunMiddleware{}
	billingOnly, conflicting := true, false
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider, RunMiddleware: []plugin.RunMiddleware{host}, ModelOutcomeVerifier: agent.ModelOutcomeVerifierFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		original := provider.callsCopy()
		if len(original) != 1 || !reflect.DeepEqual(request, original[0]) {
			t.Fatal("verifier did not receive exact original request")
		}
		response := model.Response{ResponseID: "resp_" + request.InvocationID, Usage: &model.Usage{InputTokens: 7, OutputTokens: 3, ReasoningTokens: 2}}
		if !billingOnly {
			response.Content = "done"
		}
		if conflicting {
			response.Content = "another response"
		}
		return response, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	before, err := runner.StartRun(t.Context(), startRequest("host-unknown-evidence", "host-unknown-evidence-request", "finish"))
	if !errors.Is(err, agent.ErrRootOutcomeUnknown) || !errors.Is(err, errInjectedModelInvocationCrash) {
		t.Fatalf("fixture=%v", err)
	}
	grant, err := rt.LookupChildExecution(t.Context(), before.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
	if err != nil || completion.HostCompleted {
		t.Fatalf("Host completion=%#v %v", completion, err)
	}
	_, err = runner.ReconcileModelOutcome(t.Context(), before.Run.ID, before.Run.Revision)
	if !errors.Is(err, agent.ErrModelOutcomeUnknown) {
		t.Fatalf("billing-only became full response: %v", err)
	}
	unchanged, err := rt.Load(t.Context(), before.Run.ID)
	if err != nil || !reflect.DeepEqual(before, unchanged) {
		t.Fatal("billing-only evidence mutated original snapshot")
	}
	billingOnly = false
	evidence, err := runner.ReconcileModelOutcome(t.Context(), before.Run.ID, before.Run.Revision)
	if !errors.Is(err, agent.ErrRootOutcomeUnknown) {
		t.Fatalf("evidence unlocked unknown Host: %v", err)
	}
	if evidence.Run.Revision != before.Run.Revision+1 || evidence.EventHead != before.EventHead {
		t.Fatal("evidence must be one CAS without event/wakeup")
	}
	normalized := evidence
	normalized.Run.Revision = before.Run.Revision
	normalized.Run.UpdatedAt = before.Run.UpdatedAt
	normalized.State = before.State
	if !reflect.DeepEqual(normalized, before) {
		t.Fatal("evidence changed non-State Run/snapshot fields")
	}
	var oldState, newState map[string]json.RawMessage
	if json.Unmarshal(before.State, &oldState) != nil || json.Unmarshal(evidence.State, &newState) != nil {
		t.Fatal("state decode")
	}
	delete(oldState, "modelInvocations")
	delete(newState, "modelInvocations")
	if !reflect.DeepEqual(oldState, newState) {
		t.Fatal("evidence changed messages/budget/tools/deferred/approval")
	}
	prior := mustModelInvocationView(t, before).ModelInvocations[0]
	after := mustModelInvocationView(t, evidence).ModelInvocations[0]
	if after.Status != agent.ModelInvocationCompleted || after.ConsumedAt != nil || after.CompletedAt == nil || after.ID != prior.ID || after.RunID != prior.RunID || after.RequestHash != prior.RequestHash || after.SourceRevision != prior.SourceRevision || after.Provider != prior.Provider || after.BindingFingerprint != prior.BindingFingerprint || after.ExecutionAttempt != prior.ExecutionAttempt || after.DispatchAttempt != prior.DispatchAttempt || !reflect.DeepEqual(after.Request, prior.Request) || after.Response.Content != "done" || after.Usage == nil || after.Usage.InputTokens != 7 {
		t.Fatalf("receipt/source mismatch: %+v", after)
	}
	duplicate, err := runner.ReconcileModelOutcome(t.Context(), evidence.Run.ID, evidence.Run.Revision)
	if !errors.Is(err, agent.ErrRootOutcomeUnknown) || !reflect.DeepEqual(duplicate, evidence) {
		t.Fatalf("duplicate evidence not idempotent: %v", err)
	}
	conflicting = true
	_, err = runner.ReconcileModelOutcome(t.Context(), evidence.Run.ID, evidence.Run.Revision)
	if !errors.Is(err, agent.ErrInvalidModelResponse) {
		t.Fatalf("conflicting receipt replaced original: %v", err)
	}
	_, err = runner.Resume(t.Context(), evidence.Run.ID, evidence.Run.Revision)
	if !errors.Is(err, agent.ErrRootOutcomeUnknown) {
		t.Fatalf("Completed evidence consumed despite Host unknown: %v", err)
	}
	final, err := rt.Load(t.Context(), evidence.Run.ID)
	if err != nil || !reflect.DeepEqual(final, evidence) {
		t.Fatal("repeat/Resume mutated evidence")
	}
	retained, err := rt.LookupChildExecution(t.Context(), evidence.Run.ID)
	if err != nil || retained != grant {
		t.Fatal("evidence retired original grant")
	}
	finalCompletion, err := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
	if err != nil || finalCompletion != completion || host.calls.Load() != 1 || len(provider.callsCopy()) != 1 {
		t.Fatal("evidence changed Host completion or replayed callbacks")
	}
}
