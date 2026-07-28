package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const WorkflowSchemaVersion = 1

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
	if value == "" || strings.HasPrefix(value, "workflow_") {
		return value
	}
	return "workflow_" + value
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

func (c *workflowDefinitionCompiler) compileRoot(root *model.WorkflowNode) error {
	if root == nil || root.Type != model.WorkflowNodeSequence || len(root.Children) == 0 ||
		root.Children[len(root.Children)-1].Type != model.WorkflowNodeReturn {
		return fmt.Errorf("%w: root must be a sequence ending in return", ErrWorkflowDefinitionInvalid)
	}
	if err := c.compileNode(root, workflowCompileScope{availableNodes: make(map[string]struct{})}); err != nil {
		return err
	}
	if c.returnCount != 1 {
		return fmt.Errorf("%w: exactly one return is required", ErrWorkflowDefinitionInvalid)
	}
	return nil
}

type workflowCompileScope struct {
	availableNodes map[string]struct{}
	item           bool
	errorContext   bool
	compensation   bool
	undo           bool
}

func (c *workflowDefinitionCompiler) compileNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	if err := c.compileNodeHeader(node); err != nil {
		return err
	}
	if err := c.compileNodeExpressions(*node, scope); err != nil {
		return err
	}
	if err := c.compileNodeStructure(node, scope); err != nil {
		return err
	}
	if node.Cache != nil {
		return c.validateNodeCache(*node)
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileNodeHeader(node *model.WorkflowNode) error {
	if node == nil || strings.TrimSpace(node.ID) == "" || strings.TrimSpace(node.Type) == "" {
		return ErrWorkflowDefinitionInvalid
	}
	node.ID = strings.TrimSpace(node.ID)
	if _, duplicate := c.nodeIDs[node.ID]; duplicate {
		return fmt.Errorf("%w: duplicate node id %s", ErrWorkflowDefinitionInvalid, node.ID)
	}
	c.nodeIDs[node.ID] = struct{}{}
	c.nodeCount++
	if c.nodeCount > c.maxNodes {
		return ErrWorkflowBudgetExceeded
	}
	return validateWorkflowNodeShape(*node)
}

type workflowNodeCompileHandler func(*workflowDefinitionCompiler, *model.WorkflowNode, workflowCompileScope) error

func (c *workflowDefinitionCompiler) compileNodeStructure(node *model.WorkflowNode, scope workflowCompileScope) error {
	handlers := map[string]workflowNodeCompileHandler{
		model.WorkflowNodeSequence:   (*workflowDefinitionCompiler).compileSequenceNode,
		model.WorkflowNodeAgent:      (*workflowDefinitionCompiler).compileAgentNode,
		model.WorkflowNodeTool:       (*workflowDefinitionCompiler).compileToolNode,
		model.WorkflowNodeWorkflow:   (*workflowDefinitionCompiler).compileNestedWorkflowNode,
		model.WorkflowNodeParallel:   (*workflowDefinitionCompiler).compileParallelNode,
		model.WorkflowNodeForEach:    (*workflowDefinitionCompiler).compileForEachNode,
		model.WorkflowNodePipeline:   (*workflowDefinitionCompiler).compilePipelineNode,
		model.WorkflowNodeIf:         (*workflowDefinitionCompiler).compileIfNode,
		model.WorkflowNodeLoop:       (*workflowDefinitionCompiler).compileLoopNode,
		model.WorkflowNodeCompensate: (*workflowDefinitionCompiler).compileCompensationNode,
		model.WorkflowNodeReturn:     (*workflowDefinitionCompiler).compileReturnNode,
	}
	handler, ok := handlers[node.Type]
	if !ok {
		return nil
	}
	return handler(c, node, scope)
}

func (c *workflowDefinitionCompiler) compileSequenceNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	childScope := cloneWorkflowCompileScope(scope)
	for index := range node.Children {
		if err := c.compileNode(&node.Children[index], childScope); err != nil {
			return err
		}
		childScope.availableNodes[node.Children[index].ID] = struct{}{}
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileAgentNode(node *model.WorkflowNode, _ workflowCompileScope) error {
	return c.freezeAgentDependency(node)
}

func (c *workflowDefinitionCompiler) compileToolNode(node *model.WorkflowNode, _ workflowCompileScope) error {
	return c.freezeToolDependency(node)
}

func (c *workflowDefinitionCompiler) compileNestedWorkflowNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	definition, err := c.freezeWorkflowDependency(node)
	if err != nil {
		return err
	}
	if !scope.undo {
		return nil
	}
	return c.validateCompensationWorkflow(definition, make(map[string]struct{}))
}

func (c *workflowDefinitionCompiler) compileParallelNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	for index := range node.Branches {
		if err := c.compileNode(&node.Branches[index], cloneWorkflowCompileScope(scope)); err != nil {
			return err
		}
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileForEachNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	childScope := cloneWorkflowCompileScope(scope)
	childScope.item = true
	return c.compileNode(node.Body, childScope)
}

func (c *workflowDefinitionCompiler) compilePipelineNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	childScope := cloneWorkflowCompileScope(scope)
	childScope.item = true
	for index := range node.Stages {
		if err := c.compileNode(&node.Stages[index], childScope); err != nil {
			return err
		}
		childScope.availableNodes[node.Stages[index].ID] = struct{}{}
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileIfNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	if err := c.compileNode(node.Then, cloneWorkflowCompileScope(scope)); err != nil {
		return err
	}
	if node.Else == nil {
		return nil
	}
	return c.compileNode(node.Else, cloneWorkflowCompileScope(scope))
}

func (c *workflowDefinitionCompiler) compileLoopNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	if node.MaxIterations <= 0 {
		return ErrWorkflowDefinitionInvalid
	}
	return c.compileNode(node.Body, cloneWorkflowCompileScope(scope))
}

func (c *workflowDefinitionCompiler) compileCompensationNode(node *model.WorkflowNode, scope workflowCompileScope) error {
	if scope.undo {
		return fmt.Errorf("%w: nested compensation", ErrWorkflowDefinitionInvalid)
	}
	if err := c.compileNode(node.Do, cloneWorkflowCompileScope(scope)); err != nil {
		return err
	}
	undoScope := cloneWorkflowCompileScope(scope)
	undoScope.undo, undoScope.errorContext, undoScope.compensation = true, true, true
	if err := c.compileNode(node.Undo, undoScope); err != nil {
		return err
	}
	if !workflowUndoNodeAllowed(*node.Undo) {
		return fmt.Errorf("%w: illegal compensation node", ErrWorkflowDefinitionInvalid)
	}
	return nil
}

func (c *workflowDefinitionCompiler) compileReturnNode(_ *model.WorkflowNode, scope workflowCompileScope) error {
	c.returnCount++
	if scope.undo {
		return fmt.Errorf("%w: return in compensation", ErrWorkflowDefinitionInvalid)
	}
	return nil
}

func cloneWorkflowCompileScope(scope workflowCompileScope) workflowCompileScope {
	result := scope
	result.availableNodes = make(map[string]struct{}, len(scope.availableNodes))
	for key := range scope.availableNodes {
		result.availableNodes[key] = struct{}{}
	}
	return result
}

func validateWorkflowNodeShape(node model.WorkflowNode) error {
	raw, err := json.Marshal(node)
	if err != nil {
		return ErrWorkflowDefinitionInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrWorkflowDefinitionInvalid
	}
	// encoding/json does not apply omitempty to zero-valued struct fields.
	// Remove the two union references only when they are genuinely unset so
	// valid nodes do not appear to contain fields from another node variant.
	if node.ManifestRef == (model.ResourceRef{}) {
		delete(fields, "manifestRef")
	}
	if node.DefinitionRef == (model.ResourceRef{}) {
		delete(fields, "definitionRef")
	}
	allowed := map[string]map[string]bool{
		model.WorkflowNodeSequence:    workflowAllowedFields("children"),
		model.WorkflowNodeAgent:       workflowAllowedFields("manifestRef", "goal", "outputSchema", "resultAttempts", "perNodeLimits", "cache"),
		model.WorkflowNodeParallel:    workflowAllowedFields("branches", "failurePolicy"),
		model.WorkflowNodeForEach:     workflowAllowedFields("items", "body", "maxConcurrency", "failurePolicy"),
		model.WorkflowNodePipeline:    workflowAllowedFields("items", "stages", "maxConcurrency", "failurePolicy"),
		model.WorkflowNodeIf:          workflowAllowedFields("condition", "then", "else"),
		model.WorkflowNodeLoop:        workflowAllowedFields("condition", "body", "maxIterations"),
		model.WorkflowNodeSet:         workflowAllowedFields("assignments"),
		model.WorkflowNodeLog:         workflowAllowedFields("level", "message", "data"),
		model.WorkflowNodeTool:        workflowAllowedFields("toolKey", "arguments", "cache"),
		model.WorkflowNodeWorkflow:    workflowAllowedFields("definitionRef", "input", "cache"),
		model.WorkflowNodeInteraction: workflowAllowedFields("title", "prompt", "schema", "expiresAfterSeconds"),
		model.WorkflowNodeTimer:       workflowAllowedFields("delaySeconds", "wakeAt"),
		model.WorkflowNodeCompensate:  workflowAllowedFields("do", "undo"),
		model.WorkflowNodeReturn:      workflowAllowedFields("value", "presentation"),
	}
	selected, ok := allowed[node.Type]
	if !ok {
		return fmt.Errorf("%w: unknown node type %s", ErrWorkflowDefinitionInvalid, node.Type)
	}
	for field := range fields {
		if !selected[field] {
			return fmt.Errorf("%w: field %s is not legal for %s", ErrWorkflowDefinitionInvalid, field, node.Type)
		}
	}
	return validateWorkflowNodeRequired(node)
}

func workflowAllowedFields(values ...string) map[string]bool {
	result := map[string]bool{"id": true, workflowPayloadType: true}
	for _, value := range values {
		result[value] = true
	}
	return result
}

func validateWorkflowNodeRequired(node model.WorkflowNode) error {
	validators := map[string]func(model.WorkflowNode) error{
		model.WorkflowNodeSequence:    validateWorkflowSequenceNode,
		model.WorkflowNodeAgent:       validateWorkflowAgentNode,
		model.WorkflowNodeParallel:    validateWorkflowParallelNode,
		model.WorkflowNodeForEach:     validateWorkflowForEachNode,
		model.WorkflowNodePipeline:    validateWorkflowPipelineNode,
		model.WorkflowNodeIf:          validateWorkflowIfNode,
		model.WorkflowNodeLoop:        validateWorkflowLoopNode,
		model.WorkflowNodeSet:         validateWorkflowSetNode,
		model.WorkflowNodeLog:         validateWorkflowLogNode,
		model.WorkflowNodeTool:        validateWorkflowToolNode,
		model.WorkflowNodeWorkflow:    validateWorkflowNestedNode,
		model.WorkflowNodeInteraction: validateWorkflowInteractionNode,
		model.WorkflowNodeTimer:       validateWorkflowTimerNode,
		model.WorkflowNodeCompensate:  validateWorkflowCompensateNode,
		model.WorkflowNodeReturn:      validateWorkflowReturnNode,
	}
	validator, ok := validators[node.Type]
	if !ok {
		return ErrWorkflowDefinitionInvalid
	}
	return validator(node)
}

func invalidWorkflowNodeIf(invalid bool) error {
	if invalid {
		return ErrWorkflowDefinitionInvalid
	}
	return nil
}

func validWorkflowFailurePolicy(value string) bool {
	return value == model.WorkflowFailureCollect || value == model.WorkflowFailureFailFast
}

func validateWorkflowSequenceNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(len(node.Children) == 0)
}

func validateWorkflowAgentNode(node model.WorkflowNode) error {
	if err := invalidWorkflowNodeIf(node.ManifestRef.Kind != model.AgentManifestKind || node.ManifestRef.ID == "" ||
		node.Goal == nil || len(node.OutputSchema) == 0 || node.ResultAttempts < 0 || node.ResultAttempts > 2); err != nil {
		return err
	}
	_, err := validateWorkflowSchema(node.OutputSchema)
	return err
}

func validateWorkflowParallelNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(len(node.Branches) == 0 || !validWorkflowFailurePolicy(node.FailurePolicy))
}

func validateWorkflowForEachNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.ItemsExpr == nil || node.Body == nil || node.MaxConcurrency <= 0 || !validWorkflowFailurePolicy(node.FailurePolicy))
}

func validateWorkflowPipelineNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.ItemsExpr == nil || len(node.Stages) == 0 || node.MaxConcurrency <= 0 || !validWorkflowFailurePolicy(node.FailurePolicy))
}

func validateWorkflowIfNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.Condition == nil || node.Then == nil)
}

func validateWorkflowLoopNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.Condition == nil || node.Body == nil || node.MaxIterations <= 0)
}

func validateWorkflowSetNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(len(node.Assignments) == 0)
}

func validateWorkflowLogNode(node model.WorkflowNode) error {
	levels := map[string]struct{}{
		model.WorkflowLogLevelDebug: {}, model.WorkflowLogLevelInfo: {},
		model.WorkflowLogLevelWarn: {}, model.WorkflowLogLevelError: {},
	}
	_, validLevel := levels[node.Level]
	return invalidWorkflowNodeIf(node.Message == nil || !validLevel)
}

func validateWorkflowToolNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(strings.TrimSpace(node.ToolKey) == "" || node.Arguments == nil)
}

func validateWorkflowNestedNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.DefinitionRef.Kind != model.WorkflowDefinitionKind || node.DefinitionRef.ID == "" || node.Input == nil)
}

func validateWorkflowInteractionNode(node model.WorkflowNode) error {
	if err := invalidWorkflowNodeIf(strings.TrimSpace(node.Title) == "" || strings.TrimSpace(node.Prompt) == "" ||
		len(node.Schema) == 0 || node.ExpiresAfterSeconds <= 0); err != nil {
		return err
	}
	_, err := validateWorkflowSchema(node.Schema)
	return err
}

func validateWorkflowTimerNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf((node.DelaySeconds == nil) == (node.WakeAt == nil))
}

func validateWorkflowCompensateNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.Do == nil || node.Undo == nil)
}

func validateWorkflowReturnNode(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(node.Value == nil)
}

func (c *workflowDefinitionCompiler) compileNodeExpressions(node model.WorkflowNode, scope workflowCompileScope) error {
	expressions := []*model.WorkflowExpr{node.Goal, node.ItemsExpr, node.Condition, node.Arguments, node.Input, node.Message, node.Data, node.DelaySeconds, node.WakeAt, node.Value, node.Presentation}
	for _, expression := range expressions {
		if expression != nil {
			if err := c.validateExpression(*expression, scope, 1, new(int)); err != nil {
				return err
			}
		}
	}
	for _, expression := range node.Assignments {
		if err := c.validateExpression(expression, scope, 1, new(int)); err != nil {
			return err
		}
	}
	return nil
}

func (c *workflowDefinitionCompiler) validateExpression(expr model.WorkflowExpr, scope workflowCompileScope, depth int, operations *int) error {
	ceiling := c.service.workflowCeilings()
	if depth > ceiling.MaxExpressionDepth {
		return ErrWorkflowExpressionLimit
	}
	*operations++
	if *operations > ceiling.MaxExpressionOps {
		return ErrWorkflowExpressionLimit
	}
	if err := validateWorkflowExprShape(expr); err != nil {
		return err
	}
	if expr.Op == model.WorkflowExprOpRef {
		return validateWorkflowReference(expr.Ref, scope)
	}
	for _, child := range workflowExpressionChildren(expr) {
		if err := c.validateExpression(child, scope, depth+1, operations); err != nil {
			return err
		}
	}
	return nil
}

func workflowExpressionChildren(expr model.WorkflowExpr) []model.WorkflowExpr {
	children := make([]model.WorkflowExpr, 0, len(expr.Fields)+len(expr.Items)+len(expr.Args))
	for _, child := range expr.Fields {
		children = append(children, child)
	}
	children = append(children, expr.Items...)
	children = append(children, expr.Args...)
	return children
}

type workflowExpressionShapeValidator func(model.WorkflowExpr) error

func validateWorkflowExprShape(expr model.WorkflowExpr) error {
	if err := validateWorkflowExpressionFields(expr); err != nil {
		return err
	}
	validators := map[string]workflowExpressionShapeValidator{
		model.WorkflowExprOpLiteral:  validateWorkflowLiteralExpression,
		model.WorkflowExprOpRef:      validateWorkflowReferenceExpression,
		model.WorkflowExprOpObject:   validateWorkflowObjectExpression,
		model.WorkflowExprOpArray:    validateWorkflowArrayExpression,
		model.WorkflowExprOpNot:      validateWorkflowUnaryExpression,
		model.WorkflowExprOpLength:   validateWorkflowUnaryExpression,
		model.WorkflowExprOpEq:       validateWorkflowBinaryExpression,
		model.WorkflowExprOpNe:       validateWorkflowBinaryExpression,
		model.WorkflowExprOpLt:       validateWorkflowBinaryExpression,
		model.WorkflowExprOpLte:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpGt:       validateWorkflowBinaryExpression,
		model.WorkflowExprOpGte:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpAppend:   validateWorkflowBinaryExpression,
		model.WorkflowExprOpContains: validateWorkflowBinaryExpression,
		model.WorkflowExprOpAdd:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpSub:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpMul:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpDiv:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpMod:      validateWorkflowBinaryExpression,
		model.WorkflowExprOpAnd:      validateWorkflowNonEmptyArgsExpression,
		model.WorkflowExprOpOr:       validateWorkflowNonEmptyArgsExpression,
		model.WorkflowExprOpCoalesce: validateWorkflowNonEmptyArgsExpression,
		model.WorkflowExprOpConcat:   validateWorkflowNonEmptyArgsExpression,
		model.WorkflowExprOpMerge:    validateWorkflowMergeExpression,
	}
	validator, ok := validators[expr.Op]
	if !ok {
		return fmt.Errorf("%w: unknown operator %s", ErrWorkflowExpressionInvalid, expr.Op)
	}
	return validator(expr)
}

func validateWorkflowExpressionFields(expr model.WorkflowExpr) error {
	raw, err := json.Marshal(expr)
	if err != nil {
		return ErrWorkflowExpressionInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrWorkflowExpressionInvalid
	}
	allowed := map[string]bool{"op": true, workflowExpressionPayloadField(expr.Op): true}
	for field := range fields {
		if !allowed[field] {
			return fmt.Errorf("%w: field %s is not legal for %s", ErrWorkflowExpressionInvalid, field, expr.Op)
		}
	}
	return nil
}

func workflowExpressionPayloadField(operation string) string {
	switch operation {
	case model.WorkflowExprOpLiteral:
		return workflowPayloadValue
	case model.WorkflowExprOpRef:
		return "ref"
	case model.WorkflowExprOpObject:
		return "fields"
	case model.WorkflowExprOpArray:
		return workflowPayloadItems
	default:
		return "args"
	}
}

func validateWorkflowLiteralExpression(expr model.WorkflowExpr) error {
	if len(expr.Value) == 0 {
		return ErrWorkflowExpressionInvalid
	}
	_, err := decodeWorkflowJSON(expr.Value)
	return err
}

func validateWorkflowReferenceExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(strings.TrimSpace(expr.Ref) == "")
}

func validateWorkflowObjectExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(expr.Fields == nil)
}

func validateWorkflowArrayExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(expr.Items == nil)
}

func validateWorkflowUnaryExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(len(expr.Args) != 1)
}

func validateWorkflowBinaryExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(len(expr.Args) != 2)
}

func validateWorkflowNonEmptyArgsExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(len(expr.Args) == 0)
}

func validateWorkflowMergeExpression(expr model.WorkflowExpr) error {
	return invalidWorkflowExpressionIf(len(expr.Args) < 2)
}

func invalidWorkflowExpressionIf(invalid bool) error {
	if invalid {
		return ErrWorkflowExpressionInvalid
	}
	return nil
}

func validateWorkflowReference(value string, scope workflowCompileScope) error {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) == 0 {
		return ErrWorkflowExpressionInvalid
	}
	switch parts[0] {
	case model.WorkflowExprRefInput, model.WorkflowExprRefVars:
		return nil
	case model.WorkflowExprRefSteps:
		return validateWorkflowStepReference(parts, scope)
	case model.WorkflowExprRefItem, model.WorkflowExprRefIndex:
		return requireWorkflowReferenceScope(scope.item)
	case model.WorkflowExprRefError:
		return requireWorkflowReferenceScope(scope.errorContext)
	case model.WorkflowExprRefCompensation:
		return requireWorkflowReferenceScope(scope.compensation)
	default:
		return ErrWorkflowExpressionInvalid
	}
}

func validateWorkflowStepReference(parts []string, scope workflowCompileScope) error {
	if len(parts) < 2 {
		return ErrWorkflowExpressionInvalid
	}
	if _, ok := scope.availableNodes[parts[1]]; !ok {
		return fmt.Errorf("%w: node %s is not upstream", ErrWorkflowExpressionInvalid, parts[1])
	}
	return nil
}

func requireWorkflowReferenceScope(available bool) error {
	if !available {
		return ErrWorkflowExpressionInvalid
	}
	return nil
}

func (c *workflowDefinitionCompiler) freezeAgentDependency(node *model.WorkflowNode) error {
	manifest, err := c.service.repo.GetAgentManifest(c.ctx, c.actor, node.ManifestRef)
	if err != nil {
		return errors.Join(ErrWorkflowDependencyMissing, err)
	}
	if manifest.Status != model.AgentManifestStatusActive {
		return ErrAgentManifestDisabled
	}
	node.ManifestRef = manifest.Ref()
	fingerprint, err := hashWorkflowValue(manifest)
	if err != nil {
		return err
	}
	c.addDependency(model.WorkflowDependency{Kind: model.WorkflowDependencyAgent, Ref: manifest.Ref(), Fingerprint: fingerprint})
	return nil
}

func (c *workflowDefinitionCompiler) freezeWorkflowDependency(node *model.WorkflowNode) (*model.WorkflowDefinition, error) {
	definition, err := c.service.repo.GetWorkflowDefinition(c.ctx, c.actor, node.DefinitionRef)
	if err != nil {
		return nil, errors.Join(ErrWorkflowDependencyMissing, err)
	}
	if definition.Status != model.WorkflowDefinitionStatusActive {
		return nil, ErrWorkflowDefinitionDisabled
	}
	if definition.WorkflowID == c.workflowID {
		return nil, ErrWorkflowDependencyCycle
	}
	if err = c.ensureNoDependencyCycle(definition, map[string]struct{}{c.workflowID: {}}); err != nil {
		return nil, err
	}
	node.DefinitionRef = definition.Ref()
	c.addDependency(model.WorkflowDependency{Kind: model.WorkflowDependencyWorkflow, Ref: definition.Ref(), Fingerprint: definition.DefinitionHash})
	return definition, nil
}

func (c *workflowDefinitionCompiler) ensureNoDependencyCycle(definition *model.WorkflowDefinition, path map[string]struct{}) error {
	if definition == nil {
		return ErrWorkflowDependencyMissing
	}
	if _, exists := path[definition.WorkflowID]; exists {
		return ErrWorkflowDependencyCycle
	}
	next := make(map[string]struct{}, len(path)+1)
	for key := range path {
		next[key] = struct{}{}
	}
	next[definition.WorkflowID] = struct{}{}
	for _, dependency := range definition.Dependencies {
		if dependency.Kind != model.WorkflowDependencyWorkflow {
			continue
		}
		child, err := c.service.repo.GetWorkflowDefinition(c.ctx, c.actor, dependency.Ref)
		if err != nil {
			return errors.Join(ErrWorkflowDependencyMissing, err)
		}
		if err = c.ensureNoDependencyCycle(child, next); err != nil {
			return err
		}
	}
	return nil
}

func (c *workflowDefinitionCompiler) freezeToolDependency(node *model.WorkflowNode) error {
	if c.service.toolCatalog == nil {
		return ErrWorkflowDependencyMissing
	}
	resolved, unavailable, err := c.service.toolCatalog.ResolveAvailable(c.ctx, c.actor, []string{node.ToolKey}, "", "", "")
	if err != nil {
		return errors.Join(ErrWorkflowDependencyMissing, err)
	}
	if !validResolvedWorkflowTool(node.ToolKey, resolved, unavailable) {
		return errors.Join(ErrWorkflowDependencyMissing, ErrRunToolUnavailable)
	}
	tool := resolved[0]
	if !workflowToolReceiptContractValid(tool) {
		return errors.Join(ErrWorkflowDefinitionInvalid, ErrRunToolProviderReceiptRequired)
	}
	fingerprint, err := hashWorkflowValue(tool)
	if err != nil {
		return err
	}
	c.addDependency(model.WorkflowDependency{
		Kind: model.WorkflowDependencyTool, ToolKey: tool.ToolKey, DefinitionVersion: tool.DefinitionVersion,
		Fingerprint: fingerprint, SideEffectLevel: tool.SideEffectLevel,
	})
	return nil
}

func validResolvedWorkflowTool(toolKey string, resolved []ResolvedTool, unavailable []string) bool {
	if len(unavailable) != 0 || len(resolved) != 1 {
		return false
	}
	return resolved[0].ToolKey == toolKey && strings.TrimSpace(resolved[0].DefinitionVersion) != ""
}

func workflowToolReceiptContractValid(tool ResolvedTool) bool {
	if !toolRequiresProviderReceipt(tool.SideEffectLevel) {
		return true
	}
	return normalizeToolIdempotencyMode(tool.IdempotencyMode) == ToolIdempotencyProviderReceipt
}

func (c *workflowDefinitionCompiler) addDependency(dependency model.WorkflowDependency) {
	key := dependency.Kind + "\x00" + dependency.Ref.ID + "\x00" + dependency.Ref.Revision + "\x00" + dependency.ToolKey + "\x00" + dependency.DefinitionVersion
	c.dependencies[key] = dependency
}

func (c *workflowDefinitionCompiler) sortedDependencies() []model.WorkflowDependency {
	keys := make([]string, 0, len(c.dependencies))
	for key := range c.dependencies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]model.WorkflowDependency, 0, len(keys))
	for _, key := range keys {
		result = append(result, c.dependencies[key])
	}
	return result
}

func (c *workflowDefinitionCompiler) validateNodeCache(node model.WorkflowNode) error {
	if node.Cache == nil || !node.Cache.Enabled {
		return nil
	}
	if node.Cache.TTLSeconds <= 0 || node.Cache.TTLSeconds > c.service.workflowCeilings().MaxCacheTTLSeconds {
		return ErrWorkflowDefinitionInvalid
	}
	validators := map[string]func(model.WorkflowNode) error{
		model.WorkflowNodeAgent:    c.validateAgentNodeCache,
		model.WorkflowNodeTool:     c.validateToolNodeCache,
		model.WorkflowNodeWorkflow: c.validateNestedWorkflowNodeCache,
	}
	validator, ok := validators[node.Type]
	if !ok {
		return ErrWorkflowDefinitionInvalid
	}
	return validator(node)
}

func (c *workflowDefinitionCompiler) validateAgentNodeCache(node model.WorkflowNode) error {
	return invalidWorkflowNodeIf(len(node.OutputSchema) == 0)
}

func (c *workflowDefinitionCompiler) validateToolNodeCache(node model.WorkflowNode) error {
	for _, dependency := range c.dependencies {
		if dependency.Kind == model.WorkflowDependencyTool && dependency.ToolKey == node.ToolKey && dependency.SideEffectLevel == ToolSideEffectRead {
			return nil
		}
	}
	return ErrWorkflowDefinitionInvalid
}

func (c *workflowDefinitionCompiler) validateNestedWorkflowNodeCache(node model.WorkflowNode) error {
	definition, err := c.service.repo.GetWorkflowDefinition(c.ctx, c.actor, node.DefinitionRef)
	if err != nil {
		return ErrWorkflowDefinitionInvalid
	}
	return invalidWorkflowNodeIf(!workflowDefinitionStaticallyPure(*definition))
}

func workflowDefinitionStaticallyPure(definition model.WorkflowDefinition) bool {
	for _, dependency := range definition.Dependencies {
		if dependency.Kind == model.WorkflowDependencyAgent || dependency.Kind == model.WorkflowDependencyTool && dependency.SideEffectLevel != ToolSideEffectRead {
			return false
		}
	}
	return workflowNodeStaticallyNonWaiting(definition.Root)
}

func workflowNodeStaticallyNonWaiting(node model.WorkflowNode) bool {
	switch node.Type {
	case model.WorkflowNodeInteraction, model.WorkflowNodeTimer, model.WorkflowNodeAgent:
		return false
	}
	for _, children := range [][]model.WorkflowNode{node.Children, node.Branches, node.Stages} {
		for _, child := range children {
			if !workflowNodeStaticallyNonWaiting(child) {
				return false
			}
		}
	}
	for _, child := range []*model.WorkflowNode{node.Body, node.Then, node.Else, node.Do, node.Undo} {
		if child != nil && !workflowNodeStaticallyNonWaiting(*child) {
			return false
		}
	}
	return true
}

func workflowUndoNodeAllowed(node model.WorkflowNode) bool {
	switch node.Type {
	case model.WorkflowNodeSequence:
		for _, child := range node.Children {
			if !workflowUndoNodeAllowed(child) {
				return false
			}
		}
		return true
	case model.WorkflowNodeIf:
		return node.Then != nil && workflowUndoNodeAllowed(*node.Then) && (node.Else == nil || workflowUndoNodeAllowed(*node.Else))
	case model.WorkflowNodeSet, model.WorkflowNodeLog, model.WorkflowNodeTool, model.WorkflowNodeWorkflow:
		return true
	default:
		return false
	}
}

func (c *workflowDefinitionCompiler) validateCompensationWorkflow(
	definition *model.WorkflowDefinition,
	path map[string]struct{},
) error {
	if definition == nil {
		return ErrWorkflowDependencyMissing
	}
	if _, exists := path[definition.WorkflowID]; exists {
		return ErrWorkflowDependencyCycle
	}
	next := make(map[string]struct{}, len(path)+1)
	for key := range path {
		next[key] = struct{}{}
	}
	next[definition.WorkflowID] = struct{}{}
	return c.validateCompensationWorkflowRoot(definition.Root, next)
}

func (c *workflowDefinitionCompiler) validateCompensationWorkflowRoot(
	root model.WorkflowNode,
	path map[string]struct{},
) error {
	if root.Type != model.WorkflowNodeSequence || len(root.Children) == 0 {
		return fmt.Errorf("%w: nested compensation workflow must have a sequence root", ErrWorkflowDefinitionInvalid)
	}
	for index := range root.Children {
		allowReturn := index == len(root.Children)-1
		if err := c.validateCompensationWorkflowNode(root.Children[index], path, allowReturn); err != nil {
			return err
		}
	}
	return nil
}

func (c *workflowDefinitionCompiler) validateCompensationWorkflowNode(
	node model.WorkflowNode,
	path map[string]struct{},
	allowReturn bool,
) error {
	switch node.Type {
	case model.WorkflowNodeSequence:
		return c.validateCompensationWorkflowSequence(node.Children, path)
	case model.WorkflowNodeIf:
		return c.validateCompensationWorkflowIf(node, path)
	case model.WorkflowNodeSet, model.WorkflowNodeLog, model.WorkflowNodeTool:
		return nil
	case model.WorkflowNodeWorkflow:
		return c.validateNestedCompensationWorkflow(node.DefinitionRef, path)
	case model.WorkflowNodeReturn:
		if allowReturn {
			return nil
		}
	}
	return fmt.Errorf("%w: unsafe nested compensation workflow node %s", ErrWorkflowDefinitionInvalid, node.Type)
}

func (c *workflowDefinitionCompiler) validateCompensationWorkflowSequence(
	children []model.WorkflowNode,
	path map[string]struct{},
) error {
	for index := range children {
		if err := c.validateCompensationWorkflowNode(children[index], path, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *workflowDefinitionCompiler) validateCompensationWorkflowIf(
	node model.WorkflowNode,
	path map[string]struct{},
) error {
	if node.Then == nil {
		return ErrWorkflowDefinitionInvalid
	}
	if err := c.validateCompensationWorkflowNode(*node.Then, path, false); err != nil {
		return err
	}
	if node.Else == nil {
		return nil
	}
	return c.validateCompensationWorkflowNode(*node.Else, path, false)
}

func (c *workflowDefinitionCompiler) validateNestedCompensationWorkflow(
	ref model.ResourceRef,
	path map[string]struct{},
) error {
	definition, err := c.service.repo.GetWorkflowDefinition(c.ctx, c.actor, ref)
	if err != nil {
		return errors.Join(ErrWorkflowDependencyMissing, err)
	}
	return c.validateCompensationWorkflow(definition, path)
}

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
