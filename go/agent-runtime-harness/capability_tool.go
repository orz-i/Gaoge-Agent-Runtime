package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/team"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workflow"
)

const CapabilityInvocationToolKey = "harness.invoke_capability"

const capabilityCommandIDField = "commandID"

var ErrCapabilityInvocationToolUnbound = errors.New("harness capability invocation tool is not bound")

// CapabilityMaterializeRequest contains only the frozen descriptor and
// product-level arguments required to materialize one typed Runtime Feature.
type CapabilityMaterializeRequest struct {
	Descriptor CommandDescriptor
	Goal       string
	Arguments  json.RawMessage
	Config     ConfigSnapshot
}

type TeamCapabilitySpec struct {
	Mode    team.ExecutionMode
	Members []team.Member
	Join    handoff.Join
}

type PlanExecuteCapabilitySpec struct {
	Model           string
	AllowedToolKeys []string
	ApprovalPolicy  planexecute.ApprovalPolicy
	MaxSteps        int
}

type WorkflowCapabilitySpec struct {
	Definition workflow.Definition
	Input      json.RawMessage
}

type ApplicationCapabilitySpec struct {
	Input json.RawMessage
}

// CapabilityInvocationSpec is a closed typed union. Exactly one Runtime
// Feature payload must match the frozen descriptor ExecutionClass.
type CapabilityInvocationSpec struct {
	Team        *TeamCapabilitySpec
	PlanExecute *PlanExecuteCapabilitySpec
	Workflow    *WorkflowCapabilitySpec
	Application *ApplicationCapabilitySpec
}

// CapabilityInvocationMaterializer is the single static host port that turns
// a data-only command descriptor into typed Runtime Feature input. It never
// executes Runs and is not a callback registry.
type CapabilityInvocationMaterializer interface {
	MaterializeCapability(context.Context, CapabilityMaterializeRequest) (CapabilityInvocationSpec, error)
}

type capabilityInvocationToolInput struct {
	CommandID string          `json:"commandID"`
	Goal      string          `json:"goal"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type CapabilityInvocationToolHandler struct {
	mu           sync.RWMutex
	runner       *Runner
	materializer CapabilityInvocationMaterializer
}

func NewCapabilityInvocationToolHandler(materializer CapabilityInvocationMaterializer) *CapabilityInvocationToolHandler {
	return &CapabilityInvocationToolHandler{materializer: materializer}
}

func (handler *CapabilityInvocationToolHandler) Bind(runner *Runner) error {
	if handler == nil || runner == nil || handler.materializer == nil {
		return ErrInvalidRequest
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.runner != nil && handler.runner != runner {
		return ErrConflict
	}
	handler.runner = runner
	return nil
}

func (handler *CapabilityInvocationToolHandler) Execute(
	ctx context.Context,
	request tools.ExecutionRequest,
) (tools.ExecutionResult, error) {
	runner, err := handler.boundRunner(request)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	turn, config, err := capabilityToolContext(ctx, runner, request.RunID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	input, descriptor, spec, err := handler.materializeCapability(ctx, config, request.Call.Arguments)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	parentItemID := stableID("hit", turn.ID, request.Call.ID, string(ItemStarted))
	if _, err = runner.startMaterializedCapability(ctx, turn.ID, parentItemID, request.Call.ID, input.Goal, descriptor, spec); err != nil {
		return tools.ExecutionResult{}, err
	}
	invocationID, err := InvocationID(turn.ID, parentItemID, descriptor.CapabilityKey, request.Call.ID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	return runner.capabilityToolResult(ctx, descriptor.ID, invocationID)
}

func capabilityToolContext(ctx context.Context, runner *Runner, runID string) (Turn, ConfigSnapshot, error) {
	turn, root, harnessRun, err := timelineTurn(ctx, runner.store, runID)
	if err != nil || !harnessRun || root.ExecutionClass != ExecutionAgent || strings.TrimSpace(root.ParentItemID) != "" {
		return Turn{}, ConfigSnapshot{}, tools.NewRecoverableCallError(
			"capability.invalid_parent", "capability invocation is available only to the root Harness agent", err,
		)
	}
	config, err := runner.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	return turn, config, err
}

func (handler *CapabilityInvocationToolHandler) materializeCapability(
	ctx context.Context,
	config ConfigSnapshot,
	raw json.RawMessage,
) (capabilityInvocationToolInput, CommandDescriptor, CapabilityInvocationSpec, error) {
	input, descriptor, err := decodeCapabilityToolInput(raw, config.Commands)
	if err != nil {
		return capabilityInvocationToolInput{}, CommandDescriptor{}, CapabilityInvocationSpec{}, err
	}
	spec, err := handler.materializer.MaterializeCapability(ctx, CapabilityMaterializeRequest{
		Descriptor: descriptor, Goal: input.Goal, Arguments: input.Arguments, Config: config,
	})
	if err != nil {
		return capabilityInvocationToolInput{}, CommandDescriptor{}, CapabilityInvocationSpec{}, err
	}
	if err = validateCapabilityInvocationSpec(descriptor, spec); err != nil {
		return capabilityInvocationToolInput{}, CommandDescriptor{}, CapabilityInvocationSpec{}, tools.NewRecoverableCallError(
			"capability.invalid_arguments", "capability arguments could not be materialized", err,
		)
	}
	return input, descriptor, spec, nil
}

func (handler *CapabilityInvocationToolHandler) boundRunner(request tools.ExecutionRequest) (*Runner, error) {
	if handler == nil || strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.Call.ID) == "" ||
		request.Call.ToolKey != CapabilityInvocationToolKey || !json.Valid(request.Call.Arguments) {
		return nil, tools.ErrInvalidCall
	}
	handler.mu.RLock()
	runner := handler.runner
	handler.mu.RUnlock()
	if runner == nil {
		return nil, ErrCapabilityInvocationToolUnbound
	}
	return runner, nil
}

func decodeCapabilityToolInput(raw json.RawMessage, commands []CommandDescriptor) (capabilityInvocationToolInput, CommandDescriptor, error) {
	var input capabilityInvocationToolInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return capabilityInvocationToolInput{}, CommandDescriptor{}, tools.NewRecoverableCallError("capability.invalid_input", "invalid capability invocation input", err)
	}
	input.CommandID = strings.TrimSpace(input.CommandID)
	input.Goal = strings.TrimSpace(input.Goal)
	if len(input.Arguments) == 0 {
		input.Arguments = json.RawMessage(`{}`)
	}
	if input.CommandID == "" || input.Goal == "" || !json.Valid(input.Arguments) {
		return capabilityInvocationToolInput{}, CommandDescriptor{}, tools.NewRecoverableCallError("capability.invalid_input", "commandID, goal, and valid arguments are required", ErrInvalidRequest)
	}
	descriptor, ok := frozenCommand(commands, input.CommandID)
	if !ok {
		return capabilityInvocationToolInput{}, CommandDescriptor{}, tools.NewRecoverableCallError("capability.not_authorized", "the requested capability is not authorized for this Harness Turn", ErrNotFound)
	}
	return input, descriptor, nil
}

func frozenCommand(commands []CommandDescriptor, id string) (CommandDescriptor, bool) {
	for _, descriptor := range commands {
		if descriptor.ID == id {
			return cloneCommandDescriptor(descriptor), true
		}
	}
	return CommandDescriptor{}, false
}

func validateCapabilityInvocationSpec(descriptor CommandDescriptor, spec CapabilityInvocationSpec) error {
	executionClass, err := capabilityInvocationSpecClass(spec)
	if err != nil || executionClass != descriptor.ExecutionClass {
		return ErrInvalidRequest
	}
	return nil
}

func capabilityInvocationSpecClass(spec CapabilityInvocationSpec) (ExecutionClass, error) {
	classes := make([]ExecutionClass, 0, 1)
	if spec.Team != nil {
		classes = append(classes, ExecutionTeam)
	}
	if spec.PlanExecute != nil {
		classes = append(classes, ExecutionPlanExecute)
	}
	if spec.Workflow != nil {
		classes = append(classes, ExecutionWorkflow)
	}
	if spec.Application != nil {
		classes = append(classes, ExecutionApplication)
	}
	if len(classes) != 1 {
		return "", ErrInvalidRequest
	}
	return classes[0], nil
}

func (runner *Runner) startMaterializedCapability(
	ctx context.Context,
	turnID, parentItemID, requestID, goal string,
	descriptor CommandDescriptor,
	spec CapabilityInvocationSpec,
) (Snapshot, error) {
	switch {
	case spec.Team != nil:
		return runner.StartTeamInvocation(ctx, turnID, TeamInvocationRequest{
			ParentItemID: parentItemID, RequestID: requestID, Goal: goal,
			Mode: spec.Team.Mode, Members: append([]team.Member(nil), spec.Team.Members...), Join: spec.Team.Join,
		})
	case spec.PlanExecute != nil:
		return runner.StartPlanExecuteInvocation(ctx, turnID, PlanExecuteInvocationRequest{
			ParentItemID: parentItemID, RequestID: requestID, Goal: goal,
			AllowedToolKeys: append([]string{}, spec.PlanExecute.AllowedToolKeys...),
			Model:           spec.PlanExecute.Model, ApprovalPolicy: spec.PlanExecute.ApprovalPolicy, MaxSteps: spec.PlanExecute.MaxSteps,
		})
	case spec.Workflow != nil:
		return runner.StartWorkflowInvocation(ctx, turnID, WorkflowInvocationRequest{
			ParentItemID: parentItemID, RequestID: requestID, Goal: goal,
			Definition: spec.Workflow.Definition, Input: append(json.RawMessage(nil), spec.Workflow.Input...),
		})
	case spec.Application != nil:
		return runner.StartApplicationInvocation(ctx, turnID, ApplicationInvocationRequest{
			ParentItemID: parentItemID, RequestID: requestID, Goal: goal,
			CapabilityKey: descriptor.CapabilityKey, DefinitionVersion: descriptor.DefinitionVersion,
			Input: append(json.RawMessage(nil), spec.Application.Input...),
		})
	default:
		return Snapshot{}, ErrInvalidRequest
	}
}

func (runner *Runner) capabilityToolResult(ctx context.Context, commandID, invocationID string) (tools.ExecutionResult, error) {
	invocation, err := runner.store.GetInvocation(ctx, invocationID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	disposition := "committed"
	payload := map[string]any{
		capabilityCommandIDField: commandID, "invocationID": invocation.ID, "status": invocation.Status,
	}
	switch invocation.Status {
	case InvocationAccepted, InvocationRunning, InvocationWaitingInput:
		disposition = tools.ReceiptDispositionPending
	case InvocationCompleted:
		if invocation.ExecutionClass == ExecutionApplication {
			payload["outputRefs"] = append([]HostRef(nil), invocation.OutputRefs...)
			break
		}
		runtimeSnapshot, loadErr := runner.runtime.Load(ctx, invocation.ExecutionRefID)
		if loadErr != nil {
			return tools.ExecutionResult{}, loadErr
		}
		if runtimeSnapshot.Result != nil {
			payload["result"] = map[string]any{
				"contentType": runtimeSnapshot.Result.ContentType,
				"content":     runtimeSnapshot.Result.Content,
			}
		}
	default:
		payload["error"] = map[string]string{"code": invocation.ErrorCode, "detail": invocation.ErrorDetail}
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	return tools.ExecutionResult{
		Content: content, Receipt: tools.Receipt{ExecutionID: invocation.ID, Disposition: disposition},
	}, nil
}

func CapabilityInvocationToolRegistration(handler *CapabilityInvocationToolHandler) tools.Registration {
	return tools.Registration{
		Definition: tools.Definition{
			Key: CapabilityInvocationToolKey, Name: "invoke_capability",
			Description: "Invoke one authorized Harness capability as a durable child execution. Choose only from the command IDs in this Tool schema.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["commandID","goal"],"properties":{"commandID":{"type":"string"},"goal":{"type":"string","minLength":1,"maxLength":200000},"arguments":{"type":"object"}}}`),
		},
		Handler: handler,
	}
}

func CapabilityInvocationToolPolicySnapshot() ToolPolicySnapshot {
	return ToolPolicySnapshot{
		Key: CapabilityInvocationToolKey, DefinitionVersion: "harness-v1",
		ApprovalCapability: "per_call", ApprovalMode: "never",
		RiskLevel: "low", SideEffectLevel: "compute", IdempotencyMode: "request_key",
	}
}

// CapabilityInvocationModelMiddleware narrows the generic registry definition
// to the exact command union sealed in the current Harness Config Snapshot.
type CapabilityInvocationModelMiddleware struct{ store Store }

func NewCapabilityInvocationModelMiddleware(store Store) (*CapabilityInvocationModelMiddleware, error) {
	if store == nil {
		return nil, ErrInvalidRequest
	}
	return &CapabilityInvocationModelMiddleware{store: store}, nil
}

func (*CapabilityInvocationModelMiddleware) Name() string { return "harness.capability_schema" }

func (middleware *CapabilityInvocationModelMiddleware) Model(
	ctx context.Context,
	request model.Request,
	emit model.StreamSink,
	next plugin.ModelNext,
) (model.Response, error) {
	turn, _, harnessRun, err := timelineTurn(ctx, middleware.store, request.RunID)
	if err != nil || !harnessRun {
		return next(ctx, request, emit)
	}
	config, err := middleware.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil {
		return model.Response{}, err
	}
	schema, err := capabilityInvocationToolSchema(config.Commands)
	if err != nil {
		return model.Response{}, err
	}
	for index := range request.Tools {
		if request.Tools[index].Key == CapabilityInvocationToolKey {
			request.Tools[index].InputSchema = schema
		}
	}
	return next(ctx, request, emit)
}

func capabilityInvocationToolSchema(commands []CommandDescriptor) (json.RawMessage, error) {
	variants := make([]any, 0, len(commands))
	for _, descriptor := range commands {
		variants = append(variants, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{capabilityCommandIDField, "goal"},
			"properties": map[string]any{
				capabilityCommandIDField: map[string]any{"const": descriptor.ID},
				"goal":                   map[string]any{"type": "string", "minLength": 1, "maxLength": 200000},
				"arguments":              descriptor.InputSchema,
			},
		})
	}
	if len(variants) == 0 {
		return nil, ErrInvalidRequest
	}
	return json.Marshal(map[string]any{"oneOf": variants})
}
