package planexecute

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

const (
	RunKind          kernel.RunKind    = "plan_execute"
	CapabilityRunner kernel.Capability = "planexecute.runner"
)

const planApprovalCheckpointKind = "plan_approval"

const autonomousWakeupDelay = time.Second

var (
	ErrInvalidRequest        = errors.New("invalid planexecute request")
	ErrInvalidPlan           = errors.New("invalid generated plan")
	ErrPlannerFailure        = errors.New("plan generation failed")
	ErrPlannerInvocationBusy = errors.New("plan generation execution is leased")
	ErrPlannerPending        = errors.New("plan generation is waiting for admission")
	ErrApprovalRequired      = errors.New("plan approval is required")
	ErrInvalidApproval       = errors.New("invalid plan approval")
	ErrStepPending           = errors.New("plan step is not terminal")
	ErrStepFailure           = errors.New("plan step failed")
	ErrPlanAlreadyTerminal   = errors.New("planexecute run is terminal")
)

// ApprovalPolicy controls only Plan approval and is not an Agent execution mode.
type ApprovalPolicy string

const (
	ApprovalAuto     ApprovalPolicy = "auto"
	ApprovalRequired ApprovalPolicy = "required"
)

// ApprovalDecision is the explicit Plan approval outcome.
type ApprovalDecision string

const (
	DecisionApprove ApprovalDecision = "approve"
	DecisionReject  ApprovalDecision = "reject"
)

// StepStatus is the durable lifecycle of one Plan step.
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
)

// PlanStatus is the durable lifecycle of a generated Plan.
type PlanStatus string

const (
	PlanProposed  PlanStatus = "proposed"
	PlanApproved  PlanStatus = "approved"
	PlanRejected  PlanStatus = "rejected"
	PlanRunning   PlanStatus = "running"
	PlanCompleted PlanStatus = "completed"
	PlanFailed    PlanStatus = "failed"
)

// StepDraft is one model-generated step before runtime identity assignment.
type StepDraft struct {
	Title    string   `json:"title"`
	Goal     string   `json:"goal"`
	ToolKeys []string `json:"toolKeys,omitempty"`
}

func planWakeupAt(now time.Time) *time.Time {
	wakeupAt := now.UTC().Add(autonomousWakeupDelay)
	return &wakeupAt
}

// PlanDraft is the validated planner output before durable identity assignment.
type PlanDraft struct {
	Summary string      `json:"summary"`
	Steps   []StepDraft `json:"steps"`
}

// PlannerRequest is one provider-neutral planning request.
type PlannerRequest struct {
	InvocationID    string   `json:"invocationID"`
	RunID           string   `json:"runID"`
	Goal            string   `json:"goal"`
	Model           string   `json:"model,omitempty"`
	MaxSteps        int      `json:"maxSteps"`
	AllowedToolKeys []string `json:"allowedToolKeys,omitempty"`
}

// PlannerResponse is the normalized receipt from one Planner invocation.
type PlannerResponse struct {
	Draft      PlanDraft `json:"draft"`
	ResponseID string    `json:"responseID,omitempty"`
}

// Planner generates a bounded Plan without executing it. InvocationID is a
// stable logical identity that provider-backed adapters may map to their own
// idempotency or response-retrieval facility.
type Planner interface {
	GeneratePlan(context.Context, PlannerRequest) (PlannerResponse, error)
}

// AgentRunner is the narrow direct Agent capability consumed by PlanExecute.
type AgentRunner interface {
	StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error)
	LoadRun(context.Context, string) (kernel.Snapshot, error)
}

// Step is one durable Plan step and its stable Child Agent Run identity.
type Step struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Goal       string          `json:"goal"`
	ToolKeys   []string        `json:"toolKeys,omitempty"`
	Status     StepStatus      `json:"status"`
	ChildRunID string          `json:"childRunID,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// Plan is the durable model-generated execution plan.
type Plan struct {
	ID       string     `json:"id"`
	Revision int        `json:"revision"`
	Status   PlanStatus `json:"status"`
	Summary  string     `json:"summary"`
	Steps    []Step     `json:"steps"`
}

// View is the public PlanExecute state projected from Kernel opaque state.
type View struct {
	ApprovalPolicy    ApprovalPolicy     `json:"approvalPolicy"`
	PlannerInvocation *PlannerInvocation `json:"plannerInvocation,omitempty"`
	Plan              Plan               `json:"plan"`
	NextStep          int                `json:"nextStep"`
}

// StartRequest starts one explicit Plan-and-Execute Run.
type StartRequest struct {
	ID              string
	Actor           kernel.ActorRef
	Thread          kernel.ThreadRef
	RequestID       string
	Goal            string
	Model           string
	ApprovalPolicy  ApprovalPolicy
	MaxSteps        int
	AllowedToolKeys []string
}

// ApprovalResponse resolves one pending Plan approval checkpoint.
type ApprovalResponse struct {
	Decision ApprovalDecision `json:"decision"`
	Comment  string           `json:"comment,omitempty"`
}

// Dependencies are the only requirements of the PlanExecute feature.
type Dependencies struct {
	Runtime         *kernel.Runtime
	Planner         Planner
	Agent           AgentRunner
	Relations       runrelation.Recorder
	MaxSteps        int
	DeferResumption bool
}

// Runner owns Plan generation, approval, sequential Step execution and recovery.
type Runner struct {
	runtime     *kernel.Runtime
	planner     Planner
	agent       AgentRunner
	relations   runrelation.Recorder
	maxSteps    int
	deferResume bool
}

type executionState struct {
	Model             string             `json:"model,omitempty"`
	ApprovalPolicy    ApprovalPolicy     `json:"approvalPolicy"`
	AllowedToolKeys   []string           `json:"allowedToolKeys"`
	PlannerInvocation *PlannerInvocation `json:"plannerInvocation"`
	Plan              Plan               `json:"plan"`
	NextStep          int                `json:"nextStep"`
}

type approvalPayload struct {
	PlanID  string      `json:"planID"`
	Summary string      `json:"summary"`
	Steps   []StepDraft `json:"steps"`
}

// NewRunner creates an independent Plan-and-Execute feature.
func NewRunner(dependencies Dependencies) (*Runner, error) {
	if dependencies.Runtime == nil || dependencies.Planner == nil || dependencies.Agent == nil {
		return nil, ErrInvalidRequest
	}
	if dependencies.MaxSteps <= 0 {
		dependencies.MaxSteps = 16
	}
	return &Runner{
		runtime: dependencies.Runtime, planner: dependencies.Planner,
		agent: dependencies.Agent, relations: dependencies.Relations, maxSteps: dependencies.MaxSteps,
		deferResume: dependencies.DeferResumption,
	}, nil
}

// Descriptor declares the explicit PlanExecute capability graph.
func (runner *Runner) Descriptor() kernel.FeatureDescriptor {
	requires := []kernel.Capability{kernel.CapabilityRuntime, agent.CapabilityRunner}
	if runner != nil && runner.relations != nil {
		requires = append(requires, runrelation.CapabilityRelations)
	}
	return kernel.FeatureDescriptor{
		Name: "planexecute", Requires: requires,
		Provides: []kernel.Capability{CapabilityRunner},
	}
}

// StartRun creates a PlanExecute Run, generates a Plan and waits or executes.
func (runner *Runner) StartRun(ctx context.Context, request StartRequest) (kernel.Snapshot, error) {
	request = normalizeStartRequest(request, runner.maxSteps)
	if !validStartRequest(request) {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	if request.ID == "" {
		var err error
		request.ID, err = runner.runtime.NewID("run")
		if err != nil {
			return kernel.Snapshot{}, err
		}
	}
	plannerRequest := PlannerRequest{
		RunID: request.ID, Goal: request.Goal, Model: request.Model, MaxSteps: request.MaxSteps,
		AllowedToolKeys: cloneOptionalStrings(request.AllowedToolKeys),
	}
	invocation, err := newPlannerInvocation(plannerRequest, 1, runner.runtime.Now())
	if err != nil {
		return kernel.Snapshot{}, err
	}
	initial, err := encodeState(executionState{
		Model: strings.TrimSpace(request.Model), ApprovalPolicy: request.ApprovalPolicy,
		AllowedToolKeys: cloneOptionalStrings(request.AllowedToolKeys), PlannerInvocation: &invocation,
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := runner.runtime.Create(ctx, kernel.CreateRequest{
		ID: request.ID, Kind: RunKind, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: request.Goal, State: initial,
		Events: []kernel.EventDraft{{
			Type: "planexecute.started", Message: "Plan generation started", Wakeup: true,
			WakeupAt: planWakeupAt(runner.runtime.Now()),
		}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.continueRun(ctx, snapshot)
}

// ResolveApproval resumes a PlanExecute Run after explicit Plan approval.
func (runner *Runner) ResolveApproval(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
	response ApprovalResponse,
) (kernel.Snapshot, error) {
	snapshot, state, err := runner.load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if snapshot.Run.Status != kernel.RunStatusWaitingInput || snapshot.Checkpoint == nil ||
		snapshot.Checkpoint.Kind != planApprovalCheckpointKind || state.Plan.Status != PlanProposed {
		return kernel.Snapshot{}, ErrApprovalRequired
	}
	resolved, err := runner.resolveApprovalCheckpoint(snapshot.Checkpoint, response)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if response.Decision == DecisionReject {
		state.Plan.Status = PlanRejected
		return runner.cancel(ctx, snapshot, state, resolved, response.Comment)
	}
	approved, err := runner.approvePlan(ctx, snapshot, state, resolved, "plan.approved", "Plan approved")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if runner.deferResume {
		return approved, nil
	}
	return runner.continueRun(ctx, approved)
}

// Resume continues one non-terminal PlanExecute Run after interruption or Child completion.
func (runner *Runner) Resume(ctx context.Context, runID string, expectedRevision uint64) (kernel.Snapshot, error) {
	snapshot, _, err := runner.load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if snapshot.Run.Status == kernel.RunStatusWaitingInput {
		return snapshot, ErrApprovalRequired
	}
	if snapshot.Run.Status != kernel.RunStatusRunning {
		return snapshot, ErrPlanAlreadyTerminal
	}
	return runner.continueRun(ctx, snapshot)
}

func (runner *Runner) continueRun(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, error) {
	for snapshot.Run.Status == kernel.RunStatusRunning {
		state, err := decodeState(snapshot.State)
		if err != nil {
			return runner.fail(ctx, snapshot, executionState{}, "planexecute.state_invalid", err)
		}
		if state.PlannerInvocation != nil && state.PlannerInvocation.Status != PlannerInvocationConsumed {
			next, advanceErr := runner.advancePlannerInvocation(ctx, snapshot, state)
			if errors.Is(advanceErr, kernel.ErrConflict) {
				snapshot, err = runner.runtime.Load(ctx, snapshot.Run.ID)
				if err != nil {
					return kernel.Snapshot{}, err
				}
				continue
			}
			if advanceErr != nil {
				return next, advanceErr
			}
			snapshot = next
			continue
		}
		switch state.Plan.Status {
		case PlanProposed:
			if state.ApprovalPolicy == ApprovalRequired {
				return runner.waitForApproval(ctx, snapshot, state)
			}
			approved, approveErr := runner.approvePlan(
				ctx, snapshot, state, snapshot.Checkpoint, "plan.auto_approved", "Plan auto-approved",
			)
			if errors.Is(approveErr, kernel.ErrConflict) {
				snapshot, err = runner.runtime.Load(ctx, snapshot.Run.ID)
				if err != nil {
					return kernel.Snapshot{}, err
				}
				continue
			}
			if approveErr != nil {
				return approved, approveErr
			}
			snapshot = approved
			continue
		case PlanApproved, PlanRunning:
			return runner.execute(ctx, snapshot)
		case PlanRejected, PlanCompleted, PlanFailed:
			return snapshot, ErrPlanAlreadyTerminal
		default:
			return runner.fail(ctx, snapshot, state, "planexecute.state_invalid", ErrInvalidPlan)
		}
	}
	return snapshot, nil
}

// ViewState decodes an isolated public view from a Kernel snapshot.
func ViewState(snapshot kernel.Snapshot) (View, error) {
	state, err := decodeState(snapshot.State)
	if err != nil {
		return View{}, err
	}
	return View{
		ApprovalPolicy: state.ApprovalPolicy, PlannerInvocation: clonePlannerInvocation(state.PlannerInvocation),
		Plan: clonePlan(state.Plan), NextStep: state.NextStep,
	}, nil
}

func (runner *Runner) materializePlan(
	draft PlanDraft,
	model string,
	policy ApprovalPolicy,
	maxSteps int,
	allowedToolKeys []string,
) (executionState, error) {
	draft.Summary = strings.TrimSpace(draft.Summary)
	if draft.Summary == "" || len(draft.Steps) == 0 || len(draft.Steps) > maxSteps {
		return executionState{}, ErrInvalidPlan
	}
	planID, err := runner.runtime.NewID("plan")
	if err != nil {
		return executionState{}, err
	}
	steps := make([]Step, 0, len(draft.Steps))
	allowedToolKeys = normalizedOptionalStrings(allowedToolKeys)
	for _, item := range draft.Steps {
		item.Title = strings.TrimSpace(item.Title)
		item.Goal = strings.TrimSpace(item.Goal)
		if item.Title == "" || item.Goal == "" {
			return executionState{}, ErrInvalidPlan
		}
		stepID, idErr := runner.runtime.NewID("step")
		if idErr != nil {
			return executionState{}, idErr
		}
		toolKeys := normalizedStrings(item.ToolKeys)
		if !toolKeysAllowed(toolKeys, allowedToolKeys) {
			return executionState{}, ErrInvalidPlan
		}
		steps = append(steps, Step{
			ID: stepID, Title: item.Title, Goal: item.Goal,
			ToolKeys: toolKeys, Status: StepPending,
		})
	}
	return executionState{
		Model:           strings.TrimSpace(model),
		ApprovalPolicy:  policy,
		AllowedToolKeys: allowedToolKeys,
		Plan:            Plan{ID: planID, Revision: 1, Status: PlanProposed, Summary: draft.Summary, Steps: steps},
	}, nil
}

func (runner *Runner) persistPlan(
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
		Events: []kernel.EventDraft{{
			Type: "plan.created", Message: state.Plan.Summary, Wakeup: true,
			WakeupAt: planWakeupAt(runner.runtime.Now()),
		}},
	})
}

func (runner *Runner) waitForApproval(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	checkpoint, err := runner.newApprovalCheckpoint(state.Plan)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput, State: encoded, Checkpoint: checkpoint,
		Events: []kernel.EventDraft{{Type: "plan.proposed", Message: "Plan approval required"}},
	})
}

func (runner *Runner) approvePlan(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	checkpoint *kernel.Checkpoint,
	eventType string,
	message string,
) (kernel.Snapshot, error) {
	state.Plan.Status = PlanApproved
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: checkpoint,
		Events: []kernel.EventDraft{{
			Type: eventType, Message: message, Wakeup: true,
			WakeupAt: planWakeupAt(runner.runtime.Now()),
		}},
	})
}

func (runner *Runner) execute(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, error) {
	for snapshot.Run.Status == kernel.RunStatusRunning {
		next, done, err := runner.executeStep(ctx, snapshot)
		if err != nil || done {
			return next, err
		}
		snapshot = next
	}
	return snapshot, nil
}

func (runner *Runner) executeStep(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, bool, error) {
	state, err := decodeState(snapshot.State)
	if err != nil {
		failed, failErr := runner.fail(ctx, snapshot, executionState{}, "planexecute.state_invalid", err)
		return failed, true, failErr
	}
	if state.NextStep >= len(state.Plan.Steps) {
		completed, completeErr := runner.complete(ctx, snapshot, state)
		return completed, true, completeErr
	}
	state.Plan.Status = PlanRunning
	step := state.Plan.Steps[state.NextStep]
	if step.Status == StepPending {
		prepared, prepareErr := runner.prepareStep(ctx, snapshot, state)
		if prepareErr != nil {
			return prepared, true, prepareErr
		}
		snapshot = prepared
		state, err = decodeState(snapshot.State)
		if err != nil {
			failed, failErr := runner.fail(ctx, snapshot, executionState{}, "planexecute.state_invalid", err)
			return failed, true, failErr
		}
		step = state.Plan.Steps[state.NextStep]
	}
	child, childErr := runner.loadOrStartChild(ctx, snapshot, state.Model, step)
	if childErr != nil && child.Run.ID == "" {
		failed, failErr := runner.fail(ctx, snapshot, state, "planexecute.step_start_failed", childErr)
		return failed, true, failErr
	}
	return runner.applyChildOutcome(ctx, snapshot, state, child, childErr)
}

func (runner *Runner) prepareStep(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	step := &state.Plan.Steps[state.NextStep]
	childRunID, err := runner.runtime.NewID("run")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	step.Status = StepRunning
	step.ChildRunID = childRunID
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "plan.step_started", Message: step.Title, Wakeup: true,
			WakeupAt: planWakeupAt(runner.runtime.Now()),
		}},
	})
}

func (runner *Runner) loadOrStartChild(
	ctx context.Context,
	parent kernel.Snapshot,
	model string,
	step Step,
) (kernel.Snapshot, error) {
	if runner.relations != nil {
		_, err := runner.relations.Ensure(ctx, runrelation.Draft{
			ParentRunID: parent.Run.ID, ChildRunID: step.ChildRunID,
			Kind: runrelation.KindPlanStep, OwnerNodeID: step.ID,
		})
		if err != nil {
			return kernel.Snapshot{}, err
		}
	}
	child, err := runner.agent.LoadRun(ctx, step.ChildRunID)
	if err == nil {
		return child, nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return kernel.Snapshot{}, err
	}
	child, startErr := runner.agent.StartRun(ctx, agent.StartRequest{
		ID: step.ChildRunID, Actor: parent.Run.Actor, Thread: parent.Run.Thread,
		RequestID: parent.Run.ID + ":" + step.ID, Goal: step.Goal,
		Model: model, ToolKeys: step.ToolKeys,
	})
	if errors.Is(startErr, kernel.ErrAlreadyExists) {
		return runner.agent.LoadRun(ctx, step.ChildRunID)
	}
	return child, startErr
}

func (runner *Runner) applyChildOutcome(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	child kernel.Snapshot,
	childErr error,
) (kernel.Snapshot, bool, error) {
	switch child.Run.Status {
	case kernel.RunStatusCompleted:
		completed, err := runner.completeStep(ctx, snapshot, state, child)
		return completed, false, err
	case kernel.RunStatusFailed, kernel.RunStatusCancelled:
		failed, err := runner.failStep(ctx, snapshot, state, child, childErr)
		return failed, true, err
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput:
		return snapshot, true, ErrStepPending
	default:
		failed, err := runner.failStep(ctx, snapshot, state, child, childErr)
		return failed, true, err
	}
}

func (runner *Runner) completeStep(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	child kernel.Snapshot,
) (kernel.Snapshot, error) {
	step := &state.Plan.Steps[state.NextStep]
	step.Status = StepCompleted
	if child.Result != nil {
		step.Result = append(json.RawMessage(nil), child.Result.Content...)
	}
	state.NextStep++
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "plan.step_completed", Message: step.Title, Wakeup: true,
			WakeupAt: planWakeupAt(runner.runtime.Now()),
		}},
	})
}

func (runner *Runner) failStep(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	child kernel.Snapshot,
	childErr error,
) (kernel.Snapshot, error) {
	step := &state.Plan.Steps[state.NextStep]
	step.Status = StepFailed
	step.Error = child.Run.ErrorDetail
	if step.Error == "" && childErr != nil {
		step.Error = childErr.Error()
	}
	state.Plan.Status = PlanFailed
	return runner.fail(ctx, snapshot, state, "planexecute.step_failed", errors.Join(ErrStepFailure, childErr))
}

func (runner *Runner) complete(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	state.Plan.Status = PlanCompleted
	encodedState, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	result, err := json.Marshal(View{
		ApprovalPolicy: state.ApprovalPolicy, Plan: clonePlan(state.Plan), NextStep: state.NextStep,
	})
	if err != nil {
		return kernel.Snapshot{}, errors.Join(ErrInvalidPlan, err)
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encodedState, Checkpoint: snapshot.Checkpoint,
		Result: &kernel.Result{ContentType: "application/json", Content: result},
		Events: []kernel.EventDraft{{Type: "plan.completed", Message: state.Plan.Summary}},
	})
}

func (runner *Runner) cancel(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	checkpoint *kernel.Checkpoint,
	comment string,
) (kernel.Snapshot, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCancelled, State: encoded, Checkpoint: checkpoint,
		ErrorCode: "planexecute.plan_rejected", ErrorDetail: strings.TrimSpace(comment),
		Events: []kernel.EventDraft{{Type: "plan.rejected", Message: "Plan rejected"}},
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
		Status: kernel.RunStatusFailed, State: encoded, Checkpoint: snapshot.Checkpoint,
		ErrorCode: code, ErrorDetail: errorText(cause),
		Events: []kernel.EventDraft{{Type: "planexecute.failed", Message: code}},
	})
	return failed, errors.Join(cause, transitionErr)
}

func (runner *Runner) newApprovalCheckpoint(plan Plan) (*kernel.Checkpoint, error) {
	checkpointID, err := runner.runtime.NewID("checkpoint")
	if err != nil {
		return nil, err
	}
	steps := make([]StepDraft, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		steps = append(steps, StepDraft{Title: step.Title, Goal: step.Goal, ToolKeys: append([]string(nil), step.ToolKeys...)})
	}
	payload, err := json.Marshal(approvalPayload{PlanID: plan.ID, Summary: plan.Summary, Steps: steps})
	if err != nil {
		return nil, errors.Join(ErrInvalidPlan, err)
	}
	return &kernel.Checkpoint{
		ID: checkpointID, Kind: planApprovalCheckpointKind, Status: kernel.CheckpointPending,
		Payload: payload, CreatedAt: runner.runtime.Now(),
	}, nil
}

func (runner *Runner) resolveApprovalCheckpoint(
	checkpoint *kernel.Checkpoint,
	response ApprovalResponse,
) (*kernel.Checkpoint, error) {
	if checkpoint == nil || checkpoint.Kind != planApprovalCheckpointKind || checkpoint.Status != kernel.CheckpointPending ||
		(response.Decision != DecisionApprove && response.Decision != DecisionReject) {
		return nil, ErrInvalidApproval
	}
	response.Comment = strings.TrimSpace(response.Comment)
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, errors.Join(ErrInvalidApproval, err)
	}
	resolvedAt := runner.runtime.Now()
	resolved := *checkpoint
	resolved.Status = kernel.CheckpointResolved
	resolved.Payload = append(json.RawMessage(nil), checkpoint.Payload...)
	resolved.Response = encoded
	resolved.ResolvedAt = &resolvedAt
	return &resolved, nil
}

func (runner *Runner) load(ctx context.Context, runID string) (kernel.Snapshot, executionState, error) {
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, executionState{}, err
	}
	state, err := decodeState(snapshot.State)
	return snapshot, state, err
}

func normalizeStartRequest(request StartRequest, defaultMax int) StartRequest {
	request.ID = strings.TrimSpace(request.ID)
	request.Goal = strings.TrimSpace(request.Goal)
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Model = strings.TrimSpace(request.Model)
	request.AllowedToolKeys = normalizedOptionalStrings(request.AllowedToolKeys)
	if request.ApprovalPolicy == "" {
		request.ApprovalPolicy = ApprovalRequired
	}
	if request.MaxSteps <= 0 || request.MaxSteps > defaultMax {
		request.MaxSteps = defaultMax
	}
	return request
}

func validStartRequest(request StartRequest) bool {
	return request.Goal != "" && request.MaxSteps > 0 &&
		(request.ApprovalPolicy == ApprovalAuto || request.ApprovalPolicy == ApprovalRequired)
}

func encodeState(state executionState) (json.RawMessage, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, errors.Join(ErrInvalidPlan, err)
	}
	return encoded, nil
}

func decodeState(encoded json.RawMessage) (executionState, error) {
	var state executionState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return executionState{}, errors.Join(ErrInvalidPlan, err)
	}
	if !validPlannerInvocation(state.PlannerInvocation) ||
		state.PlannerInvocation.RunID == "" || state.PlannerInvocation.RunID != state.PlannerInvocation.Request.RunID {
		return executionState{}, ErrInvalidPlan
	}
	return state, nil
}

func clonePlan(plan Plan) Plan {
	plan.Steps = append([]Step(nil), plan.Steps...)
	for index := range plan.Steps {
		plan.Steps[index].ToolKeys = append([]string(nil), plan.Steps[index].ToolKeys...)
		plan.Steps[index].Result = append(json.RawMessage(nil), plan.Steps[index].Result...)
	}
	return plan
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

func normalizedOptionalStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return normalizedStrings(values)
}

func cloneOptionalStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func toolKeysAllowed(toolKeys, allowedToolKeys []string) bool {
	if allowedToolKeys == nil {
		return true
	}
	allowed := make(map[string]struct{}, len(allowedToolKeys))
	for _, key := range allowedToolKeys {
		allowed[key] = struct{}{}
	}
	for _, key := range toolKeys {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

func errorText(err error) string {
	if err == nil {
		return ErrStepFailure.Error()
	}
	return err.Error()
}
