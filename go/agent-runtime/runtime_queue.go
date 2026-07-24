package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const valueDefaultA60C25E3 = "default"

type RunQueueInput struct {
	Content             string   `json:"content"`
	ContentType         string   `json:"contentType"`
	FileIDs             []string `json:"fileIDs,omitempty"`
	OutputIDs           []string `json:"outputIDs,omitempty"`
	EvidenceIDs         []string `json:"evidenceIDs,omitempty"`
	HTMLVisualPrompt    bool     `json:"htmlVisualPrompt,omitempty"`
	HTMLVisualColorMode string   `json:"htmlVisualColorMode,omitempty"`
}

// RunQueueRequest is the immutable runtime request stored while a turn waits to
// be dispatched. It deliberately contains only public runtime references: host
// database keys and host message records never cross this boundary.
type RunQueueRequest struct {
	SemanticVersion   int                    `json:"semanticVersion"`
	Input             RunQueueInput          `json:"input"`
	Environment       model.ResourceRef      `json:"environment"`
	Model             string                 `json:"model,omitempty"`
	ExecutionMode     string                 `json:"executionMode,omitempty"`
	ResolvedStrategy  string                 `json:"resolvedStrategy"`
	StrategyReason    string                 `json:"strategyReason"`
	RequestedMode     string                 `json:"requestedMode"`
	Options           map[string]interface{} `json:"options,omitempty"`
	ToolKeys          *[]string              `json:"toolKeys,omitempty"`
	SkillRefs         *[]model.ResourceRef   `json:"skillRefs,omitempty"`
	ParentProjection  *model.ProjectionRef   `json:"parentProjection,omitempty"`
	SourceProjection  *model.ProjectionRef   `json:"sourceProjection,omitempty"`
	BranchReason      string                 `json:"branchReason,omitempty"`
	Workspace         *WorkspaceRequest      `json:"workspace,omitempty"`
	WorkspaceType     string                 `json:"workspaceType,omitempty"`
	WorkspaceMode     string                 `json:"workspaceMode,omitempty"`
	ToolFingerprints  map[string]string      `json:"toolFingerprints,omitempty"`
	SkillFingerprints map[string]string      `json:"skillFingerprints,omitempty"`

	ThreadModel    string `json:"threadModel,omitempty"`
	ThreadProvider string `json:"threadProvider,omitempty"`
	ThreadScope    string `json:"threadScope,omitempty"`
}

type EnqueueRunInput struct {
	Actor         model.ActorRef
	Thread        model.ThreadRef
	ClientQueueID string
	Request       RunQueueRequest
}

func (s *Engine) EnqueueRun(ctx context.Context, input EnqueueRunInput) (*model.QueueItem, bool, error) {
	if !validRunQueueInput(input) {
		return nil, false, ErrInvalidInput
	}
	thread, err := s.ResolveThread(ctx, input.Actor, input.Thread)
	if err != nil {
		return nil, false, err
	}
	request, err := s.freezeRunQueueRequest(ctx, input.Actor, thread, normalizeRunQueueRequest(input.Request))
	if err != nil {
		return nil, false, err
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, false, ErrInvalidInput
	}
	anchorRunID, err := s.runQueueAnchor(ctx, input.Actor, input.Thread)
	if err != nil {
		return nil, false, err
	}
	item := &model.QueueItem{
		QueueID:            "queue_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		ClientQueueID:      strings.TrimSpace(input.ClientQueueID),
		RequestFingerprint: hashRunQueueRequest(input.Actor, input.Thread, requestJSON),
		Actor:              input.Actor,
		Thread:             input.Thread,
		Status:             model.QueueQueued,
		RequestJSON:        string(requestJSON),
		AnchorRunID:        anchorRunID,
	}
	if request.ParentProjection != nil {
		item.AnchorProjection = *request.ParentProjection
	}
	saved, reused, err := s.repo.CreateRunQueueItem(ctx, item)
	if err != nil {
		return nil, false, err
	}
	if reused && saved.RequestFingerprint != item.RequestFingerprint {
		return nil, false, ErrRunQueueConflict
	}
	s.wakeRunQueue()
	return saved, reused, nil
}

func validRunQueueInput(input EnqueueRunInput) bool {
	clientQueueID := strings.TrimSpace(input.ClientQueueID)
	return validActorRef(input.Actor) && strings.TrimSpace(input.Thread.ID) != "" && clientQueueID != "" && len(clientQueueID) <= 64 && strings.TrimSpace(input.Request.Input.Content) != ""
}

func (s *Engine) runQueueAnchor(ctx context.Context, actor model.ActorRef, thread model.ThreadRef) (string, error) {
	active, err := s.repo.GetActiveRun(ctx, actor, thread)
	if err == nil {
		return active.RunID, nil
	}
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return "", err
}

func (s *Engine) ListRunQueue(ctx context.Context, actor model.ActorRef, thread model.ThreadRef) ([]model.QueueItem, error) {
	if _, err := s.ResolveThread(ctx, actor, thread); err != nil {
		return nil, err
	}
	return s.repo.ListRunQueueItems(ctx, actor, thread)
}

func (s *Engine) UpdateRunQueue(ctx context.Context, actor model.ActorRef, threadRef model.ThreadRef, queueID string, expectedRevision int, request RunQueueRequest) (*model.QueueItem, error) {
	item, err := s.repo.GetRunQueueItem(ctx, actor, threadRef, queueID)
	if err != nil {
		return nil, err
	}
	request = normalizeRunQueueRequest(request)
	if strings.TrimSpace(request.Input.Content) == "" {
		return nil, ErrInvalidInput
	}
	thread, err := s.ResolveThread(ctx, actor, threadRef)
	if err != nil {
		return nil, err
	}
	request, err = s.freezeRunQueueRequest(ctx, actor, thread, request)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, ErrInvalidInput
	}
	item.RequestJSON = string(encoded)
	item.RequestFingerprint = hashRunQueueRequest(actor, threadRef, encoded)
	item.AnchorProjection = model.ProjectionRef{}
	if request.ParentProjection != nil {
		item.AnchorProjection = *request.ParentProjection
	}
	if err = s.repo.UpdateRunQueueItem(ctx, item, expectedRevision); err != nil {
		return nil, err
	}
	s.wakeRunQueue()
	return s.repo.GetRunQueueItem(ctx, actor, threadRef, queueID)
}

func freezeRunQueueRequest(thread *ThreadSnapshot, request RunQueueRequest) RunQueueRequest {
	if thread == nil {
		return request
	}
	if strings.TrimSpace(request.Environment.ID) == "" {
		request.Environment = thread.Environment
	}
	request.Model = firstNonEmptyString(request.Model, thread.DefaultModel)
	request.ThreadModel = thread.DefaultModel
	request.ThreadProvider = thread.ModelProvider
	request.ThreadScope = thread.BindingScope
	return request
}

func (s *Engine) freezeRunQueueRequest(ctx context.Context, actor model.ActorRef, thread *ThreadSnapshot, request RunQueueRequest) (RunQueueRequest, error) {
	request = freezeRunQueueRequest(thread, request)
	if thread == nil {
		return RunQueueRequest{}, ErrHostProjectionUnavailable
	}
	environment, err := s.resolveTextRunProfile(ctx, actor, request.Environment)
	if err != nil {
		return RunQueueRequest{}, err
	}
	modelName, err := selectEnvironmentModel(environment, firstNonEmptyString(request.Model, thread.DefaultModel))
	if err != nil {
		return RunQueueRequest{}, err
	}
	return s.freezeRunQueueCapabilities(ctx, actor, thread, request, environment, modelName)
}

func (s *Engine) freezeRunQueueCapabilities(ctx context.Context, actor model.ActorRef, thread *ThreadSnapshot, request RunQueueRequest, environment *EnvironmentProfile, modelName string) (RunQueueRequest, error) {
	request.Environment = environment.Ref
	effectiveToolSelection := request.ToolKeys
	workspace, found, err := s.compileTextRunWorkspace(ctx, StartTextRunInput{Actor: actor, Thread: thread.Thread, ThreadScope: thread.BindingScope, Workspace: request.Workspace}, modelName)
	if err != nil {
		return RunQueueRequest{}, err
	}
	var compiledWorkspace *WorkspaceSnapshot
	if found {
		compiledWorkspace = &workspace
		keys := workspaceSnapshotToolKeys(&workspace)
		effectiveToolSelection = &keys
	}
	if !textRunEnvironmentWorkspaceCompatible(environment, compiledWorkspace) {
		return RunQueueRequest{}, ErrEnvironmentBindingNotAllowed
	}
	toolSelection, err := compileEnvironmentToolSelection(effectiveToolSelection, environment)
	if err != nil {
		return RunQueueRequest{}, err
	}
	effectiveSkills, _, err := resolveEnvironmentSkillSelectionWithDiagnostics(request.SkillRefs, environment)
	if err != nil {
		return RunQueueRequest{}, err
	}
	request.WorkspaceType, request.WorkspaceMode = textRunWorkspaceScope(compiledWorkspace)
	cfg := s.cfg.Snapshot()
	strictTools, err := s.resolveTextRunToolPolicies(ctx, actor, toolSelection.StrictKeys, valueRequest3E6DBD23, request.WorkspaceType, request.WorkspaceMode, modelName, nonNegativeTextRunValue(cfg.Tools.RetryCount), positiveTextRunValue(cfg.Tools.MaxConcurrentCalls))
	if err != nil {
		return RunQueueRequest{}, err
	}
	defaultTools, err := s.resolveTextRunToolPolicies(ctx, actor, toolSelection.DefaultKeys, textRunEnvironmentDefault, request.WorkspaceType, request.WorkspaceMode, modelName, nonNegativeTextRunValue(cfg.Tools.RetryCount), positiveTextRunValue(cfg.Tools.MaxConcurrentCalls))
	if err != nil {
		return RunQueueRequest{}, err
	}
	effectiveTools := mergeTextRunToolResolutions(strictTools, defaultTools)
	toolKeys := append([]string{}, effectiveTools.ResolvedKeys...)
	skillRefs := append([]model.ResourceRef{}, effectiveSkills...)
	request.ToolKeys, request.SkillRefs = &toolKeys, &skillRefs
	request.ToolFingerprints = toolResolutionFingerprints(effectiveTools)
	request.SkillFingerprints, err = s.skillFingerprints(ctx, actor, effectiveSkills)
	if err != nil {
		return RunQueueRequest{}, err
	}
	request.Model = modelName
	request.ResolvedStrategy, request.StrategyReason, request.RequestedMode, err = resolveTextRunStrategy(request.ExecutionMode, environment.DefaultMode, environment.AllowedModes, request.Input.Content, effectiveTools.Policies)
	if err != nil {
		return RunQueueRequest{}, err
	}
	return request, nil
}

func (s *Engine) CancelRunQueue(ctx context.Context, actor model.ActorRef, thread model.ThreadRef, queueID string) (*model.QueueItem, error) {
	return s.repo.CancelRunQueueItem(ctx, actor, thread, queueID)
}

func (s *Engine) PrioritizeRunQueue(ctx context.Context, actor model.ActorRef, thread model.ThreadRef, queueID string) (*model.QueueItem, error) {
	item, err := s.repo.PrioritizeRunQueueItem(ctx, actor, thread, queueID)
	if err == nil {
		s.wakeRunQueue()
	}
	return item, err
}

func (s *Engine) InterruptAndSendRun(ctx context.Context, actor model.ActorRef, thread model.ThreadRef, queueID string) (*model.QueueItem, error) {
	item, err := s.PrioritizeRunQueue(ctx, actor, thread, queueID)
	if err != nil {
		return nil, err
	}
	active, activeErr := s.repo.GetActiveRun(ctx, actor, thread)
	if activeErr == nil {
		if _, cancelErr := s.CancelRun(ctx, actor, active.RunID); cancelErr != nil {
			return nil, cancelErr
		}
	} else if !errors.Is(activeErr, ErrNotFound) {
		return nil, activeErr
	}
	s.wakeRunQueue()
	return item, nil
}

func normalizeRunQueueRequest(input RunQueueRequest) RunQueueRequest {
	input.Input.Content = strings.TrimSpace(input.Input.Content)
	input.SemanticVersion = RuntimeSnapshotVersion
	input.Model = strings.TrimSpace(input.Model)
	input.ExecutionMode = strings.TrimSpace(input.ExecutionMode)
	input.Input.ContentType = fallbackContentType(input.Input.ContentType)
	input.Environment.Kind = strings.TrimSpace(input.Environment.Kind)
	input.Environment.ID = strings.TrimSpace(input.Environment.ID)
	input.Environment.Revision = strings.TrimSpace(input.Environment.Revision)
	input.ParentProjection = normalizeProjectionRef(input.ParentProjection)
	input.SourceProjection = normalizeProjectionRef(input.SourceProjection)
	input.BranchReason = strings.TrimSpace(input.BranchReason)
	if input.BranchReason == "" {
		input.BranchReason = valueDefaultA60C25E3
	}
	input.Input.HTMLVisualColorMode = strings.TrimSpace(input.Input.HTMLVisualColorMode)
	input.Input.FileIDs = sortedUniqueStrings(input.Input.FileIDs)
	input.Input.OutputIDs = sortedUniqueStrings(input.Input.OutputIDs)
	input.Input.EvidenceIDs = sortedUniqueStrings(input.Input.EvidenceIDs)
	if input.ToolKeys != nil {
		items := sortedUniqueStrings(*input.ToolKeys)
		input.ToolKeys = &items
	}
	if input.SkillRefs != nil {
		items := normalizeSelectedSkillRefs(*input.SkillRefs)
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		input.SkillRefs = &items
	}
	return input
}

func normalizeProjectionRef(input *model.ProjectionRef) *model.ProjectionRef {
	if input == nil {
		return nil
	}
	value := model.ProjectionRef{Kind: strings.TrimSpace(input.Kind), ID: strings.TrimSpace(input.ID)}
	if value.ID == "" {
		return nil
	}
	return &value
}

func sortedUniqueStrings(input []string) []string {
	items := uniqueRuntimeStrings(input)
	sort.Strings(items)
	return items
}

func hashRunQueueRequest(actor model.ActorRef, thread model.ThreadRef, raw []byte) string {
	prefix := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00", actor.TenantID, actor.ActorID, thread.Kind, thread.ID)
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte(prefix), raw...)))
}

func (s *Engine) wakeRunQueue() {
	if s == nil || s.runQueueWake == nil {
		return
	}
	select {
	case s.runQueueWake <- struct{}{}:
	default:
	}
}

func (s *Engine) startRunQueueDispatcher(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.wakeRunQueue()
	s.runQueueDispatchLoop(ctx)
}

func (s *Engine) runQueueDispatchLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.runQueueWake:
		}
		s.dispatchRunQueueBatch(ctx)
	}
}

func (s *Engine) dispatchRunQueueBatch(ctx context.Context) {
	for index := 0; index < 10; index++ {
		item, err := s.repo.ClaimNextRunQueueItem(ctx, time.Now())
		if errors.Is(err, ErrNotFound) {
			return
		}
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("run_queue_claim_failed", Error(err))
			}
			return
		}
		if err = s.dispatchRunQueueItem(ctx, *item); err != nil && s.logger != nil {
			s.logger.Warn("run_queue_dispatch_failed", String("queue_id", item.QueueID), Error(err))
		}
	}
}

func (s *Engine) dispatchRunQueueItem(ctx context.Context, item model.QueueItem) error {
	if blocked, err := s.requeueWhenAnchorSuspended(ctx, item); blocked {
		return err
	}
	request, valid := decodeRunQueueRequest(item.RequestJSON)
	if !valid {
		return s.repo.RequeueRunQueueItem(ctx, item.QueueID, "request_invalid", "queued request is invalid", nil)
	}
	thread, err := s.ResolveThread(ctx, item.Actor, item.Thread)
	if err != nil {
		return s.repo.RequeueRunQueueItem(ctx, item.QueueID, "thread_unavailable", err.Error(), nil)
	}
	if !queuedThreadSnapshotUnchanged(thread, request) {
		return s.repo.RequeueRunQueueItem(ctx, item.QueueID, "environment_changed", "thread runtime binding changed; submit the goal again", nil)
	}
	if !s.queuedCapabilitiesUnchanged(ctx, item.Actor, request) {
		return s.repo.RequeueRunQueueItem(ctx, item.QueueID, "queue.capability_changed", "queued capabilities changed; submit the goal again", nil)
	}
	result, startErr := s.StartTextRun(ctx, StartTextRunInput{
		Actor:                   item.Actor,
		Thread:                  item.Thread,
		RequestID:               "queue:" + item.QueueID,
		Goal:                    request.Input.Content,
		ContentType:             request.Input.ContentType,
		Environment:             request.Environment,
		ClientRunID:             "run_queue_" + strings.TrimPrefix(item.QueueID, "queue_"),
		PlatformModelName:       request.Model,
		ExecutionMode:           request.ExecutionMode,
		FrozenStrategy:          request.ResolvedStrategy,
		FrozenStrategyReason:    request.StrategyReason,
		FrozenRequestedMode:     request.RequestedMode,
		Options:                 request.Options,
		FileIDs:                 request.Input.FileIDs,
		OutputIDs:               request.Input.OutputIDs,
		EvidenceIDs:             request.Input.EvidenceIDs,
		ToolKeys:                request.ToolKeys,
		SkillRefs:               request.SkillRefs,
		ParentProjection:        request.ParentProjection,
		SourceProjection:        request.SourceProjection,
		BranchReason:            request.BranchReason,
		HTMLVisualPromptEnabled: request.Input.HTMLVisualPrompt,
		HTMLVisualColorMode:     request.Input.HTMLVisualColorMode,
		ThreadModel:             request.ThreadModel,
		ThreadProvider:          request.ThreadProvider,
		ThreadScope:             request.ThreadScope,
		Workspace:               request.Workspace,
	})
	if startErr == nil {
		return s.repo.MarkRunQueueStarted(ctx, item.QueueID, result.Run.RunID)
	}
	if runQueueTransient(startErr) && item.AttemptCount < 5 {
		delay := time.Second * time.Duration(1<<max(0, item.AttemptCount-1))
		next := time.Now().Add(delay)
		return s.repo.RequeueRunQueueItem(ctx, item.QueueID, "dispatch_retry", startErr.Error(), &next)
	}
	return s.repo.RequeueRunQueueItem(ctx, item.QueueID, runQueueErrorCode(startErr), startErr.Error(), nil)
}

func queuedThreadSnapshotUnchanged(thread *ThreadSnapshot, request RunQueueRequest) bool {
	return thread != nil && sameQueuedEnvironmentBinding(thread.Environment, request.Environment) &&
		thread.DefaultModel == request.ThreadModel &&
		thread.ModelProvider == request.ThreadProvider &&
		thread.BindingScope == request.ThreadScope
}

func sameQueuedEnvironmentBinding(current, frozen model.ResourceRef) bool {
	if current.Kind != frozen.Kind || current.ID != frozen.ID {
		return false
	}
	return current.Revision == "" || current.Revision == frozen.Revision
}

func (s *Engine) queuedCapabilitiesUnchanged(ctx context.Context, actor model.ActorRef, request RunQueueRequest) bool {
	environment, err := s.resolveTextRunProfileAtRevision(ctx, actor, request.Environment)
	if err != nil || request.ToolKeys == nil || request.SkillRefs == nil {
		return false
	}
	selection, err := compileEnvironmentToolSelection(request.ToolKeys, environment)
	if err != nil || len(selection.DefaultKeys) > 0 {
		return false
	}
	cfg := s.cfg.Snapshot()
	tools, err := s.resolveTextRunToolPolicies(ctx, actor, selection.StrictKeys, valueRequest3E6DBD23, request.WorkspaceType, request.WorkspaceMode, request.Model, nonNegativeTextRunValue(cfg.Tools.RetryCount), positiveTextRunValue(cfg.Tools.MaxConcurrentCalls))
	if err != nil || !equalStringMap(request.ToolFingerprints, toolResolutionFingerprints(tools)) {
		return false
	}
	skills, err := s.skillFingerprints(ctx, actor, *request.SkillRefs)
	return err == nil && equalStringMap(request.SkillFingerprints, skills)
}

func toolResolutionFingerprints(resolution textRunToolResolution) map[string]string {
	result := make(map[string]string, len(resolution.Policies))
	for _, policy := range resolution.Policies {
		result[policy.ToolKey] = policy.Fingerprint
	}
	return result
}

func (s *Engine) skillFingerprints(ctx context.Context, actor model.ActorRef, refs []model.ResourceRef) (map[string]string, error) {
	result := make(map[string]string, len(refs))
	for _, ref := range normalizeSelectedSkillRefs(refs) {
		if s.skillResolver == nil {
			return nil, ErrRunSkillUnavailable
		}
		skill, err := s.skillResolver.ResolveAvailable(ctx, actor, ref)
		if err != nil || skill == nil || !skill.Enabled {
			return nil, ErrRunSkillUnavailable
		}
		raw := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s", skill.Ref.ID, skill.Title, skill.Trigger, skill.Description, skill.Markdown, skill.UpdatedAt.UTC().Format(time.RFC3339Nano))
		result[ref.ID] = fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	}
	return result, nil
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func (s *Engine) requeueWhenAnchorSuspended(ctx context.Context, item model.QueueItem) (bool, error) {
	if item.AnchorRunID == "" {
		return false, nil
	}
	anchor, _ := s.repo.GetRun(ctx, item.Actor, item.AnchorRunID)
	if anchor == nil || anchor.Status != model.RunStatusSuspended {
		return false, nil
	}
	next := time.Now().Add(15 * time.Second)
	return true, s.repo.RequeueRunQueueItem(ctx, item.QueueID, "blocked_by_suspended_run", "resume or cancel the suspended run", &next)
}

func decodeRunQueueRequest(raw string) (RunQueueRequest, bool) {
	var request RunQueueRequest
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&request)
	return request, err == nil && request.SemanticVersion == RuntimeSnapshotVersion
}

func runQueueTransient(err error) bool {
	return errors.Is(err, ErrTextRunAlreadyActive) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func runQueueErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrRunEnvironmentChanged):
		return "environment_changed"
	case errors.Is(err, ErrEnvironmentModelUnconfigured):
		return "environment.model_unconfigured"
	case errors.Is(err, ErrEnvironmentDefaultUnavailable):
		return "environment.default_model_unavailable"
	case errors.Is(err, ErrEnvironmentModelNotAccessible):
		return "environment.model_not_accessible"
	case errors.Is(err, ErrEnvironmentModelNotAuthorized):
		return "environment.model_not_authorized"
	case errors.Is(err, ErrRunEnvironmentUnavailable):
		return "environment_unavailable"
	case errors.Is(err, ErrRunToolProviderReceiptRequired):
		return "run.tool_provider_receipt_required"
	case errors.Is(err, ErrUsageBalanceInsufficient):
		return "payment_required"
	case errors.Is(err, ErrModelPricingRequired):
		return "pricing_required"
	default:
		return "dispatch_failed"
	}
}
