package team

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

const (
	RunKind          kernel.RunKind    = "team"
	CapabilityRunner kernel.Capability = "team.runner"
	ResultKind                         = "team_result"
)

var (
	ErrInvalidRequest = errors.New("invalid team request")
	ErrMemberPending  = errors.New("team member run is not terminal")
	ErrTeamFailed     = errors.New("team execution failed")
	ErrTeamTerminal   = errors.New("team run is terminal")
)

// ExecutionMode controls member launch ordering and is owned by Team.
type ExecutionMode string

const (
	ExecutionSequential ExecutionMode = "sequential"
	ExecutionParallel   ExecutionMode = "parallel"
)

// Member defines one specialist task in a Team.
type Member struct {
	ID           string          `json:"id"`
	Goal         string          `json:"goal"`
	Model        string          `json:"model,omitempty"`
	ModelOptions json.RawMessage `json:"modelOptions,omitempty"`
	ToolKeys     []string        `json:"toolKeys,omitempty"`
}

func publicResult(state executionState) Result {
	result := Result{
		Kind: ResultKind, Mode: state.Mode,
		Members:   make([]MemberResult, 0, len(state.Members)),
		Completed: state.Join.Completed, Failed: state.Join.Failed, Cancelled: state.Join.Cancelled,
	}
	for _, member := range state.Members {
		item := MemberResult{
			ID: member.Member.ID, Goal: member.Member.Goal,
			Status: member.Delegation.Status, ErrorCode: strings.TrimSpace(member.Delegation.ErrorCode),
		}
		item.Content = delegatedResultContent(member.Delegation.Result)
		result.Members = append(result.Members, item)
	}
	return result
}

func delegatedResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var envelope struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &envelope) == nil && strings.TrimSpace(envelope.Content) != "" {
		return strings.TrimSpace(envelope.Content)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	return ""
}

// MemberState binds one member to its durable Handoff Delegation.
type MemberState struct {
	Member     Member             `json:"member"`
	Delegation handoff.Delegation `json:"delegation"`
}

// View is the public durable Team state.
type View struct {
	Mode    ExecutionMode `json:"mode"`
	Members []MemberState `json:"members"`
	Join    handoff.Join  `json:"join"`
}

// Result is the public terminal Team output. Durable topology and delegation
// identities remain in View/State; the result intentionally exposes only
// member-level outcomes that are safe for product projection.
type Result struct {
	Kind      string         `json:"kind"`
	Mode      ExecutionMode  `json:"mode"`
	Members   []MemberResult `json:"members"`
	Completed int            `json:"completed"`
	Failed    int            `json:"failed"`
	Cancelled int            `json:"cancelled"`
}

type MemberResult struct {
	ID        string         `json:"id"`
	Goal      string         `json:"goal"`
	Status    handoff.Status `json:"status"`
	Content   string         `json:"content,omitempty"`
	ErrorCode string         `json:"errorCode,omitempty"`
}

// StartRequest creates one explicit Team Run.
type StartRequest struct {
	ID        string
	Actor     kernel.ActorRef
	Thread    kernel.ThreadRef
	RequestID string
	Goal      string
	Mode      ExecutionMode
	Members   []Member
	Join      handoff.Join
}

// Delegator is the narrow reusable Handoff capability consumed by Team.
type Delegator interface {
	StartOrLoad(context.Context, kernel.Snapshot, handoff.Delegation) (handoff.Delegation, error)
}

// Dependencies are the only requirements of the Team feature.
type Dependencies struct {
	Runtime    *kernel.Runtime
	Handoffs   Delegator
	Relations  runrelation.Recorder
	MaxMembers int
}

// Runner owns Team topology, member lifecycle and Join completion.
type Runner struct {
	runtime    *kernel.Runtime
	handoffs   Delegator
	relations  runrelation.Recorder
	maxMembers int
}

type executionState View

type memberResult struct {
	index      int
	delegation handoff.Delegation
	err        error
}

// NewRunner creates an independent Team feature.
func NewRunner(dependencies Dependencies) (*Runner, error) {
	if dependencies.Runtime == nil || dependencies.Handoffs == nil {
		return nil, ErrInvalidRequest
	}
	if dependencies.MaxMembers <= 0 || dependencies.MaxMembers > 16 {
		dependencies.MaxMembers = 16
	}
	return &Runner{
		runtime: dependencies.Runtime, handoffs: dependencies.Handoffs,
		relations: dependencies.Relations, maxMembers: dependencies.MaxMembers,
	}, nil
}

// Descriptor declares the explicit Team capability graph.
func (runner *Runner) Descriptor() kernel.FeatureDescriptor {
	requires := []kernel.Capability{kernel.CapabilityRuntime, handoff.CapabilityCoordinator}
	if runner != nil && runner.relations != nil {
		requires = append(requires, runrelation.CapabilityRelations)
	}
	return kernel.FeatureDescriptor{
		Name:     "team",
		Requires: requires,
		Provides: []kernel.Capability{CapabilityRunner},
	}
}

// StartRun materializes all stable member identities before any Child Run starts.
func (runner *Runner) StartRun(ctx context.Context, request StartRequest) (kernel.Snapshot, error) {
	request = normalizeStartRequest(request)
	if !validStartRequest(request, runner.maxMembers) {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	state, err := runner.materializeState(request)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := runner.runtime.Create(ctx, kernel.CreateRequest{
		ID: request.ID, Kind: RunKind, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: request.Goal, State: encoded,
		Events: []kernel.EventDraft{{Type: "team.started", Message: "Team topology materialized"}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.execute(ctx, snapshot)
}

// Resume continues one non-terminal Team Run without recreating Child Runs.
func (runner *Runner) Resume(ctx context.Context, runID string, expectedRevision uint64) (kernel.Snapshot, error) {
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if snapshot.Run.Status != kernel.RunStatusRunning {
		return snapshot, ErrTeamTerminal
	}
	return runner.execute(ctx, snapshot)
}

// ViewState decodes an isolated Team view from Kernel opaque state.
func ViewState(snapshot kernel.Snapshot) (View, error) {
	state, err := decodeState(snapshot.State)
	if err != nil {
		return View{}, err
	}
	return cloneView(View(state)), nil
}

func (runner *Runner) materializeState(request StartRequest) (executionState, error) {
	members := make([]MemberState, 0, len(request.Members))
	for _, member := range request.Members {
		delegationID, err := runner.runtime.NewID("handoff")
		if err != nil {
			return executionState{}, err
		}
		childRunID, err := runner.runtime.NewID("run")
		if err != nil {
			return executionState{}, err
		}
		members = append(members, MemberState{
			Member: member,
			Delegation: handoff.Delegation{
				ID: delegationID, MemberID: member.ID, ChildRunID: childRunID,
				Goal: member.Goal, Model: strings.TrimSpace(member.Model),
				ModelOptions: append(json.RawMessage(nil), member.ModelOptions...),
				ToolKeys:     append([]string(nil), member.ToolKeys...),
				Status:       handoff.StatusQueued,
			},
		})
	}
	join := request.Join
	join.Status = handoff.JoinPending
	return executionState{Mode: request.Mode, Members: members, Join: join}, nil
}

func (runner *Runner) execute(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, error) {
	state, err := decodeState(snapshot.State)
	if err != nil {
		return runner.fail(ctx, snapshot, executionState{}, "team.state_invalid", err)
	}
	if state.Mode == ExecutionParallel {
		err = runner.runParallel(ctx, snapshot, &state)
	} else {
		err = runner.runSequential(ctx, snapshot, &state)
	}
	if err != nil && !errors.Is(err, handoff.ErrChildPending) && !errors.Is(err, handoff.ErrChildFailed) {
		return runner.fail(ctx, snapshot, state, "team.member_failed", err)
	}
	return runner.resolve(ctx, snapshot, state)
}

func (runner *Runner) runSequential(
	ctx context.Context,
	parent kernel.Snapshot,
	state *executionState,
) error {
	for index := range state.Members {
		stop, err := runner.runSequentialMember(ctx, parent, state, index)
		if err != nil || stop {
			return err
		}
	}
	return nil
}

func (runner *Runner) runSequentialMember(
	ctx context.Context,
	parent kernel.Snapshot,
	state *executionState,
	index int,
) (bool, error) {
	if terminalDelegation(state.Members[index].Delegation.Status) {
		return false, nil
	}
	if err := runner.ensureMemberRelation(ctx, parent, state.Members[index]); err != nil {
		return true, err
	}
	delegation, err := runner.handoffs.StartOrLoad(ctx, parent, state.Members[index].Delegation)
	if delegation.ID != "" {
		state.Members[index].Delegation = delegation
	}
	if errors.Is(err, handoff.ErrChildPending) {
		return true, err
	}
	if errors.Is(err, handoff.ErrChildFailed) {
		return state.Join.FailurePolicy == handoff.FailureFailFast, nil
	}
	if err != nil {
		return true, err
	}
	join, _ := handoff.ResolveJoin(state.Join, teamDelegations(*state))
	state.Join = join
	return join.Status == handoff.JoinReady || join.Status == handoff.JoinFailed, nil
}

func (runner *Runner) runParallel(
	ctx context.Context,
	parent kernel.Snapshot,
	state *executionState,
) error {
	results := make(chan memberResult, len(state.Members))
	group, groupCtx := errgroup.WithContext(ctx)
	launched := 0
	for index := range state.Members {
		if terminalDelegation(state.Members[index].Delegation.Status) {
			continue
		}
		if err := runner.ensureMemberRelation(ctx, parent, state.Members[index]); err != nil {
			return err
		}
		launched++
		delegation := cloneDelegation(state.Members[index].Delegation)
		group.Go(func() error {
			projected, err := runner.handoffs.StartOrLoad(groupCtx, parent, delegation)
			results <- memberResult{index: index, delegation: projected, err: err}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	close(results)
	memberResults := make([]memberResult, 0, launched)
	for result := range results {
		memberResults = append(memberResults, result)
	}
	sortMemberResults(memberResults)
	var resultError error
	for _, result := range memberResults {
		if result.delegation.ID != "" {
			state.Members[result.index].Delegation = result.delegation
		}
		if result.err != nil && !errors.Is(result.err, handoff.ErrChildPending) &&
			!errors.Is(result.err, handoff.ErrChildFailed) {
			resultError = errors.Join(resultError, result.err)
		}
	}
	return resultError
}

func (runner *Runner) ensureMemberRelation(
	ctx context.Context,
	parent kernel.Snapshot,
	member MemberState,
) error {
	if runner.relations == nil {
		return nil
	}
	_, err := runner.relations.Ensure(ctx, runrelation.Draft{
		ParentRunID: parent.Run.ID, ChildRunID: member.Delegation.ChildRunID,
		Kind: runrelation.KindTeamMember, OwnerNodeID: member.Member.ID,
	})
	return err
}

func (runner *Runner) resolve(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	delegations := make([]handoff.Delegation, 0, len(state.Members))
	for _, member := range state.Members {
		delegations = append(delegations, member.Delegation)
	}
	join, joinErr := handoff.ResolveJoin(state.Join, delegations)
	state.Join = join
	switch join.Status {
	case handoff.JoinReady:
		return runner.complete(ctx, snapshot, state)
	case handoff.JoinFailed:
		return runner.fail(ctx, snapshot, state, join.ErrorCode, errors.Join(ErrTeamFailed, joinErr))
	case handoff.JoinPending:
		pending, err := runner.persistRunning(ctx, snapshot, state)
		if err != nil {
			return pending, err
		}
		return pending, errors.Join(ErrMemberPending, joinErr)
	default:
		pending, err := runner.persistRunning(ctx, snapshot, state)
		if err != nil {
			return pending, err
		}
		return pending, errors.Join(ErrMemberPending, joinErr)
	}
}

func teamDelegations(state executionState) []handoff.Delegation {
	delegations := make([]handoff.Delegation, 0, len(state.Members))
	for _, member := range state.Members {
		delegations = append(delegations, member.Delegation)
	}
	return delegations
}

func (runner *Runner) persistRunning(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{Type: "team.progressed", Message: "Team members progressed"}},
	})
}

func (runner *Runner) complete(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	encodedState, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	result, err := json.Marshal(publicResult(state))
	if err != nil {
		return kernel.Snapshot{}, errors.Join(ErrInvalidRequest, err)
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encodedState,
		Result: &kernel.Result{ContentType: "application/json", Content: result},
		Events: []kernel.EventDraft{{Type: "team.completed", Message: "Team Join is ready"}},
	})
}

func (runner *Runner) fail(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	code string,
	cause error,
) (kernel.Snapshot, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, errors.Join(cause, err)
	}
	failed, transitionErr := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: encoded,
		ErrorCode: strings.TrimSpace(code), ErrorDetail: errorText(cause),
		Events: []kernel.EventDraft{{Type: "team.failed", Message: strings.TrimSpace(code)}},
	})
	return failed, errors.Join(cause, transitionErr)
}

func normalizeStartRequest(request StartRequest) StartRequest {
	request.Goal = strings.TrimSpace(request.Goal)
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.Mode == "" {
		request.Mode = ExecutionParallel
	}
	request.Members = normalizeMembers(request.Members)
	if request.Join.Mode == "" {
		request.Join.Mode = handoff.JoinAll
	}
	if request.Join.Quorum == 0 {
		request.Join.Quorum = 1
	}
	if request.Join.FailurePolicy == "" {
		request.Join.FailurePolicy = handoff.FailureFailFast
	}
	return request
}

func normalizeMembers(input []Member) []Member {
	members := append([]Member(nil), input...)
	for index := range members {
		members[index].ID = strings.TrimSpace(members[index].ID)
		members[index].Goal = strings.TrimSpace(members[index].Goal)
		members[index].ToolKeys = normalizedStrings(members[index].ToolKeys)
	}
	return members
}

func validStartRequest(request StartRequest, maxMembers int) bool {
	if request.Goal == "" || len(request.Members) == 0 || len(request.Members) > maxMembers ||
		(request.Mode != ExecutionSequential && request.Mode != ExecutionParallel) {
		return false
	}
	seen := make(map[string]struct{}, len(request.Members))
	for _, member := range request.Members {
		if member.ID == "" || member.Goal == "" {
			return false
		}
		if _, duplicate := seen[member.ID]; duplicate {
			return false
		}
		seen[member.ID] = struct{}{}
	}
	return validJoinRequest(request.Join, len(request.Members))
}

func validJoinRequest(join handoff.Join, memberCount int) bool {
	if join.FailurePolicy != handoff.FailureCollect && join.FailurePolicy != handoff.FailureFailFast {
		return false
	}
	switch join.Mode {
	case handoff.JoinAll, handoff.JoinAny:
		return join.Quorum == 1
	case handoff.JoinQuorum:
		return join.Quorum > 0 && join.Quorum <= memberCount
	default:
		return false
	}
}

func encodeState(state executionState) (json.RawMessage, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	return encoded, nil
}

func decodeState(encoded json.RawMessage) (executionState, error) {
	var state executionState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return executionState{}, errors.Join(ErrInvalidRequest, err)
	}
	return state, nil
}

func terminalDelegation(status handoff.Status) bool {
	return status == handoff.StatusCompleted || status == handoff.StatusFailed || status == handoff.StatusCancelled
}

func sortMemberResults(results []memberResult) {
	for index := 1; index < len(results); index++ {
		for current := index; current > 0 && results[current].index < results[current-1].index; current-- {
			results[current], results[current-1] = results[current-1], results[current]
		}
	}
}

func cloneView(view View) View {
	view.Members = append([]MemberState(nil), view.Members...)
	for index := range view.Members {
		view.Members[index].Member.ToolKeys = append([]string(nil), view.Members[index].Member.ToolKeys...)
		view.Members[index].Delegation = cloneDelegation(view.Members[index].Delegation)
	}
	view.Join.ResultIDs = append([]string(nil), view.Join.ResultIDs...)
	return view
}

func cloneDelegation(delegation handoff.Delegation) handoff.Delegation {
	delegation.ToolKeys = append([]string(nil), delegation.ToolKeys...)
	delegation.Result = append(json.RawMessage(nil), delegation.Result...)
	return delegation
}

func normalizedStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func errorText(err error) string {
	if err == nil {
		return ErrTeamFailed.Error()
	}
	return err.Error()
}
