package workflow_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestDefinitionRegistryResolvesMostSpecificVisibleDefinition(t *testing.T) {
	t.Parallel()
	registry, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), registryClock{})
	if err != nil {
		t.Fatal(err)
	}
	system := workflow.DefinitionScope{Kind: workflow.DefinitionScopeSystem}
	tenant := workflow.DefinitionScope{Kind: workflow.DefinitionScopeTenant, TenantID: "tenant"}
	actor := workflow.DefinitionScope{Kind: workflow.DefinitionScopeActor, TenantID: "tenant", ActorID: "actor"}
	for index, scope := range []workflow.DefinitionScope{system, tenant, actor} {
		_, _, _, err = registry.Publish(t.Context(), workflow.PublishDefinitionRequest{
			Scope: scope, Draft: registryDraft("story.flow", fmt.Sprintf("flow-%d", index)),
			IdempotencyKey: fmt.Sprintf("request-%d", index), PublishedBy: "publisher",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := registry.ResolveForStart(t.Context(), actor, workflow.DefinitionReference{ID: "story.flow"})
	if err != nil || resolved.Scope != actor || resolved.Definition.Name != "flow-2" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	visible, err := registry.ListVisible(t.Context(), actor)
	if err != nil || len(visible) != 1 || visible[0].Scope != actor {
		t.Fatalf("visible=%#v err=%v", visible, err)
	}
}

func TestDefinitionRegistryDisableShadowsBroaderScope(t *testing.T) {
	t.Parallel()
	registry, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), registryClock{})
	if err != nil {
		t.Fatal(err)
	}
	system := workflow.DefinitionScope{Kind: workflow.DefinitionScopeSystem}
	tenant := workflow.DefinitionScope{Kind: workflow.DefinitionScopeTenant, TenantID: "tenant"}
	for index, scope := range []workflow.DefinitionScope{system, tenant} {
		_, _, _, err = registry.Publish(t.Context(), workflow.PublishDefinitionRequest{
			Scope: scope, Draft: registryDraft("story.flow", "flow"),
			IdempotencyKey: fmt.Sprintf("request-%d", index), PublishedBy: "publisher",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = registry.SetActivation(t.Context(), workflow.ActivateDefinitionRequest{
		Scope: tenant, DefinitionID: "story.flow", Availability: workflow.DefinitionDisabled,
		ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.ResolveForStart(t.Context(), tenant, workflow.DefinitionReference{ID: "story.flow"})
	if !errors.Is(err, workflow.ErrDefinitionDisabled) {
		t.Fatalf("resolve disabled error=%v", err)
	}
}

func TestDefinitionRegistryRejectsHashMismatchAndIdempotencyDrift(t *testing.T) {
	t.Parallel()
	registry, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), registryClock{})
	if err != nil {
		t.Fatal(err)
	}
	scope := workflow.DefinitionScope{Kind: workflow.DefinitionScopeSystem}
	request := workflow.PublishDefinitionRequest{
		Scope: scope, Draft: registryDraft("story.flow", "flow"),
		IdempotencyKey: "request", PublishedBy: "publisher",
	}
	created, _, _, err := registry.Publish(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Draft.Name = "changed"
	if _, _, _, err = registry.Publish(t.Context(), request); !errors.Is(err, workflow.ErrDefinitionConflict) {
		t.Fatalf("idempotency drift error=%v", err)
	}
	_, err = registry.ResolveForStart(t.Context(), scope, workflow.DefinitionReference{
		ID: "story.flow", Revision: 1, Hash: "wrong",
	})
	if !errors.Is(err, workflow.ErrDefinitionHash) || created.Definition.Hash == "" {
		t.Fatalf("hash mismatch error=%v created=%#v", err, created)
	}
}

func registryDraft(id, name string) workflow.DefinitionDraft {
	return workflow.DefinitionDraft{
		ID: id, Name: name,
		Nodes: []workflow.Node{
			{ID: "effect", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
				Kind: "test.effect", Input: json.RawMessage(`{"ok":true}`),
			}},
			{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{FromNode: "effect"}},
		},
	}
}

type registryClock struct{}

func (registryClock) Now() time.Time {
	return time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
}
