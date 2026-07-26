package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

// AgentTeamMemberInput defines one specialist task in a coordinator-led team.
// The member Manifest is frozen before the child Run starts.
type AgentTeamMemberInput struct {
	MemberID      string
	AgentManifest model.ResourceRef
	Goal          string
	ContentType   string
	OutputIDs     []string
	EvidenceIDs   []string
	Options       map[string]interface{}
}

// AgentTeamJoinInput defines the fan-in contract that resumes the coordinator.
type AgentTeamJoinInput struct {
	Mode           string
	Quorum         int
	FailurePolicy  string
	TimeoutSeconds int
	TimeoutPolicy  string
}

// StartAgentTeamInput starts a deferred coordinator Run, specialist child Runs,
// and one durable Handoff Join. ClientTeamID is the stable idempotency key for
// the complete topology.
type StartAgentTeamInput struct {
	ClientTeamID string
	Coordinator  StartTextRunInput
	Members      []AgentTeamMemberInput
	Join         AgentTeamJoinInput
}

type AgentTeamTaskStart struct {
	MemberID string
	Handoff  model.RunHandoff
	Run      model.Run
	Step     model.Step
}

type AgentTeamStartResult struct {
	Root  TextRunStartResult
	Tasks []AgentTeamTaskStart
	Join  model.RunHandoffJoin
}

// StartAgentTeam owns coordinator-led fan-out/fan-in orchestration. It is
// replay-safe across partial failures: the root Run, each member Handoff, and
// the Join all derive stable identities from ClientTeamID.
func (s *Engine) StartAgentTeam(ctx context.Context, input StartAgentTeamInput) (*AgentTeamStartResult, error) {
	normalized, err := normalizeStartAgentTeamInput(input)
	if err != nil {
		return nil, err
	}
	if err = s.validateAgentTeamCoordinator(ctx, normalized); err != nil {
		return nil, err
	}

	rootInput := normalized.Coordinator
	rootInput.ClientRunID = agentTeamClientPartID("team_root", rootInput.Actor, normalized.ClientTeamID, "coordinator")
	rootInput.DeferInitialContinuation = true
	root, err := s.startTextRun(ctx, rootInput)
	if err != nil {
		return nil, err
	}
	if err = validateDeferredAgentTeamRoot(root.Run); err != nil {
		return nil, err
	}

	tasks := make([]AgentTeamTaskStart, 0, len(normalized.Members))
	handoffIDs := make([]string, 0, len(normalized.Members))
	for _, member := range normalized.Members {
		delegated, delegateErr := s.ensureAgentTeamMember(ctx, normalized, root.Run, member)
		if delegateErr != nil {
			return nil, delegateErr
		}
		tasks = append(tasks, AgentTeamTaskStart{MemberID: member.MemberID, Handoff: delegated.Handoff, Run: delegated.Run, Step: delegated.Step})
		handoffIDs = append(handoffIDs, delegated.Handoff.HandoffID)
	}

	join, _, err := s.CreateRunHandoffJoin(ctx, CreateRunHandoffJoinInput{
		Actor:          rootInput.Actor,
		ParentRunID:    root.Run.RunID,
		ClientJoinID:   agentTeamClientPartID("team_join", rootInput.Actor, normalized.ClientTeamID, "fan_in"),
		HandoffIDs:     handoffIDs,
		Mode:           normalized.Join.Mode,
		Quorum:         normalized.Join.Quorum,
		FailurePolicy:  normalized.Join.FailurePolicy,
		TimeoutSeconds: normalized.Join.TimeoutSeconds,
		TimeoutPolicy:  normalized.Join.TimeoutPolicy,
	})
	if err != nil {
		return nil, err
	}
	return &AgentTeamStartResult{Root: *root, Tasks: tasks, Join: *join}, nil
}

func normalizeStartAgentTeamInput(input StartAgentTeamInput) (StartAgentTeamInput, error) {
	input.ClientTeamID = strings.TrimSpace(input.ClientTeamID)
	if !validAgentTeamEnvelope(input) || !validAgentTeamCoordinator(input.Coordinator) {
		return StartAgentTeamInput{}, ErrInvalidInput
	}
	members, err := normalizeAgentTeamMembers(input.Members)
	if err != nil {
		return StartAgentTeamInput{}, err
	}
	join, err := normalizeAgentTeamJoin(input.Join, len(members))
	if err != nil {
		return StartAgentTeamInput{}, err
	}
	input.Members, input.Join = members, join
	return input, nil
}

func validAgentTeamEnvelope(input StartAgentTeamInput) bool {
	return input.ClientTeamID != "" && len(input.ClientTeamID) <= 128 && len(input.Members) > 0 && len(input.Members) <= hardAgentMaxChildRuns
}

func validAgentTeamCoordinator(input StartTextRunInput) bool {
	return input.AgentManifest.Kind == model.AgentManifestKind && strings.TrimSpace(input.AgentManifest.ID) != "" && validTextRunStartRequest(input, strings.TrimSpace(input.Goal))
}

func normalizeAgentTeamMembers(input []AgentTeamMemberInput) ([]AgentTeamMemberInput, error) {
	result := append([]AgentTeamMemberInput(nil), input...)
	seen := make(map[string]struct{}, len(result))
	for index := range result {
		member, err := normalizeAgentTeamMember(result[index], seen)
		if err != nil {
			return nil, err
		}
		result[index] = member
	}
	return result, nil
}

func normalizeAgentTeamMember(member AgentTeamMemberInput, seen map[string]struct{}) (AgentTeamMemberInput, error) {
	member.MemberID = strings.TrimSpace(member.MemberID)
	member.Goal = strings.TrimSpace(member.Goal)
	member.ContentType = strings.TrimSpace(member.ContentType)
	if member.ContentType == "" {
		member.ContentType = valueText6CED98CE
	}
	_, duplicate := seen[member.MemberID]
	validManifest := member.AgentManifest.Kind == model.AgentManifestKind && strings.TrimSpace(member.AgentManifest.ID) != ""
	if member.MemberID == "" || len(member.MemberID) > 64 || duplicate || !validManifest || !validTextRunStartInput(member.Goal) {
		return AgentTeamMemberInput{}, ErrInvalidInput
	}
	seen[member.MemberID] = struct{}{}
	return member, nil
}

func normalizeAgentTeamJoin(input AgentTeamJoinInput, memberCount int) (AgentTeamJoinInput, error) {
	mode, quorum, failurePolicy, err := normalizeRunHandoffJoinPolicy(input.Mode, input.Quorum, input.FailurePolicy, memberCount)
	if err != nil {
		return AgentTeamJoinInput{}, err
	}
	timeoutSeconds, timeoutPolicy, err := normalizeRunHandoffJoinTimeout(input.TimeoutSeconds, input.TimeoutPolicy)
	if err != nil {
		return AgentTeamJoinInput{}, err
	}
	input.Mode, input.Quorum, input.FailurePolicy = mode, quorum, failurePolicy
	input.TimeoutSeconds, input.TimeoutPolicy = timeoutSeconds, timeoutPolicy
	return input, nil
}

func (s *Engine) validateAgentTeamCoordinator(ctx context.Context, input StartAgentTeamInput) error {
	manifest, err := s.repo.GetAgentManifest(ctx, input.Coordinator.Actor, input.Coordinator.AgentManifest)
	if err != nil {
		return err
	}
	if manifest.Status != model.AgentManifestStatusActive {
		return ErrAgentManifestDisabled
	}
	if manifest.MaxChildRuns < len(input.Members) {
		return ErrRunHandoffLimit
	}
	if manifest.MaxDepth < 1 {
		return ErrRunHandoffDepth
	}
	return nil
}

func validateDeferredAgentTeamRoot(run model.Run) error {
	effective, err := effectiveTextRunConfigFromRun(run)
	if err != nil {
		return err
	}
	if !effective.InitialContinuationDeferred {
		return ErrRunHandoffConflict
	}
	return nil
}

func (s *Engine) ensureAgentTeamMember(ctx context.Context, team StartAgentTeamInput, root model.Run, member AgentTeamMemberInput) (*DelegateTextRunResult, error) {
	input := DelegateTextRunInput{
		Actor:            team.Coordinator.Actor,
		ParentRunID:      root.RunID,
		ClientHandoffID:  agentTeamClientPartID("team_member", team.Coordinator.Actor, team.ClientTeamID, member.MemberID),
		AgentManifest:    member.AgentManifest,
		Goal:             member.Goal,
		ContentType:      member.ContentType,
		OutputIDs:        member.OutputIDs,
		EvidenceIDs:      member.EvidenceIDs,
		Options:          member.Options,
		RequestID:        team.Coordinator.RequestID,
		HTMLVisualPrompt: team.Coordinator.HTMLVisualPromptEnabled,
		HTMLColorMode:    team.Coordinator.HTMLVisualColorMode,
	}
	manifest, err := s.repo.GetAgentManifest(ctx, input.Actor, input.AgentManifest)
	if err != nil {
		return nil, err
	}
	handoffID, _ := delegatedPublicIDs(input.Actor, input.ClientHandoffID)
	existing, err := s.repo.GetRunHandoff(ctx, input.Actor, handoffID)
	if err == nil {
		return s.reusedDelegationResult(ctx, existing, handoffRequestFingerprint(input, manifest.Ref()))
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return s.DelegateTextRun(ctx, input)
}

func agentTeamClientPartID(prefix string, actor model.ActorRef, clientTeamID, part string) string {
	sum := sha256.Sum256([]byte(actor.TenantID + "\x00" + actor.ActorID + "\x00" + strings.TrimSpace(clientTeamID) + "\x00" + strings.TrimSpace(part)))
	return prefix + "_" + hex.EncodeToString(sum[:16])
}
