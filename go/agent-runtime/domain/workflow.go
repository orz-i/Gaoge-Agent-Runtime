package domain

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

const (
	RuntimeKindText     = "text"
	RuntimeKindWorkflow = "workflow"

	WorkflowDefinitionKind           = "workflow_definition"
	WorkflowDefinitionStatusActive   = "active"
	WorkflowDefinitionStatusDisabled = "disabled"
	WorkflowDefinitionScopeActor     = "actor"
	WorkflowDefinitionScopeTenant    = "tenant"
	WorkflowDefinitionScopeSystem    = "system"

	WorkflowNodeSequence    = "sequence"
	WorkflowNodeAgent       = "agent"
	WorkflowNodeParallel    = "parallel"
	WorkflowNodeForEach     = "forEach"
	WorkflowNodePipeline    = "pipeline"
	WorkflowNodeIf          = "if"
	WorkflowNodeLoop        = "loop"
	WorkflowNodeSet         = "set"
	WorkflowNodeLog         = "log"
	WorkflowNodeTool        = "tool"
	WorkflowNodeWorkflow    = "workflow"
	WorkflowNodeInteraction = "interaction"
	WorkflowNodeTimer       = "timer"
	WorkflowNodeCompensate  = "compensate"
	WorkflowNodeReturn      = "return"

	WorkflowExprOpLiteral  = "literal"
	WorkflowExprOpRef      = "ref"
	WorkflowExprOpObject   = "object"
	WorkflowExprOpArray    = "array"
	WorkflowExprOpEq       = "eq"
	WorkflowExprOpNe       = "ne"
	WorkflowExprOpLt       = "lt"
	WorkflowExprOpLte      = "lte"
	WorkflowExprOpGt       = "gt"
	WorkflowExprOpGte      = "gte"
	WorkflowExprOpAnd      = "and"
	WorkflowExprOpOr       = "or"
	WorkflowExprOpNot      = "not"
	WorkflowExprOpCoalesce = "coalesce"
	WorkflowExprOpMerge    = "merge"
	WorkflowExprOpAppend   = "append"
	WorkflowExprOpConcat   = "concat"
	WorkflowExprOpLength   = "length"
	WorkflowExprOpContains = "contains"
	WorkflowExprOpAdd      = "add"
	WorkflowExprOpSub      = "sub"
	WorkflowExprOpMul      = "mul"
	WorkflowExprOpDiv      = "div"
	WorkflowExprOpMod      = "mod"

	WorkflowExprRefInput        = "input"
	WorkflowExprRefVars         = "vars"
	WorkflowExprRefSteps        = "steps"
	WorkflowExprRefItem         = "item"
	WorkflowExprRefIndex        = "index"
	WorkflowExprRefError        = "error"
	WorkflowExprRefCompensation = "compensation"

	WorkflowFailureCollect  = "collect"
	WorkflowFailureFailFast = "fail_fast"

	WorkflowCacheUse     = "use"
	WorkflowCacheRefresh = "refresh"
	WorkflowCacheBypass  = "bypass"

	WorkflowExecutionQueued       = "queued"
	WorkflowExecutionRunning      = "running"
	WorkflowExecutionWaiting      = "waiting"
	WorkflowExecutionCancelling   = "cancelling"
	WorkflowExecutionCompensating = "compensating"
	WorkflowExecutionCompleted    = "completed"
	WorkflowExecutionFailed       = "failed"
	WorkflowExecutionCancelled    = "cancelled"
	WorkflowExecutionSuspended    = "suspended"

	WorkflowWaitTimer       = "timer"
	WorkflowWaitInteraction = "interaction"
	WorkflowWaitAgent       = "agent"
	WorkflowWaitWorkflow    = "workflow"

	WorkflowDependencyAgent    = "agent_manifest"
	WorkflowDependencyWorkflow = "workflow_definition"
	WorkflowDependencyTool     = "tool"

	WorkflowStepStatusReady       = "ready"
	WorkflowStepStatusRunning     = "running"
	WorkflowStepStatusWaiting     = "waiting"
	WorkflowStepStatusCompleted   = "completed"
	WorkflowStepStatusFailed      = "failed"
	WorkflowStepStatusCancelled   = "cancelled"
	WorkflowStepStatusCompensated = "compensated"

	WorkflowCompensationPending   = "pending"
	WorkflowCompensationRunning   = "compensating"
	WorkflowCompensationCompleted = "compensated"
	WorkflowCompensationFailed    = "failed"

	WorkflowLogLevelDebug = "debug"
	WorkflowLogLevelInfo  = "info"
	WorkflowLogLevelWarn  = "warn"
	WorkflowLogLevelError = "error"
)

// WorkflowLimits is the hard aggregate budget for one root Workflow run.
// Nested workflows share the root ledger and may only narrow these values.
type WorkflowLimits struct {
	MaxNodeActivations int `json:"maxNodeActivations"`
	MaxChildRuns       int `json:"maxChildRuns"`
	MaxConcurrentRuns  int `json:"maxConcurrentRuns"`
	MaxTotalLLMCalls   int `json:"maxTotalLLMCalls"`
	MaxTotalToolCalls  int `json:"maxTotalToolCalls"`
	MaxDurationSeconds int `json:"maxDurationSeconds"`
	MaxLoopIterations  int `json:"maxLoopIterations"`
	MaxNestedDepth     int `json:"maxNestedDepth"`
	MaxStateBytes      int `json:"maxStateBytes"`
}

// WorkflowNodeLimits narrows the aggregate limits for a single activation.
type WorkflowNodeLimits struct {
	MaxLLMCalls  int `json:"maxLLMCalls,omitempty"`
	MaxToolCalls int `json:"maxToolCalls,omitempty"`
}

// WorkflowExpr is a closed, serializable expression AST. Only fields selected
// by Op are legal; the compiler rejects unknown combinations.
type WorkflowExpr struct {
	Op     string                  `json:"op"`
	Value  json.RawMessage         `json:"value,omitempty"`
	Ref    string                  `json:"ref,omitempty"`
	Fields map[string]WorkflowExpr `json:"fields,omitempty"`
	Items  []WorkflowExpr          `json:"items,omitempty"`
	Args   []WorkflowExpr          `json:"args,omitempty"`
}

type WorkflowCachePolicy struct {
	Enabled    bool   `json:"enabled"`
	TTLSeconds int    `json:"ttlSeconds,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

// WorkflowNode is the strict V1 node union. It deliberately contains data
// only: arbitrary callbacks and executable host code are not part of the DSL.
type WorkflowNode struct {
	ID   string `json:"id"`
	Type string `json:"type"`

	Children []WorkflowNode `json:"children,omitempty"`
	Branches []WorkflowNode `json:"branches,omitempty"`
	Stages   []WorkflowNode `json:"stages,omitempty"`
	Body     *WorkflowNode  `json:"body,omitempty"`
	Then     *WorkflowNode  `json:"then,omitempty"`
	Else     *WorkflowNode  `json:"else,omitempty"`
	Do       *WorkflowNode  `json:"do,omitempty"`
	Undo     *WorkflowNode  `json:"undo,omitempty"`

	ManifestRef   ResourceRef `json:"manifestRef,omitempty"`
	DefinitionRef ResourceRef `json:"definitionRef,omitempty"`
	ToolKey       string      `json:"toolKey,omitempty"`

	Goal         *WorkflowExpr `json:"goal,omitempty"`
	ItemsExpr    *WorkflowExpr `json:"items,omitempty"`
	Condition    *WorkflowExpr `json:"condition,omitempty"`
	Arguments    *WorkflowExpr `json:"arguments,omitempty"`
	Input        *WorkflowExpr `json:"input,omitempty"`
	Message      *WorkflowExpr `json:"message,omitempty"`
	Data         *WorkflowExpr `json:"data,omitempty"`
	DelaySeconds *WorkflowExpr `json:"delaySeconds,omitempty"`
	WakeAt       *WorkflowExpr `json:"wakeAt,omitempty"`
	Value        *WorkflowExpr `json:"value,omitempty"`
	Presentation *WorkflowExpr `json:"presentation,omitempty"`

	Assignments  map[string]WorkflowExpr `json:"assignments,omitempty"`
	OutputSchema json.RawMessage         `json:"outputSchema,omitempty"`
	Schema       json.RawMessage         `json:"schema,omitempty"`

	ResultAttempts      int                  `json:"resultAttempts,omitempty"`
	PerNodeLimits       *WorkflowNodeLimits  `json:"perNodeLimits,omitempty"`
	MaxConcurrency      int                  `json:"maxConcurrency,omitempty"`
	MaxIterations       int                  `json:"maxIterations,omitempty"`
	FailurePolicy       string               `json:"failurePolicy,omitempty"`
	Level               string               `json:"level,omitempty"`
	Title               string               `json:"title,omitempty"`
	Prompt              string               `json:"prompt,omitempty"`
	ExpiresAfterSeconds int                  `json:"expiresAfterSeconds,omitempty"`
	Cache               *WorkflowCachePolicy `json:"cache,omitempty"`
}

type WorkflowDependency struct {
	Kind              string      `json:"kind"`
	Ref               ResourceRef `json:"ref"`
	ToolKey           string      `json:"toolKey,omitempty"`
	DefinitionVersion string      `json:"definitionVersion,omitempty"`
	Fingerprint       string      `json:"fingerprint"`
	SideEffectLevel   string      `json:"sideEffectLevel,omitempty"`
}

// WorkflowDefinition is one immutable, fully compiled revision.
type WorkflowDefinition struct {
	WorkflowID         string
	Revision           int
	SchemaVersion      int
	Scope              string
	TenantID           string
	OwnerActorID       string
	Name               string
	Description        string
	Status             string
	InputSchema        json.RawMessage
	OutputSchema       json.RawMessage
	Root               WorkflowNode
	Limits             WorkflowLimits
	Dependencies       []WorkflowDependency
	DependencyHash     string
	DefinitionHash     string
	CreatedBy          ActorRef
	RequestID          string
	RequestFingerprint string
	RevisionNote       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (d WorkflowDefinition) Ref() ResourceRef {
	revision := ""
	if d.Revision > 0 {
		revision = strconv.Itoa(d.Revision)
	}
	return ResourceRef{Kind: WorkflowDefinitionKind, ID: d.WorkflowID, Revision: revision}
}

func WorkflowDefinitionVisibleTo(item WorkflowDefinition, actor ActorRef) bool {
	switch item.Scope {
	case WorkflowDefinitionScopeActor:
		return item.TenantID == actor.TenantID && item.OwnerActorID == actor.ActorID
	case WorkflowDefinitionScopeTenant:
		return item.TenantID == actor.TenantID
	case WorkflowDefinitionScopeSystem:
		return true
	default:
		return false
	}
}

type WorkflowDefinitionFilter struct {
	Status       string
	Scope        string
	TenantID     string
	OwnerActorID string
	Admin        bool
	Limit        int
	Offset       int
}

type WorkflowDefinitionPage struct {
	Total   int64
	Results []WorkflowDefinition
}

type WorkflowBudget struct {
	Limits                WorkflowLimits `json:"limits"`
	NodeActivations       int            `json:"nodeActivations"`
	ChildRuns             int            `json:"childRuns"`
	ConcurrentRuns        int            `json:"concurrentRuns"`
	ReservedLLMCalls      int            `json:"reservedLLMCalls"`
	UsedLLMCalls          int            `json:"usedLLMCalls"`
	ReservedToolCalls     int            `json:"reservedToolCalls"`
	UsedToolCalls         int            `json:"usedToolCalls"`
	LoopIterations        int            `json:"loopIterations"`
	CompensationToolCalls int            `json:"compensationToolCalls"`
}

type WorkflowWait struct {
	WaitID        string          `json:"waitID"`
	Kind          string          `json:"kind"`
	ActivationKey string          `json:"activationKey"`
	StepID        string          `json:"stepID"`
	InteractionID string          `json:"interactionID,omitempty"`
	ChildRunID    string          `json:"childRunID,omitempty"`
	WakeAt        *time.Time      `json:"wakeAt,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
}

type WorkflowCompensation struct {
	ActivationKey string       `json:"activationKey"`
	CompletionSeq int64        `json:"completionSeq"`
	Undo          WorkflowNode `json:"undo"`
	Status        string       `json:"status"`
	Attempt       int          `json:"attempt"`
	Error         string       `json:"error,omitempty"`
}

// WorkflowExecution is the durable interpreter state for one workflow Run.
// StateJSON contains the closed V1 interpreter state and is guarded by Version.
type WorkflowExecution struct {
	RunID               string
	WorkflowID          string
	WorkflowRevision    int
	DefinitionHash      string
	DependencyHash      string
	RootRunID           string
	BudgetOwnerRunID    string
	ParentRunID         string
	Depth               int
	Version             int64
	Status              string
	StateJSON           string
	VarsJSON            string
	WaitsJSON           string
	CompensationJSON    string
	BudgetJSON          string
	EnvironmentSnapshot string
	WorkspaceSnapshot   string
	ThreadSnapshotHash  string
	CompletionSeq       int64
	ErrorCode           string
	ErrorMessage        string
	StartedAt           time.Time
	EndedAt             *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type RunResult struct {
	RunID         string
	RuntimeKind   string
	CanonicalJSON string
	Presentation  string
	SchemaHash    string
	ContentHash   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type WorkflowCacheEntry struct {
	CacheKey       string
	Actor          ActorRef
	WorkflowRef    ResourceRef
	NodeID         string
	DependencyHash string
	SchemaHash     string
	ContextHash    string
	InputHash      string
	ValueJSON      string
	ContentHash    string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// WorkflowTransition is one CAS-guarded interpreter commit. Store adapters
// apply every listed effect atomically or report a version conflict.
type WorkflowTransition struct {
	ExpectedVersion  int64
	Execution        WorkflowExecution
	Run              Run
	Steps            []Step
	Interactions     []Interaction
	Checkpoints      []Checkpoint
	ContinuationJobs []ContinuationJob
	Events           []Event
	Result           *RunResult
	CacheEntries     []WorkflowCacheEntry
}

func NormalizeRuntimeKind(value string) string {
	if strings.TrimSpace(value) == RuntimeKindWorkflow {
		return RuntimeKindWorkflow
	}
	return RuntimeKindText
}
