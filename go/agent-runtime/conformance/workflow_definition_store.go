package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// WorkflowDefinitionStoreFactory creates one isolated Definition store per test.
type WorkflowDefinitionStoreFactory func(testing.TB) workflow.DefinitionStore

// RunWorkflowDefinitionStoreSuite verifies immutable revision, CAS and activation semantics.
func RunWorkflowDefinitionStoreSuite(t *testing.T, factory WorkflowDefinitionStoreFactory) {
	t.Helper()
	if factory == nil {
		t.Fatal("workflow definition store factory is required")
	}
	t.Run("publish-idempotency", func(t *testing.T) { testWorkflowPublishIdempotency(t, factory(t)) })
	t.Run("revision-cas", func(t *testing.T) { testWorkflowRevisionCAS(t, factory(t)) })
	t.Run("activation-and-history", func(t *testing.T) { testWorkflowActivationAndHistory(t, factory(t)) })
	t.Run("scope-list", func(t *testing.T) { testWorkflowScopeList(t, factory(t)) })
}

func testWorkflowPublishIdempotency(t *testing.T, store workflow.DefinitionStore) {
	t.Helper()
	mutation := testDefinitionPublish(t, workflow.DefinitionScope{Kind: workflow.DefinitionScopeSystem}, "flow", 0, "request-1")
	created, head, reused, err := store.Publish(context.Background(), mutation)
	if err != nil || reused || head.LatestRevision != 1 || head.ActiveRevision != 1 {
		t.Fatalf("publish = %#v, head=%#v, reused=%t, err=%v", created, head, reused, err)
	}
	replayed := mutation
	replayed.Revision.PublishedAt = replayed.Revision.PublishedAt.Add(time.Hour)
	existing, replayHead, reused, err := store.Publish(context.Background(), replayed)
	if err != nil || !reused || existing.PublishedAt != created.PublishedAt || replayHead.Version != head.Version {
		t.Fatalf("replay = %#v, head=%#v, reused=%t, err=%v", existing, replayHead, reused, err)
	}
	conflict := mutation
	conflict.Revision.RequestFingerprint = "different"
	if _, _, _, err = store.Publish(context.Background(), conflict); !errors.Is(err, workflow.ErrDefinitionConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
}

func testWorkflowRevisionCAS(t *testing.T, store workflow.DefinitionStore) {
	t.Helper()
	first := testDefinitionPublish(t, workflow.DefinitionScope{Kind: workflow.DefinitionScopeSystem}, "flow", 0, "request-1")
	if _, _, _, err := store.Publish(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	stale := testDefinitionPublish(t, first.Revision.Scope, "flow", 0, "request-stale")
	if _, _, _, err := store.Publish(context.Background(), stale); !errors.Is(err, workflow.ErrDefinitionConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
	second := testDefinitionPublish(t, first.Revision.Scope, "flow", 1, "request-2")
	if _, head, _, err := store.Publish(context.Background(), second); err != nil || head.LatestRevision != 2 {
		t.Fatalf("second head=%#v err=%v", head, err)
	}
}

func testWorkflowActivationAndHistory(t *testing.T, store workflow.DefinitionStore) {
	t.Helper()
	scope := workflow.DefinitionScope{Kind: workflow.DefinitionScopeTenant, TenantID: "tenant"}
	first := testDefinitionPublish(t, scope, "flow", 0, "request-1")
	if _, _, _, err := store.Publish(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := testDefinitionPublish(t, scope, "flow", 1, "request-2")
	second.Mode = workflow.PublishStaged
	if _, head, _, err := store.Publish(context.Background(), second); err != nil ||
		head.LatestRevision != 2 || head.ActiveRevision != 1 {
		t.Fatalf("staged head=%#v err=%v", head, err)
	}
	head, reused, err := store.SetActivation(context.Background(), workflow.DefinitionActivationMutation{
		Scope: scope, DefinitionID: "flow", TargetRevision: 2,
		Availability: workflow.DefinitionActive, ExpectedVersion: 2,
		UpdatedAt: time.Date(2026, 8, 24, 0, 0, 3, 0, time.UTC),
	})
	if err != nil || reused || head.ActiveRevision != 2 || head.Version != 3 {
		t.Fatalf("activate head=%#v reused=%t err=%v", head, reused, err)
	}
	head, reused, err = store.SetActivation(context.Background(), workflow.DefinitionActivationMutation{
		Scope: scope, DefinitionID: "flow", TargetRevision: 2,
		Availability: workflow.DefinitionActive, ExpectedVersion: 2,
		UpdatedAt: time.Date(2026, 8, 24, 0, 0, 4, 0, time.UTC),
	})
	if err != nil || !reused || head.Version != 3 {
		t.Fatalf("activation replay head=%#v reused=%t err=%v", head, reused, err)
	}
	head, _, err = store.SetActivation(context.Background(), workflow.DefinitionActivationMutation{
		Scope: scope, DefinitionID: "flow", Availability: workflow.DefinitionDisabled,
		ExpectedVersion: 3, UpdatedAt: time.Date(2026, 8, 24, 0, 0, 5, 0, time.UTC),
	})
	if err != nil || head.Availability != workflow.DefinitionDisabled || head.Version != 4 {
		t.Fatalf("disable head=%#v err=%v", head, err)
	}
	historical, err := store.GetRevision(context.Background(), scope, "flow", 1)
	if err != nil || historical.Definition.Revision != 1 {
		t.Fatalf("historical=%#v err=%v", historical, err)
	}
	historical.Definition.Name = "mutated"
	reloaded, err := store.GetRevision(context.Background(), scope, "flow", 1)
	if err != nil || reloaded.Definition.Name == "mutated" {
		t.Fatalf("immutable reload=%#v err=%v", reloaded, err)
	}
}

func testWorkflowScopeList(t *testing.T, store workflow.DefinitionStore) {
	t.Helper()
	system := workflow.DefinitionScope{Kind: workflow.DefinitionScopeSystem}
	tenant := workflow.DefinitionScope{Kind: workflow.DefinitionScopeTenant, TenantID: "tenant"}
	for _, mutation := range []workflow.DefinitionPublishMutation{
		testDefinitionPublish(t, system, "z-flow", 0, "request-z"),
		testDefinitionPublish(t, system, "a-flow", 0, "request-a"),
		testDefinitionPublish(t, tenant, "tenant-flow", 0, "request-tenant"),
	} {
		if _, _, _, err := store.Publish(context.Background(), mutation); err != nil {
			t.Fatal(err)
		}
	}
	heads, err := store.ListHeads(context.Background(), system)
	if err != nil || len(heads) != 2 || heads[0].DefinitionID != "a-flow" || heads[1].DefinitionID != "z-flow" {
		t.Fatalf("system heads=%#v err=%v", heads, err)
	}
}

func testDefinitionPublish(
	t *testing.T,
	scope workflow.DefinitionScope,
	id string,
	expectedRevision int,
	requestID string,
) workflow.DefinitionPublishMutation {
	t.Helper()
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: id, Revision: expectedRevision + 1, Name: "Test workflow",
		Nodes: []workflow.Node{
			{ID: "effect", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
				Kind: "test.effect", Input: json.RawMessage(`{"ok":true}`),
			}},
			{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{FromNode: "effect"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return workflow.DefinitionPublishMutation{
		Revision: workflow.DefinitionRevision{
			Scope: scope, Definition: definition, PublishedBy: "actor",
			IdempotencyKey: requestID, RequestFingerprint: definition.Hash + requestID,
			PublishedAt: time.Date(2026, 8, 24, 0, 0, expectedRevision+1, 0, time.UTC),
		},
		ExpectedRevision: expectedRevision, Mode: workflow.PublishAndActivate,
	}
}
