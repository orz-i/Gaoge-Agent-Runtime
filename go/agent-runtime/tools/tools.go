package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const (
	CapabilityCatalog  kernel.Capability = "tools.catalog"
	CapabilityExecutor kernel.Capability = "tools.executor"

	// MaxExecutionResultContentBytes is the absolute in-process safety ceiling for one exact Tool
	// result payload. Larger payloads must be externalized by the Tool instead of entering Runtime
	// state or Context artifacts as inline JSON.
	MaxExecutionResultContentBytes = 4 << 20
)

var (
	ErrInvalidDefinition = errors.New("invalid tool definition")
	ErrDuplicateTool     = errors.New("duplicate tool definition")
	ErrToolNotFound      = errors.New("tool not found")
	ErrInvalidCall       = errors.New("invalid tool call")
)

const recoverableCallErrorMessageLimit = 2000

// RecoverableCallError marks a Tool call rejection that the model can correct
// without restarting the Run. Infrastructure, authorization, cancellation,
// and concurrency failures must remain ordinary fatal errors.
type RecoverableCallError struct {
	Code            string
	Message         string
	Cause           error
	BlockedToolKeys []string
	// Some correctable capability refusals allow an honest final answer rather
	// than requiring another Tool call. Default remains strict correction.
	AllowCompletion bool
}

// RetryableExecutionError marks a transient executor failure that is safe to
// retry with the same stable ExecutionRequest.Call.ID. Executors must only use
// it when repeating the call cannot duplicate an externally visible effect.
type RetryableExecutionError struct {
	Cause error
}

func (err *RetryableExecutionError) Error() string {
	if err == nil || err.Cause == nil {
		return "retryable tool execution error"
	}
	return err.Cause.Error()
}

func (err *RetryableExecutionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// NewRetryableExecutionError explicitly opts one transient Tool failure into
// bounded retries. A nil error remains nil.
func NewRetryableExecutionError(err error) error {
	if err == nil {
		return nil
	}
	return &RetryableExecutionError{Cause: err}
}

// IsRetryableExecutionError reports whether an executor explicitly declared
// that replaying the same stable Tool call is safe.
func IsRetryableExecutionError(err error) bool {
	var retryable *RetryableExecutionError
	return errors.As(err, &retryable)
}

// ValidateDefinition compiles the Tool input schema and proves that the
// definition is safe to register or expose to an Agent. Compiled schemas are
// cached by jsoncontract and reused by later call validation.
func ValidateDefinition(definition Definition) error {
	definition.Key = strings.TrimSpace(definition.Key)
	definition.Name = strings.TrimSpace(definition.Name)
	if definition.Key == "" || definition.Name == "" {
		return ErrInvalidDefinition
	}
	if _, err := jsoncontract.Compile(definition.InputSchema); err != nil {
		return errors.Join(ErrInvalidDefinition, err)
	}
	return nil
}

// ValidateCall enforces a Tool's JSON Schema contract before approval policy or
// handler execution. Instance violations are model-correctable call errors;
// invalid registered schemas remain fatal definition errors.
func ValidateCall(definition Definition, call Call) error {
	if !validCall(call) || strings.TrimSpace(call.ToolKey) != strings.TrimSpace(definition.Key) {
		return ErrInvalidCall
	}
	validator, err := jsoncontract.Compile(definition.InputSchema)
	if err != nil {
		return errors.Join(ErrInvalidDefinition, err)
	}
	if err = validator.Validate(call.Arguments); err != nil {
		return NewRecoverableCallError(
			"tool.arguments_schema",
			"Tool arguments do not match the declared input schema: "+err.Error(),
			errors.Join(ErrInvalidCall, err),
		)
	}
	return nil
}

// CloneDefinition returns an isolated Tool definition copy.
func CloneDefinition(definition Definition) Definition {
	definition.InputSchema = cloneJSON(definition.InputSchema)
	return definition
}

// CloneCall returns an isolated Tool call copy.
func CloneCall(call Call) Call {
	call.Arguments = cloneJSON(call.Arguments)
	return call
}

// CloneExecutionRequest returns an isolated Tool execution request copy.
func CloneExecutionRequest(request ExecutionRequest) ExecutionRequest {
	request.Call = CloneCall(request.Call)
	return request
}

// CloneExecutionResult returns an isolated Tool execution result copy.
func CloneExecutionResult(result ExecutionResult) ExecutionResult {
	result.Content = cloneJSON(result.Content)
	result.Pending = ClonePending(result.Pending)
	return result
}

// ValidateExecutionResult validates the stable Tool result boundary.
func ValidateExecutionResult(result ExecutionResult) error {
	if !validExecutionResult(result) ||
		result.Receipt.Disposition == ReceiptDispositionRejected {
		// A rejected receipt must have an accompanying trusted typed
		// no-effect error, never be consumed as ordinary Tool success.
		return ErrInvalidCall
	}
	return nil
}

func (err *RecoverableCallError) Error() string {
	if err == nil {
		return ErrInvalidCall.Error()
	}
	if strings.TrimSpace(err.Message) != "" {
		return strings.TrimSpace(err.Message)
	}
	if err.Cause != nil {
		return err.Cause.Error()
	}
	return ErrInvalidCall.Error()
}

func (err *RecoverableCallError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// NewRecoverableCallError creates an explicit model-correctable Tool error.
func NewRecoverableCallError(code, message string, cause error) error {
	return newRecoverableCallError(code, message, cause, nil)
}

// NewRecoverableCallErrorAllowCompletion signals a non-retryable capability
// refusal. It still records a structured recoverable Tool result, but the
// Agent may finish without being forced to call another Tool.
func NewRecoverableCallErrorAllowCompletion(code, message string, cause error) error {
	err := newRecoverableCallError(code, message, cause, nil)
	var typed *RecoverableCallError
	if errors.As(err, &typed) {
		typed.AllowCompletion = true
	}
	return err
}

// RecoverableCallErrorAllowsCompletion detects this explicit opt-in only.
func RecoverableCallErrorAllowsCompletion(err error) bool {
	var target *RecoverableCallError
	return errors.As(err, &target) && target != nil && target.AllowCompletion
}

// NewRecoverableCallErrorWithBlockedTools creates a correctable Tool error and
// marks Tools that cannot make further progress for removal from later turns.
func NewRecoverableCallErrorWithBlockedTools(
	code string,
	message string,
	cause error,
	blockedToolKeys ...string,
) error {
	return newRecoverableCallError(code, message, cause, blockedToolKeys)
}

func newRecoverableCallError(
	code string,
	message string,
	cause error,
	blockedToolKeys []string,
) error {
	code = strings.TrimSpace(code)
	if code == "" {
		code = "tool_call_invalid"
	}
	message = strings.TrimSpace(message)
	if message == "" && cause != nil {
		message = strings.TrimSpace(cause.Error())
	}
	if message == "" {
		message = ErrInvalidCall.Error()
	}
	runes := []rune(message)
	if len(runes) > recoverableCallErrorMessageLimit {
		message = string(runes[:recoverableCallErrorMessageLimit])
	}
	return &RecoverableCallError{
		Code: code, Message: message, Cause: cause,
		BlockedToolKeys: normalizedBlockedToolKeys(blockedToolKeys),
	}
}

// RecoverableCallErrorInfo returns safe model-facing correction metadata.
func RecoverableCallErrorInfo(err error) (string, string, bool) {
	var target *RecoverableCallError
	if !errors.As(err, &target) || target == nil {
		return "", "", false
	}
	return strings.TrimSpace(target.Code), target.Error(), true
}

// RecoverableCallErrorBlockedToolKeys returns an isolated list of Tools that
// must not be offered again after this correction.
func RecoverableCallErrorBlockedToolKeys(err error) []string {
	var target *RecoverableCallError
	if !errors.As(err, &target) || target == nil {
		return nil
	}
	return append([]string(nil), target.BlockedToolKeys...)
}

func normalizedBlockedToolKeys(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// Definition is the model-visible, provider-neutral Tool contract.
type Definition struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Terminal    bool            `json:"terminal,omitempty"`
}

// Call is one stable Tool intent produced by a Text model.
type Call struct {
	ID        string          `json:"id"`
	ToolKey   string          `json:"toolKey"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments"`
}

// ExecutionRequest carries a stable call identity for idempotent execution.
type ExecutionRequest struct {
	RunID string
	Call  Call
}

// Receipt records the executor identity and replay disposition.
type Receipt struct {
	ExecutionID string `json:"executionID"`
	Disposition string `json:"disposition"`
}

// ReceiptDispositionPending keeps the current Agent Tool call durable without
// consuming it. A composed continuation may resume the same idempotent call
// after its child execution changes state.
const ReceiptDispositionPending = "pending"

// ReceiptDispositionRejected is an authoritative, terminal certification by
// the trusted Tool Handler that NO external effect was performed. It permits
// model-correctable guidance only with the exact frozen Call ID; error types
// alone cannot certify no-effect after the Tool/Middleware boundary.
const ReceiptDispositionRejected = "rejected"

func isModelCorrectableError(err error) bool {
	_, _, ok := RecoverableCallErrorInfo(err)
	return ok
}

// RecoverableNoEffectResult binds a pre-effect model-correctable refusal to
// the original Tool Call ID. A Handler MUST use it only if it knows that no
// external operation occurred. This is a trusted Handler attestation, not a
// claim that the Agent can independently inspect remote side effects.
func RecoverableNoEffectResult(callID string, cause error) (ExecutionResult, error) {
	if strings.TrimSpace(callID) == "" || !isModelCorrectableError(cause) {
		return ExecutionResult{}, cause
	}
	return ExecutionResult{
		Content: json.RawMessage("{}"),
		Receipt: Receipt{ExecutionID: callID, Disposition: ReceiptDispositionRejected},
	}, cause
}

// ValidRecoverableNoEffectReceipt is the only post-dispatch exception to
// fail-closed handling of errors. Both the Tool result and the typed refusal
// must be present, with an exact Call ID and explicit no-effect disposition.
// validExactNoEffectReceipt is the one identity proof for a trusted Handler
// rejection. The caller separately verifies the *kind* of refusal: a model
// correction or a bounded immediate retry. The string status on its own is
// never authorization to physically reexecute a Tool.
func validExactNoEffectReceipt(result ExecutionResult, callID string) bool {
	return strings.TrimSpace(callID) != "" &&
		validExecutionResult(result) && result.Receipt.ExecutionID == callID &&
		result.Receipt.Disposition == ReceiptDispositionRejected && result.Pending == nil
}

func ValidRecoverableNoEffectReceipt(result ExecutionResult, cause error, callID string) bool {
	return isModelCorrectableError(cause) && validExactNoEffectReceipt(result, callID)
}

// RetryableNoEffectResult is the explicit original-CallID no-effect proof
// required for a transient Handler result to authorize a bounded retry.
// Merely returning RetryableExecutionError (including after a remote write)
// MUST NOT execute the same Call a second time.
func RetryableNoEffectResult(callID string, cause error) (ExecutionResult, error) {
	if strings.TrimSpace(callID) == "" || !IsRetryableExecutionError(cause) {
		return ExecutionResult{}, cause
	}
	return ExecutionResult{
		Content: json.RawMessage("{}"),
		Receipt: Receipt{ExecutionID: callID, Disposition: ReceiptDispositionRejected},
	}, cause
}

// ValidRetryableNoEffectReceipt requires explicit original-ID evidence of
// no physical effect, not just a typed error or a transient transport status.
func ValidRetryableNoEffectReceipt(result ExecutionResult, cause error, callID string) bool {
	return IsRetryableExecutionError(cause) && validExactNoEffectReceipt(result, callID)
}

// ExecutionResult is the JSON Tool output and its durable receipt.
type ExecutionResult struct {
	Content json.RawMessage   `json:"content"`
	Receipt Receipt           `json:"receipt"`
	Pending *PendingExecution `json:"pending,omitempty"`
}

// Catalog resolves immutable Tool definitions.
type Catalog interface {
	Resolve(string) (Definition, bool)
	List([]string) ([]Definition, error)
}

// Executor executes one stable Tool intent. Implementations must honor ctx
// cancellation and deadlines and return promptly when ctx is done.
type Executor interface {
	Execute(context.Context, ExecutionRequest) (ExecutionResult, error)
}

// Handler executes one registered Tool and follows the same context contract as
// Executor.
type Handler interface {
	Execute(context.Context, ExecutionRequest) (ExecutionResult, error)
}

// HandlerFunc adapts a function into a Handler.
type HandlerFunc func(context.Context, ExecutionRequest) (ExecutionResult, error)

// Execute calls the adapted handler function.
func (handler HandlerFunc) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	return handler(ctx, request)
}

// Registration binds one immutable definition to its executor.
type Registration struct {
	Definition Definition
	Handler    Handler
}

// Registry is an immutable, explicitly composed Tool catalog and executor.
type Registry struct {
	definitions map[string]Definition
	handlers    map[string]Handler
}

// NewRegistry validates and freezes Tool registrations.
func NewRegistry(registrations []Registration) (*Registry, error) {
	registry := &Registry{
		definitions: make(map[string]Definition, len(registrations)),
		handlers:    make(map[string]Handler, len(registrations)),
	}
	for _, registration := range registrations {
		definition, err := normalizeDefinition(registration.Definition)
		if err != nil || registration.Handler == nil {
			return nil, ErrInvalidDefinition
		}
		if _, duplicate := registry.definitions[definition.Key]; duplicate {
			return nil, ErrDuplicateTool
		}
		registry.definitions[definition.Key] = definition
		registry.handlers[definition.Key] = registration.Handler
	}
	return registry, nil
}

// Descriptor provides the Tool catalog and executor capabilities.
func (registry *Registry) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{
		Name: "tools", Provides: []kernel.Capability{CapabilityCatalog, CapabilityExecutor},
	}
}

// Resolve returns one isolated Tool definition.
func (registry *Registry) Resolve(key string) (Definition, bool) {
	if registry == nil {
		return Definition{}, false
	}
	definition, ok := registry.definitions[strings.TrimSpace(key)]
	return cloneDefinition(definition), ok
}

// List resolves the exact selected Tool set in caller order.
func (registry *Registry) List(keys []string) ([]Definition, error) {
	result := make([]Definition, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, ErrToolNotFound
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		definition, ok := registry.Resolve(key)
		if !ok {
			return nil, ErrToolNotFound
		}
		seen[key] = struct{}{}
		result = append(result, definition)
	}
	return result, nil
}

// Execute dispatches one validated Tool call by stable key.
func (registry *Registry) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	if registry == nil || !validCall(request.Call) || strings.TrimSpace(request.RunID) == "" {
		return ExecutionResult{}, ErrInvalidCall
	}
	definition, ok := registry.definitions[request.Call.ToolKey]
	if !ok {
		return ExecutionResult{}, ErrToolNotFound
	}
	if err := ValidateCall(definition, request.Call); err != nil {
		return ExecutionResult{}, err
	}
	handler, ok := registry.handlers[request.Call.ToolKey]
	if !ok {
		return ExecutionResult{}, ErrToolNotFound
	}
	result, err := handler.Execute(ctx, request)
	if err != nil {
		if ValidRecoverableNoEffectReceipt(result, err, request.Call.ID) ||
			ValidRetryableNoEffectReceipt(result, err, request.Call.ID) {
			// Trusted Handler returned an exact no-effect attestation.
			// Preserve it across this Executor boundary. A bare typed
			// error still discards any unsupported or mismatched result.
			return CloneExecutionResult(result), err
		}
		return ExecutionResult{}, err
	}
	if err := ValidateExecutionResult(result); err != nil {
		return ExecutionResult{}, err
	}
	return CloneExecutionResult(result), nil
}

func normalizeDefinition(definition Definition) (Definition, error) {
	definition.Key = strings.TrimSpace(definition.Key)
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Description = strings.TrimSpace(definition.Description)
	definition.InputSchema = cloneJSON(definition.InputSchema)
	if err := ValidateDefinition(definition); err != nil {
		return Definition{}, ErrInvalidDefinition
	}
	return definition, nil
}

func validCall(call Call) bool {
	return strings.TrimSpace(call.ID) != "" && strings.TrimSpace(call.ToolKey) != "" && json.Valid(call.Arguments)
}

func validExecutionResult(result ExecutionResult) bool {
	if len(result.Content) > MaxExecutionResultContentBytes ||
		!json.Valid(result.Content) ||
		strings.TrimSpace(result.Receipt.ExecutionID) == "" ||
		!ValidPending(result.Pending) {
		return false
	}
	switch result.Receipt.Disposition {
	case "read", "committed", ReceiptDispositionPending, ReceiptDispositionRejected:
		return result.Pending == nil || result.Receipt.Disposition == ReceiptDispositionPending
	default:
		// Unknown provider/client dispositions are NOT evidence of a
		// terminal effect. Never clear a durable Agent ToolDispatchID.
		return false
	}
}

// PendingKind distinguishes scheduled retries from durable external/child waits.
type PendingKind string

const (
	PendingScheduled      PendingKind = "scheduled"
	PendingExternalResult PendingKind = "external_result"
	PendingChildTerminal  PendingKind = "child_terminal"
)

// PendingExecution contains only neutral execution identities. Host credentials,
// delivery leases and executor-specific inputs must remain outside Runtime.
type PendingExecution struct {
	Kind        PendingKind `json:"kind"`
	OperationID string      `json:"operationID,omitempty"`
	InputDigest string      `json:"inputDigest,omitempty"`
	DeadlineAt  *time.Time  `json:"deadlineAt,omitempty"`
	ChildRunID  string      `json:"childRunID,omitempty"`
}

// ClonePending isolates a pending descriptor and its deadline.
func ClonePending(value *PendingExecution) *PendingExecution {
	if value == nil {
		return nil
	}
	cloned := *value
	if value.DeadlineAt != nil {
		deadline := value.DeadlineAt.UTC()
		cloned.DeadlineAt = &deadline
	}
	return &cloned
}

// ValidPending validates the typed wait boundary. An omitted descriptor retains
// the existing pending receipt's scheduled behavior.
func ValidPending(value *PendingExecution) bool {
	if value == nil {
		return true
	}
	switch value.Kind {
	case PendingScheduled:
		return value.ChildRunID == ""
	case PendingExternalResult:
		return strings.TrimSpace(value.OperationID) != "" && len(value.OperationID) <= 128 &&
			validDigest(value.InputDigest) && value.DeadlineAt != nil && !value.DeadlineAt.IsZero() && value.ChildRunID == ""
	case PendingChildTerminal:
		return strings.TrimSpace(value.OperationID) != "" && len(value.OperationID) <= 128 &&
			strings.TrimSpace(value.ChildRunID) != "" && len(value.ChildRunID) <= 128
	default:
		return false
	}
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func cloneDefinition(definition Definition) Definition {
	definition.InputSchema = cloneJSON(definition.InputSchema)
	return definition
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
