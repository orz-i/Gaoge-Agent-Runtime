package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueErrorC990847E             = "error"
	errWorkspaceMultipleJSONValues = "workspace must contain one JSON value"
	errWorkspaceUnsupportedSchema  = "unsupported workspace schema"
)

var (
	errWorkspaceMultipleJSON     = errors.New(errWorkspaceMultipleJSONValues)
	errWorkspaceSchema           = errors.New(errWorkspaceUnsupportedSchema)
	ErrHostProjectionUnavailable = errors.New("host projection is unavailable")
)

// ConfigProvider supplies the latest Conversation configuration snapshot.
// Implementations must make Snapshot safe for concurrent use.
type ConfigProvider interface {
	Snapshot() Config
}

type staticConfigProvider struct{ value Config }

func StaticConfigProvider(value Config) ConfigProvider { return staticConfigProvider{value: value} }
func (p staticConfigProvider) Snapshot() Config        { return p.value }

// Dependencies is the complete immutable dependency set for Agent Runtime.
type Dependencies struct {
	Store               Store
	ThreadContext       ThreadContextSource
	ProjectionContent   ProjectionContentSource
	TurnProjections     TurnProjectionWriter
	Attachments         AttachmentResolver
	Settings            ActorSettingsSource
	Cache               GenerationStreamCacheRepository
	TextModelGateway    TextModelGateway
	Billing             Billing
	Knowledge           KnowledgeDependencies
	Memory              Memory
	Skills              SkillResolver
	EnvironmentProfiles EnvironmentProfileResolver
	UnitOfWork          UnitOfWork
	ToolCatalog         ToolCatalog
	ToolExecutor        ToolExecutor
	Workspaces          WorkspaceRegistry
	Audit               AuditWriter
	Logger              Logger
	Tracer              Tracer
	Evaluations         EvaluationRegistry
	Clock               RuntimeClock
	IDSource            RuntimeIDSource
}

type WorkspaceSelection struct {
	Kind       string   `json:"kind"`
	ID         string   `json:"id,omitempty"`
	TargetKind string   `json:"targetKind,omitempty"`
	TargetID   string   `json:"targetID,omitempty"`
	BlockIDs   []string `json:"blockIDs,omitempty"`
}

type WorkspaceDirectiveSource struct {
	Kind           string                `json:"kind"`
	Thread         domain.ThreadRef      `json:"thread"`
	HeadProjection domain.ProjectionRef  `json:"headProjection"`
	Projection     *domain.ProjectionRef `json:"projection,omitempty"`
	EvidenceID     string                `json:"evidenceID,omitempty"`
}

type WorkspaceDirective struct {
	ActionID          string                    `json:"actionID,omitempty"`
	ArtifactContract  string                    `json:"artifactContract,omitempty"`
	ExecutionStage    string                    `json:"executionStage,omitempty"`
	SourceChangeSetID string                    `json:"sourceChangeSetID,omitempty"`
	Source            *WorkspaceDirectiveSource `json:"source,omitempty"`
}

type WorkspaceRequest struct {
	SchemaVersion    int                 `json:"schemaVersion"`
	Type             string              `json:"type"`
	ResourceID       string              `json:"resourceID,omitempty"`
	Selection        *WorkspaceSelection `json:"selection,omitempty"`
	ExpectedRevision uint64              `json:"expectedRevision,omitempty"`
	Directive        *WorkspaceDirective `json:"directive,omitempty"`
}

func (request *WorkspaceRequest) UnmarshalJSON(data []byte) error {
	type wire WorkspaceRequest
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errWorkspaceMultipleJSON
	}
	if decoded.SchemaVersion != RuntimeSnapshotVersion || strings.TrimSpace(decoded.Type) == "" {
		return errWorkspaceSchema
	}
	*request = WorkspaceRequest(decoded)
	return nil
}

type ResolvedWorkspaceContext struct {
	SchemaVersion    int                 `json:"schemaVersion"`
	Type             string              `json:"type"`
	ResourceID       string              `json:"resourceID"`
	Selection        WorkspaceSelection  `json:"selection"`
	Revision         uint64              `json:"revision"`
	ArtifactContract string              `json:"artifactContract"`
	Directive        *WorkspaceDirective `json:"directive,omitempty"`
}

type WorkspaceToolDefinition struct {
	ToolKey, Name, Description string
	InputSchema, OutputSchema  json.RawMessage
	SideEffectLevel            string
}

type WorkspaceSnapshot struct {
	SchemaVersion    int                       `json:"schemaVersion"`
	Request          ResolvedWorkspaceContext  `json:"request"`
	Revision         uint64                    `json:"revision"`
	SnapshotID       string                    `json:"snapshotID"`
	StateHash        string                    `json:"stateHash"`
	ContentJSON      string                    `json:"contentJSON"`
	Prompt           string                    `json:"prompt"`
	TokenEstimate    int64                     `json:"tokenEstimate"`
	ContextBudget    int                       `json:"contextBudget"`
	ExpectedArtifact string                    `json:"expectedArtifact"`
	Policy           WorkspacePolicy           `json:"policy"`
	Tools            []WorkspaceToolDefinition `json:"tools"`
}

type WorkspacePolicy struct {
	RequiredArtifact       bool                   `json:"requiredArtifact"`
	TerminalArtifactTypes  []string               `json:"terminalArtifactTypes,omitempty"`
	ArtifactResourceField  string                 `json:"artifactResourceField,omitempty"`
	AllowAskUser           bool                   `json:"allowAskUser"`
	AllowPublishOutput     bool                   `json:"allowPublishOutput"`
	SerializeToolProtocols []string               `json:"serializeToolProtocols,omitempty"`
	Failure                WorkspaceFailurePolicy `json:"failure,omitempty"`
}

// WorkspaceFailurePolicy freezes provider-owned user receipts while Runtime
// retains ownership of the generic failure lifecycle.
type WorkspaceFailurePolicy struct {
	RequiredArtifactErrorCode        string `json:"requiredArtifactErrorCode,omitempty"`
	CorrectionPrompt                 string `json:"correctionPrompt,omitempty"`
	RequiredToolCallAssistantContent string `json:"requiredToolCallAssistantContent,omitempty"`
	DefaultAssistantContent          string `json:"defaultAssistantContent,omitempty"`
	ToolResultBudgetGuidance         string `json:"toolResultBudgetGuidance,omitempty"`
}

type WorkspaceToolExecution struct {
	Actor                                     domain.ActorRef
	Thread                                    domain.ThreadRef
	RunID, RequestID, ToolName, ArgumentsJSON string
	Snapshot                                  WorkspaceSnapshot
}

type WorkspaceProvider interface {
	CompileWorkspace(context.Context, domain.ActorRef, domain.ThreadRef, *WorkspaceRequest, int) (*WorkspaceSnapshot, error)
	ExecuteWorkspaceTool(context.Context, WorkspaceToolExecution) (string, error)
}

type WorkspaceReceiptProvider interface {
	ExecuteWorkspaceToolWithReceipt(context.Context, WorkspaceToolExecution) (ToolExecutionResult, error)
}

// WorkspaceRouteValidation contains provider-neutral facts measured from the
// exact model route and tool payload selected for a run step.
type WorkspaceRouteValidation struct {
	ModelCapabilitiesJSON    string
	ProviderProtocol         string
	ToolCount                int
	ProviderToolPayloadBytes int
	ProviderPayloadObserved  bool
}

// WorkspaceRouteValidator is an optional WorkspaceProvider capability. A
// provider uses it to enforce bounded-context-specific model requirements.
type WorkspaceRouteValidator interface {
	ValidateWorkspaceRoute(context.Context, WorkspaceRouteValidation) error
}

type WorkspaceErrorKind string

const (
	WorkspaceErrorCapability       WorkspaceErrorKind = "capability"
	WorkspaceErrorInvalidInput     WorkspaceErrorKind = "invalid_input"
	WorkspaceErrorConflict         WorkspaceErrorKind = "conflict"
	WorkspaceErrorRequiredArtifact WorkspaceErrorKind = "required_artifact"
)

// WorkspaceErrorClassification is provider-owned classification data. Code and
// user receipts are intentionally opaque to Runtime.
type WorkspaceErrorClassification struct {
	Kind                     WorkspaceErrorKind
	Code                     string
	Message                  string
	Diagnostic               string
	Deterministic            bool
	AssistantContent         string
	RepeatedAssistantContent string
}

// WorkspaceErrorClassifier is an optional WorkspaceProvider capability used at
// compile, route-validation, and tool-execution boundaries.
type WorkspaceErrorClassifier interface {
	ClassifyWorkspaceError(error) (WorkspaceErrorClassification, bool)
}

// WorkspaceError carries an opaque provider classification through Runtime's
// generic retry, persistence, and failure-finalization paths.
type WorkspaceError struct {
	classification WorkspaceErrorClassification
	cause          error
}

func NewWorkspaceError(classification WorkspaceErrorClassification, cause error) *WorkspaceError {
	return &WorkspaceError{classification: classification, cause: cause}
}

func (e *WorkspaceError) Error() string {
	if e == nil {
		return "workspace error"
	}
	if diagnostic := strings.TrimSpace(e.classification.Diagnostic); diagnostic != "" {
		return diagnostic
	}
	if e.cause != nil && strings.TrimSpace(e.cause.Error()) != "" {
		return e.cause.Error()
	}
	if message := strings.TrimSpace(e.classification.Message); message != "" {
		return message
	}
	if code := strings.TrimSpace(e.classification.Code); code != "" {
		return code
	}
	return "workspace error"
}

func (e *WorkspaceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is preserves Runtime's transport-neutral error semantics while retaining the
// provider-owned diagnostic and receipt carried by WorkspaceError.
func (e *WorkspaceError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch e.classification.Kind {
	case WorkspaceErrorInvalidInput:
		return target == ErrInvalidInput
	case WorkspaceErrorConflict:
		return target == ErrWorkspaceSourceStale
	default:
		return false
	}
}

func (e *WorkspaceError) DeterministicToolFailure() bool {
	return e != nil && e.classification.Deterministic
}

func (e *WorkspaceError) Code() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.classification.Code)
}

func (e *WorkspaceError) AssistantContent(repeated bool) string {
	if e == nil {
		return ""
	}
	if repeated {
		if content := strings.TrimSpace(e.classification.RepeatedAssistantContent); content != "" {
			return content
		}
	}
	return strings.TrimSpace(e.classification.AssistantContent)
}

func classifyWorkspaceProviderError(provider WorkspaceProvider, err error) error {
	if err == nil || provider == nil {
		return err
	}
	var classified *WorkspaceError
	if errors.As(err, &classified) {
		return err
	}
	classifier, ok := provider.(WorkspaceErrorClassifier)
	if !ok {
		return err
	}
	classification, ok := classifier.ClassifyWorkspaceError(err)
	if !ok {
		return err
	}
	return NewWorkspaceError(classification, err)
}

// WorkspaceRegistry resolves immutable bounded-context providers by workspace
// type. Runtime never interprets provider-owned workspace types.
type WorkspaceRegistry interface {
	ResolveWorkspace(string) (WorkspaceProvider, bool)
}

type staticWorkspaceRegistry struct {
	providers map[string]WorkspaceProvider
}

// NewWorkspaceRegistry freezes the supplied provider set for the lifetime of a
// Runtime service. Empty names and nil providers are ignored.
func NewWorkspaceRegistry(providers map[string]WorkspaceProvider) WorkspaceRegistry {
	frozen := make(map[string]WorkspaceProvider, len(providers))
	for kind, provider := range providers {
		kind = strings.TrimSpace(kind)
		if kind != "" && provider != nil {
			frozen[kind] = provider
		}
	}
	return staticWorkspaceRegistry{providers: frozen}
}

func (r staticWorkspaceRegistry) ResolveWorkspace(kind string) (WorkspaceProvider, bool) {
	provider, ok := r.providers[strings.TrimSpace(kind)]
	return provider, ok
}

type MemoryItem struct {
	Actor     domain.ActorRef
	MemoryKey string
	Value     string
	Scope     string
	UpdatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Memory interface {
	UpsertUserMemory(context.Context, domain.ActorRef, string, string, string, string) error
	ListUserMemories(context.Context, domain.ActorRef) ([]MemoryItem, error)
	SearchUserMemoriesByEmbedding(context.Context, domain.ActorRef, []float32, int, float64) ([]MemoryItem, error)
	UpsertUserMemoryEmbedding(context.Context, domain.ActorRef, string, string, []float32) error
}

const (
	SkillScopeBuiltin = "builtin"
	SkillScopeUser    = "user"
)

type Skill struct {
	Ref         domain.ResourceRef
	Scope       string
	Owner       domain.ActorRef
	Title       string
	Trigger     string
	Description string
	Markdown    string
	Enabled     bool
	SortOrder   int
	CreatedBy   domain.ActorRef
	UpdatedBy   domain.ActorRef
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type SkillResolver interface {
	ResolveAvailable(context.Context, domain.ActorRef, domain.ResourceRef) (*Skill, error)
}

type EnvironmentModelPolicy struct {
	PlatformModelName                string
	IsDefault, Selectable, Available bool
	UnavailableReason                string
}
type EnvironmentToolPolicy struct {
	ToolKey, ActivationMode string
	Available               bool
	UnavailableReason       string
}
type EnvironmentSkillPolicy struct {
	SkillRef          domain.ResourceRef
	ActivationMode    string
	Available         bool
	UnavailableReason string
	Title             string
	Trigger           string
	Description       string
}
type UnavailableEnvironmentCapability struct {
	Kind     string
	Key      string
	SkillRef domain.ResourceRef
	Reason   string
}
type EnvironmentProfile struct {
	Ref                                         domain.ResourceRef
	Revision                                    uint
	SystemKey, Name, Description, Instructions  string
	DefaultMode, PlanApprovalMode, MemoryPolicy string
	BindingScopes, AllowedModes                 []string
	Models                                      []EnvironmentModelPolicy
	Skills                                      []EnvironmentSkillPolicy
	Tools                                       []EnvironmentToolPolicy
	UnavailableRequiredCapabilities             []UnavailableEnvironmentCapability
	UpdatedAt                                   time.Time
}

func (p *EnvironmentProfile) SupportsBindingScope(scope string) bool {
	if p == nil {
		return false
	}
	for _, candidate := range p.BindingScopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

type EnvironmentProfileResolver interface {
	ResolveAvailableEnvironmentProfile(context.Context, domain.ActorRef, domain.ResourceRef) (*EnvironmentProfile, error)
	ResolveBuiltinEnvironmentProfile(context.Context, string) (*EnvironmentProfile, error)
}

type ResolvedTool struct {
	ToolKey            string
	ProviderKind       string
	ProviderKey        string
	ModelName          string
	OriginalName       string
	Description        string
	DefinitionVersion  string
	InputSchema        json.RawMessage
	OutputSchema       json.RawMessage
	ExecutionMode      string
	ApprovalCapability string
	ApprovalMode       string
	RiskLevel          string
	SideEffectLevel    string
	IdempotencyMode    string
	HostedVariants     []HostedToolVariant
}

type ToolCatalog interface {
	DefaultToolKeys(context.Context, string, string, string) ([]string, error)
	ResolveAvailable(context.Context, domain.ActorRef, []string, string, string, string) ([]ResolvedTool, []string, error)
}

type ToolExecutionInput struct {
	ToolKey       string
	ProviderKind  string
	ProviderKey   string
	ToolName      string
	ArgumentsJSON string
	Actor         domain.ActorRef
	Thread        domain.ThreadRef
	RequestID     string
}

type ToolExecutor interface {
	Execute(context.Context, ToolExecutionInput) (string, error)
}

const (
	ToolSideEffectRead        = "read"
	ToolSideEffectStagedWrite = "staged_write"
	ToolSideEffectWrite       = "write"
	ToolSideEffectDestructive = "destructive"

	ToolIdempotencyNone            = "none"
	ToolIdempotencyRequestKey      = "request_key"
	ToolIdempotencyProviderReceipt = "provider_receipt"

	ToolReceiptCommitted = "committed"
	ToolReceiptReplayed  = "replayed"
)

type ToolExecutionReceipt struct {
	RequestID           string `json:"requestID"`
	ProviderExecutionID string `json:"providerExecutionID"`
	Disposition         string `json:"disposition"`
}

type ToolExecutionResult struct {
	OutputJSON string               `json:"outputJSON"`
	Receipt    ToolExecutionReceipt `json:"receipt"`
	Attempts   int                  `json:"attempts,omitempty"`
}

type ReceiptToolExecutor interface {
	ExecuteWithReceipt(context.Context, ToolExecutionInput) (ToolExecutionResult, error)
}

type AuditWriter interface {
	Write(context.Context, string, domain.ActorRef, string, domain.ThreadRef, string, string, interface{})
}

type LogField struct {
	Key   string
	Value interface{}
}

func String(key, value string) LogField          { return LogField{Key: key, Value: value} }
func Uint(key string, value uint) LogField       { return LogField{Key: key, Value: value} }
func Int(key string, value int) LogField         { return LogField{Key: key, Value: value} }
func Int64(key string, value int64) LogField     { return LogField{Key: key, Value: value} }
func Int32(key string, value int32) LogField     { return LogField{Key: key, Value: value} }
func Float64(key string, value float64) LogField { return LogField{Key: key, Value: value} }
func Bool(key string, value bool) LogField       { return LogField{Key: key, Value: value} }
func Error(err error) LogField                   { return LogField{Key: valueErrorC990847E, Value: err} }

type Logger interface {
	Debug(string, ...LogField)
	Info(string, ...LogField)
	Warn(string, ...LogField)
	Error(string, ...LogField)
}

type Tracer interface {
	TraceID(context.Context) string
	Start(context.Context, string, ...LogField) (context.Context, Span)
	Inject(context.Context) TraceContext
	Extract(context.Context, TraceContext) context.Context
}

// TraceContext is the W3C trace carrier persisted across durable Runtime jobs.
// Baggage is intentionally excluded from scheduler storage.
type TraceContext struct {
	TraceParent string
	TraceState  string
}

type Span interface {
	End()
	SetAttributes(...LogField)
	RecordError(error)
}

type UsageBalanceReservation struct {
	Actor         domain.ActorRef
	AmountNanousd int64
	RefNo         string
}

type UsageLedger struct {
	IdempotencyKey      string
	Actor               domain.ActorRef
	Thread              domain.ThreadRef
	ServiceCode         string
	ProviderProtocol    string
	UpstreamName        string
	PlatformModelName   string
	RoutedBindingCode   string
	UpstreamModelName   string
	IsFreeModel         bool
	BillingAt           time.Time
	UsageDate           time.Time
	InputTokens         int64
	CacheReadTokens     int64
	CacheWriteTokens    int64
	CacheWrite5mTokens  int64
	CacheWrite1hTokens  int64
	OutputTokens        int64
	ReasoningTokens     int64
	CallCount           int64
	DurationSeconds     int64
	LatencyMS           int64
	UsageSpeed          string
	ServiceTier         string
	BilledCurrency      string
	BilledNanousd       int64
	PricingSnapshotJSON string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type ServiceUsageInput struct {
	ServiceCode, ServiceName, PlatformModelName, UpstreamModelName, ProviderProtocol       string
	CacheTimeout, RequestSpeed, UsageSpeed, RequestServiceTier, UsageServiceTier           string
	BillingRateClass                                                                       string
	InputTokens, CacheReadTokens, CacheWriteTokens, CacheWrite5mTokens, CacheWrite1hTokens int64
	OutputTokens, ReasoningTokens, CallCount, DurationSeconds                              int64
}

type UsagePricingInput struct {
	IdempotencyKey                                                                                 string
	ServiceCode                                                                                    string
	Actor                                                                                          domain.ActorRef
	Thread                                                                                         domain.ThreadRef
	PlatformModelName, RoutedBindingCode, ProviderProtocol, UpstreamName, UpstreamModelName        string
	CacheTimeout, RequestSpeed, UsageSpeed, RequestServiceTier, UsageServiceTier, BillingRateClass string
	ServiceOnly                                                                                    bool
	InputTokens, CacheReadTokens, CacheWriteTokens, CacheWrite5mTokens, CacheWrite1hTokens         int64
	OutputTokens, ReasoningTokens, CallCount, DurationSeconds, LatencyMS                           int64
	ServerSideToolUsage                                                                            map[string]int64
	ServiceItems                                                                                   []ServiceUsageInput
	RawUsageJSON                                                                                   string
	BillingAt                                                                                      time.Time
}

type Billing interface {
	EnsureModelUsable(context.Context, domain.ActorRef, string, time.Time) error
	ReserveUsageBalance(context.Context, domain.ActorRef, string, string) (*UsageBalanceReservation, bool, error)
	ReleaseUsageBalanceReservation(context.Context, *UsageBalanceReservation, string) error
	BuildUsageLedger(context.Context, UsagePricingInput) (*UsageLedger, error)
	RecordUsage(context.Context, *UsageLedger) error
	RecordUsageWithReservation(context.Context, *UsageLedger, *UsageBalanceReservation) error
}
