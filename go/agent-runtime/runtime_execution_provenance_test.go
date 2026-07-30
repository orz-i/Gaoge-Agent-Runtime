package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	testProvenanceRunID      = "run_provenance"
	testProvenanceRootRunID  = "run_root"
	testProvenanceSnapshotID = "snapshot_7"
	testProvenanceTenantID   = "tenant_1"
	testProvenanceActorID    = "actor_1"
	testProvenanceModel      = "gpt-5.6-terra"
)

type runtimeExecutionProvenanceStore struct {
	Store
	run      model.Run
	context  *model.ContextSnapshot
	result   *model.RunResult
	workflow *model.WorkflowExecution
}

func (s *runtimeExecutionProvenanceStore) GetRun(
	_ context.Context,
	actor model.ActorRef,
	runID string,
) (*model.Run, error) {
	if actor != s.run.Actor || runID != s.run.RunID {
		return nil, ErrNotFound
	}
	item := s.run
	return &item, nil
}

func (s *runtimeExecutionProvenanceStore) GetRunContextSnapshot(
	_ context.Context,
	actor model.ActorRef,
	runID string,
) (*model.ContextSnapshot, error) {
	if s.context == nil || actor != s.run.Actor || runID != s.run.RunID {
		return nil, ErrNotFound
	}
	item := *s.context
	return &item, nil
}

func (s *runtimeExecutionProvenanceStore) GetRunResult(
	_ context.Context,
	actor model.ActorRef,
	runID string,
) (*model.RunResult, error) {
	if s.result == nil || actor != s.run.Actor || runID != s.run.RunID {
		return nil, ErrNotFound
	}
	item := *s.result
	return &item, nil
}

func (s *runtimeExecutionProvenanceStore) GetWorkflowExecution(
	_ context.Context,
	actor model.ActorRef,
	runID string,
) (*model.WorkflowExecution, error) {
	if s.workflow == nil || actor != s.run.Actor || runID != s.run.RunID {
		return nil, ErrNotFound
	}
	item := *s.workflow
	return &item, nil
}

func TestRuntimeExecutionProvenanceFreezesNeutralExecutionSource(t *testing.T) {
	actor, store := newRuntimeExecutionProvenanceFixture()
	engine := &Engine{repo: store}

	provenance, err := engine.GetRuntimeExecutionProvenance(t.Context(), actor, testProvenanceRunID)
	if err != nil {
		t.Fatal(err)
	}
	assertRuntimeExecutionProvenanceIdentity(t, provenance)
	assertRuntimeExecutionProvenanceRefs(t, provenance)
	assertRuntimeExecutionProvenanceRouteAndHashes(t, provenance)
	assertRuntimeExecutionProvenanceRedacted(t, provenance)
}

func newRuntimeExecutionProvenanceFixture() (
	model.ActorRef,
	*runtimeExecutionProvenanceStore,
) {
	actor := model.ActorRef{TenantID: testProvenanceTenantID, ActorID: testProvenanceActorID}
	return actor, &runtimeExecutionProvenanceStore{
		run: model.Run{
			RunID: testProvenanceRunID, RootRunID: testProvenanceRootRunID,
			RuntimeKind: model.RuntimeKindWorkflow, Actor: actor, Status: model.RunStatusCompleted,
			Environment:           model.ResourceRef{Kind: resourceKindEnvironment, ID: "environment_1", Revision: "7"},
			AgentManifest:         model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent_writer", Revision: "4"},
			WorkflowDefinition:    model.ResourceRef{Kind: model.WorkflowDefinitionKind, ID: "workflow_story", Revision: "9"},
			RunConfigSnapshotJSON: `{"endpoint":"https://private.invalid","apiKey":"secret-value","prompt":"hidden prompt","toolArguments":{"path":"hidden"}}`,
			RequestFingerprint:    "request_hash", CurrentStepID: "step_done", LastEventSeq: 12,
			LastPresentationEventSeq: 11, RequestedModelName: "writer", PlatformModelName: "5.6 Terra",
			Provider: valueOpenai1473EEB2, ProviderProtocol: "responses", RoutedBindingCode: "primary",
			ModelVendor: "OpenAI", UpstreamModelName: testProvenanceModel,
		},
		context: &model.ContextSnapshot{
			SnapshotID: testProvenanceSnapshotID, RunID: testProvenanceRunID,
			ContentHash: "context_hash",
		},
		result: &model.RunResult{RunID: testProvenanceRunID, ContentHash: "result_hash"},
		workflow: &model.WorkflowExecution{
			RunID: testProvenanceRunID, Version: 3, Status: model.WorkflowExecutionCompleted,
			StateJSON: `{"secret":"workflow state"}`, VarsJSON: `{"draft":"hidden"}`,
			WaitsJSON: `{}`, CompensationJSON: `[]`, BudgetJSON: `{"used":2}`,
			ThreadSnapshotHash: "thread_hash", CompletionSeq: 12,
		},
	}
}

func assertRuntimeExecutionProvenanceIdentity(
	t *testing.T,
	provenance *RuntimeExecutionProvenanceV1,
) {
	t.Helper()
	if provenance.SchemaVersion != 1 || provenance.RunID != testProvenanceRunID ||
		provenance.RootRunID != testProvenanceRootRunID ||
		provenance.RuntimeKind != model.RuntimeKindWorkflow {
		t.Fatalf("identity = %#v", provenance)
	}
}

func assertRuntimeExecutionProvenanceRefs(
	t *testing.T,
	provenance *RuntimeExecutionProvenanceV1,
) {
	t.Helper()
	if provenance.EnvironmentRef == nil || provenance.EnvironmentRef.Revision != "7" ||
		provenance.AgentManifestRef == nil || provenance.AgentManifestRef.Revision != "4" ||
		provenance.WorkflowDefinitionRef == nil || provenance.WorkflowDefinitionRef.Revision != "9" {
		t.Fatalf("revision refs = %#v", provenance)
	}
}

func assertRuntimeExecutionProvenanceRouteAndHashes(
	t *testing.T,
	provenance *RuntimeExecutionProvenanceV1,
) {
	t.Helper()
	if provenance.ModelRouting.PlatformModelName != "5.6 Terra" ||
		provenance.ModelRouting.UpstreamModelName != testProvenanceModel {
		t.Fatalf("model routing = %#v", provenance.ModelRouting)
	}
	if len(provenance.SnapshotHash) != 64 || len(provenance.StateHash) != 64 ||
		provenance.SnapshotHash == provenance.StateHash {
		t.Fatalf("hashes = %#v", provenance)
	}
}

func assertRuntimeExecutionProvenanceRedacted(
	t *testing.T,
	provenance *RuntimeExecutionProvenanceV1,
) {
	t.Helper()
	raw, err := json.Marshal(provenance)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(raw)
	for _, forbidden := range []string{
		"secret-value", "private.invalid", "hidden prompt", "workflow state",
		"toolArguments", `"endpoint"`, `"apiKey"`, `"prompt"`,
	} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("provenance leaked %q: %s", forbidden, payload)
		}
	}
}

func TestRuntimeExecutionProvenanceRejectsMutableRun(t *testing.T) {
	actor := model.ActorRef{TenantID: testProvenanceTenantID, ActorID: testProvenanceActorID}
	store := &runtimeExecutionProvenanceStore{run: model.Run{
		RunID: testProvenanceRunID, Actor: actor, Status: model.RunStatusRunning,
		RunConfigSnapshotJSON: `{}`,
	}}
	engine := &Engine{repo: store}

	_, err := engine.GetRuntimeExecutionProvenance(t.Context(), actor, testProvenanceRunID)
	if !errors.Is(err, ErrRuntimeExecutionProvenanceNotFrozen) {
		t.Fatalf("error = %v", err)
	}
}

func TestWorkspaceToolExecutionProvenanceFreezesMutableRunInvocation(t *testing.T) {
	run := model.Run{
		RunID: testProvenanceRunID, RootRunID: testProvenanceRootRunID,
		RuntimeKind: model.RuntimeKindText, Status: model.RunStatusRunning,
		RunConfigSnapshotJSON: `{"prompt":"hidden","apiKey":"secret"}`,
		RequestFingerprint:    "request_hash", CurrentStepID: "step_1",
		LastEventSeq: 8, LastPresentationEventSeq: 7,
		PlatformModelName: "5.6 Terra", UpstreamModelName: testProvenanceModel,
	}
	workspace := WorkspaceSnapshot{
		SnapshotID: testProvenanceSnapshotID,
		StateHash:  "story_state_hash",
		Prompt:     "private workspace prompt",
	}
	first, err := runtimeWorkspaceToolExecutionProvenance(
		run,
		"step_1",
		workspace,
		"tool_call_1",
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtimeWorkspaceToolExecutionProvenance(
		run,
		"step_1",
		workspace,
		"tool_call_2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != run.RunID || first.StateHash == second.StateHash {
		t.Fatalf("invocation provenance first=%#v second=%#v", first, second)
	}
	assertRuntimeExecutionProvenanceRouteAndHashes(t, first)
	assertRuntimeExecutionProvenanceRedacted(t, first)
}

func TestRuntimeExecutionProvenanceStateHashCoversWorkflowState(t *testing.T) {
	actor := model.ActorRef{TenantID: testProvenanceTenantID, ActorID: testProvenanceActorID}
	store := &runtimeExecutionProvenanceStore{
		run: model.Run{
			RunID: testProvenanceRunID, Actor: actor, RuntimeKind: model.RuntimeKindWorkflow,
			Status: model.RunStatusCompleted, RunConfigSnapshotJSON: `{"version":1}`,
		},
		workflow: &model.WorkflowExecution{
			RunID: testProvenanceRunID, Version: 1, Status: model.WorkflowExecutionCompleted,
			StateJSON: `{"value":1}`,
		},
	}
	engine := &Engine{repo: store}
	first, err := engine.GetRuntimeExecutionProvenance(t.Context(), actor, testProvenanceRunID)
	if err != nil {
		t.Fatal(err)
	}

	store.workflow.StateJSON = `{"value":2}`
	second, err := engine.GetRuntimeExecutionProvenance(t.Context(), actor, testProvenanceRunID)
	if err != nil {
		t.Fatal(err)
	}
	if first.SnapshotHash != second.SnapshotHash || first.StateHash == second.StateHash {
		t.Fatalf("hash coverage first=%#v second=%#v", first, second)
	}
}
