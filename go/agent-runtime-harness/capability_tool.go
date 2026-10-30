package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
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
	Actor      kernel.ActorRef
	Thread     kernel.ThreadRef
}

type TeamCapabilitySpec struct {
	Mode    team.ExecutionMode
	Members []team.Member
	Join    handoff.Join
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
	turn, session, config, err := capabilityToolContext(ctx, runner, request.RunID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	input, descriptor, spec, err := handler.materializeCapability(
		ctx, session.Actor, kernel.ThreadRef{Kind: session.HostThread.Kind, ID: session.HostThread.ID},
		config, request.Call.Arguments,
	)
	if err != nil {
		// The materializer validates a frozen command before any child
		// invocation is started. An explicitly correctable rejection can
		// therefore attest no physical child effect under the original ID.
		return tools.RecoverableNoEffectResult(request.Call.ID, err)
	}
	parentItemID := stableID("hit", turn.ID, request.Call.ID, string(ItemStarted))
	if _, err = runner.startMaterializedCapability(ctx, turn.ID, parentItemID, request.Call.ID, input.Goal, descriptor, spec); err != nil {
		return tools.ExecutionResult{}, err
	}
	invocationID, err := InvocationID(turn.ID, parentItemID, descriptor.CapabilityKey, request.Call.ID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	return runner.capabilityToolResult(ctx, descriptor.ID, invocationID, request.Call.ID)
}

// VerifyCommittedTool reads the same frozen command and child identity as
// Execute, but never materializes, starts, retries, or resumes a capability.
// The authoritative child status is obtained read-only from its original
// Invocation/Kernel Run and must be terminal before a COMMIT is accepted.
func (handler *CapabilityInvocationToolHandler) VerifyCommittedTool(
	ctx context.Context, request tools.ExecutionRequest,
) (tools.ExecutionResult, error) {
	runner, err := handler.boundRunner(request)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	turn, _, config, err := capabilityToolContext(ctx, runner, request.RunID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	_, descriptor, err := decodeCapabilityToolInput(request.Call.Arguments, config.Commands)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	parentItemID := stableID("hit", turn.ID, request.Call.ID, string(ItemStarted))
	invocationID, err := InvocationID(turn.ID, parentItemID, descriptor.CapabilityKey, request.Call.ID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	result, err := runner.capabilityToolResult(ctx, descriptor.ID, invocationID, request.Call.ID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if result.Receipt.Disposition != "committed" {
		return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
	}
	return result, nil
}

func capabilityToolContext(
	ctx context.Context,
	runner *Runner,
	runID string,
) (Turn, Session, ConfigSnapshot, error) {
	turn, root, harnessRun, err := timelineTurn(ctx, runner.store, runID)
	if err != nil || !harnessRun || root.ExecutionClass != ExecutionAgent || strings.TrimSpace(root.ParentItemID) != "" {
		return Turn{}, Session{}, ConfigSnapshot{}, tools.NewRecoverableCallError(
			"capability.invalid_parent", "capability invocation is available only to the root Harness agent", err,
		)
	}
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return Turn{}, Session{}, ConfigSnapshot{}, err
	}
	config, err := runner.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	return turn, session, config, err
}

func (handler *CapabilityInvocationToolHandler) materializeCapability(
	ctx context.Context,
	actor kernel.ActorRef,
	thread kernel.ThreadRef,
	config ConfigSnapshot,
	raw json.RawMessage,
) (capabilityInvocationToolInput, CommandDescriptor, CapabilityInvocationSpec, error) {
	input, descriptor, err := decodeCapabilityToolInput(raw, config.Commands)
	if err != nil {
		return capabilityInvocationToolInput{}, CommandDescriptor{}, CapabilityInvocationSpec{}, err
	}
	spec, err := handler.materializer.MaterializeCapability(ctx, CapabilityMaterializeRequest{
		Descriptor: descriptor, Goal: input.Goal, Arguments: input.Arguments, Config: config,
		Actor: actor, Thread: thread,
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
	// The catalog Tool schema is generic; the model middleware only narrows its
	// declaration. Enforce the selected frozen command before host decoding so
	// invalid nested types can be corrected without starting a child execution.
	if err := tools.ValidateCall(tools.Definition{
		Key: CapabilityInvocationToolKey, Name: "invoke_capability", InputSchema: descriptor.InputSchema,
	}, tools.Call{ID: "capability-arguments", ToolKey: CapabilityInvocationToolKey, Arguments: input.Arguments}); err != nil {
		return capabilityInvocationToolInput{}, CommandDescriptor{}, err
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

func (runner *Runner) capabilityToolResult(ctx context.Context, commandID, invocationID, callID string) (tools.ExecutionResult, error) {
	invocation, err := runner.store.GetInvocation(ctx, invocationID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	// Projection may lag a terminal child Kernel fact after a restart.
	// Read the child without mutating the Harness Store or reentering its
	// physical Feature. The result is for Tool receipt verification only.
	if invocation.ExecutionClass != ExecutionApplication &&
		(invocation.Status == InvocationRunning || invocation.Status == InvocationWaitingInput ||
			invocation.Status == InvocationPausedBudget) {
		child, loadErr := runner.runtime.Load(ctx, invocation.ExecutionRefID)
		if loadErr != nil {
			return tools.ExecutionResult{}, loadErr
		}
		if terminalRuntimeStatus(child.Run.Status) {
			invocation.Status = InvocationStatus(child.Run.Status)
			invocation.ErrorCode = child.Run.ErrorCode
			invocation.ErrorDetail = child.Run.ErrorDetail
		}
	}
	disposition := "committed"
	payload := map[string]any{
		capabilityCommandIDField: commandID, "invocationID": invocation.ID, "status": invocation.Status,
	}
	switch invocation.Status {
	case InvocationAccepted, InvocationRunning, InvocationWaitingInput, InvocationPausedBudget:
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
	case InvocationFailed, InvocationCancelled:
		payload["error"] = map[string]string{"code": invocation.ErrorCode, "detail": invocation.ErrorDetail}
	default:
		payload["error"] = map[string]string{"code": invocation.ErrorCode, "detail": invocation.ErrorDetail}
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	var pending *tools.PendingExecution
	if disposition == tools.ReceiptDispositionPending && invocation.ExecutionClass != ExecutionApplication {
		pending = &tools.PendingExecution{
			Kind: tools.PendingChildTerminal, OperationID: invocation.ID, ChildRunID: invocation.ExecutionRefID,
		}
	}
	return tools.ExecutionResult{
		Content: content, Receipt: tools.Receipt{ExecutionID: callID, Disposition: disposition},
		Pending: pending,
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
		Key: CapabilityInvocationToolKey, DefinitionVersion: harnessToolDefinitionVersion,
		ApprovalCapability: approvalCapabilityPerCall, ApprovalMode: approvalModeNever,
		RiskLevel: toolRiskLevelLow, SideEffectLevel: "compute", IdempotencyMode: toolIdempotencyRequestKey,
	}
}

// NewCapabilityInvocationRequestPreparer freezes the exact command Tool schema
// before the ModelInvocation is stored. No post-intent schema transformer.
func NewCapabilityInvocationRequestPreparer(store Store) (agent.ModelRequestPreparer, error) {
	if store == nil {
		return nil, ErrInvalidRequest
	}
	return func(ctx context.Context, request model.Request) (model.Request, error) {
		if !containsCapabilityInvocationTool(request.Tools) {
			return request, nil
		}
		turn, _, harnessRun, err := timelineTurn(ctx, store, request.RunID)
		if err != nil {
			return model.Request{}, err
		}
		if !harnessRun {
			return model.Request{}, ErrInvalidRequest
		}
		config, err := store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
		if err != nil {
			return model.Request{}, err
		}
		schema, err := capabilityInvocationToolSchema(config.Commands)
		if err != nil {
			return model.Request{}, err
		}
		request = model.CloneRequest(request)
		for index := range request.Tools {
			if request.Tools[index].Key == CapabilityInvocationToolKey {
				request.Tools[index].InputSchema = schema
			}
		}
		return request, nil
	}, nil
}

func containsCapabilityInvocationTool(definitions []tools.Definition) bool {
	for _, definition := range definitions {
		if definition.Key == CapabilityInvocationToolKey {
			return true
		}
	}
	return false
}

func capabilityInvocationToolSchema(commands []CommandDescriptor) (json.RawMessage, error) {
	variants := make([]any, 0, len(commands))
	commandIDs := make([]string, 0, len(commands))
	for _, descriptor := range commands {
		commandIDs = append(commandIDs, descriptor.ID)
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
	arguments, err := capabilityArgumentSchema(commands)
	if err != nil {
		return nil, err
	}
	// Expose ordinary object properties as well as the exact command union.
	// Providers that infer nested argument types from properties must not see
	// an empty object just because the constraints also include oneOf.
	return json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{capabilityCommandIDField, "goal"},
		"properties": map[string]any{
			capabilityCommandIDField: map[string]any{"type": "string", "enum": commandIDs},
			"goal":                   map[string]any{"type": "string", "minLength": 1, "maxLength": 200000},
			"arguments":              arguments,
		},
		"oneOf": variants,
	})
}

func capabilityArgumentSchema(commands []CommandDescriptor) (map[string]any, error) {
	properties := make(map[string]json.RawMessage)
	for _, command := range commands {
		raw := bytes.TrimSpace(command.InputSchema)
		if bytes.Equal(raw, []byte("true")) || bytes.Equal(raw, []byte("false")) {
			return map[string]any{"type": "object"}, nil
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			return nil, errors.Join(ErrInvalidRequest, err)
		}
		// Copying a property out of its resource can detach local references,
		// dialects or anchors from their scope. Keep those complete schemas in
		// oneOf and omit the optional flattened hint for this command catalog.
		if capabilitySchemaHasScope(document) {
			return map[string]any{"type": "object"}, nil
		}
		var schema struct {
			Properties           map[string]json.RawMessage `json:"properties"`
			AdditionalProperties json.RawMessage            `json:"additionalProperties"`
			PatternProperties    map[string]json.RawMessage `json:"patternProperties"`
		}
		// An open command can accept a property with any type. Projecting another
		// command's type onto that name would narrow its original contract.
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, errors.Join(ErrInvalidRequest, err)
		}
		if !bytes.Equal(bytes.TrimSpace(schema.AdditionalProperties), []byte("false")) || len(schema.PatternProperties) != 0 {
			return map[string]any{"type": "object"}, nil
		}
		for name, property := range schema.Properties {
			if previous, exists := properties[name]; exists && !bytes.Equal(previous, property) {
				merged, err := json.Marshal(map[string]any{"anyOf": []json.RawMessage{previous, property}})
				if err != nil {
					return nil, err
				}
				property = merged
			}
			properties[name] = property
		}
	}
	return map[string]any{"type": "object", "properties": properties}, nil
}

func capabilitySchemaHasScope(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			switch key {
			case "$id", "$schema", "$ref", "$defs", "definitions", "$anchor", "$dynamicRef", "$dynamicAnchor", "$recursiveRef", "$recursiveAnchor":
				return true
			}
			if capabilitySchemaHasScope(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if capabilitySchemaHasScope(child) {
				return true
			}
		}
	}
	return false
}
