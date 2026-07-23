package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
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
	ErrRunToolUnavailable               = errors.New("selected tool is unavailable")
	ErrRunSkillUnavailable              = errors.New("selected skill is unavailable")
	ErrRunToolIncompatible              = errors.New("selected hosted tool is incompatible with the routed model protocol")
	ErrWorkspaceSourceStale             = errors.New("workspace directive source is stale")
	ErrWorkspaceSourceTooLarge          = errors.New("workspace conversation source exceeds directive limits; select a message or text range")
	ErrWorkspaceSourceCompacted         = errors.New("workspace conversation source contains a compaction gap; select a message or text range")
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
	SnapshotID             string    `json:"snapshotID"`
	SemanticVersion        int       `json:"semanticVersion"`
	ContentHash            string    `json:"contentHash"`
	FileCount              int       `json:"fileCount"`
	RAGCount               int       `json:"ragCount"`
	SkillCount             int       `json:"skillCount"`
	MemoryCount            int       `json:"memoryCount"`
	OutputCount            int       `json:"outputCount"`
	EvidenceCount          int       `json:"evidenceCount"`
	RetrievalFallbackCount int       `json:"retrievalFallbackCount"`
	SkippedCount           int       `json:"skippedCount"`
	CompiledAt             time.Time `json:"compiledAt"`
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
	ExecutionMode      string              `json:"executionMode"`
	ApprovalCapability string              `json:"approvalCapability"`
	ApprovalMode       string              `json:"approvalMode"`
	RiskLevel          string              `json:"riskLevel"`
	SideEffectLevel    string              `json:"sideEffectLevel"`
	HostedVariants     []HostedToolVariant `json:"hostedVariants,omitempty"`
	RetryCount         int                 `json:"retryCount"`
	Concurrency        int                 `json:"concurrency"`
	Fingerprint        string              `json:"fingerprint"`
}

type effectiveTextRunConfig struct {
	SemanticVersion         int                       `json:"semanticVersion"`
	Strategy                string                    `json:"strategy"`
	StrategyReason          string                    `json:"strategyReason"`
	RequestedMode           string                    `json:"requestedMode"`
	DefaultMode             string                    `json:"defaultMode"`
	AllowedModes            []string                  `json:"allowedModes"`
	Environment             model.ResourceRef         `json:"environment"`
	EnvironmentProfileName  string                    `json:"environmentProfileName"`
	Instructions            string                    `json:"instructions"`
	PlatformModelName       string                    `json:"platformModelName"`
	MemoryEnabled           bool                      `json:"memoryEnabled"`
	SkillRefs               []model.ResourceRef       `json:"skillRefs"`
	ToolKeys                []string                  `json:"toolKeys"`
	UnavailableSkillRefs    []model.ResourceRef       `json:"unavailableSkillRefs"`
	UnavailableToolKeys     []string                  `json:"unavailableToolKeys"`
	AllowedModelSnapshot    []EnvironmentModelPolicy  `json:"allowedModelSnapshot"`
	MemoryPolicy            string                    `json:"memoryPolicy"`
	FileIDs                 []string                  `json:"fileIDs"`
	Options                 map[string]interface{}    `json:"options,omitempty"`
	HTMLVisualPromptEnabled bool                      `json:"htmlVisualPrompt,omitempty"`
	HTMLVisualColorMode     string                    `json:"htmlVisualColorMode,omitempty"`
	MaxLLMCalls             int                       `json:"maxLLMCalls"`
	MaxToolCalls            int                       `json:"maxToolCalls"`
	ToolRetryCount          int                       `json:"toolRetryCount"`
	ToolConcurrency         int                       `json:"toolConcurrency"`
	PlanApprovalMode        string                    `json:"planApprovalMode,omitempty"`
	ToolPolicies            []effectiveRunToolPolicy  `json:"toolPolicies,omitempty"`
	PlanMaxSteps            int                       `json:"planMaxSteps,omitempty"`
	PlanMaxRevisions        int                       `json:"planMaxRevisions,omitempty"`
	InteractionTTLHours     int                       `json:"interactionTTLHours,omitempty"`
	OutputMaxPerRun         int                       `json:"outputMaxPerRun,omitempty"`
	OutputIDs               []string                  `json:"outputIDs,omitempty"`
	OutputRefs              []effectiveRunOutputRef   `json:"outputRefs,omitempty"`
	EvidenceIDs             []string                  `json:"evidenceIDs,omitempty"`
	EvidenceRefs            []effectiveRunEvidenceRef `json:"evidenceRefs,omitempty"`
	Workspace               *WorkspaceSnapshot        `json:"workspace,omitempty"`
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
		ToolKey, ProviderKind, ProviderKey, ModelName, OriginalName, Description, DefinitionVersion string
		InputSchema, ExecutionMode, ApprovalCapability, ApprovalMode, RiskLevel, SideEffectLevel    string
		HostedVariants                                                                              []HostedToolVariant
		RetryCount, Concurrency                                                                     int
	}{
		ToolKey: item.ToolKey, ProviderKind: item.ProviderKind, ProviderKey: item.ProviderKey,
		ModelName: item.ModelName, OriginalName: item.OriginalName, Description: item.Description,
		DefinitionVersion: item.DefinitionVersion, InputSchema: canonicalRunJSON(item.InputSchema),
		ExecutionMode: item.ExecutionMode, ApprovalCapability: item.ApprovalCapability,
		ApprovalMode: item.ApprovalMode, RiskLevel: item.RiskLevel, SideEffectLevel: item.SideEffectLevel,
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

func (s *Engine) StartTextRun(ctx context.Context, input StartTextRunInput) (*TextRunStartResult, error) {
	goal := strings.TrimSpace(input.Goal)
	if !validTextRunStartRequest(input, goal) {
		return nil, ErrInvalidInput
	}
	runID := EnsureRunID(input.ClientRunID)
	fingerprint := textRunRequestFingerprint(input, goal)
	existingResult, exists, err := s.existingTextRunStart(ctx, input.Actor, input.Thread, runID, fingerprint)
	if err != nil {
		return nil, err
	}
	if exists {
		return &existingResult, nil
	}
	prepared, err := s.prepareTextRunConfiguration(ctx, input, goal)
	if err != nil {
		return nil, err
	}
	profile, modelName, strategy, effective, snapshot := prepared.Profile, prepared.ModelName, prepared.Effective.Strategy, prepared.Effective, prepared.Snapshot
	if err = s.EnsureRunBillingAccess(ctx, RunBillingInput{Actor: input.Actor, Thread: input.Thread, PlatformModelName: modelName, ThreadModel: input.ThreadModel, ClientRunID: runID}); err != nil {
		return nil, err
	}
	startSegmentKey := runID + ":start"
	reservation, _, err := s.ReserveRunUsageBalance(ctx, RunBillingInput{Actor: input.Actor, Thread: input.Thread, PlatformModelName: modelName, ThreadModel: input.ThreadModel, ClientRunID: startSegmentKey})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	stepID := "step_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	run := model.Run{RunID: runID, RequestID: strings.TrimSpace(input.RequestID), Actor: input.Actor, Thread: input.Thread, Environment: input.Environment, Goal: goal, RunConfigSnapshotJSON: string(snapshot), RequestFingerprint: fingerprint, CurrentStepID: stepID, StartedBy: valueUserDD885A59, RequestedModelName: modelName, PlatformModelName: modelName, Provider: input.ThreadProvider, Status: model.RunStatusQueued, StartedAt: now}
	step := model.Step{StepID: stepID, RunID: runID, StepIndex: 0, Kind: valueOrchestration1BD4660D, Title: truncateRunTitle(goal), Description: goal, Status: model.RunStatusQueued, StartedAt: now}
	continuationType, targetStatus := textRunInitialContinuation(strategy)
	checkpoint := newRunContinuationCheckpoint(run, stepID, "initial_context", runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: startSegmentKey, Type: continuationType, TargetStatus: targetStatus, StepID: stepID, NextRevision: 1})
	var initial textRunInitialContext
	var savedEvents []model.Event
	if s.unitOfWork == nil || s.turnProjections == nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行宿主投影不可用退回预扣")
		return nil, ErrHostProjectionUnavailable
	}
	err = s.unitOfWork.Within(ctx, func(txCtx context.Context) error {
		var prepareErr error
		initial, _, prepareErr = s.prepareTextRunInitialContext(txCtx, input, effective, run)
		if prepareErr != nil {
			return prepareErr
		}
		run.InputProjection, run.OutputProjection = initial.Projection.Input, initial.Projection.Output
		checkpoint = newRunContinuationCheckpoint(run, stepID, "initial_context", runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: startSegmentKey, Type: continuationType, TargetStatus: targetStatus, StepID: stepID, NextRevision: 1})
		initialEvents := textRunInitialEvents(run, step, profile.Ref, strategy, effective.StrategyReason, initial.ContextSnapshot)
		initialEvents = append(initialEvents, newRunEvent(run, "checkpoint.created", stepID, "Initial context checkpoint", map[string]interface{}{valueCheckpointID7923DD64: checkpoint.CheckpointID, valueKindDAA7F13C: checkpoint.Kind}, nil))
		savedEvents, prepareErr = s.repo.CreateRunStartBundle(txCtx, &run, &step, initial.ContextSnapshot, initial.Artifacts, checkpoint, initialEvents)
		return prepareErr
	})
	if err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行创建失败退回预扣")
		if errors.Is(err, ErrDuplicate) {
			return s.recoverDuplicateTextRunStart(ctx, input.Actor, runID, fingerprint)
		}
		return nil, err
	}
	s.publishRunEvents(run.RunID, savedEvents)
	segmentCtx := context.WithValue(context.Background(), runSegmentKeyContextKey{}, startSegmentKey)
	s.launchRunContinuation(func() {
		s.executeRunContinuation(segmentCtx, run, step, effective, reservation, *checkpoint, "text_run_start")
	})
	return &TextRunStartResult{Run: run, Step: step, Projection: initial.Projection}, nil
}

func validTextRunStartInput(goal string) bool {
	return goal != "" && len([]rune(goal)) <= 20000
}

func validTextRunStartRequest(input StartTextRunInput, goal string) bool {
	return validActorRef(input.Actor) && strings.TrimSpace(input.Thread.ID) != "" && strings.TrimSpace(input.Environment.ID) != "" && validTextRunStartInput(goal)
}

type textRunPreparedConfiguration struct {
	Profile   *EnvironmentProfile
	ModelName string
	Effective effectiveTextRunConfig
	Snapshot  []byte
}

func (s *Engine) prepareTextRunConfiguration(ctx context.Context, input StartTextRunInput, goal string) (textRunPreparedConfiguration, error) {
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
	workspaceType, workspaceMode := textRunWorkspaceScope(resources.Workspace)
	effectiveToolSelection := input.ToolKeys
	if resources.Workspace != nil {
		keys := workspaceSnapshotToolKeys(resources.Workspace)
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
	effective := effectiveTextRunConfig{
		SemanticVersion: RuntimeSnapshotVersion, Strategy: strategy, StrategyReason: strategyReason, RequestedMode: requestedMode, DefaultMode: profile.DefaultMode, AllowedModes: append([]string(nil), profile.AllowedModes...), Environment: input.Environment, EnvironmentProfileName: profile.Name,
		Instructions: profile.Instructions, PlatformModelName: modelName, AllowedModelSnapshot: append([]EnvironmentModelPolicy(nil), profile.Models...), MemoryPolicy: profile.MemoryPolicy, MemoryEnabled: profile.MemoryPolicy != "disabled", SkillRefs: selectedSkillRefs, UnavailableSkillRefs: unavailableSkillRefs,
		ToolKeys: toolResolution.ResolvedKeys, UnavailableToolKeys: uniqueRuntimeStrings(toolResolution.Unavailable),
		FileIDs: append([]string(nil), input.FileIDs...), Options: input.Options, HTMLVisualPromptEnabled: input.HTMLVisualPromptEnabled, HTMLVisualColorMode: input.HTMLVisualColorMode,
		MaxLLMCalls: s.resolveMaxLLMCallsPerRun(), MaxToolCalls: s.resolveMaxToolCallsPerRun(), ToolRetryCount: toolRetryCount, ToolConcurrency: toolConcurrency,
		PlanApprovalMode: normalizedPlanApprovalMode(profile.PlanApprovalMode), ToolPolicies: toolResolution.Policies,
		PlanMaxSteps: boundedTextRunConfig(cfg.Planner.MaxSteps, 12, 50), PlanMaxRevisions: boundedTextRunConfig(cfg.Planner.MaxRevisions, 5, 20),
		InteractionTTLHours: boundedTextRunConfig(cfg.Execution.InteractionTTLHours, 168, 24*365), OutputMaxPerRun: boundedTextRunConfig(cfg.Outputs.MaxPerRun, 50, 500),
		OutputIDs: uniqueRuntimeStrings(input.OutputIDs), OutputRefs: resources.OutputRefs, EvidenceIDs: resources.EvidenceIDs, EvidenceRefs: resources.EvidenceRefs, Workspace: resources.Workspace,
	}
	snapshot, err := json.Marshal(effective)
	if err != nil {
		return textRunPreparedConfiguration{}, ErrInvalidInput
	}
	return textRunPreparedConfiguration{Profile: profile, ModelName: modelName, Effective: effective, Snapshot: snapshot}, nil
}

func textRunEnvironmentWorkspaceCompatible(environment *EnvironmentProfile, workspace *WorkspaceSnapshot) bool {
	if environment == nil {
		return false
	}
	if workspace != nil {
		return environment.SupportsBindingScope(strings.TrimSpace(workspace.Request.Type))
	}
	return environment.SupportsBindingScope("general")
}

func workspaceSnapshotToolKeys(workspace *WorkspaceSnapshot) []string {
	if workspace == nil {
		return nil
	}
	keys := make([]string, 0, len(workspace.Tools))
	for _, tool := range workspace.Tools {
		if key := strings.TrimSpace(tool.ToolKey); key != "" {
			keys = append(keys, key)
		}
	}
	return uniqueRuntimeStrings(keys)
}

// applyWorkspaceToolDefinitions replaces catalog InputSchema/Description with
// the workspace-compiled definitions for matching provider tools and recomputes
// fingerprints so frozen run snapshots stay consistent.
func applyWorkspaceToolDefinitions(policies []effectiveRunToolPolicy, tools []WorkspaceToolDefinition) ([]effectiveRunToolPolicy, error) {
	if len(tools) == 0 {
		return policies, nil
	}
	byKey, byName, err := indexWorkspaceToolDefinitions(tools)
	if err != nil {
		return nil, err
	}
	result := make([]effectiveRunToolPolicy, len(policies))
	copy(result, policies)
	matched := make(map[string]struct{}, len(tools))
	for index := range result {
		tool, ok := lookupWorkspaceToolDefinition(result[index], byKey, byName)
		if !ok {
			continue
		}
		if err := overlayWorkspaceToolPolicy(&result[index], tool); err != nil {
			return nil, err
		}
		matched[tool.Name] = struct{}{}
	}
	if len(matched) != len(byName) {
		// Every workspace tool must resolve into a frozen policy; otherwise the
		// model would miss a contract the workspace expects to execute.
		return nil, ErrRunToolUnavailable
	}
	return result, nil
}

func indexWorkspaceToolDefinitions(tools []WorkspaceToolDefinition) (byKey, byName map[string]WorkspaceToolDefinition, err error) {
	byKey = make(map[string]WorkspaceToolDefinition, len(tools))
	byName = make(map[string]WorkspaceToolDefinition, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" || len(tool.InputSchema) == 0 {
			return nil, nil, ErrInvalidInput
		}
		byName[name] = tool
		if key := strings.TrimSpace(tool.ToolKey); key != "" {
			byKey[key] = tool
		}
	}
	return byKey, byName, nil
}

func lookupWorkspaceToolDefinition(policy effectiveRunToolPolicy, byKey, byName map[string]WorkspaceToolDefinition) (WorkspaceToolDefinition, bool) {
	if tool, ok := byKey[strings.TrimSpace(policy.ToolKey)]; ok {
		return tool, true
	}
	if tool, ok := byName[strings.TrimSpace(policy.ModelName)]; ok {
		return tool, true
	}
	tool, ok := byName[strings.TrimSpace(policy.OriginalName)]
	return tool, ok
}

func overlayWorkspaceToolPolicy(policy *effectiveRunToolPolicy, tool WorkspaceToolDefinition) error {
	policy.Description = tool.Description
	policy.InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
	policy.Fingerprint = fingerprintRunToolSnapshot(*policy)
	if policy.Fingerprint == "" {
		return ErrInvalidInput
	}
	return nil
}

func positiveTextRunValue(value int) int {
	if value <= 0 {
		return 8
	}
	return value
}

func nonNegativeTextRunValue(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func normalizedPlanApprovalMode(value string) string {
	if strings.TrimSpace(value) == valueAuto407FFE1D {
		return valueAuto407FFE1D
	}
	return valueRequired466769C7
}

type textRunInitialContext struct {
	UserMessage     *ContextMessage
	Attachments     []ResolvedAttachment
	ContextSnapshot *model.ContextSnapshot
	Artifacts       []model.ContextArtifact
	Projection      TurnProjection
}

func (s *Engine) prepareTextRunInitialContext(ctx context.Context, input StartTextRunInput, effective effectiveTextRunConfig, run model.Run) (textRunInitialContext, string, error) {
	userMessage, attachments, branch, err := s.prepareTextRunMessagePair(ctx, input, effective, run.RunID)
	if err != nil {
		return textRunInitialContext{}, "文本运行消息创建准备失败退回预扣", err
	}
	if s.llmGateway == nil {
		return textRunInitialContext{}, "文本运行上下文路由缺失退回预扣", ErrModelRouteNotConfigured
	}
	projection, err := s.turnProjections.BeginTurn(ctx, BeginTurnRequest{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, ContentType: userMessage.ContentType, Content: userMessage.Content, TokenEstimate: estimateTokens(userMessage.Content), Parent: userMessage.Parent, Source: userMessage.Source, BranchReason: input.BranchReason, Attachments: attachments})
	if err != nil {
		return textRunInitialContext{}, "文本运行宿主投影创建失败退回预扣", err
	}
	userMessage.Projection = projection.Input
	run.InputProjection, run.OutputProjection = projection.Input, projection.Output
	route, err := s.llmGateway.PrepareTextRoute(ctx, LLMRouteInput{PlatformModelName: effective.PlatformModelName, TaskType: LLMTaskTypeText, Scope: LLMRouteScopeUser, Actor: run.Actor, Thread: run.Thread, RequestID: run.RequestID})
	if err != nil {
		return textRunInitialContext{}, "文本运行上下文路由准备失败退回预扣", err
	}
	if _, err = hostedToolsForProtocol(effective, route.Protocol); err != nil {
		return textRunInitialContext{}, "文本运行工具与路由协议不兼容退回预扣", err
	}
	snapshot, artifacts, err := s.compileTextRunContext(ctx, run, effective, route, userMessage, branch)
	if err != nil {
		return textRunInitialContext{}, "文本运行上下文编译失败退回预扣", err
	}
	return textRunInitialContext{UserMessage: userMessage, Attachments: attachments, ContextSnapshot: snapshot, Artifacts: artifacts, Projection: projection}, "", nil
}

func textRunInitialContinuation(strategy string) (string, string) {
	if strategy == TextRunStrategyPlanned {
		return runContinuationStartPlanning, model.RunStatusPreparing
	}
	return runContinuationStartDirect, model.RunStatusRunning
}

func textRunInitialEvents(run model.Run, step model.Step, environmentRef model.ResourceRef, strategy, reason string, snapshot *model.ContextSnapshot) []model.Event {
	events := []model.Event{
		newRunEvent(run, "run.started", step.StepID, "Text run started", map[string]interface{}{valueGoal855E06D1: run.Goal, "environmentRef": environmentRef, "strategy": strategy}, nil),
		newRunEvent(run, "run.strategy_selected", step.StepID, "Text run strategy selected", map[string]interface{}{"strategy": strategy, "reasonCode": reason}, nil),
		newRunEvent(run, "step.started", step.StepID, "Orchestration started", map[string]interface{}{valueStepID549B95DB: step.StepID, valueTitle1D003E0B: step.Title}, nil),
	}
	if strategy == TextRunStrategyPlanned {
		events = append(events, newRunEvent(run, valueRunPreparingA8E38F48, step.StepID, "Preparing plan", map[string]interface{}{valueRevision0742568C: 1}, nil))
	}
	return append(events, newRunEvent(run, "context.compiled", step.StepID, "Text context compiled", map[string]interface{}{"contextHash": snapshot.ContentHash, "fileCount": snapshot.FileCount, "ragCount": snapshot.RAGCount, "skillCount": snapshot.SkillCount, "memoryCount": snapshot.MemoryCount, "outputCount": snapshot.OutputCount, "evidenceCount": snapshot.EvidenceCount, "retrievalFallbackCount": snapshot.RetrievalFallbackCount, "skippedCount": snapshot.SkippedCount}, nil))
}

func (s *Engine) recoverDuplicateTextRunStart(ctx context.Context, actor model.ActorRef, runID, fingerprint string) (*TextRunStartResult, error) {
	existing, err := s.repo.GetRun(ctx, actor, runID)
	if err != nil {
		return nil, ErrTextRunAlreadyActive
	}
	if !textRunFingerprintMatches(existing, fingerprint) {
		return nil, ErrTextRunIdempotencyConflict
	}
	return s.textRunStartResult(ctx, actor, existing)
}

func (s *Engine) resolveTextRunProfile(ctx context.Context, actor model.ActorRef, environment model.ResourceRef) (*EnvironmentProfile, error) {
	if strings.TrimSpace(environment.ID) == "" || s.environmentProfiles == nil {
		return nil, ErrRunEnvironmentUnavailable
	}
	profile, err := s.environmentProfiles.ResolveAvailableEnvironmentProfile(ctx, actor, environment)
	if err != nil || profile == nil || len(profile.UnavailableRequiredCapabilities) > 0 {
		return nil, ErrRunEnvironmentUnavailable
	}
	return profile, nil
}

func (s *Engine) resolveTextRunProfileAtRevision(ctx context.Context, actor model.ActorRef, environment model.ResourceRef) (*EnvironmentProfile, error) {
	profile, err := s.resolveTextRunProfile(ctx, actor, environment)
	if err != nil {
		return nil, err
	}
	if environment.Revision != "" && strconv.FormatUint(uint64(profile.Revision), 10) != environment.Revision {
		return nil, ErrRunEnvironmentChanged
	}
	return profile, nil
}

func (s *Engine) existingTextRunStart(ctx context.Context, actor model.ActorRef, thread model.ThreadRef, runID, fingerprint string) (TextRunStartResult, bool, error) {
	existing, err := s.repo.GetRun(ctx, actor, runID)
	if err == nil {
		if !textRunFingerprintMatches(existing, fingerprint) {
			return TextRunStartResult{}, false, ErrTextRunIdempotencyConflict
		}
		result, resultErr := s.textRunStartResult(ctx, actor, existing)
		if resultErr != nil {
			return TextRunStartResult{}, false, resultErr
		}
		return *result, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return TextRunStartResult{}, false, err
	}
	_, err = s.repo.GetActiveRun(ctx, actor, thread)
	if err == nil {
		return TextRunStartResult{}, false, ErrTextRunAlreadyActive
	}
	if !errors.Is(err, ErrNotFound) {
		return TextRunStartResult{}, false, err
	}
	return TextRunStartResult{}, false, nil
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

func resolvedToolsRespectEnvironmentBoundary(policies []effectiveRunToolPolicy, environment *EnvironmentProfile) bool {
	localKeys := make(map[string]struct{}, len(environment.Tools))
	for _, policy := range environment.Tools {
		if policy.Available {
			localKeys[policy.ToolKey] = struct{}{}
		}
	}
	for _, policy := range policies {
		if policy.ExecutionMode != valueLocalDispatch71FF6D47 {
			continue
		}
		if _, allowed := localKeys[policy.ToolKey]; !allowed {
			return false
		}
	}
	return true
}

func resolveEnvironmentSkillSelection(selected *[]model.ResourceRef, environment *EnvironmentProfile) ([]model.ResourceRef, error) {
	result, _, err := resolveEnvironmentSkillSelectionWithDiagnostics(selected, environment)
	return result, err
}

func resolveEnvironmentSkillSelectionWithDiagnostics(selected *[]model.ResourceRef, environment *EnvironmentProfile) ([]model.ResourceRef, []model.ResourceRef, error) {
	explicit := selected != nil
	chosen := selectedEnvironmentSkillRefs(selected)
	var result []model.ResourceRef
	var unavailable []model.ResourceRef
	for _, policy := range environment.Skills {
		selectedByRequest := chosen[policy.SkillRef]
		if environmentSkillUnavailable(policy, selectedByRequest) {
			return nil, nil, ErrRunSkillUnavailable
		}
		if !policy.Available && !explicit && policy.ActivationMode == valueDefaultD98758A6 {
			unavailable = append(unavailable, policy.SkillRef)
		}
		if includeEnvironmentSkill(policy, explicit, selectedByRequest) {
			result = append(result, policy.SkillRef)
			delete(chosen, policy.SkillRef)
		}
	}
	if len(chosen) > 0 {
		return nil, nil, ErrRunSkillUnavailable
	}
	return normalizeSelectedSkillRefs(result), normalizeSelectedSkillRefs(unavailable), nil
}

func mergeTextRunToolResolutions(items ...textRunToolResolution) textRunToolResolution {
	var result textRunToolResolution
	for _, item := range items {
		result.Policies = append(result.Policies, item.Policies...)
		result.ResolvedKeys = append(result.ResolvedKeys, item.ResolvedKeys...)
		result.Unavailable = append(result.Unavailable, item.Unavailable...)
	}
	result.ResolvedKeys = uniqueRuntimeStrings(result.ResolvedKeys)
	result.Unavailable = uniqueRuntimeStrings(result.Unavailable)
	return result
}

func selectedEnvironmentSkillRefs(selected *[]model.ResourceRef) map[model.ResourceRef]bool {
	chosen := map[model.ResourceRef]bool{}
	if selected == nil {
		return chosen
	}
	for _, ref := range normalizeSelectedSkillRefs(*selected) {
		chosen[ref] = true
	}
	return chosen
}

func environmentSkillUnavailable(policy EnvironmentSkillPolicy, selected bool) bool {
	return !policy.Available && (policy.ActivationMode == valueRequired466769C7 || selected)
}

func includeEnvironmentSkill(policy EnvironmentSkillPolicy, explicit, selected bool) bool {
	return policy.Available && (policy.ActivationMode == valueRequired466769C7 || selected || (!explicit && policy.ActivationMode == valueDefaultD98758A6))
}

func selectEnvironmentModel(environment *EnvironmentProfile, requested string) (string, error) {
	if environment == nil {
		return "", ErrRunEnvironmentUnavailable
	}
	if len(environment.Models) == 0 {
		return "", ErrEnvironmentModelUnconfigured
	}
	defaultPolicy, hasDefault := environmentModelPolicy(environment.Models, "", true)
	if requested == "" {
		if !hasDefault {
			return "", ErrEnvironmentModelUnconfigured
		}
		if !defaultPolicy.Available {
			return "", ErrEnvironmentDefaultUnavailable
		}
		return defaultPolicy.PlatformModelName, nil
	}
	policy, found := environmentModelPolicy(environment.Models, requested, false)
	if !found || (!policy.IsDefault && !policy.Selectable) {
		return "", ErrEnvironmentModelNotAuthorized
	}
	if !policy.Available {
		return "", ErrEnvironmentModelNotAccessible
	}
	return requested, nil
}

func environmentModelPolicy(models []EnvironmentModelPolicy, name string, defaultOnly bool) (EnvironmentModelPolicy, bool) {
	for _, policy := range models {
		if (defaultOnly && policy.IsDefault) || (!defaultOnly && policy.PlatformModelName == name) {
			return policy, true
		}
	}
	return EnvironmentModelPolicy{}, false
}

type textRunToolResolution struct {
	Policies     []effectiveRunToolPolicy
	Unavailable  []string
	ResolvedKeys []string
}

func (s *Engine) resolveTextRunToolPolicies(ctx context.Context, actor model.ActorRef, requested []string, source, workspaceType, workspaceMode, modelName string, retryCount, concurrency int) (textRunToolResolution, error) {
	result := textRunToolResolution{Policies: make([]effectiveRunToolPolicy, 0, len(requested)), ResolvedKeys: make([]string, 0, len(requested))}
	if len(requested) == 0 {
		return result, nil
	}
	if s.toolCatalog == nil {
		if source == valueRequest3E6DBD23 {
			return textRunToolResolution{}, ErrRunToolUnavailable
		}
		result.Unavailable = append(result.Unavailable, requested...)
		return result, nil
	}
	resolved, unavailable, err := s.toolCatalog.ResolveAvailable(ctx, actor, requested, workspaceType, workspaceMode, modelName)
	if err != nil {
		return textRunToolResolution{}, err
	}
	if source == valueRequest3E6DBD23 && len(unavailable) > 0 {
		return textRunToolResolution{}, ErrRunToolUnavailable
	}
	result.Unavailable = append(result.Unavailable, unavailable...)
	for _, tool := range resolved {
		policy, policyErr := snapshotResolvedRunTool(tool, retryCount, concurrency)
		if policyErr != nil {
			return textRunToolResolution{}, policyErr
		}
		result.Policies = append(result.Policies, policy)
		result.ResolvedKeys = append(result.ResolvedKeys, tool.ToolKey)
	}
	return result, nil
}

func snapshotResolvedRunTool(tool ResolvedTool, retryCount, concurrency int) (effectiveRunToolPolicy, error) {
	if !validResolvedRunTool(tool) {
		return effectiveRunToolPolicy{}, ErrRunEnvironmentUnavailable
	}
	mode := strings.TrimSpace(tool.ApprovalMode)
	if tool.ApprovalCapability == valuePerCall2570116D {
		if mode != valueNever4C6E2E88 {
			mode = valueAlways6FAD1299
		}
	} else {
		mode = "activation_only"
	}
	validLevels := map[string]bool{valueRead3A612695: true, "staged_write": true, "write": true, "destructive": true}
	level := strings.TrimSpace(tool.SideEffectLevel)
	if !validLevels[level] {
		level = valueUnknown26BF6906
	}
	snapshot := effectiveRunToolPolicy{ToolKey: tool.ToolKey, ProviderKind: tool.ProviderKind, ProviderKey: tool.ProviderKey, ModelName: tool.ModelName, OriginalName: tool.OriginalName, Description: tool.Description, DefinitionVersion: tool.DefinitionVersion, InputSchema: append(json.RawMessage(nil), tool.InputSchema...), ExecutionMode: tool.ExecutionMode, ApprovalCapability: tool.ApprovalCapability, ApprovalMode: mode, RiskLevel: tool.RiskLevel, SideEffectLevel: level, HostedVariants: cloneHostedToolVariants(tool.HostedVariants), RetryCount: retryCount, Concurrency: concurrency}
	snapshot.Fingerprint = fingerprintRunToolSnapshot(snapshot)
	return snapshot, nil
}

func validResolvedRunTool(tool ResolvedTool) bool {
	required := []string{tool.ToolKey, tool.ProviderKind, tool.ProviderKey, tool.ModelName, tool.OriginalName, tool.DefinitionVersion}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	if tool.ExecutionMode == valueLocalDispatch71FF6D47 {
		return len(tool.InputSchema) > 0
	}
	return tool.ExecutionMode == valueProviderHosted7ED91AC1 && len(tool.HostedVariants) > 0
}

func (s *Engine) compileTextRunWorkspace(ctx context.Context, input StartTextRunInput, modelName string) (WorkspaceSnapshot, bool, error) {
	scope := strings.TrimSpace(input.ThreadScope)
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
	workspace, err := provider.CompileWorkspace(ctx, input.Actor, input.Thread, input.Workspace, EffectiveContextBudget(modelName))
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

func (s *Engine) resolveTextRunResources(ctx context.Context, input StartTextRunInput, modelName string) (textRunResources, error) {
	result := textRunResources{}
	var err error
	result.OutputRefs, err = s.resolveTextRunOutputRefs(ctx, input.Actor, input.OutputIDs)
	if err != nil {
		return textRunResources{}, err
	}
	result.EvidenceIDs = uniqueRuntimeStrings(input.EvidenceIDs)
	result.EvidenceRefs, err = s.resolveTextRunEvidenceRefs(ctx, input.Actor, input.Thread, result.EvidenceIDs)
	if err != nil {
		return textRunResources{}, err
	}
	if err = s.validateWorkspaceDirectiveSource(ctx, input, result.EvidenceRefs); err != nil {
		return textRunResources{}, err
	}
	workspaceValue, found, err := s.compileTextRunWorkspace(ctx, input, modelName)
	if err != nil {
		return textRunResources{}, err
	}
	if found {
		result.Workspace = &workspaceValue
	}
	return result, nil
}

func (s *Engine) validateWorkspaceDirectiveSource(ctx context.Context, input StartTextRunInput, evidence []effectiveRunEvidenceRef) error {
	source := workspaceDirectiveSource(input.Workspace)
	if source == nil {
		return nil
	}
	if input.ParentProjection == nil || !sameProjectionRef(source.HeadProjection, *input.ParentProjection) {
		return ErrWorkspaceSourceStale
	}
	if !sameThreadRef(source.Thread, input.Thread) {
		return ErrWorkspaceSourceStale
	}
	if source.Kind == threadKindConversation {
		return s.validateConversationDirectiveSource(ctx, input.Actor, input.Thread, *input.ParentProjection)
	}
	return s.validateMessageDirectiveSource(input, source, evidence)
}

func workspaceDirectiveSource(workspace *WorkspaceRequest) *WorkspaceDirectiveSource {
	if workspace == nil || workspace.SchemaVersion != RuntimeSnapshotVersion || workspace.Directive == nil {
		return nil
	}
	return workspace.Directive.Source
}

func (s *Engine) validateConversationDirectiveSource(ctx context.Context, actor model.ActorRef, thread model.ThreadRef, head model.ProjectionRef) error {
	if s.threadContext == nil {
		return ErrHostProjectionUnavailable
	}
	path, err := s.threadContext.LoadThreadPath(ctx, LoadThreadPathRequest{Actor: actor, Thread: thread, Head: &head, MaxDepth: 51})
	if err != nil {
		return err
	}
	if path.Compaction != nil {
		return ErrWorkspaceSourceCompacted
	}
	if len(path.Messages) > 50 {
		return ErrWorkspaceSourceTooLarge
	}
	var tokenEstimate int64
	for _, message := range path.Messages {
		tokenEstimate += estimateTokens(message.Content) + 5
	}
	if tokenEstimate > 32_000 {
		return ErrWorkspaceSourceTooLarge
	}
	return nil
}

func (s *Engine) validateMessageDirectiveSource(input StartTextRunInput, source *WorkspaceDirectiveSource, evidence []effectiveRunEvidenceRef) error {
	if !containsRuntimeString(input.EvidenceIDs, source.EvidenceID) {
		return ErrWorkspaceSourceStale
	}
	wantKind := valueFull
	if source.Kind == "message_range" {
		wantKind = valueTextRange
	}
	for _, item := range evidence {
		if source.Projection != nil && item.EvidenceID == source.EvidenceID && item.SourceKind == valueProjectionSource && sameProjectionRef(item.Projection, *source.Projection) && item.Kind == wantKind {
			return nil
		}
	}
	return ErrWorkspaceSourceStale
}

func sameThreadRef(left, right model.ThreadRef) bool {
	return strings.TrimSpace(left.Kind) != "" && left.Kind == right.Kind && strings.TrimSpace(left.ID) != "" && left.ID == right.ID
}

func sameProjectionRef(left, right model.ProjectionRef) bool {
	return strings.TrimSpace(left.Kind) != "" && left.Kind == right.Kind && strings.TrimSpace(left.ID) != "" && left.ID == right.ID
}

func containsRuntimeString(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func (s *Engine) resolveTextRunOutputRefs(ctx context.Context, actor model.ActorRef, outputIDs []string) ([]effectiveRunOutputRef, error) {
	ids := uniqueRuntimeStrings(outputIDs)
	if len(ids) == 0 {
		return []effectiveRunOutputRef{}, nil
	}
	outputs, err := s.repo.GetOutputsByIDs(ctx, actor, ids)
	if err != nil || len(outputs) != len(ids) {
		return nil, ErrInvalidInput
	}
	refs := make([]effectiveRunOutputRef, 0, len(outputs))
	for _, output := range outputs {
		refs = append(refs, effectiveRunOutputRef{OutputID: output.OutputID, Version: output.Version, ContentHash: hashOutputRef(output)})
	}
	return refs, nil
}

func (s *Engine) resolveTextRunEvidenceRefs(ctx context.Context, actor model.ActorRef, thread model.ThreadRef, evidenceIDs []string) ([]effectiveRunEvidenceRef, error) {
	if len(evidenceIDs) == 0 {
		return []effectiveRunEvidenceRef{}, nil
	}
	items, err := s.repo.GetEvidenceByIDs(ctx, actor, evidenceIDs)
	if err != nil || len(items) != len(evidenceIDs) {
		return nil, ErrInvalidInput
	}
	refs := make([]effectiveRunEvidenceRef, 0, len(items))
	for _, item := range items {
		if !validTextRunEvidenceRef(item) {
			return nil, ErrInvalidInput
		}
		if item.SourceKind == valueProjectionSource {
			if s.projectionContent == nil {
				return nil, ErrWorkspaceSourceStale
			}
			content, resolveErr := s.projectionContent.ResolveProjectionContent(ctx, ResolveProjectionContentRequest{Actor: actor, Thread: thread, Projection: item.Projection})
			if resolveErr != nil || !strings.EqualFold(strings.TrimSpace(content.ContentHash), strings.TrimSpace(item.SourceContentHash)) {
				return nil, ErrWorkspaceSourceStale
			}
		}
		refs = append(refs, effectiveRunEvidenceRef{EvidenceID: item.EvidenceID, SourceKind: item.SourceKind, Kind: item.Kind, SourceID: item.SourceID, Projection: item.Projection, Title: item.Title, ContentHash: item.ContentHash, SourceContentHash: item.SourceContentHash, Excerpt: item.Excerpt})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].EvidenceID < refs[j].EvidenceID })
	return refs, nil
}

func validTextRunEvidenceRef(item model.Evidence) bool {
	excerptHash := sha256.Sum256([]byte(item.Excerpt))
	if !strings.EqualFold(item.ContentHash, hex.EncodeToString(excerptHash[:])) {
		return false
	}
	switch item.SourceKind {
	case valueOutput6DD2E13C:
		return strings.TrimSpace(item.SourceID) != ""
	case valueProjectionSource:
		return strings.TrimSpace(item.SourceID) != "" && item.SourceID == item.Projection.ID && strings.TrimSpace(item.Projection.Kind) != "" && strings.TrimSpace(item.SourceContentHash) != ""
	default:
		return false
	}
}

type textRunFingerprintInput struct {
	Actor               model.ActorRef
	Thread              model.ThreadRef
	Goal                string
	Environment         model.ResourceRef
	Model               string
	ExecutionMode       string
	Options             map[string]interface{}
	FileIDs             []string
	OutputIDs           []string
	EvidenceIDs         []string
	ToolKeys            *[]string
	SkillRefs           *[]model.ResourceRef
	ParentProjection    *model.ProjectionRef
	SourceProjection    *model.ProjectionRef
	BranchReason        string
	HTMLVisualPrompt    bool
	HTMLVisualColorMode string
	Workspace           *WorkspaceRequest
}

func textRunRequestFingerprint(input StartTextRunInput, goal string) string {
	files := append([]string(nil), input.FileIDs...)
	sort.Strings(files)
	copyRefs := func(value *[]model.ResourceRef) *[]model.ResourceRef {
		if value == nil {
			return nil
		}
		items := normalizeSelectedSkillRefs(*value)
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		return &items
	}
	copyKeys := func(value *[]string) *[]string {
		if value == nil {
			return nil
		}
		items := uniqueRuntimeStrings(*value)
		sort.Strings(items)
		return &items
	}
	outputs := uniqueRuntimeStrings(input.OutputIDs)
	sort.Strings(outputs)
	evidence := uniqueRuntimeStrings(input.EvidenceIDs)
	sort.Strings(evidence)
	payload := textRunFingerprintInput{Actor: input.Actor, Thread: input.Thread, Goal: goal, Environment: input.Environment, Model: strings.TrimSpace(input.PlatformModelName), ExecutionMode: strings.TrimSpace(input.ExecutionMode), Options: input.Options, FileIDs: files, OutputIDs: outputs, EvidenceIDs: evidence, ToolKeys: copyKeys(input.ToolKeys), SkillRefs: copyRefs(input.SkillRefs), ParentProjection: input.ParentProjection, SourceProjection: input.SourceProjection, BranchReason: strings.TrimSpace(input.BranchReason), HTMLVisualPrompt: input.HTMLVisualPromptEnabled, HTMLVisualColorMode: strings.TrimSpace(input.HTMLVisualColorMode), Workspace: input.Workspace}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func resolveTextRunStrategy(requestedMode, defaultMode string, allowedModes []string, goal string, tools []effectiveRunToolPolicy) (string, string, string, error) {
	mode, explicit, err := resolveTextRunExecutionMode(requestedMode, defaultMode)
	if err != nil {
		return "", "", "", err
	}
	switch mode {
	case TextRunExecutionModeDirect:
		strategy, reason, routeErr := resolveFixedTextRunStrategy(mode, explicit, allowedModes)
		return strategy, reason, mode, routeErr
	case TextRunExecutionModePlan:
		strategy, reason, routeErr := resolveFixedTextRunStrategy(mode, explicit, allowedModes)
		return strategy, reason, mode, routeErr
	default:
		strategy, reason, routeErr := resolveAutomaticTextRunStrategy(allowedModes, goal, tools)
		return strategy, reason, mode, routeErr
	}
}

func resolveTextRunExecutionMode(requestedMode, defaultMode string) (string, bool, error) {
	mode := strings.TrimSpace(requestedMode)
	explicit := mode != ""
	if mode == "" {
		mode = strings.TrimSpace(defaultMode)
	}
	if mode != TextRunExecutionModeAuto && mode != TextRunExecutionModeDirect && mode != TextRunExecutionModePlan {
		return "", false, ErrExecutionModeNotAllowed
	}
	return mode, explicit, nil
}

func resolveFixedTextRunStrategy(mode string, explicit bool, allowedModes []string) (string, string, error) {
	if !containsTextRunMode(allowedModes, mode) {
		return "", "", ErrExecutionModeNotAllowed
	}
	strategy, requestedReason := TextRunStrategyDirect, textRunStrategyReasonRequestedDirect
	if mode == TextRunExecutionModePlan {
		strategy, requestedReason = TextRunStrategyPlanned, textRunStrategyReasonRequestedPlan
	}
	reason := textRunStrategyReasonEnvironmentDefault
	if explicit {
		reason = requestedReason
	}
	return strategy, reason, nil
}

func resolveAutomaticTextRunStrategy(allowedModes []string, goal string, tools []effectiveRunToolPolicy) (string, string, error) {
	if len(allowedModes) == 1 {
		switch allowedModes[0] {
		case TextRunExecutionModeDirect:
			return TextRunStrategyDirect, textRunStrategyReasonEnvironmentSingleMode, nil
		case TextRunExecutionModePlan:
			return TextRunStrategyPlanned, textRunStrategyReasonEnvironmentSingleMode, nil
		}
	}
	if !containsTextRunMode(allowedModes, TextRunExecutionModeDirect) || !containsTextRunMode(allowedModes, TextRunExecutionModePlan) {
		return "", "", ErrExecutionModeNotAllowed
	}
	if textRunToolsRequireApproval(tools) {
		return TextRunStrategyPlanned, textRunStrategyReasonAutoApprovalRequired, nil
	}
	if textRunRequiresPlannedIntent(goal) {
		return TextRunStrategyPlanned, textRunStrategyReasonAutoPlanIntent, nil
	}
	if runExplicitDirectIntent(strings.ToLower(strings.TrimSpace(goal))) {
		return TextRunStrategyDirect, textRunStrategyReasonAutoDirectIntent, nil
	}
	return TextRunStrategyDirect, textRunStrategyReasonAutoSimple, nil
}

func textRunToolsRequireApproval(tools []effectiveRunToolPolicy) bool {
	for i := range tools {
		if tools[i].ApprovalMode != valueNever4C6E2E88 {
			return true
		}
	}
	return false
}

func containsTextRunMode(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func textRunFingerprintMatches(existing *model.Run, fingerprint string) bool {
	return existing != nil && strings.TrimSpace(existing.RequestFingerprint) != "" && existing.RequestFingerprint == fingerprint
}

func (s *Engine) textRunStartResult(ctx context.Context, actor model.ActorRef, run *model.Run) (*TextRunStartResult, error) {
	if run == nil {
		return nil, ErrNotFound
	}
	if run.Actor != actor {
		return nil, ErrNotFound
	}
	result := &TextRunStartResult{Run: *run, Projection: TurnProjection{Input: run.InputProjection, Output: run.OutputProjection}}
	steps, err := s.repo.ListRunSteps(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	if len(steps) > 0 {
		result.Step = steps[0]
	}
	return result, nil
}

func (s *Engine) appendRunEvent(ctx context.Context, run *model.Run, eventType, stepID, summary string, payload map[string]interface{}, projection *model.ProjectionRef) error {
	event := newRunEvent(*run, eventType, stepID, summary, payload, projection)
	saved, created, err := s.repo.AppendRunEvent(ctx, &event)
	if err != nil {
		return err
	}
	if created {
		s.PublishRunNotification(run.RunID, runEventEnvelope(saved))
	}
	return nil
}

func newRunEvent(run model.Run, eventType, stepID, summary string, payload map[string]interface{}, projection *model.ProjectionRef) model.Event {
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(`{"error":"event_payload_encoding_failed"}`)
	}
	event := model.Event{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, EventID: "evt_" + strings.ReplaceAll(uuid.NewString(), "-", ""), EventType: eventType, SchemaVersion: 1, StepID: stepID, Visibility: valueUserDD885A59, Status: runEventStatus(eventType), Summary: truncateRunEventSummary(summary), PayloadJSON: string(data), StartedAt: time.Now()}
	if projection != nil {
		event.Projection = *projection
	}
	return event
}

func truncateRunEventSummary(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 255 {
		return string(runes[:255])
	}
	return string(runes)
}

func (s *Engine) appendRunEvents(ctx context.Context, runID string, events []model.Event) error {
	saved, err := s.repo.AppendRunEvents(ctx, events)
	if err != nil {
		return err
	}
	s.publishRunEvents(runID, saved)
	return nil
}

func (s *Engine) publishRunEvents(runID string, events []model.Event) {
	for i := range events {
		event := events[i]
		s.PublishRunNotification(runID, runEventEnvelope(&event))
	}
}
func runEventEnvelope(e *model.Event) map[string]interface{} {
	var payload map[string]interface{}
	_ = json.Unmarshal([]byte(e.PayloadJSON), &payload)
	if payload == nil {
		payload = map[string]interface{}{}
	}
	appendPublicRunEventFields(payload, e.Summary, e.Status, e.ToolCallID, e.ToolName)
	durable := map[string]interface{}{"schemaVersion": 1, "eventID": e.EventID, "runID": e.RunID, valueActorRefKey: map[string]string{"tenantID": e.Actor.TenantID, "id": e.Actor.ActorID}, valueThreadRefKey: map[string]string{"kind": e.Thread.Kind, "id": e.Thread.ID}, "seq": e.Seq, valueType106D7553: e.EventType, valueStepID549B95DB: e.StepID, "parentEventID": e.ParentEventID, "timestamp": e.StartedAt.UTC().Format(time.RFC3339Nano), "payload": payload}
	return map[string]interface{}{valueType106D7553: "run_event", "event": durable}
}

func appendPublicRunEventFields(payload map[string]interface{}, summary, status, toolCallID, toolName string) {
	for key, value := range map[string]string{"summary": summary, "status": status, "toolCallID": toolCallID, "toolName": toolName} {
		if _, exists := payload[key]; exists || strings.TrimSpace(value) == "" {
			continue
		}
		payload[key] = strings.TrimSpace(value)
	}
}

func runEventStatus(kind string) string {
	switch kind {
	case valueRunPreparingA8E38F48:
		return model.RunStatusPreparing
	case valueRunWaitingInputF2C37C0A, "step.waiting_input":
		return model.RunStatusWaitingInput
	case "step.created":
		return model.RunStatusQueued
	case "step.skipped":
		return "skipped"
	}
	if strings.HasSuffix(kind, ".failed") {
		return model.RunStatusFailed
	}
	if strings.HasSuffix(kind, ".completed") {
		return model.RunStatusCompleted
	}
	if strings.HasSuffix(kind, ".cancelled") {
		return model.RunStatusCancelled
	}
	if strings.HasSuffix(kind, ".suspended") {
		return model.RunStatusSuspended
	}
	return model.RunStatusRunning
}
func uniqueRuntimeIDs(in []uint) []uint {
	seen := map[uint]bool{}
	out := []uint{}
	for _, id := range in {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
func truncateRunTitle(v string) string {
	r := []rune(strings.TrimSpace(v))
	if len(r) > 80 {
		return string(r[:80])
	}
	return string(r)
}

func mustRunJSON(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `{"error":"json_encoding_failed"}`
	}
	return string(data)
}

func (s *Engine) GetTextRunDetail(ctx context.Context, actor model.ActorRef, runID string) (*TextRunDetail, error) {
	run, err := s.repo.GetRun(ctx, actor, runID)
	if err != nil {
		return nil, err
	}
	steps, err := s.repo.ListRunSteps(ctx, runID)
	if err != nil {
		return nil, err
	}
	detail := &TextRunDetail{Run: *run, Steps: steps, Projection: TurnProjection{Input: run.InputProjection, Output: run.OutputProjection}}
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &effective) == nil {
		protocol := s.resolveProviderProtocolForSummary(ctx, *run, effective.PlatformModelName)
		detail.Config = summarizeTextRunConfig(effective, protocol)
	}
	if snapshot, snapshotErr := s.repo.GetRunContextSnapshot(ctx, actor, runID); snapshotErr == nil {
		detail.Context = &TextRunContextSummary{SnapshotID: snapshot.SnapshotID, SemanticVersion: snapshot.SchemaVersion, ContentHash: snapshot.ContentHash, FileCount: snapshot.FileCount, RAGCount: snapshot.RAGCount, SkillCount: snapshot.SkillCount, MemoryCount: snapshot.MemoryCount, OutputCount: snapshot.OutputCount, EvidenceCount: snapshot.EvidenceCount, RetrievalFallbackCount: snapshot.RetrievalFallbackCount, SkippedCount: snapshot.SkippedCount, CompiledAt: snapshot.CreatedAt}
	}
	return detail, nil
}
func (s *Engine) ListRunEventsAfter(ctx context.Context, actor model.ActorRef, runID string, after int64) ([]model.Event, error) {
	return s.repo.ListRunEventsAfter(ctx, actor, runID, after, 500)
}

func (s *Engine) GetRunCursor(ctx context.Context, actor model.ActorRef, runID string) (*model.RunCursor, error) {
	return s.repo.GetRunCursor(ctx, actor, runID)
}

func (s *Engine) ListRunEventHistory(ctx context.Context, actor model.ActorRef, runID string, beforeSeq int64, limit int) (*RunEventHistoryPage, error) {
	if _, err := s.repo.GetRunCursor(ctx, actor, runID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 300
	} else if limit > 500 {
		limit = 500
	}
	items, err := s.repo.ListRunEventsBefore(ctx, actor, runID, beforeSeq, limit+1)
	if err != nil {
		return nil, err
	}
	return buildRunEventHistoryPage(items, limit), nil
}

func buildRunEventHistoryPage(items []model.Event, limit int) *RunEventHistoryPage {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	page := &RunEventHistoryPage{Results: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		page.NextBeforeSeq = int64(items[0].Seq)
	}
	return page
}
func (s *Engine) GetRunEvent(ctx context.Context, actor model.ActorRef, runID, eventID string) (*model.Event, error) {
	return s.repo.GetRunEvent(ctx, actor, runID, eventID)
}

func (s *Engine) startTextRunReconciliation(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := s.ReconcileTextRunsOnce(ctx, time.Now().Add(-60*time.Second)); err != nil && s.logger != nil {
			s.logger.Error("reconcile_text_runs_failed", Error(err))
		}
		if err := s.ExpireRunInteractionsOnce(ctx, time.Now()); err != nil && s.logger != nil {
			s.logger.Error("expire_run_interactions_failed", Error(err))
		}
	}
}

// ReconcileTextRunsOnce runs the same fail-safe reconciliation pass used by
// the periodic worker. It is exported for operational probes and deterministic
// crash-recovery tests; it does not alter the public HTTP contract.
func (s *Engine) ReconcileTextRunsOnce(ctx context.Context, olderThan time.Time) error {
	return reconcileTextRunsOnce(ctx, olderThan, textRunReconciliationDependencies{
		list:       s.repo.ListNonterminalRuns,
		leaseState: s.TextRunLeaseState,
		warn: func(runID string, state GenerationLeaseState, leaseErr error) {
			evidence := "store_unknown"
			if state == GenerationLeaseActive {
				evidence = "local_active"
			}
			if s.logger != nil {
				s.logger.Warn("text_run_generation_lease_degraded", String("run_id", runID), String("lease_state", string(state)), String("evidence_source", evidence), Error(leaseErr))
			}
		},
		suspend: func(run model.Run, events []model.Event) (bool, error) {
			saved, applied, appendErr := s.repo.AppendRunEventsIfCurrent(ctx, run.RunID, run.Status, run.LastEventSeq, events)
			if appendErr == nil && applied {
				s.publishRunEvents(run.RunID, saved)
			}
			return applied, appendErr
		},
	})
}

// ExpireRunInteractionsOnce runs one deterministic interaction-expiry pass.
// It is exported for operational probes and crash-recovery contract tests.
func (s *Engine) ExpireRunInteractionsOnce(ctx context.Context, before time.Time) error {
	return expireRunInteractionsOnce(ctx, before, interactionExpiryDependencies{
		list:    s.repo.ListExpiredRunInteractions,
		expire:  s.repo.ExpireRunInteraction,
		publish: s.publishRunEvents,
		finish:  s.FinishRunNotifications,
	})
}

type interactionExpiryDependencies struct {
	list    func(context.Context, time.Time, int) ([]model.ExpiredInteraction, error)
	expire  func(context.Context, string) ([]model.Event, bool, error)
	publish func(string, []model.Event)
	finish  func(string)
}

func expireRunInteractionsOnce(ctx context.Context, before time.Time, deps interactionExpiryDependencies) error {
	items, err := deps.list(ctx, before, 100)
	if err != nil {
		return err
	}
	for _, item := range items {
		saved, applied, expireErr := deps.expire(ctx, item.InteractionID)
		if expireErr != nil {
			return expireErr
		}
		if applied {
			deps.publish(item.RunID, saved)
			deps.finish(item.RunID)
		}
	}
	return nil
}

type textRunReconciliationDependencies struct {
	list       func(context.Context, time.Time) ([]model.Run, error)
	leaseState func(context.Context, string) (GenerationLeaseState, error)
	warn       func(string, GenerationLeaseState, error)
	suspend    func(model.Run, []model.Event) (bool, error)
}

func reconcileTextRunsOnce(ctx context.Context, olderThan time.Time, deps textRunReconciliationDependencies) error {
	runs, err := deps.list(ctx, olderThan)
	if err != nil {
		return err
	}
	var reconcileErr error
	for i := range runs {
		state, leaseErr := deps.leaseState(ctx, runs[i].RunID)
		if leaseErr != nil && deps.warn != nil {
			deps.warn(runs[i].RunID, state, leaseErr)
		}
		if state != GenerationLeaseExpired {
			continue
		}
		reason := "runtime lease is no longer active"
		events := []model.Event{
			newRunEvent(runs[i], "step.suspended", runs[i].CurrentStepID, reason, map[string]interface{}{valueStatus6CF1EE63: model.RunStatusSuspended}, nil),
			newRunEvent(runs[i], "run.suspended", runs[i].CurrentStepID, reason, map[string]interface{}{valueStatus6CF1EE63: model.RunStatusSuspended}, nil),
		}
		if _, err := deps.suspend(runs[i], events); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("suspend stale text run %s: %w", runs[i].RunID, err))
		}
	}
	return reconcileErr
}
