package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	defaultAgentMaxChildRuns = 4
	defaultAgentMaxDepth     = 3
	hardAgentMaxChildRuns    = 16
	hardAgentMaxDepth        = 6
	agentBranchReasonDefault = "default"
	handoffPayloadStatusKey  = "status"
	handoffPayloadSummaryKey = "summary"
)

type AgentManifestRevisionInput struct {
	Actor            model.ActorRef
	ManifestID       string
	ExpectedRevision int
	Name             string
	Description      string
	Instructions     string
	Status           string
	ModelName        string
	ExecutionMode    string
	ToolKeys         []string
	SkillRefs        []model.ResourceRef
	MaxChildRuns     int
	MaxDepth         int
	RequestID        string
	RevisionNote     string
}

func normalizeRunHandoffJoinIDs(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > hardAgentMaxChildRuns {
		return nil, ErrInvalidInput
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, ErrInvalidInput
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, ErrInvalidInput
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func runHandoffJoinPublicID(actor model.ActorRef, clientJoinID string) string {
	sum := sha256.Sum256([]byte(actor.TenantID + "\x00" + actor.ActorID + "\x00" + strings.TrimSpace(clientJoinID)))
	return "join_" + hex.EncodeToString(sum[:16])
}

func runHandoffJoinFingerprint(join model.RunHandoffJoin) string {
	payload := struct {
		Actor         model.ActorRef
		ParentRunID   string
		HandoffIDs    []string
		Mode          string
		Quorum        int
		FailurePolicy string
	}{join.Actor, join.ParentRunID, join.HandoffIDs, join.Mode, join.Quorum, join.FailurePolicy}
	return hashAgentPayload(payload)
}

func (s *Engine) CreateRunHandoffJoin(ctx context.Context, input CreateRunHandoffJoinInput) (*model.RunHandoffJoin, bool, error) {
	return s.createRunHandoffJoinWait(ctx, input)
}

func (s *Engine) GetRunHandoffJoin(ctx context.Context, actor model.ActorRef, joinID string) (*model.RunHandoffJoin, error) {
	if !validActorRef(actor) || strings.TrimSpace(joinID) == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.GetRunHandoffJoin(ctx, actor, strings.TrimSpace(joinID))
}

func (s *Engine) ListRunHandoffJoins(ctx context.Context, actor model.ActorRef, filter model.RunHandoffJoinFilter) (model.RunHandoffJoinPage, error) {
	if !validActorRef(actor) {
		return model.RunHandoffJoinPage{}, ErrInvalidInput
	}
	return s.repo.ListRunHandoffJoins(ctx, actor, filter)
}

func normalizeRunHandoffJoinPolicy(mode string, quorum int, failurePolicy string, members int) (string, int, string, error) {
	if members <= 0 || members > hardAgentMaxChildRuns {
		return "", 0, "", ErrInvalidInput
	}
	failurePolicy, err := normalizeRunHandoffJoinFailurePolicy(failurePolicy)
	if err != nil {
		return "", 0, "", ErrInvalidInput
	}
	mode, quorum, err = normalizeRunHandoffJoinMode(mode, quorum, members)
	if err != nil {
		return "", 0, "", err
	}
	return mode, quorum, failurePolicy, nil
}

func normalizeRunHandoffJoinFailurePolicy(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return model.RunHandoffJoinFailureCollect, nil
	}
	if value != model.RunHandoffJoinFailureCollect && value != model.RunHandoffJoinFailureFailFast {
		return "", ErrInvalidInput
	}
	return value, nil
}

func normalizeRunHandoffJoinMode(mode string, quorum, members int) (string, int, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = model.RunHandoffJoinModeAll
	}
	switch mode {
	case model.RunHandoffJoinModeAll, model.RunHandoffJoinModeAny:
		return mode, 1, nil
	case model.RunHandoffJoinModeQuorum:
		if quorum <= 0 || quorum > members {
			return "", 0, ErrInvalidInput
		}
		return mode, quorum, nil
	default:
		return "", 0, ErrInvalidInput
	}
}

type DelegateTextRunInput struct {
	Actor            model.ActorRef
	ParentRunID      string
	ClientHandoffID  string
	AgentManifest    model.ResourceRef
	Goal             string
	ContentType      string
	OutputIDs        []string
	EvidenceIDs      []string
	Options          map[string]interface{}
	RequestID        string
	HTMLVisualPrompt bool
	HTMLColorMode    string
}

type DelegateTextRunResult struct {
	Handoff model.RunHandoff
	Run     model.Run
	Step    model.Step
}

type CreateRunHandoffJoinInput struct {
	Actor         model.ActorRef
	ParentRunID   string
	ClientJoinID  string
	HandoffIDs    []string
	Mode          string
	Quorum        int
	FailurePolicy string
}

type RunTask struct {
	Handoff model.RunHandoff
	Run     model.Run
}

type RunTaskTree struct {
	RootRunID    string
	CurrentRunID string
	RootRun      model.Run
	Tasks        []RunTask
}

type RunDelegationStart struct {
	Manifest model.AgentManifest
	Handoff  model.RunHandoff
}

type preparedDelegation struct {
	parent   model.Run
	manifest model.AgentManifest
	handoff  model.RunHandoff
}

func (s *Engine) CreateAgentManifestRevision(ctx context.Context, input AgentManifestRevisionInput) (*model.AgentManifest, bool, error) {
	if !validActorRef(input.Actor) || strings.TrimSpace(input.Name) == "" || input.ExpectedRevision < 0 {
		return nil, false, ErrInvalidInput
	}
	manifestID := normalizeAgentManifestID(input.ManifestID)
	if manifestID == "" {
		manifestID = "agent_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = model.AgentManifestStatusActive
	}
	item := &model.AgentManifest{
		ManifestID: manifestID, TenantID: input.Actor.TenantID, Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description),
		Instructions: strings.TrimSpace(input.Instructions), Status: status, ModelName: strings.TrimSpace(input.ModelName), ExecutionMode: strings.TrimSpace(input.ExecutionMode),
		ToolKeys: uniqueRuntimeStrings(input.ToolKeys), SkillRefs: normalizeSelectedSkillRefs(input.SkillRefs),
		MaxChildRuns: boundedTextRunConfig(input.MaxChildRuns, defaultAgentMaxChildRuns, hardAgentMaxChildRuns),
		MaxDepth:     boundedTextRunConfig(input.MaxDepth, defaultAgentMaxDepth, hardAgentMaxDepth), CreatedBy: input.Actor,
		RequestID: strings.TrimSpace(input.RequestID), RevisionNote: strings.TrimSpace(input.RevisionNote),
	}
	item.RequestFingerprint = agentManifestRequestFingerprint(*item, input.ExpectedRevision)
	return s.repo.CreateAgentManifestRevision(ctx, item, input.ExpectedRevision)
}

func (s *Engine) ListAgentManifests(ctx context.Context, actor model.ActorRef, filter model.AgentManifestFilter) (model.AgentManifestPage, error) {
	if !validActorRef(actor) {
		return model.AgentManifestPage{}, ErrInvalidInput
	}
	return s.repo.ListAgentManifests(ctx, actor, filter)
}

func (s *Engine) GetAgentManifest(ctx context.Context, actor model.ActorRef, ref model.ResourceRef) (*model.AgentManifest, error) {
	if !validActorRef(actor) {
		return nil, ErrInvalidInput
	}
	return s.repo.GetAgentManifest(ctx, actor, ref)
}

func (s *Engine) DelegateTextRun(ctx context.Context, input DelegateTextRunInput) (*DelegateTextRunResult, error) {
	goal, err := validateDelegateTextRunInput(input)
	if err != nil {
		return nil, err
	}
	prepared, reused, err := s.prepareDelegation(ctx, input, goal)
	if err != nil {
		return nil, err
	}
	if reused != nil {
		return reused, nil
	}
	return s.startDelegatedTextRun(ctx, input, prepared)
}

func validateDelegateTextRunInput(input DelegateTextRunInput) (string, error) {
	goal := strings.TrimSpace(input.Goal)
	validManifest := input.AgentManifest.Kind == model.AgentManifestKind && strings.TrimSpace(input.AgentManifest.ID) != ""
	if !validActorRef(input.Actor) || strings.TrimSpace(input.ParentRunID) == "" || strings.TrimSpace(input.ClientHandoffID) == "" || !validManifest || !validTextRunStartInput(goal) {
		return "", ErrInvalidInput
	}
	return goal, nil
}

func (s *Engine) prepareDelegation(ctx context.Context, input DelegateTextRunInput, goal string) (preparedDelegation, *DelegateTextRunResult, error) {
	parent, manifest, err := s.resolveDelegationSource(ctx, input)
	if err != nil {
		return preparedDelegation{}, nil, err
	}
	fingerprint := handoffRequestFingerprint(input, manifest.Ref())
	handoffID, childRunID := delegatedPublicIDs(input.Actor, input.ClientHandoffID)
	reused, found, err := s.findReusedDelegation(ctx, input.Actor, handoffID, fingerprint)
	if err != nil || found {
		return preparedDelegation{}, reused, err
	}
	rootRunID := firstNonEmptyString(parent.RootRunID, parent.RunID)
	depth := parent.Depth + 1
	if err = s.validateDelegationLimits(ctx, input.Actor, *parent, *manifest, depth); err != nil {
		return preparedDelegation{}, nil, err
	}
	handoff := model.RunHandoff{
		HandoffID: handoffID, ClientHandoffID: strings.TrimSpace(input.ClientHandoffID), RequestFingerprint: fingerprint, Actor: input.Actor,
		RootRunID: rootRunID, ParentRunID: parent.RunID, ChildRunID: childRunID, AgentManifest: manifest.Ref(), AgentName: manifest.Name,
		Goal: goal, Status: model.RunHandoffStatusQueued, Depth: depth, InputProjection: parent.OutputProjection,
	}
	return preparedDelegation{parent: *parent, manifest: *manifest, handoff: handoff}, nil, nil
}

func (s *Engine) findReusedDelegation(ctx context.Context, actor model.ActorRef, handoffID, fingerprint string) (*DelegateTextRunResult, bool, error) {
	existing, err := s.repo.GetRunHandoff(ctx, actor, handoffID)
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	result, err := s.reusedDelegationResult(ctx, existing, fingerprint)
	return result, true, err
}

func (s *Engine) startDelegatedTextRun(ctx context.Context, input DelegateTextRunInput, prepared preparedDelegation) (*DelegateTextRunResult, error) {
	toolKeys := append([]string(nil), prepared.manifest.ToolKeys...)
	skillRefs := append([]model.ResourceRef(nil), prepared.manifest.SkillRefs...)
	modelName := firstNonEmptyString(prepared.manifest.ModelName, prepared.parent.PlatformModelName)
	executionMode := firstNonEmptyString(prepared.manifest.ExecutionMode, TextRunExecutionModeAuto)
	start, err := s.StartTextRun(ctx, StartTextRunInput{
		Actor: input.Actor, Thread: prepared.parent.Thread, RequestID: input.RequestID, Goal: prepared.handoff.Goal, ContentType: firstNonEmptyString(input.ContentType, "text"),
		Environment: prepared.parent.Environment, ClientRunID: prepared.handoff.ChildRunID, PlatformModelName: modelName, ExecutionMode: executionMode, Options: input.Options,
		OutputIDs: input.OutputIDs, EvidenceIDs: input.EvidenceIDs, ToolKeys: &toolKeys, SkillRefs: &skillRefs,
		ParentProjection: &prepared.parent.OutputProjection, BranchReason: agentBranchReasonDefault, HTMLVisualPromptEnabled: input.HTMLVisualPrompt,
		HTMLVisualColorMode: input.HTMLColorMode, ThreadModel: prepared.parent.PlatformModelName, ThreadProvider: prepared.parent.Provider,
		Delegation: &RunDelegationStart{Manifest: prepared.manifest, Handoff: prepared.handoff},
	})
	if err != nil {
		return nil, err
	}
	savedHandoff, err := s.repo.GetRunHandoff(ctx, input.Actor, prepared.handoff.HandoffID)
	if err != nil {
		return nil, err
	}
	return &DelegateTextRunResult{Handoff: *savedHandoff, Run: start.Run, Step: start.Step}, nil
}

func (s *Engine) GetRunTaskTree(ctx context.Context, actor model.ActorRef, runID string) (*RunTaskTree, error) {
	current, err := s.repo.GetRun(ctx, actor, strings.TrimSpace(runID))
	if err != nil {
		return nil, err
	}
	rootRunID := current.RootRunID
	if rootRunID == "" {
		rootRunID = current.RunID
	}
	rootRun, err := s.repo.GetRun(ctx, actor, rootRunID)
	if err != nil {
		return nil, err
	}
	page, err := s.repo.ListRunHandoffs(ctx, actor, model.RunHandoffFilter{RootRunID: rootRunID, Limit: 500})
	if err != nil {
		return nil, err
	}
	tasks := make([]RunTask, 0, len(page.Results))
	for _, handoff := range page.Results {
		child, childErr := s.repo.GetRun(ctx, actor, handoff.ChildRunID)
		if childErr != nil {
			if errors.Is(childErr, ErrNotFound) {
				continue
			}
			return nil, childErr
		}
		tasks = append(tasks, RunTask{Handoff: handoff, Run: *child})
	}
	return &RunTaskTree{RootRunID: rootRunID, CurrentRunID: current.RunID, RootRun: *rootRun, Tasks: tasks}, nil
}

func (s *Engine) resolveDelegationSource(ctx context.Context, input DelegateTextRunInput) (*model.Run, *model.AgentManifest, error) {
	parent, err := s.repo.GetRun(ctx, input.Actor, strings.TrimSpace(input.ParentRunID))
	if err != nil {
		return nil, nil, err
	}
	if !runCanDelegate(*parent) || parent.OutputProjection.ID == "" {
		return nil, nil, ErrRunHandoffParentBlocked
	}
	manifest, err := s.repo.GetAgentManifest(ctx, input.Actor, input.AgentManifest)
	if err != nil {
		return nil, nil, err
	}
	if manifest.Status != model.AgentManifestStatusActive {
		return nil, nil, ErrAgentManifestDisabled
	}
	return parent, manifest, nil
}

func (s *Engine) validateDelegationLimits(ctx context.Context, actor model.ActorRef, parent model.Run, manifest model.AgentManifest, depth int) error {
	if depth > manifest.MaxDepth || depth > hardAgentMaxDepth {
		return ErrRunHandoffDepth
	}
	page, err := s.repo.ListRunHandoffs(ctx, actor, model.RunHandoffFilter{ParentRunID: parent.RunID, Limit: hardAgentMaxChildRuns + 1})
	if err != nil {
		return err
	}
	if page.Total >= int64(manifest.MaxChildRuns) || page.Total >= hardAgentMaxChildRuns {
		return ErrRunHandoffLimit
	}
	return nil
}

func (s *Engine) reusedDelegationResult(ctx context.Context, handoff *model.RunHandoff, fingerprint string) (*DelegateTextRunResult, error) {
	if handoff == nil || handoff.RequestFingerprint != fingerprint {
		return nil, ErrRunHandoffConflict
	}
	run, err := s.repo.GetRun(ctx, handoff.Actor, handoff.ChildRunID)
	if err != nil {
		return nil, err
	}
	steps, err := s.repo.ListRunSteps(ctx, run.RunID)
	if err != nil || len(steps) == 0 {
		if err == nil {
			err = ErrNotFound
		}
		return nil, err
	}
	return &DelegateTextRunResult{Handoff: *handoff, Run: *run, Step: steps[0]}, nil
}

func runCanDelegate(run model.Run) bool {
	return run.Status == model.RunStatusQueued || run.Status == model.RunStatusPreparing || run.Status == model.RunStatusRunning
}

func normalizeAgentManifestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "agent_") {
		return value
	}
	return "agent_" + value
}

func agentManifestRequestFingerprint(item model.AgentManifest, expectedRevision int) string {
	toolKeys := append([]string(nil), item.ToolKeys...)
	sort.Strings(toolKeys)
	skillRefs := append([]model.ResourceRef(nil), item.SkillRefs...)
	sort.Slice(skillRefs, func(i, j int) bool {
		return skillRefs[i].Kind+skillRefs[i].ID+skillRefs[i].Revision < skillRefs[j].Kind+skillRefs[j].ID+skillRefs[j].Revision
	})
	payload := struct {
		ManifestID, TenantID, Name, Description, Instructions, Status, ModelName, ExecutionMode, RevisionNote string
		ExpectedRevision, MaxChildRuns, MaxDepth                                                              int
		ToolKeys                                                                                              []string
		SkillRefs                                                                                             []model.ResourceRef
	}{item.ManifestID, item.TenantID, item.Name, item.Description, item.Instructions, item.Status, item.ModelName, item.ExecutionMode, item.RevisionNote, expectedRevision, item.MaxChildRuns, item.MaxDepth, toolKeys, skillRefs}
	return hashAgentPayload(payload)
}

func handoffRequestFingerprint(input DelegateTextRunInput, manifest model.ResourceRef) string {
	outputs := uniqueRuntimeStrings(input.OutputIDs)
	evidence := uniqueRuntimeStrings(input.EvidenceIDs)
	sort.Strings(outputs)
	sort.Strings(evidence)
	payload := struct {
		Actor           model.ActorRef
		ParentRunID     string
		ClientHandoffID string
		Manifest        model.ResourceRef
		Goal            string
		ContentType     string
		OutputIDs       []string
		EvidenceIDs     []string
		Options         map[string]interface{}
	}{input.Actor, strings.TrimSpace(input.ParentRunID), strings.TrimSpace(input.ClientHandoffID), manifest, strings.TrimSpace(input.Goal), strings.TrimSpace(input.ContentType), outputs, evidence, input.Options}
	return hashAgentPayload(payload)
}

func hashAgentPayload(payload interface{}) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func delegatedPublicIDs(actor model.ActorRef, clientHandoffID string) (string, string) {
	sum := sha256.Sum256([]byte(actor.TenantID + "\x00" + actor.ActorID + "\x00" + strings.TrimSpace(clientHandoffID)))
	value := hex.EncodeToString(sum[:16])
	return "handoff_" + value, "run_delegate_" + value
}

func applyRunDelegation(run *model.Run, delegation *RunDelegationStart) {
	if run == nil || delegation == nil {
		return
	}
	run.AgentManifest = delegation.Manifest.Ref()
	run.AgentName = delegation.Manifest.Name
	run.RootRunID = delegation.Handoff.RootRunID
	run.ParentRunID = delegation.Handoff.ParentRunID
	run.HandoffID = delegation.Handoff.HandoffID
	run.Depth = delegation.Handoff.Depth
}

func textRunAgentManifest(input StartTextRunInput, environmentInstructions string) (string, *effectiveAgentManifest) {
	if input.Delegation == nil {
		return environmentInstructions, nil
	}
	manifest := input.Delegation.Manifest
	sections := []string{strings.TrimSpace(environmentInstructions), "## Delegated agent\nName: " + manifest.Name}
	if manifest.Description != "" {
		sections = append(sections, manifest.Description)
	}
	if manifest.Instructions != "" {
		sections = append(sections, manifest.Instructions)
	}
	return strings.Join(nonEmptyAgentStrings(sections), "\n\n"), &effectiveAgentManifest{
		Ref: manifest.Ref(), Name: manifest.Name, Description: manifest.Description, Instructions: manifest.Instructions,
		ModelName: manifest.ModelName, ExecutionMode: manifest.ExecutionMode, ToolKeys: append([]string(nil), manifest.ToolKeys...),
		SkillRefs: append([]model.ResourceRef(nil), manifest.SkillRefs...), MaxChildRuns: manifest.MaxChildRuns, MaxDepth: manifest.MaxDepth,
	}
}

func nonEmptyAgentStrings(items []string) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		if value := strings.TrimSpace(item); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func delegationFingerprint(input *RunDelegationStart) *runDelegationFingerprint {
	if input == nil {
		return nil
	}
	return &runDelegationFingerprint{
		AgentManifest: input.Manifest.Ref(), RootRunID: input.Handoff.RootRunID, ParentRunID: input.Handoff.ParentRunID,
		HandoffID: input.Handoff.HandoffID, Depth: input.Handoff.Depth,
	}
}

func (s *Engine) persistRunDelegationStart(ctx context.Context, child model.Run, delegation *RunDelegationStart) ([]model.Event, error) {
	if delegation == nil {
		return nil, nil
	}
	parent, err := s.validateDelegationParentAtCommit(ctx, child, delegation.Handoff)
	if err != nil {
		return nil, err
	}
	saved, reused, err := s.createDelegationHandoffAtCommit(ctx, child, delegation.Handoff, delegation.Manifest.MaxChildRuns)
	if err != nil {
		return nil, err
	}
	if reused {
		return nil, validateReusedDelegationHandoff(saved, child, delegation.Handoff)
	}
	return s.appendHandoffCreatedEvent(ctx, *parent, *saved)
}

func (s *Engine) validateDelegationParentAtCommit(ctx context.Context, child model.Run, handoff model.RunHandoff) (*model.Run, error) {
	parent, err := s.repo.GetRun(ctx, child.Actor, handoff.ParentRunID)
	if err != nil {
		return nil, err
	}
	if !runCanDelegate(*parent) || parent.OutputProjection != handoff.InputProjection {
		return nil, ErrRunHandoffParentBlocked
	}
	return parent, nil
}

func (s *Engine) createDelegationHandoffAtCommit(ctx context.Context, child model.Run, input model.RunHandoff, maxChildren int) (*model.RunHandoff, bool, error) {
	handoff := input
	handoff.ChildRunID = child.RunID
	handoff.InputProjection = child.InputProjection
	limit := min(maxChildren, hardAgentMaxChildRuns)
	return s.repo.CreateRunHandoffWithinLimit(ctx, &handoff, limit)
}

func validateReusedDelegationHandoff(saved *model.RunHandoff, child model.Run, expected model.RunHandoff) error {
	if saved != nil && saved.ChildRunID == child.RunID && saved.RequestFingerprint == expected.RequestFingerprint {
		return nil
	}
	return ErrRunHandoffConflict
}

func (s *Engine) appendHandoffCreatedEvent(ctx context.Context, parent model.Run, handoff model.RunHandoff) ([]model.Event, error) {
	payload := map[string]interface{}{
		"handoffID": handoff.HandoffID, "childRunID": handoff.ChildRunID, "agentManifest": handoff.AgentManifest,
		"agentName": handoff.AgentName, "goal": handoff.Goal, "depth": handoff.Depth,
	}
	event := newRunEvent(parent, "handoff.created", parent.CurrentStepID, "Delegated to "+handoff.AgentName, payload, &parent.OutputProjection)
	persisted, created, err := s.repo.AppendRunEvent(ctx, &event)
	if err != nil {
		return nil, err
	}
	if !created {
		return nil, nil
	}
	return []model.Event{*persisted}, nil
}

func handoffTerminalStatus(outcome string) string {
	switch outcome {
	case model.TerminalCompleted:
		return model.RunHandoffStatusCompleted
	case model.TerminalFailed:
		return model.RunHandoffStatusFailed
	case model.TerminalCancelled:
		return model.RunHandoffStatusCancelled
	default:
		return ""
	}
}

func handoffTerminalEventType(status string) string {
	return "handoff." + status
}

func handoffOutputIDs(output *model.OutputRef) []string {
	if output == nil || strings.TrimSpace(output.OutputID) == "" {
		return nil
	}
	return []string{output.OutputID}
}

func (s *Engine) finalizeRunHandoff(ctx context.Context, run model.Run, intent model.TerminalIntent, output *model.OutputRef) (string, []model.Event, error) {
	if strings.TrimSpace(run.HandoffID) == "" {
		return "", nil, nil
	}
	status := handoffTerminalStatus(intent.Outcome)
	if status == "" {
		return "", nil, ErrInvalidInput
	}
	resultSummary := strings.TrimSpace(intent.Summary)
	if output != nil && strings.TrimSpace(output.Summary) != "" {
		resultSummary = strings.TrimSpace(output.Summary)
	}
	completion, err := s.repo.CompleteRunHandoffWithJoins(ctx, run.Actor, run.RunID, model.RunHandoffCompletion{
		Status: status, ResultSummary: resultSummary, ResultOutputIDs: handoffOutputIDs(output), ErrorCode: intent.ErrorCode, ErrorMessage: intent.ErrorMessage,
	})
	if err != nil {
		return "", nil, err
	}
	handoff := completion.Handoff
	parent, err := s.repo.GetRun(ctx, run.Actor, handoff.ParentRunID)
	if err != nil {
		return "", nil, err
	}
	events := make([]model.Event, 0, 1+len(completion.ResolvedJoins)*4)
	if !completion.Reused {
		payload := map[string]interface{}{
			"handoffID": handoff.HandoffID, "childRunID": run.RunID, "agentManifest": handoff.AgentManifest,
			"agentName": handoff.AgentName, handoffPayloadStatusKey: status, handoffPayloadSummaryKey: resultSummary, "outputIDs": handoff.ResultOutputIDs,
		}
		event := newRunEvent(*parent, handoffTerminalEventType(status), parent.CurrentStepID, "Delegated task "+status, payload, &parent.OutputProjection)
		persisted, created, appendErr := s.repo.AppendRunEvent(ctx, &event)
		if appendErr != nil {
			return "", nil, appendErr
		}
		if created {
			events = append(events, *persisted)
		}
	}
	for _, join := range completion.ResolvedJoins {
		resolved, _, resolveErr := s.resolveRunHandoffJoinAtCommit(ctx, *parent, join)
		if resolveErr != nil {
			return "", nil, resolveErr
		}
		events = append(events, resolved...)
	}
	return handoff.ParentRunID, events, nil
}

var (
	ErrAgentManifestConflict   = errors.New("agent manifest revision conflict")
	ErrAgentManifestDisabled   = errors.New("agent manifest is disabled")
	ErrRunHandoffConflict      = errors.New("run handoff idempotency conflict")
	ErrRunHandoffLimit         = errors.New("run handoff child limit reached")
	ErrRunHandoffDepth         = errors.New("run handoff depth limit reached")
	ErrRunHandoffParentBlocked = errors.New("parent run cannot delegate")
	ErrRunHandoffJoinConflict  = errors.New("run handoff join idempotency conflict")
	ErrRunHandoffJoinMember    = errors.New("run handoff join contains an invalid member")
)
