package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	WorkflowSchemaVersion          = 1
	maxWorkflowDefinitionIDRunes   = 64
	workflowDefinitionIDSuffixSize = 12
)

var (
	ErrWorkflowDefinitionConflict = errors.New("workflow definition revision conflict")
	ErrWorkflowDefinitionDisabled = errors.New("workflow definition is disabled")
	ErrWorkflowDefinitionInvalid  = errors.New("workflow definition is invalid")
	ErrWorkflowDependencyCycle    = errors.New("workflow dependency cycle")
	ErrWorkflowDependencyMissing  = errors.New("workflow dependency is unavailable")
	ErrWorkflowSchemaInvalid      = errors.New("workflow JSON Schema is invalid")
	ErrWorkflowSchemaValidation   = errors.New("workflow JSON value does not match schema")
	ErrWorkflowExpressionInvalid  = errors.New("workflow expression is invalid")
	ErrWorkflowExpressionLimit    = errors.New("workflow expression resource limit exceeded")
	ErrWorkflowBudgetExceeded     = errors.New("workflow budget exceeded")
	ErrWorkflowVersionConflict    = errors.New("workflow execution version conflict")
	ErrWorkflowResultConflict     = errors.New("workflow run result conflict")
	ErrWorkflowResultInvalid      = errors.New("workflow run result is invalid")
	ErrWorkflowStateInvalid       = errors.New("workflow execution state is invalid")
	ErrWorkflowStateTooLarge      = errors.New("workflow execution state exceeds limit")
	ErrWorkflowCompensationFailed = errors.New("workflow compensation failed")
	ErrWorkflowWaitPending        = errors.New("workflow activation is waiting")
)

type WorkflowDefinitionRevisionInput struct {
	Actor            model.ActorRef
	WorkflowID       string
	ExpectedRevision int
	SchemaVersion    int
	Scope            string
	TenantID         string
	OwnerActorID     string
	Name             string
	Description      string
	Status           string
	InputSchema      json.RawMessage
	OutputSchema     json.RawMessage
	Root             model.WorkflowNode
	Limits           model.WorkflowLimits
	RequestID        string
	RevisionNote     string
}

type WorkflowDefinitionValidation struct {
	Definition model.WorkflowDefinition
	NodeCount  int
}

func (s *Engine) ValidateWorkflowDefinition(ctx context.Context, input WorkflowDefinitionRevisionInput) (*WorkflowDefinitionValidation, error) {
	if s == nil || s.repo == nil || !validActorRef(input.Actor) || input.ExpectedRevision < 0 || strings.TrimSpace(input.Name) == "" {
		return nil, ErrInvalidInput
	}
	item, nodeCount, err := s.compileWorkflowDefinition(ctx, input)
	if err != nil {
		return nil, err
	}
	return &WorkflowDefinitionValidation{Definition: item, NodeCount: nodeCount}, nil
}

func (s *Engine) CreateWorkflowDefinition(ctx context.Context, input WorkflowDefinitionRevisionInput) (*model.WorkflowDefinition, bool, error) {
	compiled, err := s.ValidateWorkflowDefinition(ctx, input)
	if err != nil {
		return nil, false, err
	}
	return s.repo.CreateWorkflowDefinitionRevision(ctx, &compiled.Definition, input.ExpectedRevision)
}

func (s *Engine) GetWorkflowDefinition(ctx context.Context, actor model.ActorRef, ref model.ResourceRef) (*model.WorkflowDefinition, error) {
	if s == nil || s.repo == nil || !validActorRef(actor) {
		return nil, ErrInvalidInput
	}
	ref.ID = normalizeWorkflowID(ref.ID)
	return s.repo.GetWorkflowDefinition(ctx, actor, ref)
}

func (s *Engine) ListWorkflowDefinitions(ctx context.Context, actor model.ActorRef, filter model.WorkflowDefinitionFilter) (model.WorkflowDefinitionPage, error) {
	if s == nil || s.repo == nil || !validActorRef(actor) {
		return model.WorkflowDefinitionPage{}, ErrInvalidInput
	}
	return s.repo.ListWorkflowDefinitions(ctx, actor, filter)
}

func (s *Engine) compileWorkflowDefinition(ctx context.Context, input WorkflowDefinitionRevisionInput) (model.WorkflowDefinition, int, error) {
	draft, err := s.prepareWorkflowDefinition(input)
	if err != nil {
		return model.WorkflowDefinition{}, 0, err
	}
	compiler := workflowDefinitionCompiler{
		service: s, ctx: ctx, actor: input.Actor, workflowID: draft.workflowID,
		nodeIDs: make(map[string]struct{}), dependencies: make(map[string]model.WorkflowDependency),
		maxNodes: s.workflowCeilings().MaxDefinitionNodes,
	}
	if err = compiler.compileRoot(&draft.root); err != nil {
		return model.WorkflowDefinition{}, 0, err
	}
	dependencies := compiler.sortedDependencies()
	dependencyHash, err := hashWorkflowValue(dependencies)
	if err != nil {
		return model.WorkflowDefinition{}, 0, err
	}
	item := model.WorkflowDefinition{
		WorkflowID: draft.workflowID, SchemaVersion: draft.schemaVersion, Scope: draft.scope,
		TenantID: draft.tenantID, OwnerActorID: draft.ownerActorID,
		Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description), Status: draft.status,
		InputSchema: json.RawMessage(draft.inputSchema), OutputSchema: json.RawMessage(draft.outputSchema),
		Root: draft.root, Limits: draft.limits, Dependencies: dependencies, DependencyHash: dependencyHash,
		CreatedBy: input.Actor, RequestID: strings.TrimSpace(input.RequestID), RevisionNote: strings.TrimSpace(input.RevisionNote),
	}
	if err = hashCompiledWorkflowDefinition(&item, input.ExpectedRevision); err != nil {
		return model.WorkflowDefinition{}, 0, err
	}
	return item, compiler.nodeCount, nil
}

type workflowDefinitionDraft struct {
	schemaVersion               int
	workflowID, scope, tenantID string
	ownerActorID, status        string
	inputSchema, outputSchema   string
	root                        model.WorkflowNode
	limits                      model.WorkflowLimits
}

func (s *Engine) prepareWorkflowDefinition(input WorkflowDefinitionRevisionInput) (workflowDefinitionDraft, error) {
	schemaVersion, err := normalizeWorkflowSchemaVersion(input.SchemaVersion)
	if err != nil {
		return workflowDefinitionDraft{}, err
	}
	workflowID := normalizeWorkflowID(input.WorkflowID)
	if workflowID == "" {
		workflowID = s.newRuntimeID("workflow")
	}
	scope, tenantID, ownerActorID, ok := normalizeWorkflowOwnership(input)
	if !ok {
		return workflowDefinitionDraft{}, ErrWorkflowDefinitionInvalid
	}
	status, err := normalizeWorkflowDefinitionStatus(input.Status)
	if err != nil {
		return workflowDefinitionDraft{}, err
	}
	contracts, err := s.prepareWorkflowDefinitionContracts(input)
	if err != nil {
		return workflowDefinitionDraft{}, err
	}
	return workflowDefinitionDraft{
		schemaVersion: schemaVersion, workflowID: workflowID, scope: scope, tenantID: tenantID,
		ownerActorID: ownerActorID, status: status, inputSchema: contracts.inputSchema, outputSchema: contracts.outputSchema,
		root: contracts.root, limits: contracts.limits,
	}, nil
}

func normalizeWorkflowSchemaVersion(value int) (int, error) {
	if value == 0 {
		value = WorkflowSchemaVersion
	}
	if value != WorkflowSchemaVersion {
		return 0, ErrWorkflowDefinitionInvalid
	}
	return value, nil
}

func normalizeWorkflowDefinitionStatus(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = model.WorkflowDefinitionStatusActive
	}
	if value != model.WorkflowDefinitionStatusActive && value != model.WorkflowDefinitionStatusDisabled {
		return "", ErrWorkflowDefinitionInvalid
	}
	return value, nil
}

type workflowDefinitionContracts struct {
	inputSchema, outputSchema string
	root                      model.WorkflowNode
	limits                    model.WorkflowLimits
}

func (s *Engine) prepareWorkflowDefinitionContracts(input WorkflowDefinitionRevisionInput) (workflowDefinitionContracts, error) {
	inputSchema, err := validateWorkflowSchema(input.InputSchema)
	if err != nil {
		return workflowDefinitionContracts{}, err
	}
	outputSchema, err := validateWorkflowSchema(input.OutputSchema)
	if err != nil {
		return workflowDefinitionContracts{}, err
	}
	limits, err := s.validateWorkflowLimits(input.Limits)
	if err != nil {
		return workflowDefinitionContracts{}, err
	}
	root, err := cloneWorkflowNode(input.Root)
	if err != nil {
		return workflowDefinitionContracts{}, errors.Join(ErrWorkflowDefinitionInvalid, err)
	}
	return workflowDefinitionContracts{inputSchema: inputSchema, outputSchema: outputSchema, root: root, limits: limits}, nil
}

func hashCompiledWorkflowDefinition(item *model.WorkflowDefinition, expectedRevision int) error {
	hashPayload := *item
	hashPayload.RequestID, hashPayload.RequestFingerprint, hashPayload.RevisionNote = "", "", ""
	var err error
	item.DefinitionHash, err = hashWorkflowValue(hashPayload)
	if err != nil {
		return err
	}
	item.RequestFingerprint, err = hashWorkflowValue(struct {
		Definition       model.WorkflowDefinition
		ExpectedRevision int
	}{*item, expectedRevision})
	return err
}

func normalizeWorkflowOwnership(input WorkflowDefinitionRevisionInput) (string, string, string, bool) {
	scope := strings.TrimSpace(input.Scope)
	if scope == "" {
		scope = model.WorkflowDefinitionScopeActor
	}
	tenantID, ownerActorID := strings.TrimSpace(input.TenantID), strings.TrimSpace(input.OwnerActorID)
	switch scope {
	case model.WorkflowDefinitionScopeActor:
		if tenantID == "" {
			tenantID = input.Actor.TenantID
		}
		if ownerActorID == "" {
			ownerActorID = input.Actor.ActorID
		}
		return scope, tenantID, ownerActorID, tenantID != "" && ownerActorID != ""
	case model.WorkflowDefinitionScopeTenant:
		if tenantID == "" {
			tenantID = input.Actor.TenantID
		}
		return scope, tenantID, "", tenantID != ""
	case model.WorkflowDefinitionScopeSystem:
		return scope, "", "", true
	default:
		return "", "", "", false
	}
}

func normalizeWorkflowID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if !strings.HasPrefix(value, "workflow_") {
		value = "workflow_" + value
	}
	runes := []rune(value)
	if len(runes) <= maxWorkflowDefinitionIDRunes {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	suffix := "_" + hex.EncodeToString(sum[:workflowDefinitionIDSuffixSize/2])
	return string(runes[:maxWorkflowDefinitionIDRunes-len(suffix)]) + suffix
}

func (s *Engine) workflowCeilings() WorkflowConfig {
	cfg := s.cfg.Snapshot().Workflow
	cfg.MaxNodeActivations = positiveWorkflowCeiling(cfg.MaxNodeActivations, 10000)
	cfg.MaxChildRuns = positiveWorkflowCeiling(cfg.MaxChildRuns, 128)
	cfg.MaxConcurrentRuns = positiveWorkflowCeiling(cfg.MaxConcurrentRuns, 16)
	cfg.MaxTotalLLMCalls = positiveWorkflowCeiling(cfg.MaxTotalLLMCalls, 256)
	cfg.MaxTotalToolCalls = positiveWorkflowCeiling(cfg.MaxTotalToolCalls, 1024)
	cfg.MaxDurationSeconds = positiveWorkflowCeiling(cfg.MaxDurationSeconds, 86400)
	cfg.MaxLoopIterations = positiveWorkflowCeiling(cfg.MaxLoopIterations, 10000)
	cfg.MaxNestedDepth = positiveWorkflowCeiling(cfg.MaxNestedDepth, 8)
	cfg.MaxStateBytes = positiveWorkflowCeiling(cfg.MaxStateBytes, 16*1024*1024)
	cfg.MaxExpressionDepth = positiveWorkflowCeiling(cfg.MaxExpressionDepth, 64)
	cfg.MaxExpressionOps = positiveWorkflowCeiling(cfg.MaxExpressionOps, 10000)
	cfg.MaxExpressionBytes = positiveWorkflowCeiling(cfg.MaxExpressionBytes, 1024*1024)
	cfg.MaxDefinitionNodes = positiveWorkflowCeiling(cfg.MaxDefinitionNodes, 5000)
	cfg.MaxCacheTTLSeconds = positiveWorkflowCeiling(cfg.MaxCacheTTLSeconds, 30*24*60*60)
	return cfg
}

func positiveWorkflowCeiling(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func (s *Engine) validateWorkflowLimits(limits model.WorkflowLimits) (model.WorkflowLimits, error) {
	ceiling := s.workflowCeilings()
	values := []struct {
		value, ceiling int
	}{
		{limits.MaxNodeActivations, ceiling.MaxNodeActivations},
		{limits.MaxChildRuns, ceiling.MaxChildRuns},
		{limits.MaxConcurrentRuns, ceiling.MaxConcurrentRuns},
		{limits.MaxTotalLLMCalls, ceiling.MaxTotalLLMCalls},
		{limits.MaxTotalToolCalls, ceiling.MaxTotalToolCalls},
		{limits.MaxDurationSeconds, ceiling.MaxDurationSeconds},
		{limits.MaxLoopIterations, ceiling.MaxLoopIterations},
		{limits.MaxNestedDepth, ceiling.MaxNestedDepth},
		{limits.MaxStateBytes, ceiling.MaxStateBytes},
	}
	for _, item := range values {
		if item.value <= 0 || item.value > item.ceiling {
			return model.WorkflowLimits{}, ErrWorkflowBudgetExceeded
		}
	}
	if limits.MaxConcurrentRuns > limits.MaxChildRuns {
		return model.WorkflowLimits{}, ErrWorkflowDefinitionInvalid
	}
	return limits, nil
}

type workflowDefinitionCompiler struct {
	service      *Engine
	ctx          context.Context
	actor        model.ActorRef
	workflowID   string
	nodeIDs      map[string]struct{}
	dependencies map[string]model.WorkflowDependency
	nodeCount    int
	returnCount  int
	maxNodes     int
}

type workflowCompileScope struct {
	availableNodes map[string]struct{}
	item           bool
	errorContext   bool
	compensation   bool
	undo           bool
}

type workflowNodeCompileHandler func(*workflowDefinitionCompiler, *model.WorkflowNode, workflowCompileScope) error

type workflowExpressionShapeValidator func(model.WorkflowExpr) error

func cloneWorkflowNode(input model.WorkflowNode) (model.WorkflowNode, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return model.WorkflowNode{}, err
	}
	var result model.WorkflowNode
	if err = json.Unmarshal(raw, &result); err != nil {
		return model.WorkflowNode{}, err
	}
	return result, nil
}

func hashWorkflowValue(value interface{}) (string, error) {
	canonical, err := canonicalWorkflowJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalWorkflowJSON(value interface{}) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded interface{}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err = decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}
