package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// TeamFeature is the narrow Team Runtime capability consumed by Harness.
type TeamFeature interface {
	StartRun(context.Context, team.StartRequest) (kernel.Snapshot, error)
	Resume(context.Context, string, uint64) (kernel.Snapshot, error)
}

// GroupChatFeature is the narrow Group Chat Runtime capability consumed by Harness.
type GroupChatFeature interface {
	StartRun(context.Context, groupchat.StartRequest) (kernel.Snapshot, error)
	Resume(context.Context, string, uint64) (kernel.Snapshot, error)
}

// TeamTurnRequest starts Team as the top-level capability of one Harness Turn.
type TeamTurnRequest struct {
	StartRequest
	Mode    team.ExecutionMode
	Members []team.Member
	Join    handoff.Join
}

// GroupChatSpeakerSelection binds a host-owned visible participant identity to
// one frozen executable Role snapshot. RoleID is only the Harness config lookup
// key; Author* is the visible product identity projected by Group Chat.
type GroupChatSpeakerSelection struct {
	ParticipantID  string `json:"participantID"`
	RoleID         string `json:"roleID"`
	AuthorKind     string `json:"authorKind"`
	AuthorID       string `json:"authorID"`
	AuthorRevision string `json:"authorRevision"`
	AuthorName     string `json:"authorName"`
}

// GroupChatTurnRequest starts directed Group Chat as the top-level capability.
type GroupChatTurnRequest struct {
	StartRequest
	SpeakerPolicy groupchat.SpeakerPolicy
	Participants  []groupchat.Participant
	Speakers      []GroupChatSpeakerSelection
	MaxUtterances int
}

// WorkflowTurnRequest starts one compiled Dynamic Workflow as the top-level capability.
type WorkflowTurnRequest struct {
	StartRequest
	Definition workflow.Definition
	Input      json.RawMessage
}

type topLevelFeatureStart struct {
	turn       Turn
	invocation Invocation
	runContext context.Context
	replayed   bool
}

type topLevelFeatureEnvelope struct {
	request StartRequest
	turn    Turn
	config  ConfigSnapshot
	created bool
	now     time.Time
}

type teamInvocationInput struct {
	Goal    string             `json:"goal"`
	Actor   kernel.ActorRef    `json:"actor"`
	Thread  kernel.ThreadRef   `json:"thread"`
	Mode    team.ExecutionMode `json:"mode"`
	Members []team.Member      `json:"members"`
	Join    handoff.Join       `json:"join"`
}

type groupChatInvocationInput struct {
	Goal                 string                    `json:"goal"`
	Actor                kernel.ActorRef           `json:"actor"`
	Thread               kernel.ThreadRef          `json:"thread"`
	SpeakerPolicy        groupchat.SpeakerPolicy   `json:"speakerPolicy"`
	Participants         []groupchat.Participant   `json:"participants"`
	SpeakerConfigs       []groupchat.SpeakerConfig `json:"speakerConfigs"`
	DirectedSpeakerIDs   []string                  `json:"directedSpeakerIDs,omitempty"`
	CandidateSpeakerIDs  []string                  `json:"candidateSpeakerIDs,omitempty"`
	SelectorModel        string                    `json:"selectorModel,omitempty"`
	SelectorModelOptions json.RawMessage           `json:"selectorModelOptions,omitempty"`
	MaxUtterances        int                       `json:"maxUtterances"`
}

type workflowInvocationInput struct {
	Goal       string              `json:"goal"`
	Actor      kernel.ActorRef     `json:"actor"`
	Thread     kernel.ThreadRef    `json:"thread"`
	Definition workflow.Definition `json:"definition"`
	Input      json.RawMessage     `json:"input"`
}

// StartTeamTurn starts Team without creating a placeholder Agent root.
func (runner *Runner) StartTeamTurn(ctx context.Context, request TeamTurnRequest) (Snapshot, error) {
	if runner == nil || runner.teams == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(teamInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Actor: request.Actor, Thread: request.Thread,
		Mode: request.Mode, Members: append([]team.Member(nil), request.Members...), Join: request.Join,
	})
	if err != nil {
		return Snapshot{}, err
	}
	prepared, err := runner.prepareTopLevelFeatureStart(
		ctx, request.StartRequest, CapabilityTeam, RuntimeCapabilityVersion, ExecutionTeam, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if prepared.replayed {
		if snapshot, handled, replayErr := runner.replayTopLevelFeatureStart(ctx, prepared); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	var frozen teamInvocationInput
	if err = json.Unmarshal(prepared.invocation.Input, &frozen); err != nil {
		return Snapshot{}, ErrConflict
	}
	runtimeSnapshot, startErr := runner.withPhysicalInvocationDispatch(prepared.runContext, prepared.invocation, "start", func() (kernel.Snapshot, error) {
		return runner.teams.StartRun(prepared.runContext, team.StartRequest{
			ID: prepared.invocation.ExecutionRefID, Actor: frozen.Actor, Thread: frozen.Thread,
			RequestID: firstNonEmpty(strings.TrimSpace(request.RequestID), prepared.turn.ID), Goal: frozen.Goal,
			Mode: frozen.Mode, Members: frozen.Members, Join: frozen.Join,
		})
	})
	return runner.finishTopLevelFeatureStart(ctx, prepared.turn, prepared.invocation, runtimeSnapshot, startErr)
}

// StartGroupChatTurn starts Group Chat without creating a placeholder Agent root.
func (runner *Runner) StartGroupChatTurn(ctx context.Context, request GroupChatTurnRequest) (Snapshot, error) {
	if runner == nil || runner.groupChats == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	speakerConfigs, speakerIDs, err := materializeGroupChatSpeakerConfigs(request.Config, request.Speakers)
	if err != nil {
		return Snapshot{}, err
	}
	runtimeRequest, input, err := materializeGroupChatPolicy(request, speakerConfigs, speakerIDs)
	if err != nil {
		return Snapshot{}, err
	}
	encodedInput, inputHash, err := marshalInvocationValue(input)
	if err != nil {
		return Snapshot{}, err
	}
	prepared, err := runner.prepareTopLevelFeatureStart(
		ctx, request.StartRequest, CapabilityGroupChat, RuntimeCapabilityVersion, ExecutionGroupChat, encodedInput, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if prepared.replayed {
		if snapshot, handled, replayErr := runner.replayTopLevelFeatureStart(ctx, prepared); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	var frozen groupChatInvocationInput
	if err = json.Unmarshal(prepared.invocation.Input, &frozen); err != nil {
		return Snapshot{}, ErrConflict
	}
	runtimeRequest.ID = prepared.invocation.ExecutionRefID
	runtimeRequest.RequestID = firstNonEmpty(strings.TrimSpace(request.RequestID), prepared.turn.ID)
	runtimeRequest.Goal, runtimeRequest.Actor, runtimeRequest.Thread = frozen.Goal, frozen.Actor, frozen.Thread
	runtimeRequest.SpeakerPolicy, runtimeRequest.MaxUtterances = frozen.SpeakerPolicy, frozen.MaxUtterances
	runtimeRequest.Participants, runtimeRequest.SpeakerConfigs = frozen.Participants, frozen.SpeakerConfigs
	runtimeRequest.DirectedSpeakerIDs, runtimeRequest.CandidateSpeakerIDs = frozen.DirectedSpeakerIDs, frozen.CandidateSpeakerIDs
	runtimeRequest.SelectorModel, runtimeRequest.SelectorModelOptions = frozen.SelectorModel, frozen.SelectorModelOptions
	runtimeSnapshot, startErr := runner.withPhysicalInvocationDispatch(prepared.runContext, prepared.invocation, "start", func() (kernel.Snapshot, error) {
		return runner.groupChats.StartRun(prepared.runContext, runtimeRequest)
	})
	return runner.finishTopLevelFeatureStart(ctx, prepared.turn, prepared.invocation, runtimeSnapshot, startErr)
}

func materializeGroupChatPolicy(
	request GroupChatTurnRequest,
	speakerConfigs []groupchat.SpeakerConfig,
	speakerIDs []string,
) (groupchat.StartRequest, groupChatInvocationInput, error) {
	runtimeRequest := groupchat.StartRequest{
		Actor: request.Actor, Thread: request.Thread, Goal: request.Goal,
		SpeakerPolicy:  request.SpeakerPolicy,
		Participants:   append([]groupchat.Participant(nil), request.Participants...),
		SpeakerConfigs: speakerConfigs, MaxUtterances: request.MaxUtterances,
	}
	input := groupChatInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Actor: request.Actor, Thread: request.Thread,
		SpeakerPolicy:  request.SpeakerPolicy,
		Participants:   append([]groupchat.Participant(nil), request.Participants...),
		SpeakerConfigs: speakerConfigs, MaxUtterances: request.MaxUtterances,
	}
	switch request.SpeakerPolicy {
	case groupchat.SpeakerDirected:
		runtimeRequest.DirectedSpeakerIDs = append([]string(nil), speakerIDs...)
		input.DirectedSpeakerIDs = append([]string(nil), speakerIDs...)
	case groupchat.SpeakerSelector:
		if strings.TrimSpace(request.Config.Model) == "" {
			return groupchat.StartRequest{}, groupChatInvocationInput{}, ErrInvalidRequest
		}
		runtimeRequest.CandidateSpeakerIDs = append([]string(nil), speakerIDs...)
		runtimeRequest.SelectorModel = strings.TrimSpace(request.Config.Model)
		runtimeRequest.SelectorModelOptions = append(json.RawMessage(nil), request.Config.ModelOptions...)
		input.CandidateSpeakerIDs = append([]string(nil), speakerIDs...)
		input.SelectorModel = runtimeRequest.SelectorModel
		input.SelectorModelOptions = append(json.RawMessage(nil), runtimeRequest.SelectorModelOptions...)
	case groupchat.SpeakerHandoff:
		if len(speakerIDs) < 2 {
			return groupchat.StartRequest{}, groupChatInvocationInput{}, ErrInvalidRequest
		}
		for index := range speakerConfigs {
			speakerConfigs[index].ToolKeys = normalizeStrings(append(speakerConfigs[index].ToolKeys, GroupChatHandoffToolKey))
		}
		runtimeRequest.SpeakerConfigs = speakerConfigs
		input.SpeakerConfigs = speakerConfigs
		runtimeRequest.CandidateSpeakerIDs = append([]string(nil), speakerIDs...)
		input.CandidateSpeakerIDs = append([]string(nil), speakerIDs...)
	default:
		return groupchat.StartRequest{}, groupChatInvocationInput{}, ErrInvalidRequest
	}
	return runtimeRequest, input, nil
}

func materializeGroupChatSpeakerConfigs(
	config ConfigSnapshot,
	selections []GroupChatSpeakerSelection,
) ([]groupchat.SpeakerConfig, []string, error) {
	if len(selections) == 0 || len(selections) > groupchat.MaxParticipantCount {
		return nil, nil, ErrInvalidRequest
	}
	configs := make([]groupchat.SpeakerConfig, 0, len(selections))
	ids := make([]string, 0, len(selections))
	seen := make(map[string]struct{}, len(selections))
	for _, selection := range selections {
		participantID := strings.TrimSpace(selection.ParticipantID)
		roleID := strings.TrimSpace(selection.RoleID)
		authorKind := strings.TrimSpace(selection.AuthorKind)
		authorID := strings.TrimSpace(selection.AuthorID)
		authorRevision := strings.TrimSpace(selection.AuthorRevision)
		authorName := strings.TrimSpace(selection.AuthorName)
		if participantID == "" || roleID == "" || authorKind == "" || authorID == "" ||
			authorRevision == "" || authorName == "" {
			return nil, nil, ErrInvalidRequest
		}
		if _, duplicate := seen[participantID]; duplicate {
			return nil, nil, ErrInvalidRequest
		}
		seen[participantID] = struct{}{}
		role, found := findRole(config.Roles, roleID)
		if !found {
			return nil, nil, toolsRoleUnavailable()
		}
		modelName := firstNonEmpty(role.Model, config.Model)
		modelOptions := append(json.RawMessage(nil), config.ModelOptions...)
		if len(role.ModelOptions) != 0 {
			modelOptions = append(json.RawMessage(nil), role.ModelOptions...)
		}
		memberID := strings.TrimSpace(role.MemberID)
		if memberID == "" {
			memberID = participantID
		}
		configs = append(configs, groupchat.SpeakerConfig{
			ParticipantID: participantID,
			AuthorKind:    authorKind, AuthorID: authorID, AuthorRevision: authorRevision, AuthorName: authorName,
			MemberID: memberID, MemberRevision: strings.TrimSpace(role.MemberRevision),
			Instructions: roleInstructions(role), Model: modelName, ModelOptions: modelOptions,
			ToolKeys: roleToolKeys(role), Limits: roleAgentLimits(config.Limits, role.Limits),
		})
		ids = append(ids, participantID)
	}
	return configs, ids, nil
}

// StartWorkflowTurn starts Workflow without a placeholder Agent root.
func (runner *Runner) StartWorkflowTurn(ctx context.Context, request WorkflowTurnRequest) (Snapshot, error) {
	if runner == nil || runner.workflows == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(workflowInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Actor: request.Actor, Thread: request.Thread,
		Definition: request.Definition, Input: append(json.RawMessage(nil), request.Input...),
	})
	if err != nil {
		return Snapshot{}, err
	}
	prepared, err := runner.prepareTopLevelFeatureStart(
		ctx, request.StartRequest, CapabilityWorkflow, RuntimeCapabilityVersion, ExecutionWorkflow, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if prepared.replayed {
		if snapshot, handled, replayErr := runner.replayTopLevelFeatureStart(ctx, prepared); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	var frozen workflowInvocationInput
	if err = json.Unmarshal(prepared.invocation.Input, &frozen); err != nil {
		return Snapshot{}, ErrConflict
	}
	runtimeSnapshot, startErr := runner.withPhysicalInvocationDispatch(prepared.runContext, prepared.invocation, "start", func() (kernel.Snapshot, error) {
		return runner.workflows.StartRun(prepared.runContext, workflow.StartRequest{
			ID: prepared.invocation.ExecutionRefID, Actor: frozen.Actor, Thread: frozen.Thread,
			RequestID: firstNonEmpty(strings.TrimSpace(request.RequestID), prepared.turn.ID), Goal: frozen.Goal,
			Definition: frozen.Definition, Input: frozen.Input,
		})
	})
	return runner.finishTopLevelFeatureStart(ctx, prepared.turn, prepared.invocation, runtimeSnapshot, startErr)
}

func (runner *Runner) prepareTopLevelFeatureStart(
	ctx context.Context,
	request StartRequest,
	capabilityKey string,
	definitionVersion string,
	executionClass ExecutionClass,
	input json.RawMessage,
	inputHash string,
) (topLevelFeatureStart, error) {
	envelope, err := runner.prepareTopLevelFeatureEnvelope(ctx, request, rootAdmission{
		ExecutionClass: executionClass, CapabilityKey: capabilityKey,
		DefinitionVersion: definitionVersion, InputHash: inputHash,
	})
	if err != nil {
		return topLevelFeatureStart{}, err
	}
	invocation, err := runner.prepareTopLevelFeatureInvocation(
		ctx, envelope, capabilityKey, definitionVersion, executionClass, input, inputHash,
	)
	if err != nil {
		return topLevelFeatureStart{}, err
	}
	if !envelope.created {
		if err := runner.repairStartItems(ctx, envelope.turn, invocation); err != nil {
			return topLevelFeatureStart{}, err
		}
		turn, runCtx, contextErr := runner.restoreOrBuildContext(
			ctx, envelope.turn, envelope.request.Context, envelope.config,
		)
		if contextErr != nil {
			// A replay's Context read is not terminal execution authority.
			// Another Start may own the same immutable Turn concurrently.
			return topLevelFeatureStart{}, contextErr
		}
		runCtx = withContextWindowBinding(runCtx, turn.ID, ContextWindowReadOnly)
		return topLevelFeatureStart{turn: turn, invocation: invocation, runContext: runCtx, replayed: true}, nil
	}
	turn, runCtx, err := runner.prepareTopLevelFeatureContext(ctx, envelope, invocation)
	if err != nil {
		return topLevelFeatureStart{}, err
	}
	return topLevelFeatureStart{turn: turn, invocation: invocation, runContext: runCtx}, nil
}

func (runner *Runner) prepareTopLevelFeatureEnvelope(
	ctx context.Context,
	request StartRequest,
	root rootAdmission,
) (topLevelFeatureEnvelope, error) {
	request, sessionID, turnID, err := normalizeStartRequest(request)
	if err != nil {
		return topLevelFeatureEnvelope{}, err
	}
	now := runner.clock.Now().UTC()
	resolvedConfig, err := runner.resolveConfigExecutionPolicy(request.Config)
	if err != nil {
		return topLevelFeatureEnvelope{}, err
	}
	request, err = sealStartAdmission(request, &resolvedConfig, root)
	if err != nil {
		return topLevelFeatureEnvelope{}, err
	}
	config, err := SealConfigSnapshot(turnID, resolvedConfig, now)
	if err != nil {
		return topLevelFeatureEnvelope{}, err
	}
	turn, created, err := runner.persistStartEnvelope(ctx, request, sessionID, turnID, config, now)
	if err != nil {
		return topLevelFeatureEnvelope{}, err
	}
	return topLevelFeatureEnvelope{request: request, turn: turn, config: config, created: created, now: now}, nil
}

func (runner *Runner) prepareTopLevelFeatureInvocation(
	ctx context.Context,
	envelope topLevelFeatureEnvelope,
	capabilityKey string,
	definitionVersion string,
	executionClass ExecutionClass,
	input json.RawMessage,
	inputHash string,
) (Invocation, error) {
	requestID := firstNonEmpty(strings.TrimSpace(envelope.request.RequestID), envelope.turn.ID)
	invocationID, err := InvocationID(envelope.turn.ID, "", capabilityKey, requestID)
	if err != nil {
		return Invocation{}, err
	}
	invocation, fresh, err := runner.store.CreateInvocation(ctx, Invocation{
		ID: invocationID, TurnID: envelope.turn.ID, CapabilityKey: capabilityKey,
		DefinitionVersion: strings.TrimSpace(definitionVersion), ExecutionClass: executionClass,
		Input: append(json.RawMessage(nil), input...), InputHash: inputHash,
		ExecutionRefID: CapabilityExecutionRefID(invocationID), Status: InvocationAccepted,
		Attempt: 1, OutputRefs: []HostRef{}, Revision: 1, CreatedAt: envelope.now, UpdatedAt: envelope.now,
	})
	if err != nil {
		return Invocation{}, err
	}
	if envelope.created && !fresh {
		return Invocation{}, ErrConflict
	}
	if err = runner.recordInvocationItem(ctx, invocation); err != nil {
		return Invocation{}, err
	}
	return invocation, nil
}

func (runner *Runner) prepareTopLevelFeatureContext(
	ctx context.Context,
	envelope topLevelFeatureEnvelope,
	invocation Invocation,
) (Turn, context.Context, error) {
	turn := envelope.turn
	request := envelope.request
	if err := runner.recordHostMessageItems(ctx, turn, request.InputMessage, request.OutputMessage); err != nil {
		return Turn{}, nil, err
	}
	runner.publishTurnStatus(ctx, turn, EventTurnStarted, false)
	updated, runCtx, err := runner.restoreOrBuildContext(
		ctx, turn, request.Context, envelope.config,
	)
	if err != nil {
		// No physical Feature Start has been granted. An unsuccessful
		// Context build cannot terminalize a concurrently admitted owner.
		return Turn{}, nil, err
	}
	return updated, withContextWindowBinding(runCtx, updated.ID, ContextWindowReadOnly), nil
}

func (runner *Runner) replayTopLevelFeatureStart(
	ctx context.Context,
	prepared topLevelFeatureStart,
) (Snapshot, bool, error) {
	if terminalTurnStatus(prepared.turn.Status) || terminalInvocationStatus(prepared.invocation.Status) {
		snapshot, err := runner.Refresh(ctx, prepared.turn.ID)
		return snapshot, true, err
	}
	pending, err := runner.resolvedInteractionClaimPending(ctx, prepared.turn.ID, "")
	if err != nil {
		return Snapshot{}, true, err
	}
	if pending {
		snapshot, loadErr := runner.loadSnapshot(ctx, prepared.turn, nil)
		return snapshot, true, loadErr
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, prepared.invocation.ExecutionRefID)
	if err == nil {
		if runtimeSnapshot.Run.Status != kernel.RunStatusRunning {
			snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, prepared.turn, prepared.invocation, runtimeSnapshot)
			return snapshot, true, syncErr
		}
		resumed, resumeErr := runner.resumeFeature(
			prepared.runContext, prepared.invocation, runtimeSnapshot.Run.Revision,
		)
		// A replay can race the execution owner that accepted the same durable
		// invocation. The Runtime CAS then correctly rejects this second resume,
		// but the Harness request is still an idempotent success: another worker
		// has advanced (or is advancing) the exact same run. Refresh from the
		// durable Runtime snapshot instead of failing the product projection.
		if errors.Is(resumeErr, kernel.ErrConflict) {
			snapshot, refreshErr := runner.Refresh(ctx, prepared.turn.ID)
			return snapshot, true, refreshErr
		}
		if resumed.Run.ID == "" {
			return Snapshot{}, true, resumeErr
		}
		snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, prepared.turn, prepared.invocation, resumed)
		return snapshot, true, normalizedFeatureStartError(
			prepared.invocation.ExecutionClass, resumed, resumeErr, syncErr,
		)
	}
	if errors.Is(err, kernel.ErrNotFound) && prepared.invocation.Status == InvocationAccepted {
		return Snapshot{}, false, nil
	}
	return Snapshot{}, true, err
}

func (runner *Runner) finishTopLevelFeatureStart(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
	startErr error,
) (Snapshot, error) {
	if runtimeSnapshot.Run.ID == "" {
		if errors.Is(startErr, kernel.ErrConflict) {
			recovered, recoverErr := runner.Refresh(ctx, turn.ID)
			if recoverErr == nil && recovered.Turn.Status == TurnCancelled {
				return recovered, nil
			}
		}
		if !isDispatchProvenNoEffect(startErr) {
			return Snapshot{}, startErr
		}
		failed, failErr := runner.failTopLevelInvocationAndTurn(ctx, turn, invocation, startErr)
		return failed, errors.Join(startErr, failErr)
	}
	snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, runtimeSnapshot)
	return snapshot, normalizedFeatureStartError(invocation.ExecutionClass, runtimeSnapshot, startErr, syncErr)
}

// WorkflowFeature is the narrow Dynamic Workflow Runtime capability consumed by Harness.
type WorkflowFeature interface {
	StartRun(context.Context, workflow.StartRequest) (kernel.Snapshot, error)
	Resume(context.Context, string, uint64) (kernel.Snapshot, error)
	ResolveWait(context.Context, string, uint64, json.RawMessage) (kernel.Snapshot, error)
}

// TeamInvocationRequest starts one exact first-party Team capability.
type TeamInvocationRequest struct {
	ParentItemID string
	RequestID    string
	Goal         string
	Mode         team.ExecutionMode
	Members      []team.Member
	Join         handoff.Join
}

// WorkflowInvocationRequest starts one exact first-party Dynamic Workflow capability.
type WorkflowInvocationRequest struct {
	ParentItemID string
	RequestID    string
	Goal         string
	Definition   workflow.Definition
	Input        json.RawMessage
}

type childInvocationContext struct {
	turn                 Turn
	root                 Invocation
	parentAttempts       []childAttemptBinding
	parentExecutionRefID string
	actor                kernel.ActorRef
	thread               kernel.ThreadRef
}

// StartTeamInvocation starts Team as one child Invocation in an existing Harness Turn.
func (runner *Runner) StartTeamInvocation(
	ctx context.Context,
	turnID string,
	request TeamInvocationRequest,
) (Snapshot, error) {
	if runner == nil || runner.teams == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(teamInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Mode: request.Mode, Members: append([]team.Member(nil), request.Members...), Join: request.Join,
	})
	if err != nil {
		return Snapshot{}, err
	}
	invocation, child, replayed, err := runner.beginChildInvocation(
		ctx, turnID, request.ParentItemID, request.RequestID,
		CapabilityTeam, RuntimeCapabilityVersion, ExecutionTeam, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if replayed {
		if snapshot, handled, replayErr := runner.replayChildInvocationStart(ctx, child.turn, invocation); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	var frozen teamInvocationInput
	if err = json.Unmarshal(invocation.Input, &frozen); err != nil {
		return Snapshot{}, ErrConflict
	}
	runtimeSnapshot, startErr := runner.withPhysicalInvocationDispatch(ctx, invocation, "start", func() (kernel.Snapshot, error) {
		return runner.teams.StartRun(ctx, team.StartRequest{
			ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread,
			RequestID: strings.TrimSpace(request.RequestID), Goal: frozen.Goal,
			Mode: frozen.Mode, Members: frozen.Members, Join: frozen.Join,
		})
	})
	return runner.finishChildInvocationStart(ctx, child.turn, invocation, runtimeSnapshot, startErr)
}

// StartWorkflowInvocation starts one exact compiled Dynamic Workflow as a child Invocation.
func (runner *Runner) StartWorkflowInvocation(
	ctx context.Context,
	turnID string,
	request WorkflowInvocationRequest,
) (Snapshot, error) {
	if runner == nil || runner.workflows == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	input, inputHash, err := marshalInvocationValue(workflowInvocationInput{
		Goal: strings.TrimSpace(request.Goal), Definition: request.Definition, Input: append(json.RawMessage(nil), request.Input...),
	})
	if err != nil {
		return Snapshot{}, err
	}
	invocation, child, replayed, err := runner.beginChildInvocation(
		ctx, turnID, request.ParentItemID, request.RequestID,
		CapabilityWorkflow, RuntimeCapabilityVersion, ExecutionWorkflow, input, inputHash,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if replayed {
		if snapshot, handled, replayErr := runner.replayChildInvocationStart(ctx, child.turn, invocation); handled || replayErr != nil {
			return snapshot, replayErr
		}
	}
	var frozen workflowInvocationInput
	if err = json.Unmarshal(invocation.Input, &frozen); err != nil {
		return Snapshot{}, ErrConflict
	}
	runtimeSnapshot, startErr := runner.withPhysicalInvocationDispatch(ctx, invocation, "start", func() (kernel.Snapshot, error) {
		return runner.workflows.StartRun(ctx, workflow.StartRequest{
			ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread,
			RequestID: strings.TrimSpace(request.RequestID), Goal: frozen.Goal,
			Definition: frozen.Definition, Input: frozen.Input,
		})
	})
	return runner.finishChildInvocationStart(ctx, child.turn, invocation, runtimeSnapshot, startErr)
}

// RefreshInvocation reconciles one child Runtime execution into its durable Harness Invocation.
// Root Invocations belong to Refresh, which also synchronizes the owning Turn.
func (runner *Runner) RefreshInvocation(ctx context.Context, invocationID string) (Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(invocation.ParentItemID) == "" {
		return Snapshot{}, ErrInvalidRequest
	}
	if invocation.ExecutionClass == ExecutionApplication {
		turn, loadErr := runner.store.GetTurn(ctx, invocation.TurnID)
		if loadErr != nil {
			return Snapshot{}, loadErr
		}
		return runner.refreshApplicationInvocation(ctx, invocation, turn)
	}
	// A routed Handoff belongs to its original ChildRunner, not an arbitrary
	// Run with the same ID in the Harness Kernel. Only an explicitly fixed
	// same-Kernel route may be observed through the local Invocation reader.
	// A remote/dynamic route needs its own ChildRunner-owned observation.
	if isHandoffDelegationInvocation(invocation) {
		coordinator, ok := runner.handoffs.(*handoff.Coordinator)
		if !ok || !coordinator.OwnsLocalKernelRunner(runner.runtime) {
			return Snapshot{}, ErrHandoffCancellationUnverified
		}
	}
	invocation, turn, runtimeSnapshot, err := runner.loadInvocationRuntime(ctx, invocationID)
	if err != nil {
		return Snapshot{}, err
	}
	if isHandoffDelegationInvocation(invocation) {
		if _, _, err := runner.localHandoffCancellationAuthority(ctx, invocation, runtimeSnapshot); err != nil {
			return Snapshot{}, err
		}
	}
	pending, err := runner.resolvedInteractionClaimPending(ctx, turn.ID, "")
	if err != nil {
		return Snapshot{}, err
	}
	if pending {
		return runner.loadSnapshot(ctx, turn, &runtimeSnapshot)
	}
	return runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
}

// ResumeInvocation resumes a running child Feature through the exact typed adapter selected at creation.
// Root Agent continuations must use ResumeTurn; other roots remain Feature-owned.
func (runner *Runner) ResumeInvocation(ctx context.Context, invocationID string) (Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(invocation.ParentItemID) == "" || isHandoffDelegationInvocation(invocation) {
		// A routed Handoff child has no native Feature Resume adapter.
		// Reject before minting a physical dispatch grant.
		return Snapshot{}, ErrInvalidRequest
	}
	if invocation.ExecutionClass == ExecutionApplication {
		turn, loadErr := runner.store.GetTurn(ctx, invocation.TurnID)
		if loadErr != nil {
			return Snapshot{}, loadErr
		}
		return runner.refreshApplicationInvocation(ctx, invocation, turn)
	}
	invocation, turn, runtimeSnapshot, err := runner.loadInvocationRuntime(ctx, invocationID)
	if err != nil {
		return Snapshot{}, err
	}
	pending, pendingErr := runner.resolvedInteractionClaimPending(ctx, turn.ID, "")
	if pendingErr != nil {
		return Snapshot{}, pendingErr
	}
	if pending {
		return runner.loadSnapshot(ctx, turn, &runtimeSnapshot)
	}
	if runtimeSnapshot.Run.Status != kernel.RunStatusRunning {
		return runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
	}
	if err = runner.requireFrozenAdmission(ctx, turn); err != nil {
		return Snapshot{}, err
	}
	resumed, resumeErr := runner.resumeFeature(ctx, invocation, runtimeSnapshot.Run.Revision)
	if resumed.Run.ID == "" {
		return Snapshot{}, resumeErr
	}
	snapshot, syncErr := runner.syncChildInvocationSnapshot(ctx, turn, invocation, resumed)
	return snapshot, errors.Join(resumeErr, syncErr)
}

// RetryInvocation starts the next durable attempt of one failed or cancelled
// Feature Invocation. Replaying a partially-started retry repairs the same
// attempt rather than allocating another execution identity.
func (runner *Runner) RetryInvocation(ctx context.Context, invocationID string) (Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Snapshot{}, err
	}
	if len(invocation.Input) == 0 || isHandoffDelegationInvocation(invocation) {
		// A Handoff child was admitted through a frozen external route.
		// Rotating its Attempt would break the original ChildRunID and
		// must not fall through to the native Agent retry adapter.
		return Snapshot{}, ErrConflict
	}
	turn, err := runner.store.GetTurn(ctx, invocation.TurnID)
	if err != nil || (strings.TrimSpace(invocation.ParentItemID) != "" && terminalTurnStatus(turn.Status)) {
		return Snapshot{}, errors.Join(ErrConflict, err)
	}
	if err = runner.requireFrozenAdmission(ctx, turn); err != nil {
		return Snapshot{}, err
	}
	if invocation.Attempt > 1 && !retryableInvocationStatus(invocation.Status) {
		return runner.replayRetriedInvocation(ctx, turn, invocation)
	}
	if !retryableInvocationStatus(invocation.Status) {
		return Snapshot{}, ErrConflict
	}
	// A retry attempt commits a new physical Run identity. For a root,
	// its Actor and Kernel Thread must already be sealed in the Invocation:
	// never reconstruct missing authority from Session.HostThread/Actor.
	if strings.TrimSpace(invocation.ParentItemID) == "" {
		if _, _, err := frozenTopLevelInvocationIdentity(invocation); err != nil {
			return Snapshot{}, err
		}
	} else {
		// A child retry has no authority to rotate its Run when the parent
		// Kernel Run or any intermediate Invocation has already terminated.
		// Application children have no Kernel Run of their own and can be
		// nested under an Application root, so only native Features require
		// the root Runtime context in addition to the shared ancestry guard.
		if invocation.ExecutionClass != ExecutionApplication {
			if _, err := runner.childInvocationContext(ctx, turn.ID, invocation.ParentItemID, invocation.ExecutionClass); err != nil {
				return Snapshot{}, err
			}
		}
		if err := runner.requireLiveInvocationParents(ctx, invocation); err != nil {
			return Snapshot{}, err
		}
	}
	return runner.startNextInvocationAttempt(ctx, turn, invocation)
}

func (runner *Runner) startNextInvocationAttempt(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	nextAttempt := invocation.Attempt + 1
	nextRef := invocationExecutionRefID(invocation.ID, invocation.ExecutionClass, nextAttempt)
	retried, err := runner.store.RetryInvocation(ctx, invocation.ID, invocation.Revision, nextRef, runner.clock.Now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, retried); err != nil {
		return Snapshot{}, err
	}
	turn, err = runner.store.GetTurn(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.startRetriedInvocation(ctx, turn, retried)
}

func (runner *Runner) replayRetriedInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	if invocation.ExecutionClass == ExecutionApplication {
		if invocation.Status == InvocationAccepted || invocation.Status == InvocationRunning {
			return runner.executeApplicationInvocation(ctx, turn, invocation)
		}
		return runner.loadSnapshot(ctx, turn, nil)
	}
	loaded, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err == nil {
		if strings.TrimSpace(invocation.ParentItemID) == "" {
			return runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, loaded)
		}
		return runner.syncChildInvocationSnapshot(ctx, turn, invocation, loaded)
	}
	if !errors.Is(err, kernel.ErrNotFound) || invocation.Status != InvocationAccepted {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, invocation); err != nil {
		return Snapshot{}, err
	}
	return runner.startRetriedInvocation(ctx, turn, invocation)
}

func (runner *Runner) startRetriedInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	if invocation.ExecutionClass == ExecutionApplication {
		return runner.executeApplicationInvocation(ctx, turn, invocation)
	}
	if strings.TrimSpace(invocation.ParentItemID) == "" {
		return runner.startRetriedTopLevelInvocation(ctx, turn, invocation)
	}
	child, err := runner.childInvocationContext(ctx, turn.ID, invocation.ParentItemID, invocation.ExecutionClass)
	if err != nil {
		return Snapshot{}, err
	}
	if runner.relations != nil {
		if _, err = runner.relations.Ensure(ctx, runrelation.Draft{
			ParentRunID: child.parentExecutionRefID, ChildRunID: invocation.ExecutionRefID,
			Kind: runrelation.KindCapability, OwnerNodeID: invocationRelationOwnerID(invocation),
		}); err != nil {
			return Snapshot{}, err
		}
	}
	runtimeSnapshot, startErr := runner.startInvocationAttempt(ctx, invocation, child)
	return runner.finishChildInvocationStart(ctx, turn, invocation, runtimeSnapshot, startErr)
}

func (runner *Runner) startRetriedTopLevelInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	actor, thread, err := frozenTopLevelInvocationIdentity(invocation)
	if err != nil {
		return Snapshot{}, err
	}
	runCtx, err := runner.topLevelRetryContext(ctx, turn)
	if err != nil {
		return Snapshot{}, err
	}
	runner.publishTurnStatus(ctx, turn, EventTurnStarted, false)
	runtimeSnapshot, startErr := runner.startInvocationAttempt(runCtx, invocation, childInvocationContext{
		turn: turn, actor: actor, thread: thread,
	})
	return runner.finishTopLevelFeatureStart(ctx, turn, invocation, runtimeSnapshot, startErr)
}

// frozenTopLevelInvocationIdentity reads the exact Actor/Thread committed in
// the Invocation. The Session is NOT a fallback execution authority: its
// HostThread can differ from the Kernel Thread, and reconstructing missing
// fields during retry silently changes a previously frozen physical Run.
func frozenTopLevelInvocationIdentity(invocation Invocation) (kernel.ActorRef, kernel.ThreadRef, error) {
	if len(invocation.Input) == 0 || !json.Valid(invocation.Input) ||
		hashInvocationBytes(invocation.Input) != invocation.InputHash {
		return kernel.ActorRef{}, kernel.ThreadRef{}, ErrConflict
	}
	var input struct {
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}
	if err := json.Unmarshal(invocation.Input, &input); err != nil ||
		!validActor(input.Actor) || strings.TrimSpace(input.Thread.Kind) == "" ||
		strings.TrimSpace(input.Thread.ID) == "" {
		return kernel.ActorRef{}, kernel.ThreadRef{}, ErrConflict
	}
	return input.Actor, input.Thread, nil
}

func (runner *Runner) topLevelRetryContext(ctx context.Context, turn Turn) (context.Context, error) {
	checkpointID := strings.TrimSpace(turn.ContextCheckpointID)
	if checkpointID == "" {
		return ctx, nil
	}
	checkpoint, err := runner.store.GetContextCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if checkpoint.ScopeID != turn.SessionID || !sameContextCheckpointRef(checkpoint, turn.ContextRef) || checkpoint.ID != checkpointID {
		return nil, ErrConflict
	}
	return withContextCheckpoint(ctx, checkpoint), nil
}

func (runner *Runner) startInvocationAttempt(
	ctx context.Context,
	invocation Invocation,
	child childInvocationContext,
) (kernel.Snapshot, error) {
	requestID := stableID("hretry", invocation.ID, attemptString(invocation.Attempt))
	return runner.withPhysicalInvocationDispatch(ctx, invocation, "start-retry", func() (kernel.Snapshot, error) {
		switch invocation.ExecutionClass {
		case ExecutionAgent:
			return runner.startRetriedAgent(ctx, invocation, child, requestID)
		case ExecutionTeam:
			return runner.startRetriedTeam(ctx, invocation, child, requestID)
		case ExecutionGroupChat:
			return runner.startRetriedGroupChat(ctx, invocation, child, requestID)
		case ExecutionWorkflow:
			return runner.startRetriedWorkflow(ctx, invocation, child, requestID)
		case ExecutionApplication:
			return kernel.Snapshot{}, ErrInvalidRequest
		default:
			return kernel.Snapshot{}, ErrInvalidRequest
		}
	})
}

func (runner *Runner) startRetriedAgent(ctx context.Context, invocation Invocation, child childInvocationContext, requestID string) (kernel.Snapshot, error) {
	// A second defense at the physical entry: a delegated child is never
	// a generic Agent retry, even if an earlier Store operation already
	// rotated its identity. No implicit local replacement of the route.
	if isHandoffDelegationInvocation(invocation) {
		return kernel.Snapshot{}, ErrConflict
	}
	var input agentInvocationInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return kernel.Snapshot{}, ErrConflict
	}
	config, err := runner.store.GetConfigSnapshot(ctx, child.turn.ConfigSnapshotID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.agent.StartRun(ctx, agent.StartRequest{
		ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread,
		RequestID: requestID, Goal: input.Goal, Model: config.Model,
		ModelOptions: append(json.RawMessage(nil), config.ModelOptions...), ToolKeys: append([]string(nil), config.ToolKeys...),
		RequiredToolKeys: append([]string(nil), input.RequiredToolKeys...),
		InitialToolKeys:  append([]string(nil), input.InitialToolKeys...), Limits: config.Limits,
	})
}

func (runner *Runner) startRetriedTeam(ctx context.Context, invocation Invocation, child childInvocationContext, requestID string) (kernel.Snapshot, error) {
	if runner.teams == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	var input teamInvocationInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return kernel.Snapshot{}, ErrConflict
	}
	return runner.teams.StartRun(ctx, team.StartRequest{ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread, RequestID: requestID, Goal: input.Goal, Mode: input.Mode, Members: append([]team.Member(nil), input.Members...), Join: input.Join})
}

func (runner *Runner) startRetriedGroupChat(ctx context.Context, invocation Invocation, child childInvocationContext, requestID string) (kernel.Snapshot, error) {
	if runner.groupChats == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	var input groupChatInvocationInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return kernel.Snapshot{}, ErrConflict
	}
	return runner.groupChats.StartRun(ctx, groupchat.StartRequest{
		ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread, RequestID: requestID, Goal: input.Goal,
		SpeakerPolicy:        input.SpeakerPolicy,
		Participants:         append([]groupchat.Participant(nil), input.Participants...),
		SpeakerConfigs:       append([]groupchat.SpeakerConfig(nil), input.SpeakerConfigs...),
		DirectedSpeakerIDs:   append([]string(nil), input.DirectedSpeakerIDs...),
		CandidateSpeakerIDs:  append([]string(nil), input.CandidateSpeakerIDs...),
		SelectorModel:        input.SelectorModel,
		SelectorModelOptions: append(json.RawMessage(nil), input.SelectorModelOptions...),
		MaxUtterances:        input.MaxUtterances,
	})
}

func (runner *Runner) startRetriedWorkflow(ctx context.Context, invocation Invocation, child childInvocationContext, requestID string) (kernel.Snapshot, error) {
	if runner.workflows == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	var input workflowInvocationInput
	if err := json.Unmarshal(invocation.Input, &input); err != nil {
		return kernel.Snapshot{}, ErrConflict
	}
	return runner.workflows.StartRun(ctx, workflow.StartRequest{ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread, RequestID: requestID, Goal: input.Goal, Definition: input.Definition, Input: append(json.RawMessage(nil), input.Input...)})
}

// CancelInvocation cancels one child Runtime Feature by its durable Invocation identity.
func (runner *Runner) CancelInvocation(ctx context.Context, invocationID, reason string) (snapshot Snapshot, err error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(invocation.ParentItemID) == "" {
		return Snapshot{}, ErrConflict
	}
	turn, err := runner.store.GetTurn(ctx, invocation.TurnID)
	if err != nil {
		return Snapshot{}, err
	}
	if invocation.ExecutionClass == ExecutionApplication {
		return runner.cancelApplicationInvocation(ctx, turn, invocation, reason)
	}
	intent, err := runner.cancelPreflightWithIntent(ctx, turn, invocation.ExecutionRefID,
		func() error {
			_, checkErr := runner.inspectRelatedCancellation(ctx, turn, invocation.ExecutionRefID)
			return checkErr
		},
		func(writeAttempted *bool) error {
			return runner.fenceRelatedCancellation(ctx, turn, invocation.ExecutionRefID, writeAttempted)
		},
	)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() {
		if err == nil {
			err = runner.finishCancellationFenceAfterRequest(ctx, intent)
		}
	}()
	runtimeSnapshot, found, err := runner.cancelRuntimeRun(ctx, invocation.ExecutionRefID, reason)
	if err != nil {
		return Snapshot{}, runner.releaseNoEffectCancellation(ctx, turn, intent, invocation.ExecutionRefID, err)
	}
	if !found {
		runtimeSnapshot = cancelledRuntimeSnapshot(invocation.ExecutionRefID, reason)
	}
	runs := map[string]kernel.Snapshot{invocation.ExecutionRefID: runtimeSnapshot}
	if runtimeSnapshot.Run.Status == kernel.RunStatusCancelled {
		descendants, cancelErr := runner.cancelRelatedRuns(ctx, invocation.ExecutionRefID, nil, reason)
		if cancelErr != nil {
			return Snapshot{}, cancelErr
		}
		for runID, snapshot := range descendants {
			runs[runID] = snapshot
		}
	}
	if err = runner.syncInvocationRuns(ctx, turn, runs); err != nil {
		return Snapshot{}, err
	}
	if err = runner.syncTerminalDelegations(ctx, turn, runs); err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) beginChildInvocation(
	ctx context.Context,
	turnID, parentItemID, requestID, capabilityKey, definitionVersion string,
	executionClass ExecutionClass,
	input json.RawMessage,
	inputHash string,
) (Invocation, childInvocationContext, bool, error) {
	child, err := runner.childInvocationContext(ctx, turnID, parentItemID, executionClass)
	if err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	// Freeze the root Attempt in the child's first durable admission. A
	// replay against a later root Attempt must never revive an older child.
	input, inputHash, err = sealChildRootInput(input, inputHash, child.root, child.parentAttempts...)
	if err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	invocationID, err := InvocationID(turnID, parentItemID, capabilityKey, requestID)
	if err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	now := runner.clock.Now().UTC()
	value := Invocation{
		ID: invocationID, TurnID: child.turn.ID, ParentItemID: strings.TrimSpace(parentItemID),
		CapabilityKey: capabilityKey, DefinitionVersion: strings.TrimSpace(definitionVersion), ExecutionClass: executionClass,
		Input: append(json.RawMessage(nil), input...), InputHash: inputHash,
		ExecutionRefID: CapabilityExecutionRefID(invocationID), Status: InvocationAccepted,
		Attempt: 1, OutputRefs: []HostRef{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, fresh, err := runner.store.CreateInvocation(ctx, value)
	if err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	if err = runner.recordInvocationItem(ctx, created); err != nil {
		return Invocation{}, childInvocationContext{}, false, err
	}
	if runner.relations != nil && executionClass != ExecutionApplication {
		if _, err = runner.relations.Ensure(ctx, runrelation.Draft{
			ParentRunID: child.parentExecutionRefID, ChildRunID: created.ExecutionRefID,
			Kind: runrelation.KindCapability, OwnerNodeID: invocationRelationOwnerID(created),
		}); err != nil {
			return Invocation{}, childInvocationContext{}, false, err
		}
	}
	return created, child, !fresh, nil
}

func (runner *Runner) childInvocationContext(
	ctx context.Context,
	turnID, parentItemID string,
	executionClass ExecutionClass,
) (childInvocationContext, error) {
	// Child admission cannot silently become a second root by omitting its
	// parent. The Store root-uniqueness constraint is not a routing policy.
	if strings.TrimSpace(parentItemID) == "" {
		return childInvocationContext{}, ErrInvalidRequest
	}
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil || terminalTurnStatus(turn.Status) {
		return childInvocationContext{}, errors.Join(ErrConflict, err)
	}
	if err = runner.requireFrozenAdmission(ctx, turn); err != nil {
		return childInvocationContext{}, err
	}
	if err = runner.validateParentItem(ctx, turn.ID, parentItemID); err != nil {
		return childInvocationContext{}, err
	}
	parent, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil || strings.TrimSpace(parent.ExecutionRefID) == "" ||
		strings.TrimSpace(parent.ParentItemID) != "" || !validExecutionClass(parent.ExecutionClass) ||
		!validInvocationStatus(parent.Status) || terminalInvocationStatus(parent.Status) {
		return childInvocationContext{}, errors.Join(ErrConflict, err)
	}
	parentAttempts, err := runner.freezeChildParentAttempts(ctx, turn.ID, parentItemID, parent)
	if err != nil {
		return childInvocationContext{}, err
	}
	if parent.ExecutionClass == ExecutionApplication {
		// A Host Application has no Kernel Run. Only another Application
		// child can use its Invocation as the durable execution owner.
		if executionClass != ExecutionApplication || unclaimedApplicationParent(parent) {
			return childInvocationContext{}, ErrConflict
		}
		return childInvocationContext{turn: turn, root: parent, parentAttempts: parentAttempts}, nil
	}
	if runner.runtime == nil {
		return childInvocationContext{}, ErrConflict
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, parent.ExecutionRefID)
	if err != nil || runtimeSnapshot.Run.ID != parent.ExecutionRefID || terminalRuntimeStatus(runtimeSnapshot.Run.Status) {
		return childInvocationContext{}, errors.Join(ErrConflict, err)
	}
	return childInvocationContext{
		turn: turn, root: parent, parentAttempts: parentAttempts, parentExecutionRefID: parent.ExecutionRefID,
		actor: runtimeSnapshot.Run.Actor, thread: runtimeSnapshot.Run.Thread,
	}, nil
}

func (runner *Runner) validateParentItem(ctx context.Context, turnID, parentItemID string) error {
	parentItemID = strings.TrimSpace(parentItemID)
	if parentItemID == "" {
		return nil // An Interaction can target its Invocation directly.
	}
	if runner == nil || runner.store == nil || strings.TrimSpace(turnID) == "" {
		return ErrInvalidRequest
	}
	// Admission and dispatch must use the same globally indexed Item
	// identity, not independently interpret a full Turn Timeline.
	item, err := runner.store.GetItem(ctx, parentItemID)
	if errors.Is(err, ErrNotFound) {
		return ErrInvalidRequest
	}
	if err != nil {
		return err
	}
	if item.ID != parentItemID || item.Seq == 0 {
		return ErrConflict
	}
	if item.TurnID != strings.TrimSpace(turnID) {
		return ErrInvalidRequest
	}
	return nil
}

func (runner *Runner) replayChildInvocationStart(
	ctx context.Context,
	turn Turn,
	invocation Invocation,

) (Snapshot, bool, error) {
	runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err == nil {
		snapshot, syncErr := runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
		return snapshot, true, syncErr
	}
	if errors.Is(err, kernel.ErrNotFound) && invocation.Status == InvocationAccepted {
		return Snapshot{}, false, nil
	}
	if errors.Is(err, kernel.ErrNotFound) && terminalInvocationStatus(invocation.Status) {
		snapshot, loadErr := runner.loadSnapshot(ctx, turn, nil)
		return snapshot, true, loadErr
	}
	return Snapshot{}, true, err
}

func (runner *Runner) finishChildInvocationStart(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
	startErr error,
) (Snapshot, error) {
	if runtimeSnapshot.Run.ID == "" {
		if !isDispatchProvenNoEffect(startErr) {
			// An accepted child with an unknown remote Start must retain its
			// execution reference and unresolved grant, not become retryable.
			return Snapshot{}, startErr
		}
		return runner.failChildInvocation(ctx, turn, invocation, startErr)
	}
	snapshot, syncErr := runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
	return snapshot, normalizedFeatureStartError(invocation.ExecutionClass, runtimeSnapshot, startErr, syncErr)
}

func normalizedFeatureStartError(
	executionClass ExecutionClass,
	runtimeSnapshot kernel.Snapshot,
	startErr error,
	syncErr error,
) error {
	if syncErr != nil {
		return errors.Join(startErr, syncErr)
	}
	if startErr == nil || !pendingRuntimeStatus(runtimeSnapshot.Run.Status) {
		return startErr
	}
	if expectedFeaturePendingError(executionClass, startErr) {
		return nil
	}
	return startErr
}

func expectedFeaturePendingError(executionClass ExecutionClass, err error) bool {
	switch executionClass {
	case ExecutionTeam:
		return errors.Is(err, team.ErrMemberPending)
	case ExecutionGroupChat:
		return errors.Is(err, groupchat.ErrSpeakerPending) || errors.Is(err, groupchat.ErrSelectorPending) ||
			errors.Is(err, groupchat.ErrSelectorInvocationBusy)
	case ExecutionWorkflow:
		return errors.Is(err, workflow.ErrEffectPending) || errors.Is(err, workflow.ErrWaitPending) ||
			errors.Is(err, workflow.ErrSegmentYielded)
	case ExecutionAgent, ExecutionApplication:
		return false
	default:
		return false
	}
}

func pendingRuntimeStatus(status kernel.RunStatus) bool {
	return status == kernel.RunStatusRunning || status == kernel.RunStatusWaitingInput || status == kernel.RunStatusPausedBudget
}

func (runner *Runner) failChildInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	cause error,
) (Snapshot, error) {
	invocation.Status = InvocationFailed
	invocation.ErrorCode = "harness.capability_start_failed"
	invocation.ErrorDetail = "capability execution did not start"
	if cause != nil {
		invocation.ErrorDetail = strings.TrimSpace(cause.Error())
	}
	invocation.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateInvocation(ctx, invocation, invocation.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, updated); err != nil {
		return Snapshot{}, err
	}
	snapshot, loadErr := runner.loadSnapshot(ctx, turn, nil)
	return snapshot, errors.Join(cause, loadErr)
}

func (runner *Runner) loadInvocationRuntime(
	ctx context.Context,
	invocationID string,
) (Invocation, Turn, kernel.Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Invocation{}, Turn{}, kernel.Snapshot{}, err
	}
	turn, err := runner.store.GetTurn(ctx, invocation.TurnID)
	if err != nil {
		return Invocation{}, Turn{}, kernel.Snapshot{}, err
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	return invocation, turn, runtimeSnapshot, err
}

func (runner *Runner) resumeFeature(
	ctx context.Context,
	invocation Invocation,
	expectedRevision uint64,
) (kernel.Snapshot, error) {
	if err := runner.requireUnclaimedInteractionContinuation(ctx, invocation.TurnID, invocation.ID); err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.withPhysicalInvocationDispatch(ctx, invocation, "resume", func() (kernel.Snapshot, error) {
		switch invocation.ExecutionClass {
		case ExecutionTeam:
			if runner.teams == nil {
				return kernel.Snapshot{}, ErrInvalidRequest
			}
			return runner.teams.Resume(ctx, invocation.ExecutionRefID, expectedRevision)
		case ExecutionGroupChat:
			if runner.groupChats == nil {
				return kernel.Snapshot{}, ErrInvalidRequest
			}
			return runner.groupChats.Resume(ctx, invocation.ExecutionRefID, expectedRevision)
		case ExecutionWorkflow:
			if runner.workflows == nil {
				return kernel.Snapshot{}, ErrInvalidRequest
			}
			return runner.workflows.Resume(ctx, invocation.ExecutionRefID, expectedRevision)
		case ExecutionAgent, ExecutionApplication:
			return kernel.Snapshot{}, ErrInvalidRequest
		default:
			return kernel.Snapshot{}, ErrInvalidRequest
		}
	})
}

func (runner *Runner) resumeInvocationExecution(
	ctx context.Context,
	invocation Invocation,
	expectedRevision uint64,
) (kernel.Snapshot, error) {
	if isHandoffDelegationInvocation(invocation) {
		// Interaction resolution also enters here. It may not turn a routed
		// delegated child into a locally resumed native Agent Run.
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	turn, err := runner.store.GetTurn(ctx, invocation.TurnID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if err = runner.requireFrozenAdmission(ctx, turn); err != nil {
		return kernel.Snapshot{}, err
	}
	scopeID := invocation.ID
	if invocation.ParentItemID == "" {
		scopeID = "" // Root resume must also respect pending child continuations.
	}
	if err = runner.requireUnclaimedInteractionContinuation(ctx, turn.ID, scopeID); err != nil {
		return kernel.Snapshot{}, err
	}
	if invocation.ExecutionClass == ExecutionAgent {
		resumer, ok := runner.agent.(agentResumer)
		if !ok {
			return kernel.Snapshot{}, ErrInvalidRequest
		}
		resumed, resumeErr := runner.withPhysicalInvocationDispatch(ctx, invocation, "resume", func() (kernel.Snapshot, error) {
			return resumer.Resume(ctx, invocation.ExecutionRefID, expectedRevision)
		})
		if errors.Is(resumeErr, ErrConflict) && resumed.Run.ID == "" {
			// A crashed native Agent Start may retain a Harness grant even
			// after its Kernel Run acquired a sticky execution Epoch. Let
			// only the first-party Kernel lease arbitrate that recovery.
			return runner.resumeNativeFencedAgentAfterInterruptedStart(
				ctx, invocation, expectedRevision, resumer, resumeErr)
		}
		return resumed, resumeErr
	}
	return runner.resumeFeature(ctx, invocation, expectedRevision)
}

func (runner *Runner) syncChildInvocationSnapshot(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
) (Snapshot, error) {
	// Feature callbacks can also synchronize an independently advancing
	// Kernel Run. Do not overwrite a claimed, unverified business response
	// solely because its parent Turn was already projected as running.
	pending, err := runner.resolvedInteractionClaimPending(ctx, turn.ID, invocation.ID)
	if err != nil {
		return Snapshot{}, err
	}
	if pending {
		return runner.loadSnapshot(ctx, turn, &runtimeSnapshot)
	}
	if runtimeSnapshotConflicts(invocation, runtimeSnapshot) {
		return Snapshot{}, ErrConflict
	}
	status, err := runner.invocationRuntimeStatus(ctx, invocation, runtimeSnapshot)
	if err != nil {
		return Snapshot{}, err
	}
	invocation, err = runner.syncInvocationProjection(ctx, invocation, runtimeSnapshot, status)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.projectChildInvocationOutcome(ctx, invocation, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	if invocation.ExecutionClass == ExecutionAgent {
		if err = runner.recordApprovalRequestItem(ctx, turn, invocation, runtimeSnapshot); err != nil {
			return Snapshot{}, err
		}
	}
	if err = runner.projectWorkflowWaitInteraction(ctx, turn, invocation, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	if err = runner.syncSubtaskItems(ctx, turn); err != nil {
		return Snapshot{}, err
	}
	turn, err = runner.store.GetTurn(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) projectChildInvocationOutcome(
	ctx context.Context,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
) error {
	if invocation.Status == InvocationCompleted && runtimeSnapshot.Result != nil {
		payload, err := json.Marshal(struct {
			ContentType string          `json:"contentType"`
			Content     json.RawMessage `json:"content"`
		}{runtimeSnapshot.Result.ContentType, runtimeSnapshot.Result.Content})
		if err != nil {
			return err
		}
		now := runner.clock.Now().UTC()
		_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
			ID: stableID("hico", invocation.ID, "result", attemptString(invocation.Attempt)), TurnID: invocation.TurnID,
			Kind: ItemArtifact, Status: ItemCompleted, RunID: invocation.ExecutionRefID,
			InvocationID: invocation.ID, ParentItemID: invocationLifecycleItemID(invocation, InvocationAccepted, 1),
			Payload: payload, CreatedAt: now, UpdatedAt: now,
		})
		return err
	}
	if invocation.Status != InvocationFailed && invocation.Status != InvocationCancelled {
		return nil
	}
	payload, err := json.Marshal(struct {
		ErrorCode   string `json:"errorCode,omitempty"`
		ErrorDetail string `json:"errorDetail,omitempty"`
	}{invocation.ErrorCode, invocation.ErrorDetail})
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID: stableID("hicd", invocation.ID, string(invocation.Status), attemptString(invocation.Attempt)), TurnID: invocation.TurnID,
		Kind: ItemDiagnostic, Status: invocationItemStatus(invocation.Status), RunID: invocation.ExecutionRefID,
		InvocationID: invocation.ID, ParentItemID: invocationLifecycleItemID(invocation, InvocationAccepted, 1),
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}
