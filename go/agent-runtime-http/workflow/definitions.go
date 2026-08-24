package workflowhttp

import (
	"context"
	"errors"
	stdhttp "net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

var (
	ErrDefinitionForbidden       = errors.New("workflow definition action is forbidden")
	ErrDefinitionUnauthenticated = errors.New("workflow definition principal is unavailable")
)

// DefinitionAction is one host-authorized Registry or execution operation.
type DefinitionAction string

const (
	DefinitionActionCompile  DefinitionAction = "compile"
	DefinitionActionPublish  DefinitionAction = "publish"
	DefinitionActionRead     DefinitionAction = "read"
	DefinitionActionActivate DefinitionAction = "activate"
	DefinitionActionStart    DefinitionAction = "start"
)

// DefinitionAuthorization contains only declarative policy impact, never prompts or secrets.
type DefinitionAuthorization struct {
	Actor      kernel.ActorRef
	Action     DefinitionAction
	Scope      workflow.DefinitionScope
	Definition *workflow.Definition
	Impact     *workflow.DefinitionPolicyImpact
}

// DefinitionAuthorizer is statically composed by the host application.
type DefinitionAuthorizer interface {
	AuthorizeDefinition(context.Context, DefinitionAuthorization) error
}

type DefinitionScopeRequest struct {
	Kind workflow.DefinitionScopeKind `json:"kind" binding:"required"`
}

type CompileDefinitionRequest struct {
	Scope        DefinitionScopeRequest   `json:"scope" binding:"required"`
	BaseRevision int                      `json:"baseRevision" binding:"omitempty,min=0"`
	Draft        workflow.DefinitionDraft `json:"draft" binding:"required"`
}

type PublishDefinitionRequest struct {
	Scope            DefinitionScopeRequest         `json:"scope" binding:"required"`
	Draft            workflow.DefinitionDraft       `json:"draft" binding:"required"`
	ExpectedRevision int                            `json:"expectedRevision" binding:"omitempty,min=0"`
	Mode             workflow.DefinitionPublishMode `json:"mode" binding:"omitempty"`
	IdempotencyKey   string                         `json:"idempotencyKey" binding:"omitempty,max=200"`
}

type PublishDefinitionResponse struct {
	Revision workflow.DefinitionRevision `json:"revision"`
	Head     workflow.DefinitionHead     `json:"head"`
	Reused   bool                        `json:"reused"`
}

type SetDefinitionActivationRequest struct {
	Scope           DefinitionScopeRequest          `json:"scope" binding:"required"`
	TargetRevision  int                             `json:"targetRevision" binding:"omitempty,min=0"`
	Availability    workflow.DefinitionAvailability `json:"availability" binding:"required"`
	ExpectedVersion uint64                          `json:"expectedVersion" binding:"required,min=1"`
}

type SetDefinitionActivationResponse struct {
	Head   workflow.DefinitionHead `json:"head"`
	Reused bool                    `json:"reused"`
}

func (handler *Handler) CompileDefinition(context *gin.Context) {
	var request CompileDefinitionRequest
	if err := runtimehttp.BindStrictJSON(context, &request); err != nil {
		runtimehttp.InvalidBody(context, err)
		return
	}
	actor, err := handler.actor(context)
	if err != nil {
		writeError(context, err)
		return
	}
	scope, err := definitionScopeForActor(actor, request.Scope.Kind)
	if err != nil {
		writeError(context, err)
		return
	}
	base, err := handler.loadProposalBase(
		context.Request.Context(), scope, request.Draft.ID, request.BaseRevision,
	)
	if err != nil {
		writeError(context, err)
		return
	}
	report, err := workflow.CompileDefinitionProposal(base, request.Draft)
	if err != nil {
		writeError(context, err)
		return
	}
	if err = handler.authorize(context.Request.Context(), DefinitionAuthorization{
		Actor: actor, Action: DefinitionActionCompile, Scope: scope,
		Definition: &report.Candidate, Impact: &report.Impact,
	}, false); err != nil {
		writeError(context, err)
		return
	}
	runtimehttp.WriteSuccess(context, report)
}

func (handler *Handler) PublishDefinition(context *gin.Context) {
	if handler == nil || handler.registry == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "workflow.registry_unavailable", "workflow definition registry is unavailable")
		return
	}
	var request PublishDefinitionRequest
	if err := runtimehttp.BindStrictJSON(context, &request); err != nil {
		runtimehttp.InvalidBody(context, err)
		return
	}
	actor, err := handler.actor(context)
	if err != nil {
		writeError(context, err)
		return
	}
	scope, err := definitionScopeForActor(actor, request.Scope.Kind)
	if err != nil {
		writeError(context, err)
		return
	}
	base, err := handler.loadProposalBase(
		context.Request.Context(), scope, request.Draft.ID, request.ExpectedRevision,
	)
	if err != nil {
		writeError(context, err)
		return
	}
	report, err := workflow.CompileDefinitionProposal(base, request.Draft)
	if err != nil {
		writeError(context, err)
		return
	}
	if err = handler.authorize(context.Request.Context(), DefinitionAuthorization{
		Actor: actor, Action: DefinitionActionPublish, Scope: scope,
		Definition: &report.Candidate, Impact: &report.Impact,
	}, true); err != nil {
		writeError(context, err)
		return
	}
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = strings.TrimSpace(context.GetHeader("Idempotency-Key"))
	}
	revision, head, reused, err := handler.registry.Publish(context.Request.Context(), workflow.PublishDefinitionRequest{
		Scope: scope, Draft: request.Draft, ExpectedRevision: request.ExpectedRevision,
		Mode: request.Mode, IdempotencyKey: idempotencyKey, PublishedBy: actor.ActorID,
	})
	if err != nil {
		writeError(context, err)
		return
	}
	context.JSON(stdhttp.StatusCreated, PublishDefinitionResponse{Revision: revision, Head: head, Reused: reused})
}

func (handler *Handler) ListDefinitions(context *gin.Context) {
	if handler == nil || handler.registry == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "workflow.registry_unavailable", "workflow definition registry is unavailable")
		return
	}
	actor, err := handler.actor(context)
	if err != nil {
		writeError(context, err)
		return
	}
	heads, err := handler.registry.ListVisible(
		context.Request.Context(), actorDefinitionScope(actor),
	)
	if err != nil {
		writeError(context, err)
		return
	}
	runtimehttp.WriteSuccess(context, heads)
}

func (handler *Handler) GetDefinition(context *gin.Context) {
	if handler == nil || handler.registry == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "workflow.registry_unavailable", "workflow definition registry is unavailable")
		return
	}
	actor, err := handler.actor(context)
	if err != nil {
		writeError(context, err)
		return
	}
	scope, err := definitionScopeForActor(actor, workflow.DefinitionScopeKind(strings.TrimSpace(context.Query("scope"))))
	if err != nil {
		writeError(context, err)
		return
	}
	definitionID := strings.TrimSpace(context.Param("definition_id"))
	revisionNumber, err := strconv.Atoi(strings.TrimSpace(context.Param("revision")))
	if err != nil || definitionID == "" || revisionNumber <= 0 {
		writeError(context, workflow.ErrInvalidDefinitionRegistry)
		return
	}
	revision, err := handler.registry.Get(context.Request.Context(), scope, definitionID, revisionNumber)
	if err != nil {
		writeError(context, err)
		return
	}
	if err = handler.authorize(context.Request.Context(), DefinitionAuthorization{
		Actor: actor, Action: DefinitionActionRead, Scope: scope, Definition: &revision.Definition,
	}, false); err != nil {
		writeError(context, err)
		return
	}
	runtimehttp.WriteSuccess(context, revision)
}

func (handler *Handler) SetDefinitionActivation(context *gin.Context) {
	if handler == nil || handler.registry == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "workflow.registry_unavailable", "workflow definition registry is unavailable")
		return
	}
	var request SetDefinitionActivationRequest
	if err := runtimehttp.BindStrictJSON(context, &request); err != nil {
		runtimehttp.InvalidBody(context, err)
		return
	}
	actor, err := handler.actor(context)
	if err != nil {
		writeError(context, err)
		return
	}
	scope, err := definitionScopeForActor(actor, request.Scope.Kind)
	if err != nil {
		writeError(context, err)
		return
	}
	definitionID := strings.TrimSpace(context.Param("definition_id"))
	if definitionID == "" {
		writeError(context, workflow.ErrInvalidDefinitionRegistry)
		return
	}
	if err = handler.authorize(context.Request.Context(), DefinitionAuthorization{
		Actor: actor, Action: DefinitionActionActivate, Scope: scope,
	}, true); err != nil {
		writeError(context, err)
		return
	}
	head, reused, err := handler.registry.SetActivation(context.Request.Context(), workflow.ActivateDefinitionRequest{
		Scope: scope, DefinitionID: definitionID, TargetRevision: request.TargetRevision,
		Availability: request.Availability, ExpectedVersion: request.ExpectedVersion,
	})
	if err != nil {
		writeError(context, err)
		return
	}
	runtimehttp.WriteSuccess(context, SetDefinitionActivationResponse{Head: head, Reused: reused})
}

func (handler *Handler) resolveStartDefinition(
	ctx context.Context,
	actor kernel.ActorRef,
	request StartRunRequest,
) (workflow.Definition, workflow.DefinitionScope, error) {
	if (request.Definition == nil) == (request.DefinitionReference == nil) {
		return workflow.Definition{}, workflow.DefinitionScope{}, workflow.ErrInvalidExecution
	}
	if request.Definition != nil {
		definition, err := workflow.CompileDefinition(*request.Definition)
		return definition, actorDefinitionScope(actor), err
	}
	if handler == nil || handler.registry == nil {
		return workflow.Definition{}, workflow.DefinitionScope{}, workflow.ErrInvalidDefinitionRegistry
	}
	published, err := handler.registry.ResolveForStart(
		ctx, actorDefinitionScope(actor), *request.DefinitionReference,
	)
	return published.Definition, published.Scope, err
}

func (handler *Handler) loadProposalBase(
	ctx context.Context,
	scope workflow.DefinitionScope,
	definitionID string,
	revision int,
) (*workflow.Definition, error) {
	if revision == 0 {
		return nil, nil
	}
	if handler == nil || handler.registry == nil {
		return nil, workflow.ErrInvalidDefinitionRegistry
	}
	published, err := handler.registry.Get(ctx, scope, strings.TrimSpace(definitionID), revision)
	if err != nil {
		return nil, err
	}
	return &published.Definition, nil
}

func (handler *Handler) actor(context *gin.Context) (kernel.ActorRef, error) {
	if handler == nil || handler.shared == nil {
		return kernel.ActorRef{}, ErrDefinitionUnauthenticated
	}
	actor, err := handler.shared.ActorRef(context)
	if err != nil {
		return kernel.ActorRef{}, errors.Join(ErrDefinitionUnauthenticated, err)
	}
	return actor, nil
}

func (handler *Handler) authorize(
	ctx context.Context,
	request DefinitionAuthorization,
	required bool,
) error {
	if handler == nil || handler.authorizer == nil {
		if required {
			return ErrDefinitionForbidden
		}
		return nil
	}
	if err := handler.authorizer.AuthorizeDefinition(ctx, request); err != nil {
		return errors.Join(ErrDefinitionForbidden, err)
	}
	return nil
}

func actorDefinitionScope(actor kernel.ActorRef) workflow.DefinitionScope {
	return workflow.DefinitionScope{
		Kind: workflow.DefinitionScopeActor, TenantID: actor.TenantID, ActorID: actor.ActorID,
	}
}

func definitionScopeForActor(
	actor kernel.ActorRef,
	kind workflow.DefinitionScopeKind,
) (workflow.DefinitionScope, error) {
	if kind == "" {
		kind = workflow.DefinitionScopeActor
	}
	switch kind {
	case workflow.DefinitionScopeActor:
		return workflow.PrepareDefinitionScope(actorDefinitionScope(actor))
	case workflow.DefinitionScopeTenant:
		return workflow.PrepareDefinitionScope(workflow.DefinitionScope{
			Kind: kind, TenantID: actor.TenantID,
		})
	case workflow.DefinitionScopeSystem:
		return workflow.PrepareDefinitionScope(workflow.DefinitionScope{Kind: kind})
	default:
		return workflow.DefinitionScope{}, workflow.ErrInvalidDefinitionRegistry
	}
}

func writeDefinitionError(context *gin.Context, err error) bool {
	switch {
	case errors.Is(err, ErrDefinitionUnauthenticated):
		runtimehttp.WriteError(context, stdhttp.StatusUnauthorized, "auth.unauthorized", "runtime principal is unavailable")
	case errors.Is(err, ErrDefinitionForbidden), errors.Is(err, workflow.ErrEffectForbidden):
		runtimehttp.WriteError(context, stdhttp.StatusForbidden, "workflow.definition_forbidden", "workflow definition action is forbidden")
	case errors.Is(err, workflow.ErrInvalidDefinitionRegistry):
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "workflow.definition_invalid", "invalid workflow definition request")
	case errors.Is(err, workflow.ErrDefinitionNotFound):
		runtimehttp.WriteError(context, stdhttp.StatusNotFound, "workflow.definition_not_found", "workflow definition was not found")
	case errors.Is(err, workflow.ErrDefinitionConflict), errors.Is(err, workflow.ErrDefinitionHash):
		runtimehttp.WriteError(context, stdhttp.StatusConflict, "workflow.definition_conflict", "workflow definition revision conflict")
	case errors.Is(err, workflow.ErrDefinitionDisabled):
		runtimehttp.WriteError(context, stdhttp.StatusConflict, "workflow.definition_disabled", "workflow definition is disabled")
	default:
		return false
	}
	return true
}
