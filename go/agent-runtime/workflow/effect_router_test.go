package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestStaticEffectRouterAuthorizesThenSelectsExactClass(t *testing.T) {
	t.Parallel()
	executor := &routerExecutor{}
	authorizer := &routerAuthorizer{}
	router, err := workflow.NewStaticEffectRouter(authorizer, workflow.EffectRoute{
		Class: workflow.EffectClassApplication, Executor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := workflow.EffectRequest{
		RunID: "run-1", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		DefinitionID: "story.change-set.v1", DefinitionHash: "hash", NodeID: "validate",
		Class: workflow.EffectClassApplication, Kind: "story.validate", Revision: "cap-v2",
		Policy: workflow.DefinitionPolicy{
			RequiredPermissions: []string{"story:write"}, CostClass: workflow.CostLow,
			SideEffectClass: workflow.SideEffectWrite,
		},
	}
	result, err := router.Execute(t.Context(), request)
	if err != nil || result.ReceiptID != "receipt" || executor.calls != 1 ||
		authorizer.authorization.Kind != "story.validate" ||
		authorizer.authorization.RequiredPermissions[0] != "story:write" {
		t.Fatalf("result=%#v executor=%#v authorization=%#v err=%v", result, executor, authorizer.authorization, err)
	}
}

func TestStaticEffectRouterRejectsBeforeExecutor(t *testing.T) {
	t.Parallel()
	executor := &routerExecutor{}
	router, err := workflow.NewStaticEffectRouter(denyingEffectAuthorizer{}, workflow.EffectRoute{
		Class: workflow.EffectClassMedia, Executor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Execute(t.Context(), workflow.EffectRequest{Class: workflow.EffectClassMedia})
	if !errors.Is(err, workflow.ErrEffectForbidden) || executor.calls != 0 {
		t.Fatalf("calls=%d err=%v", executor.calls, err)
	}
	_, err = router.Execute(t.Context(), workflow.EffectRequest{Class: workflow.EffectClassAgent})
	if !errors.Is(err, workflow.ErrEffectUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

type routerExecutor struct{ calls int }

func (executor *routerExecutor) Execute(
	context.Context,
	workflow.EffectRequest,
) (workflow.EffectResult, error) {
	executor.calls++
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted, ReceiptID: "receipt"}, nil
}

type routerAuthorizer struct{ authorization workflow.EffectAuthorization }

func (authorizer *routerAuthorizer) AuthorizeEffect(
	_ context.Context,
	authorization workflow.EffectAuthorization,
) error {
	authorizer.authorization = authorization
	return nil
}

type denyingEffectAuthorizer struct{}

func (denyingEffectAuthorizer) AuthorizeEffect(context.Context, workflow.EffectAuthorization) error {
	return errors.New("denied")
}
