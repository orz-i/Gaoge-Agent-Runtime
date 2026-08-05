package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/modelcap"
)

const (
	valueKindDAA7F13C  = "kind"
	valueTitle1D003E0B = "title"
	valueUserDD885A59  = "user"
)

const (
	valueAlways6FAD1299          = "always"
	valueAssistantB2F999C0       = "assistant"
	valueAuto407FFE1D            = "auto"
	valueCheckpointID7923DD64    = "checkpointID"
	valueDefaultD98758A6         = "default"
	valueGoal855E06D1            = "goal"
	valueLocalDispatch71FF6D47   = "local_dispatch"
	valueNever4C6E2E88           = "never"
	valueOrchestration1BD4660D   = "orchestration"
	valuePerCall2570116D         = "per_call"
	valueProviderHosted7ED91AC1  = "provider_hosted"
	valueRead3A612695            = "read"
	valueRequest3E6DBD23         = "request"
	valueRequired466769C7        = "required"
	valueRevision0742568C        = "revision"
	valueRunPreparingA8E38F48    = "run.preparing"
	valueRunWaitingInputF2C37C0A = "run.waiting_input"
	valueStatus6CF1EE63          = "status"
	valueStepID549B95DB          = "stepID"
	valueText34DB77B4            = "text"
	valueType106D7553            = "type"
	valueUnknown26BF6906         = "unknown"
	threadKindConversation       = "conversation"
	resourceKindEnvironment      = "environment"
	valueActorRefKey             = "actor"
	valueThreadRefKey            = "thread"
	valueTenant                  = "tenant"
	valueTenantTest              = "tenant_test"
	valueThread7                 = "thread_7"
)

const (
	textRunEnvironmentDefault                  = "environment_default"
	textRunStrategyReasonEnvironmentDefault    = textRunEnvironmentDefault
	textRunStrategyReasonEnvironmentSingleMode = "environment_single_mode"
	textRunStrategyReasonRequestedDirect       = "requested_direct"
	textRunStrategyReasonRequestedPlan         = "requested_plan"
	textRunStrategyReasonAutoApprovalRequired  = "auto_approval_required"
	textRunStrategyReasonAutoPlanIntent        = "auto_plan_intent"
	textRunStrategyReasonAutoDirectIntent      = "auto_direct_intent"
	textRunStrategyReasonAutoSimple            = "auto_simple"
)

var (
	ErrRunEnvironmentUnavailable        = errors.New("environment is unavailable")
	ErrEnvironmentModelUnconfigured     = errors.New("environment has no configured model")
	ErrEnvironmentDefaultUnavailable    = errors.New("environment default model is unavailable")
	ErrEnvironmentModelNotAccessible    = errors.New("environment model is not accessible")
	ErrEnvironmentModelNotAuthorized    = errors.New("model is not authorized by environment")
	ErrRunEnvironmentChanged            = errors.New("environment revision changed")
	ErrTextRunIdempotencyConflict       = errors.New("text run idempotency conflict")
	ErrTextRunAlreadyActive             = errors.New("text run already active")
	ErrRunInteractionConflict           = errors.New("run interaction idempotency conflict")
	ErrRunInteractionResponseInvalid    = errors.New("run interaction response invalid")
	ErrRunInteractionSchemaIncompatible = errors.New("run interaction schema incompatible")
	ErrRunResumeConflict                = errors.New("run resume idempotency conflict")
	ErrRunResumeIDConflict              = errors.New("run resume request id reused with a different checkpoint")
	ErrRunSnapshotIncompatible          = errors.New("text run snapshot incompatible")
	ErrRunToolConflict                  = errors.New("run tool call idempotency conflict")
	ErrOutputVersionConflict            = errors.New("output version conflict")
	ErrOutputLineageInvalid             = errors.New("output lineage invalid")
	ErrRunCancelUnavailable             = errors.New("run cancellation is temporarily unavailable")
	ErrRunRetireConflict                = errors.New("text run cannot be retired")
	ErrPlanRevisionLimit                = errors.New("plan revision limit reached")
	ErrRunQueueConflict                 = errors.New("run queue conflict")
	ErrContinuationJobConflict          = errors.New("continuation job conflict")
	ErrContinuationRunTerminal          = errors.New("continuation run is terminal")
	ErrContinuationDeadLetter           = errors.New("continuation job exhausted")
	ErrContinuationWorkerPanic          = errors.New("continuation worker panic")
	ErrContinuationAttemptsExhausted    = errors.New("continuation attempts exhausted")
	ErrRunToolUnavailable               = errors.New("selected tool is unavailable")
	ErrRunToolProviderReceiptRequired   = errors.New("write or destructive tool requires provider execution receipts")
	ErrRunSkillUnavailable              = errors.New("selected skill is unavailable")
	ErrRunToolIncompatible              = errors.New("selected hosted tool is incompatible with the routed model protocol")
	ErrWorkspaceSourceStale             = errors.New("workspace directive source is stale")
	ErrWorkspaceSourceTooLarge          = errors.New("workspace conversation source exceeds directive limits; select a message or text range")
	ErrWorkspaceArtifactMissing         = errors.New("workspace run did not publish its required artifact")
	ErrExecutionModeNotAllowed          = errors.New("execution mode is not allowed by environment")
)

type StartTextRunInput struct {
	Actor                                         model.ActorRef
	Thread                                        model.ThreadRef
	RequestID, Goal, ContentType                  string
	Environment                                   model.ResourceRef
	ClientRunID, PlatformModelName, ExecutionMode string
	FrozenStrategy, FrozenStrategyReason          string
	FrozenRequestedMode                           string
	Options                                       map[string]interface{}
	FileIDs                                       []string
	OutputIDs                                     []string
	EvidenceIDs                                   []string
	ToolKeys                                      *[]string
	SkillRefs                                     *[]model.ResourceRef
	ParentProjection, SourceProjection            *model.ProjectionRef
	BranchReason                                  string
	HTMLVisualPromptEnabled                       bool
	HTMLVisualColorMode                           string
	ThreadModel, ThreadProvider                   string
	ThreadScope                                   string
	Workspace                                     *WorkspaceRequest
	AgentManifest                                 model.ResourceRef
	FrozenWorkspace                               *WorkspaceSnapshot
	Delegation                                    *RunDelegationStart
	DeferInitialContinuation                      bool
	MaxLLMCalls                                   int
	MaxToolCalls                                  int
	StructuredOutputSchema                        json.RawMessage
	ResultAttempts                                int
}

func freezeWorkspaceForAgentRole(workspace *WorkspaceSnapshot, allowedToolKeys []string, delegated bool) {
	if workspace == nil {
		return
	}
	allowed := make(map[string]struct{}, len(allowedToolKeys))
	for _, key := range uniqueRuntimeStrings(allowedToolKeys) {
		allowed[key] = struct{}{}
	}
	tools := make([]WorkspaceToolDefinition, 0, min(len(workspace.Tools), len(allowed)))
	for _, tool := range workspace.Tools {
		if _, ok := allowed[strings.TrimSpace(tool.ToolKey)]; ok {
			tools = append(tools, tool)
		}
	}
	workspace.Tools = tools
	if !delegated {
		return
	}
	workspace.ExpectedArtifact = ""
	workspace.Request.ArtifactContract = ""
	if workspace.Request.Directive != nil {
		directive := *workspace.Request.Directive
		directive.ArtifactContract = ""
		workspace.Request.Directive = &directive
	}
	workspace.Policy.RequiredArtifact = false
	workspace.Policy.TerminalArtifactTypes = nil
	workspace.Policy.ArtifactResourceField = ""
	workspace.Policy.AllowPublishOutput = false
	workspace.Policy.Failure.RequiredArtifactErrorCode = ""
}

type textRunStartReservation struct {
	value *UsageBalanceReservation
}

func validFrozenWorkspace(workspace WorkspaceSnapshot) bool {
	if workspace.SchemaVersion != RuntimeSnapshotVersion || workspace.Revision == 0 {
		return false
	}
	required := []string{
		workspace.Request.Type,
		workspace.Request.ResourceID,
		workspace.SnapshotID,
		workspace.StateHash,
		workspace.ContentJSON,
	}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return validFrozenWorkspaceTools(workspace.Tools)
}

func validFrozenWorkspaceTools(tools []WorkspaceToolDefinition) bool {
	for _, definition := range tools {
		if strings.TrimSpace(definition.ToolKey) == "" || strings.TrimSpace(definition.Name) == "" || len(definition.InputSchema) == 0 {
			return false
		}
	}
	return true
}

func cloneFrozenWorkspace(input *WorkspaceSnapshot) (WorkspaceSnapshot, error) {
	if input == nil {
		return WorkspaceSnapshot{}, ErrInvalidInput
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	var result WorkspaceSnapshot
	if err = json.Unmarshal(raw, &result); err != nil {
		return WorkspaceSnapshot{}, err
	}
	return result, nil
}

func narrowAgentBudget(environmentLimit, manifestLimit int) int {
	if manifestLimit <= 0 || manifestLimit >= environmentLimit {
		return environmentLimit
	}
	return manifestLimit
}

func normalizeToolIdempotencyMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ToolIdempotencyRequestKey:
		return ToolIdempotencyRequestKey
	case ToolIdempotencyProviderReceipt:
		return ToolIdempotencyProviderReceipt
	default:
		return ToolIdempotencyNone
	}
}

func toolRequiresProviderReceipt(sideEffectLevel string) bool {
	level := strings.ToLower(strings.TrimSpace(sideEffectLevel))
	return level == ToolSideEffectWrite || level == ToolSideEffectDestructive
}

type TextRunStartResult struct {
	Run        model.Run
	Step       model.Step
	Projection TurnProjection
}
type TextRunDetail struct {
	Run        model.Run
	Steps      []model.Step
	Config     *TextRunConfigSummary
	Context    *TextRunContextSummary
	Projection TurnProjection
}

type TextRunContextSummary struct {
	SnapshotID             string               `json:"snapshotID"`
	Revision               int                  `json:"revision"`
	SupersedesSnapshotID   string               `json:"supersedesSnapshotID,omitempty"`
	Mode                   string               `json:"mode"`
	ManagementStatus       string               `json:"managementStatus"`
	SemanticVersion        int                  `json:"semanticVersion"`
	ContentHash            string               `json:"contentHash"`
	HardInputTokens        int64                `json:"hardInputTokens"`
	SoftInputTokens        int64                `json:"softInputTokens"`
	RawTokenEstimate       int64                `json:"rawTokenEstimate"`
	AdjustedTokenEstimate  int64                `json:"adjustedTokenEstimate"`
	TokenCountSource       string               `json:"tokenCountSource"`
	LoadedMessageCount     int                  `json:"loadedMessageCount"`
	RetainedMessageCount   int                  `json:"retainedMessageCount"`
	SummarizedMessageCount int                  `json:"summarizedMessageCount"`
	TrimmedMessageCount    int                  `json:"trimmedMessageCount"`
	SummaryArtifactID      string               `json:"summaryArtifactID,omitempty"`
	SummaryStrategy        string               `json:"summaryStrategy,omitempty"`
	CoveredThrough         *model.ProjectionRef `json:"coveredThrough,omitempty"`
	SummaryTokenEstimate   int64                `json:"summaryTokenEstimate"`
	FileCount              int                  `json:"fileCount"`
	RAGCount               int                  `json:"ragCount"`
	SkillCount             int                  `json:"skillCount"`
	MemoryCount            int                  `json:"memoryCount"`
	OutputCount            int                  `json:"outputCount"`
	EvidenceCount          int                  `json:"evidenceCount"`
	RetrievalFallbackCount int                  `json:"retrievalFallbackCount"`
	SkippedCount           int                  `json:"skippedCount"`
	CompiledAt             time.Time            `json:"compiledAt"`
}

type RunEventHistoryPage struct {
	Results       []model.Event
	HasMore       bool
	NextBeforeSeq int64
}

type TextRunConfigSummary struct {
	Strategy               string              `json:"strategy"`
	StrategyReason         string              `json:"strategyReason"`
	RequestedMode          string              `json:"requestedMode"`
	DefaultMode            string              `json:"defaultMode"`
	AllowedModes           []string            `json:"allowedModes"`
	EnvironmentRef         model.ResourceRef   `json:"environmentRef"`
	EnvironmentProfileName string              `json:"environmentProfileName"`
	PlatformModelName      string              `json:"platformModelName"`
	MemoryEnabled          bool                `json:"memoryEnabled"`
	SkillRefs              []model.ResourceRef `json:"skillRefs"`
	ToolKeys               []string            `json:"toolKeys"`
	LocalToolKeys          []string            `json:"localToolKeys"`
	UnavailableSkillRefs   []model.ResourceRef `json:"unavailableSkillRefs"`
	UnavailableToolKeys    []string            `json:"unavailableToolKeys"`
	// ProviderToolNames is the frozen provider-visible function names after
	// workspace overlay, action narrowing, and model-name normalization.
	// Prefer this over ToolKeys / LocalToolKeys for Live Eval and exposure gates.
	ProviderToolNames []string `json:"providerToolNames"`
	// ToolSchemaBytes is the internal json.Marshal size of frozen tool definitions.
	// Not provider wire size — use ProviderToolPayloadBytes for budget gates.
	ToolSchemaBytes int `json:"toolSchemaBytes,omitempty"`
	// ProviderToolPayloadBytes is the provider-wire tool declaration size when a
	// protocol is known on the effective config; otherwise omitted.
	ProviderToolPayloadBytes int `json:"providerToolPayloadBytes,omitempty"`
	// ProviderPayloadObserved is true only when protocol was non-empty and the
	// wire measure succeeded (including a legitimate zero-byte payload).
	// Live Eval hard gates for maxToolSchemaBytes must require this flag.
	ProviderPayloadObserved bool   `json:"providerPayloadObserved"`
	FileCount               int    `json:"fileCount"`
	MaxLLMCalls             int    `json:"maxLLMCalls"`
	MaxToolCalls            int    `json:"maxToolCalls"`
	ToolRetryCount          int    `json:"toolRetryCount"`
	ToolConcurrency         int    `json:"toolConcurrency"`
	PlanApprovalMode        string `json:"planApprovalMode,omitempty"`
	PlanMaxSteps            int    `json:"planMaxSteps,omitempty"`
	PlanMaxRevisions        int    `json:"planMaxRevisions,omitempty"`
	InteractionTTLHours     int    `json:"interactionTTLHours,omitempty"`
	OutputMaxPerRun         int    `json:"outputMaxPerRun,omitempty"`
}

// summarizeTextRunConfig projects the frozen effective config for workbench /
// Live Eval. protocol selects the provider wire measure for ProviderToolPayloadBytes;
// when empty, that field is left zero (do not pretend internal marshal is wire size).
func summarizeTextRunConfig(effective effectiveTextRunConfig, protocol string) *TextRunConfigSummary {
	localToolKeys, baseTools := collectEffectiveToolPolicies(effective)
	// Same final set the model receives: workspace/policy tools + contract-aware runtime controls.
	tools := withRuntimeControlTools(baseTools, effective)
	providerToolNames := providerToolNamesFromDefinitions(tools)
	schemaBytes, _ := measureToolDefinitionsBytes(tools)
	payloadBytes, payloadObserved := measureProviderPayloadBytesIfProtocol(protocol, tools)
	return &TextRunConfigSummary{
		Strategy: effective.Strategy, StrategyReason: effective.StrategyReason, RequestedMode: effective.RequestedMode,
		DefaultMode: effective.DefaultMode, AllowedModes: append([]string{}, effective.AllowedModes...),
		EnvironmentRef: effective.Environment, EnvironmentProfileName: effective.EnvironmentProfileName,
		PlatformModelName: effective.PlatformModelName,
		MemoryEnabled:     effective.MemoryEnabled, SkillRefs: append([]model.ResourceRef{}, effective.SkillRefs...),
		ToolKeys: append([]string{}, effective.ToolKeys...), LocalToolKeys: localToolKeys,
		UnavailableSkillRefs: append([]model.ResourceRef{}, effective.UnavailableSkillRefs...),
		UnavailableToolKeys:  append([]string{}, effective.UnavailableToolKeys...),
		ProviderToolNames:    providerToolNames, ToolSchemaBytes: schemaBytes,
		ProviderToolPayloadBytes: payloadBytes, ProviderPayloadObserved: payloadObserved,
		FileCount: len(effective.FileIDs), MaxLLMCalls: effective.MaxLLMCalls, MaxToolCalls: effective.MaxToolCalls,
		ToolRetryCount: effective.ToolRetryCount, ToolConcurrency: effective.ToolConcurrency,
		PlanApprovalMode: effective.PlanApprovalMode, PlanMaxSteps: effective.PlanMaxSteps,
		PlanMaxRevisions: effective.PlanMaxRevisions, InteractionTTLHours: effective.InteractionTTLHours,
		OutputMaxPerRun: effective.OutputMaxPerRun,
	}
}

func collectEffectiveToolPolicies(effective effectiveTextRunConfig) (localToolKeys []string, baseTools []ToolDefinition) {
	localToolKeys = make([]string, 0, len(effective.ToolPolicies))
	baseTools = make([]ToolDefinition, 0, len(effective.ToolPolicies))
	for _, policy := range effective.ToolPolicies {
		if policy.ExecutionMode == valueLocalDispatch71FF6D47 {
			localToolKeys = append(localToolKeys, policy.ToolKey)
		}
		if name := strings.TrimSpace(policy.ModelName); name != "" {
			baseTools = append(baseTools, ToolDefinition{Name: name, Description: policy.Description, InputSchema: append(json.RawMessage(nil), policy.InputSchema...)})
		}
	}
	if len(baseTools) == 0 && effective.Workspace != nil {
		for _, tool := range effective.Workspace.Tools {
			baseTools = append(baseTools, ToolDefinition{Name: tool.Name, Description: tool.Description, InputSchema: append(json.RawMessage(nil), tool.InputSchema...)})
		}
	}
	return localToolKeys, baseTools
}

func providerToolNamesFromDefinitions(tools []ToolDefinition) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if name := strings.TrimSpace(tool.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// measureProviderPayloadBytesIfProtocol returns wire size and whether the measure
// was observed. Empty protocol or measure errors yield observed=false so callers
// never treat internal ToolSchemaBytes as a substitute for provider wire size.
func measureProviderPayloadBytesIfProtocol(protocol string, tools []ToolDefinition) (bytes int, observed bool) {
	if strings.TrimSpace(protocol) == "" {
		return 0, false
	}
	measured, err := measureProviderToolPayloadBytes(protocol, tools)
	if err != nil {
		return 0, false
	}
	return measured, true
}

// resolveProviderProtocolForSummary prefers a frozen run protocol, else resolves
// the platform model route so Live Eval can observe wire-size metrics.
func (s *Engine) resolveProviderProtocolForSummary(ctx context.Context, run model.Run, platformModelName string) string {
	if protocol := strings.TrimSpace(run.ProviderProtocol); protocol != "" {
		return protocol
	}
	modelName := strings.TrimSpace(platformModelName)
	if modelName == "" {
		modelName = strings.TrimSpace(run.PlatformModelName)
	}
	if s == nil || s.llmGateway == nil || modelName == "" {
		return ""
	}
	route, err := s.llmGateway.PrepareTextRoute(ctx, LLMRouteInput{
		PlatformModelName: modelName,
		TaskType:          LLMTaskTypeText,
		Scope:             LLMRouteScopeUser,
		Actor:             run.Actor,
		Thread:            run.Thread,
		RequestID:         run.RequestID,
	})
	if err != nil || route == nil {
		return ""
	}
	return strings.TrimSpace(route.Protocol)
}

type TextRunPolicy struct {
	PlanMaxSteps        int `json:"planMaxSteps"`
	PlanMaxRevisions    int `json:"planMaxRevisions"`
	InteractionTTLHours int `json:"interactionTTLHours"`
	OutputMaxPerRun     int `json:"outputMaxPerRun"`
}

type effectiveRunToolPolicy struct {
	ToolKey            string              `json:"toolKey"`
	ProviderKind       string              `json:"providerKind"`
	ProviderKey        string              `json:"providerKey"`
	ModelName          string              `json:"modelName"`
	OriginalName       string              `json:"originalName"`
	Description        string              `json:"description"`
	DefinitionVersion  string              `json:"definitionVersion"`
	InputSchema        json.RawMessage     `json:"inputSchema,omitempty"`
	OutputSchema       json.RawMessage     `json:"outputSchema,omitempty"`
	ExecutionMode      string              `json:"executionMode"`
	ApprovalCapability string              `json:"approvalCapability"`
	ApprovalMode       string              `json:"approvalMode"`
	RiskLevel          string              `json:"riskLevel"`
	SideEffectLevel    string              `json:"sideEffectLevel"`
	IdempotencyMode    string              `json:"idempotencyMode"`
	HostedVariants     []HostedToolVariant `json:"hostedVariants,omitempty"`
	RetryCount         int                 `json:"retryCount"`
	Concurrency        int                 `json:"concurrency"`
	Fingerprint        string              `json:"fingerprint"`
}

type effectiveTextRunConfig struct {
	SemanticVersion             int                       `json:"semanticVersion"`
	Strategy                    string                    `json:"strategy"`
	StrategyReason              string                    `json:"strategyReason"`
	RequestedMode               string                    `json:"requestedMode"`
	DefaultMode                 string                    `json:"defaultMode"`
	AllowedModes                []string                  `json:"allowedModes"`
	Environment                 model.ResourceRef         `json:"environment"`
	EnvironmentProfileName      string                    `json:"environmentProfileName"`
	Instructions                string                    `json:"instructions"`
	PlatformModelName           string                    `json:"platformModelName"`
	MemoryEnabled               bool                      `json:"memoryEnabled"`
	SkillRefs                   []model.ResourceRef       `json:"skillRefs"`
	ToolKeys                    []string                  `json:"toolKeys"`
	UnavailableSkillRefs        []model.ResourceRef       `json:"unavailableSkillRefs"`
	UnavailableToolKeys         []string                  `json:"unavailableToolKeys"`
	AllowedModelSnapshot        []EnvironmentModelPolicy  `json:"allowedModelSnapshot"`
	MemoryPolicy                string                    `json:"memoryPolicy"`
	FileIDs                     []string                  `json:"fileIDs"`
	Options                     map[string]interface{}    `json:"options,omitempty"`
	HTMLVisualPromptEnabled     bool                      `json:"htmlVisualPrompt,omitempty"`
	HTMLVisualColorMode         string                    `json:"htmlVisualColorMode,omitempty"`
	MaxLLMCalls                 int                       `json:"maxLLMCalls"`
	MaxToolCalls                int                       `json:"maxToolCalls"`
	ToolRetryCount              int                       `json:"toolRetryCount"`
	ToolConcurrency             int                       `json:"toolConcurrency"`
	PlanApprovalMode            string                    `json:"planApprovalMode,omitempty"`
	ToolPolicies                []effectiveRunToolPolicy  `json:"toolPolicies,omitempty"`
	PlanMaxSteps                int                       `json:"planMaxSteps,omitempty"`
	PlanMaxRevisions            int                       `json:"planMaxRevisions,omitempty"`
	InteractionTTLHours         int                       `json:"interactionTTLHours,omitempty"`
	OutputMaxPerRun             int                       `json:"outputMaxPerRun,omitempty"`
	OutputIDs                   []string                  `json:"outputIDs,omitempty"`
	OutputRefs                  []effectiveRunOutputRef   `json:"outputRefs,omitempty"`
	EvidenceIDs                 []string                  `json:"evidenceIDs,omitempty"`
	EvidenceRefs                []effectiveRunEvidenceRef `json:"evidenceRefs,omitempty"`
	Workspace                   *WorkspaceSnapshot        `json:"workspace,omitempty"`
	AgentManifest               *effectiveAgentManifest   `json:"agentManifest,omitempty"`
	InitialContinuationDeferred bool                      `json:"initialContinuationDeferred,omitempty"`
	StructuredOutputSchema      json.RawMessage           `json:"structuredOutputSchema,omitempty"`
	ResultAttempts              int                       `json:"resultAttempts,omitempty"`
	Context                     ContextConfig             `json:"context"`
}

type effectiveAgentManifest struct {
	Ref           model.ResourceRef   `json:"ref"`
	Name          string              `json:"name"`
	Description   string              `json:"description,omitempty"`
	Instructions  string              `json:"instructions,omitempty"`
	ModelName     string              `json:"modelName,omitempty"`
	ExecutionMode string              `json:"executionMode,omitempty"`
	ToolKeys      []string            `json:"toolKeys"`
	SkillRefs     []model.ResourceRef `json:"skillRefs"`
	MaxChildRuns  int                 `json:"maxChildRuns"`
	MaxDepth      int                 `json:"maxDepth"`
	MaxLLMCalls   int                 `json:"maxLLMCalls,omitempty"`
	MaxToolCalls  int                 `json:"maxToolCalls,omitempty"`
}

type effectiveRunOutputRef struct {
	OutputID    string `json:"outputID"`
	Version     int    `json:"version"`
	ContentHash string `json:"contentHash"`
}

type effectiveRunEvidenceRef struct {
	EvidenceID        string              `json:"evidenceID"`
	SourceKind        string              `json:"sourceKind"`
	Kind              string              `json:"kind,omitempty"`
	SourceID          string              `json:"sourceID,omitempty"`
	Projection        model.ProjectionRef `json:"projection,omitempty"`
	Title             string              `json:"title"`
	ContentHash       string              `json:"contentHash"`
	SourceContentHash string              `json:"sourceContentHash"`
	Excerpt           string              `json:"excerpt"`
}

func fingerprintRunToolSnapshot(item effectiveRunToolPolicy) string {
	payload := struct {
		ToolKey, ProviderKind, ProviderKey, ModelName, OriginalName, Description, DefinitionVersion                             string
		InputSchema, OutputSchema, ExecutionMode, ApprovalCapability, ApprovalMode, RiskLevel, SideEffectLevel, IdempotencyMode string
		HostedVariants                                                                                                          []HostedToolVariant
		RetryCount, Concurrency                                                                                                 int
	}{
		ToolKey: item.ToolKey, ProviderKind: item.ProviderKind, ProviderKey: item.ProviderKey,
		ModelName: item.ModelName, OriginalName: item.OriginalName, Description: item.Description,
		DefinitionVersion: item.DefinitionVersion, InputSchema: canonicalRunJSON(item.InputSchema), OutputSchema: canonicalRunJSON(item.OutputSchema),
		ExecutionMode: item.ExecutionMode, ApprovalCapability: item.ApprovalCapability,
		ApprovalMode: item.ApprovalMode, RiskLevel: item.RiskLevel, SideEffectLevel: item.SideEffectLevel, IdempotencyMode: item.IdempotencyMode,
		HostedVariants: item.HostedVariants, RetryCount: item.RetryCount, Concurrency: item.Concurrency,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func textRunWorkspaceScope(workspace *WorkspaceSnapshot) (string, string) {
	if workspace == nil {
		return "", ""
	}
	return strings.ToLower(strings.TrimSpace(workspace.Request.Type)), strings.ToLower(strings.TrimSpace(workspace.Request.ArtifactContract))
}

func cloneHostedToolVariants(items []HostedToolVariant) []HostedToolVariant {
	if len(items) == 0 {
		return nil
	}
	result := make([]HostedToolVariant, 0, len(items))
	for _, item := range items {
		payload := map[string]interface{}{}
		if encoded, err := json.Marshal(item.Payload); err == nil {
			_ = json.Unmarshal(encoded, &payload)
		}
		result = append(result, HostedToolVariant{Protocol: strings.TrimSpace(item.Protocol), Payload: payload})
	}
	return result
}

func (s *Engine) TextRunPolicy() TextRunPolicy {
	cfg := s.cfg.Snapshot()
	return TextRunPolicy{PlanMaxSteps: boundedTextRunConfig(cfg.Planner.MaxSteps, 12, 50), PlanMaxRevisions: boundedTextRunConfig(cfg.Planner.MaxRevisions, 5, 20), InteractionTTLHours: boundedTextRunConfig(cfg.Execution.InteractionTTLHours, 168, 24*365), OutputMaxPerRun: boundedTextRunConfig(cfg.Outputs.MaxPerRun, 50, 500)}
}

type textRunPreparedConfiguration struct {
	Profile         *EnvironmentProfile
	ModelName       string
	Effective       effectiveTextRunConfig
	Snapshot        []byte
	InputEvaluation EvaluationReport
}

func (s *Engine) prepareTextRunConfiguration(ctx context.Context, input StartTextRunInput, goal string) (textRunPreparedConfiguration, error) {
	input, agentManifest, err := s.resolveTextRunAgentManifest(ctx, input)
	if err != nil {
		return textRunPreparedConfiguration{}, err
	}
	profile, err := s.resolveTextRunProfileAtRevision(ctx, input.Actor, input.Environment)
	if err != nil {
		return textRunPreparedConfiguration{}, err
	}
	requestedModel := firstNonEmptyString(strings.TrimSpace(input.PlatformModelName), strings.TrimSpace(input.ThreadModel))
	modelName, err := selectEnvironmentModel(profile, requestedModel)
	if err != nil {
		return textRunPreparedConfiguration{}, err
	}
	resources, err := s.resolveTextRunResources(ctx, input, modelName)
	if err != nil {
		return textRunPreparedConfiguration{}, err
	}
	if !textRunEnvironmentWorkspaceCompatible(profile, resources.Workspace) {
		return textRunPreparedConfiguration{}, ErrEnvironmentBindingNotAllowed
	}
	if resources.Workspace != nil && (agentManifest != nil || input.ToolKeys != nil) {
		allowedToolKeys := workspaceSnapshotToolKeys(resources.Workspace)
		if agentManifest != nil {
			allowedToolKeys = intersectRuntimeStrings(allowedToolKeys, agentManifest.ToolKeys)
		}
		allowedToolKeys = narrowRuntimeToolSelection(allowedToolKeys, input.ToolKeys)
		freezeWorkspaceForAgentRole(resources.Workspace, allowedToolKeys, input.Delegation != nil)
	}
	workspaceType, workspaceMode := textRunWorkspaceScope(resources.Workspace)
	effectiveToolSelection := input.ToolKeys
	if resources.Workspace != nil {
		keys := narrowRuntimeToolSelection(workspaceSnapshotToolKeys(resources.Workspace), input.ToolKeys)
		if agentManifest != nil {
			keys = intersectRuntimeStrings(keys, agentManifest.ToolKeys)
		}
		effectiveToolSelection = &keys
	}
	toolSelection, err := compileEnvironmentToolSelection(effectiveToolSelection, profile)
	if err != nil {
		return textRunPreparedConfiguration{}, err
	}
	selectedSkillRefs, unavailableSkillRefs, err := resolveEnvironmentSkillSelectionWithDiagnostics(input.SkillRefs, profile)
	if err != nil {
		return textRunPreparedConfiguration{}, err
	}
	cfg := s.cfg.Snapshot()
	toolConcurrency := positiveTextRunValue(cfg.Tools.MaxConcurrentCalls)
	toolRetryCount := nonNegativeTextRunValue(cfg.Tools.RetryCount)
	strictToolResolution, err := s.resolveTextRunToolPolicies(ctx, input.Actor, toolSelection.StrictKeys, valueRequest3E6DBD23, workspaceType, workspaceMode, modelName, toolRetryCount, toolConcurrency)
	if err != nil {
		return textRunPreparedConfiguration{}, err
	}
	if !resolvedToolsRespectEnvironmentBoundary(strictToolResolution.Policies, profile) {
		return textRunPreparedConfiguration{}, ErrRunToolUnavailable
	}
	defaultToolResolution, err := s.resolveTextRunToolPolicies(ctx, input.Actor, toolSelection.DefaultKeys, textRunEnvironmentDefault, workspaceType, workspaceMode, modelName, toolRetryCount, toolConcurrency)
	if err != nil {
		return textRunPreparedConfiguration{}, err
	}
	toolResolution := mergeTextRunToolResolutions(strictToolResolution, defaultToolResolution)
	toolResolution.Unavailable = append(toolResolution.Unavailable, toolSelection.UnavailableDefaultKeys...)
	if resources.Workspace != nil {
		// Overlay provider-compiled definitions so the model and provider
		// execute the same immutable contract.
		toolResolution.Policies, err = applyWorkspaceToolDefinitions(toolResolution.Policies, resources.Workspace.Tools)
		if err != nil {
			return textRunPreparedConfiguration{}, err
		}
	}
	strategy, strategyReason, requestedMode, err := resolveTextRunStrategy(input.ExecutionMode, profile.DefaultMode, profile.AllowedModes, goal, toolResolution.Policies)
	if err != nil {
		return textRunPreparedConfiguration{}, err
	}
	if input.FrozenStrategy != "" {
		if input.FrozenStrategy != strategy || input.FrozenStrategyReason != strategyReason || input.FrozenRequestedMode != requestedMode {
			return textRunPreparedConfiguration{}, ErrRunSnapshotIncompatible
		}
		strategy, strategyReason, requestedMode = input.FrozenStrategy, input.FrozenStrategyReason, input.FrozenRequestedMode
	}
	instructions, agentSnapshot := textRunAgentManifest(agentManifest, profile.Instructions)
	maxLLMCalls, maxToolCalls := s.resolveMaxLLMCallsPerRun(), s.resolveMaxToolCallsPerRun()
	if agentSnapshot != nil {
		maxLLMCalls = narrowAgentBudget(maxLLMCalls, agentSnapshot.MaxLLMCalls)
		maxToolCalls = narrowAgentBudget(maxToolCalls, agentSnapshot.MaxToolCalls)
	}
	maxLLMCalls = narrowAgentBudget(maxLLMCalls, input.MaxLLMCalls)
	maxToolCalls = narrowAgentBudget(maxToolCalls, input.MaxToolCalls)
	resultAttempts := input.ResultAttempts
	if len(input.StructuredOutputSchema) > 0 {
		if _, schemaErr := validateWorkflowSchema(input.StructuredOutputSchema); schemaErr != nil || resultAttempts < 0 || resultAttempts > 2 {
			return textRunPreparedConfiguration{}, ErrInvalidInput
		}
		if resultAttempts == 0 {
			resultAttempts = 1
		}
	} else if resultAttempts != 0 {
		return textRunPreparedConfiguration{}, ErrInvalidInput
	}
	effective := effectiveTextRunConfig{
		SemanticVersion: RuntimeSnapshotVersion, Strategy: strategy, StrategyReason: strategyReason, RequestedMode: requestedMode, DefaultMode: profile.DefaultMode, AllowedModes: append([]string(nil), profile.AllowedModes...), Environment: input.Environment, EnvironmentProfileName: profile.Name,
		Instructions: instructions, PlatformModelName: modelName, AllowedModelSnapshot: append([]EnvironmentModelPolicy(nil), profile.Models...), MemoryPolicy: profile.MemoryPolicy, MemoryEnabled: profile.MemoryPolicy != "disabled", SkillRefs: selectedSkillRefs, UnavailableSkillRefs: unavailableSkillRefs,
		ToolKeys: toolResolution.ResolvedKeys, UnavailableToolKeys: uniqueRuntimeStrings(toolResolution.Unavailable),
		FileIDs: append([]string(nil), input.FileIDs...), Options: input.Options, HTMLVisualPromptEnabled: input.HTMLVisualPromptEnabled, HTMLVisualColorMode: input.HTMLVisualColorMode,
		MaxLLMCalls: maxLLMCalls, MaxToolCalls: maxToolCalls, ToolRetryCount: toolRetryCount, ToolConcurrency: toolConcurrency,
		PlanApprovalMode: normalizedPlanApprovalMode(profile.PlanApprovalMode), ToolPolicies: toolResolution.Policies,
		PlanMaxSteps: boundedTextRunConfig(cfg.Planner.MaxSteps, 12, 50), PlanMaxRevisions: boundedTextRunConfig(cfg.Planner.MaxRevisions, 5, 20),
		InteractionTTLHours: boundedTextRunConfig(cfg.Execution.InteractionTTLHours, 168, 24*365), OutputMaxPerRun: boundedTextRunConfig(cfg.Outputs.MaxPerRun, 50, 500),
		OutputIDs: uniqueRuntimeStrings(input.OutputIDs), OutputRefs: resources.OutputRefs, EvidenceIDs: resources.EvidenceIDs, EvidenceRefs: resources.EvidenceRefs, Workspace: resources.Workspace, AgentManifest: agentSnapshot,
		InitialContinuationDeferred: input.DeferInitialContinuation,
		StructuredOutputSchema:      append(json.RawMessage(nil), input.StructuredOutputSchema...), ResultAttempts: resultAttempts,
		Context: normalizeContextConfig(cfg.Context),
	}
	snapshot, err := json.Marshal(effective)
	if err != nil {
		return textRunPreparedConfiguration{}, ErrInvalidInput
	}
	return textRunPreparedConfiguration{Profile: profile, ModelName: modelName, Effective: effective, Snapshot: snapshot}, nil
}

type textRunInitialContext struct {
	UserMessage     *ContextMessage
	Attachments     []ResolvedAttachment
	ContextSnapshot *model.ContextSnapshot
	Artifacts       []model.ContextArtifact
	Projection      TurnProjection
}

type environmentToolSelection struct {
	StrictKeys             []string
	DefaultKeys            []string
	UnavailableDefaultKeys []string
}

func compileEnvironmentToolSelection(selected *[]string, environment *EnvironmentProfile) (environmentToolSelection, error) {
	explicit := selected != nil
	chosen := map[string]bool{}
	if explicit {
		for _, key := range uniqueRuntimeStrings(*selected) {
			chosen[key] = true
		}
	}
	var result environmentToolSelection
	for _, policy := range environment.Tools {
		selectedByRequest := chosen[policy.ToolKey]
		include := policy.ActivationMode == valueRequired466769C7 || (!explicit && policy.ActivationMode == valueDefaultD98758A6) || selectedByRequest
		if !include {
			continue
		}
		if !policy.Available {
			if policy.ActivationMode == valueRequired466769C7 || selectedByRequest {
				return environmentToolSelection{}, ErrRunToolUnavailable
			}
			result.UnavailableDefaultKeys = append(result.UnavailableDefaultKeys, policy.ToolKey)
			continue
		}
		if !explicit && policy.ActivationMode == valueDefaultD98758A6 {
			result.DefaultKeys = append(result.DefaultKeys, policy.ToolKey)
		} else {
			result.StrictKeys = append(result.StrictKeys, policy.ToolKey)
		}
		delete(chosen, policy.ToolKey)
	}
	for key := range chosen {
		// Provider-hosted tools are selected explicitly through Composer
		// Parameters and validated against the routed model after resolution.
		// Any unbound local_dispatch key is rejected by the post-resolution
		// Environment boundary check.
		result.StrictKeys = append(result.StrictKeys, key)
	}
	result.StrictKeys = uniqueRuntimeStrings(result.StrictKeys)
	result.DefaultKeys = uniqueRuntimeStrings(result.DefaultKeys)
	result.UnavailableDefaultKeys = uniqueRuntimeStrings(result.UnavailableDefaultKeys)
	return result, nil
}

type textRunToolResolution struct {
	Policies     []effectiveRunToolPolicy
	Unavailable  []string
	ResolvedKeys []string
}

func (s *Engine) compileTextRunWorkspace(ctx context.Context, input StartTextRunInput, modelName string) (WorkspaceSnapshot, bool, error) {
	scope := strings.TrimSpace(input.ThreadScope)
	if input.FrozenWorkspace != nil {
		if input.Workspace != nil {
			return WorkspaceSnapshot{}, false, ErrInvalidInput
		}
		workspace, err := cloneFrozenWorkspace(input.FrozenWorkspace)
		if err != nil || !validFrozenWorkspace(workspace) {
			return WorkspaceSnapshot{}, false, ErrRunSnapshotIncompatible
		}
		if scope == "" || scope != strings.TrimSpace(workspace.Request.Type) {
			return WorkspaceSnapshot{}, false, ErrEnvironmentBindingNotAllowed
		}
		return workspace, true, nil
	}
	if input.Workspace == nil {
		return WorkspaceSnapshot{}, false, nil
	}
	if scope == "" || scope != strings.TrimSpace(input.Workspace.Type) {
		return WorkspaceSnapshot{}, false, ErrEnvironmentBindingNotAllowed
	}
	if s.workspaces == nil {
		return WorkspaceSnapshot{}, false, ErrInvalidInput
	}
	provider, ok := s.workspaces.ResolveWorkspace(input.Workspace.Type)
	if !ok {
		return WorkspaceSnapshot{}, false, ErrInvalidInput
	}
	workspace, err := provider.CompileWorkspace(ctx, input.Actor, input.Thread, input.Workspace, modelcap.Default.Resolve(modelName, "").EffectiveContextBudget())
	if err != nil {
		return WorkspaceSnapshot{}, false, classifyWorkspaceProviderError(provider, err)
	}
	if workspace == nil || workspace.Revision == 0 || strings.TrimSpace(workspace.ContentJSON) == "" {
		return WorkspaceSnapshot{}, false, ErrInvalidInput
	}
	for _, definition := range workspace.Tools {
		if strings.TrimSpace(definition.Name) == "" || len(definition.InputSchema) == 0 {
			return WorkspaceSnapshot{}, false, ErrInvalidInput
		}
	}
	return *workspace, true, nil
}

type textRunResources struct {
	Workspace    *WorkspaceSnapshot
	OutputRefs   []effectiveRunOutputRef
	EvidenceIDs  []string
	EvidenceRefs []effectiveRunEvidenceRef
}

type textRunFingerprintInput struct {
	Actor                       model.ActorRef
	Thread                      model.ThreadRef
	Goal                        string
	Environment                 model.ResourceRef
	Model                       string
	ExecutionMode               string
	Options                     map[string]interface{}
	FileIDs                     []string
	OutputIDs                   []string
	EvidenceIDs                 []string
	ToolKeys                    *[]string
	SkillRefs                   *[]model.ResourceRef
	ParentProjection            *model.ProjectionRef
	SourceProjection            *model.ProjectionRef
	BranchReason                string
	HTMLVisualPrompt            bool
	HTMLVisualColorMode         string
	Workspace                   *WorkspaceRequest
	AgentManifest               model.ResourceRef
	FrozenWorkspaceID           string
	FrozenWorkspaceHash         string
	Delegation                  *runDelegationFingerprint
	InitialContinuationDeferred bool
}

type runDelegationFingerprint struct {
	AgentManifest model.ResourceRef
	RootRunID     string
	ParentRunID   string
	HandoffID     string
	Depth         int
}

type interactionExpiryDependencies struct {
	list           func(context.Context, time.Time, int) ([]model.ExpiredInteraction, error)
	expire         func(context.Context, string) ([]model.Event, bool, error)
	expireWorkflow func(context.Context, model.ExpiredInteraction) ([]model.Event, bool, bool, error)
	publish        func(string, []model.Event)
	finish         func(string)
}

type textRunReconciliationDependencies struct {
	list       func(context.Context, time.Time) ([]model.Run, error)
	leaseState func(context.Context, string) (GenerationLeaseState, error)
	warn       func(string, GenerationLeaseState, error)
	suspend    func(model.Run, []model.Event) (bool, error)
}
