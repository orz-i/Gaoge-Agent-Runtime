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

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Engine) recoverDuplicateTextRunStart(ctx context.Context, actor model.ActorRef, thread model.ThreadRef, runID, fingerprint string, duplicateErr error, allowThreadConcurrency bool) (*TextRunStartResult, error) {
	existing, err := s.repo.GetRun(ctx, actor, runID)
	if err == nil {
		if !textRunFingerprintMatches(existing, fingerprint) {
			return nil, ErrTextRunIdempotencyConflict
		}
		return s.textRunStartResult(ctx, actor, existing)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if allowThreadConcurrency {
		return nil, duplicateErr
	}
	_, err = s.repo.GetActiveRun(ctx, actor, thread)
	if err == nil {
		return nil, ErrTextRunAlreadyActive
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return nil, duplicateErr
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

func (s *Engine) existingTextRunStart(ctx context.Context, actor model.ActorRef, thread model.ThreadRef, runID, fingerprint string, allowThreadConcurrency bool) (TextRunStartResult, bool, error) {
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
	if allowThreadConcurrency {
		return TextRunStartResult{}, false, nil
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
	if tool.ExecutionMode == valueLocalDispatch71FF6D47 {
		if schemaErr := validateToolContractSchemas(tool.InputSchema, tool.OutputSchema); schemaErr != nil {
			return effectiveRunToolPolicy{}, errors.Join(ErrRunEnvironmentUnavailable, schemaErr)
		}
	}
	mode := strings.TrimSpace(tool.ApprovalMode)
	if tool.ApprovalCapability == valuePerCall2570116D {
		if mode != valueNever4C6E2E88 {
			mode = valueAlways6FAD1299
		}
	} else {
		mode = "activation_only"
	}
	validLevels := map[string]bool{valueRead3A612695: true, ToolSideEffectStagedWrite: true, ToolSideEffectWrite: true, ToolSideEffectDestructive: true}
	level := strings.TrimSpace(tool.SideEffectLevel)
	if !validLevels[level] {
		level = valueUnknown26BF6906
	}
	idempotencyMode := normalizeToolIdempotencyMode(tool.IdempotencyMode)
	if tool.ExecutionMode == valueLocalDispatch71FF6D47 && toolRequiresProviderReceipt(level) && idempotencyMode != ToolIdempotencyProviderReceipt {
		return effectiveRunToolPolicy{}, ErrRunToolProviderReceiptRequired
	}
	snapshot := effectiveRunToolPolicy{ToolKey: tool.ToolKey, ProviderKind: tool.ProviderKind, ProviderKey: tool.ProviderKey, ModelName: tool.ModelName, OriginalName: tool.OriginalName, Description: tool.Description, DefinitionVersion: tool.DefinitionVersion, InputSchema: append(json.RawMessage(nil), tool.InputSchema...), OutputSchema: append(json.RawMessage(nil), tool.OutputSchema...), ExecutionMode: tool.ExecutionMode, ApprovalCapability: tool.ApprovalCapability, ApprovalMode: mode, RiskLevel: tool.RiskLevel, SideEffectLevel: level, IdempotencyMode: idempotencyMode, HostedVariants: cloneHostedToolVariants(tool.HostedVariants), RetryCount: retryCount, Concurrency: concurrency}
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
	payload := textRunFingerprintInput{Actor: input.Actor, Thread: input.Thread, Goal: goal, Environment: input.Environment, Model: strings.TrimSpace(input.PlatformModelName), ExecutionMode: strings.TrimSpace(input.ExecutionMode), Options: input.Options, FileIDs: files, OutputIDs: outputs, EvidenceIDs: evidence, ToolKeys: copyKeys(input.ToolKeys), SkillRefs: copyRefs(input.SkillRefs), ParentProjection: input.ParentProjection, SourceProjection: input.SourceProjection, BranchReason: strings.TrimSpace(input.BranchReason), HTMLVisualPrompt: input.HTMLVisualPromptEnabled, HTMLVisualColorMode: strings.TrimSpace(input.HTMLVisualColorMode), Workspace: input.Workspace, AgentManifest: input.AgentManifest, Delegation: delegationFingerprint(input.Delegation), InitialContinuationDeferred: input.DeferInitialContinuation}
	if input.FrozenWorkspace != nil {
		payload.FrozenWorkspaceID = strings.TrimSpace(input.FrozenWorkspace.SnapshotID)
		payload.FrozenWorkspaceHash = strings.TrimSpace(input.FrozenWorkspace.StateHash)
	}
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
